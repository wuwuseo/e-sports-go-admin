package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsorderevent"
	"go-admin/internal/ent/esportsplayer"
)

// dw2AppointmentOrder 造一张已付款、待派单的预约单（档期已确认 + 服务时长快照）。
func dw2AppointmentOrder(t *testing.T, db *ent.Client, orderNo string, skuID int, start time.Time,
	durationMinutes int) *ent.EsportsOrder {
	t.Helper()
	return db.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(1).SetSkuID(skuID).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetPaymentDeadline(time.Now().UTC().Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).
		SetAppointmentStatus(esportsorder.AppointmentStatusConfirmed).
		SetRequestedStartAt(start.UTC()).SetServiceDurationMinutes(durationMinutes).
		SaveX(t.Context())
}

// §5.9-01：席位必须记录「发出邀请时间」与「服务时段」，且「进入待派单池」「可以开工」
// 各是一条独立订单事件（各只写一次）。
func TestDw2SeatInviteSlotAndTimelineEvents(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dw2_seat_slot")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"

	admin := seedAdminAccount(t, db, "dw2slot")
	sku := seedGameSku(t, db, "DW2S", game)
	start := now.Add(2 * time.Hour).UTC().Truncate(time.Second)
	order := dw2AppointmentOrder(t, db, "DW2-SLOT-1", sku.ID, start, 90)

	// ① 进入待派单池：首次写一条 order.entered_pending_dispatch，重复调用不再写。
	dw2RunInTx(t, db, func(tx *ent.Tx) error {
		return onOrderEnteredPendingDispatch(ctx, tx, order.ID, now)
	})
	dw2RunInTx(t, db, func(tx *ent.Tx) error {
		return onOrderEnteredPendingDispatch(ctx, tx, order.ID, now.Add(time.Minute))
	})
	poolEvents := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(order.ID),
		esportsorderevent.EventTypeEQ("order.entered_pending_dispatch"),
	).CountX(ctx)
	if poolEvents != 1 {
		t.Fatalf("进入待派单池必须是独立事件且只写一次，got=%d", poolEvents)
	}
	if db.EsportsOrder.GetX(ctx, order.ID).FirstPooledAt == nil {
		t.Fatal("进入待派单池仍应落 first_pooled_at 时间点")
	}

	// ② 派发邀请：席位落 invited_at 与时段起止。
	player := seedPlayer(t, db, 9201, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	seat, err := assignDispatchSeat(ctx, db, order.ID, 1, player.ID, admin.ID, now)
	if err != nil {
		t.Fatalf("指派应成功：%v", err)
	}
	if seat.InvitedAt == nil || !seat.InvitedAt.Equal(now.UTC()) {
		t.Fatalf("席位必须记录发出邀请时间：%+v", seat.InvitedAt)
	}
	if seat.SlotStart == nil || !seat.SlotStart.Equal(start) {
		t.Fatalf("席位时段起点应为订单档期：%+v", seat.SlotStart)
	}
	if seat.SlotEnd == nil || !seat.SlotEnd.Equal(start.Add(90*time.Minute)) {
		t.Fatalf("席位时段终点应为档期 + 服务时长：%+v", seat.SlotEnd)
	}
	// 席位接口（listOrderSeats）同口径返回。
	seats, err := listOrderSeats(ctx, db, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(seats) != 1 || seats[0].InvitedAt == nil || seats[0].SlotEnd == nil {
		t.Fatalf("席位接口必须返回 invited_at 与时段起止：%+v", seats)
	}

	// ③ 全部席位就绪 → 「可以开工」独立事件 + ready_to_start_at，且只写一次。
	if err = respondPlayerInvite(ctx, db, player.ID, seat.ID, true, now); err != nil {
		t.Fatalf("接受邀请应成功：%v", err)
	}
	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.FulfillmentStatus != esportsorder.FulfillmentStatusAccepted {
		t.Fatalf("全部席位就绪后订单应转已接单：%v", got.FulfillmentStatus)
	}
	if got.ReadyToStartAt == nil {
		t.Fatal("可以开工时刻必须落库")
	}
	readyEvents := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(order.ID),
		esportsorderevent.EventTypeEQ("order.ready_to_start"),
	).CountX(ctx)
	if readyEvents != 1 {
		t.Fatalf("可以开工必须是独立事件且只写一次，got=%d", readyEvents)
	}
	// 幂等：重复调用不产生第二条事件、不改写首个时间点。
	dw2RunInTx(t, db, func(tx *ent.Tx) error {
		_, err := markOrderReadyToStartIn(ctx, tx, order.ID, got.Version, now.Add(time.Hour))
		return err
	})
	if again := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(order.ID),
		esportsorderevent.EventTypeEQ("order.ready_to_start"),
	).CountX(ctx); again != 1 {
		t.Fatalf("可以开工事件不得重复：%d", again)
	}
	if after := db.EsportsOrder.GetX(ctx, order.ID); !after.ReadyToStartAt.Equal(*got.ReadyToStartAt) {
		t.Fatalf("可以开工时间点不得被覆盖：%v → %v", got.ReadyToStartAt, after.ReadyToStartAt)
	}
	// 席位保留 invited_at 与 accepted 状态（回应时间同席可见）。
	final := db.EsportsDispatchSeat.GetX(ctx, seat.ID)
	if final.Status != esportsdispatchseat.StatusAccepted || final.InvitedAt == nil ||
		final.RespondedAt == nil {
		t.Fatalf("席位应保留发出/回应时间：%+v", final)
	}
}

// §5.9-01 补充口径：即时单（无档期与时长）不臆造席位时段。
func TestDw2InstantOrderSeatHasNoSlot(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dw2_seat_slot_instant")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()

	admin := seedAdminAccount(t, db, "dw2slotinstant")
	sku := seedGameSku(t, db, "DW2SI", "王者荣耀")
	order := db.EsportsOrder.Create().SetOrderNo("DW2-SLOT-2").SetCustomerID(1).SetSkuID(sku.ID).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).
		SaveX(ctx)
	player := seedPlayer(t, db, 9211, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	seat, err := assignDispatchSeat(ctx, db, order.ID, 1, player.ID, admin.ID, now)
	if err != nil {
		t.Fatalf("指派应成功：%v", err)
	}
	if seat.InvitedAt == nil {
		t.Fatal("即时单也应记录发出邀请时间")
	}
	if seat.SlotStart != nil || seat.SlotEnd != nil {
		t.Fatalf("即时单无档期，不得臆造席位时段：%+v", seat)
	}
}

// dw2RunInTx 在独立事务里跑一段逻辑，便于直接调用事务内函数。
func dw2RunInTx(t *testing.T, db *ent.Client, fn func(tx *ent.Tx) error) {
	t.Helper()
	tx, err := db.Tx(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
