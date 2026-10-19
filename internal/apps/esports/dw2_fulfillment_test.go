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
	"go-admin/internal/ent/esportsservicesegment"
)

// dw2ServiceOrder 造一张「服务中」的双席订单 + 两条进行中服务段。
func dw2ServiceOrder(t *testing.T, db *ent.Client, orderNo string, seats int, promised, perSeat int64,
	p1, p2 *ent.EsportsPlayer, now time.Time) *ent.EsportsOrder {
	t.Helper()
	ctx := t.Context()
	order := db.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(int64(seats) * 1000).
		SetPromisedServiceUnits(promised).SetPerSeatServiceUnits(perSeat).SetRequiredSeats(seats).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusInService).
		SaveX(ctx)
	players := []*ent.EsportsPlayer{p1, p2}
	for i := 1; i <= seats; i++ {
		player := players[i-1]
		db.EsportsDispatchSeat.Create().SetOrderID(order.ID).SetSeatIndex(i).
			SetPlayerID(player.ID).SetStatus(esportsdispatchseat.StatusAccepted).
			SetRespondedAt(now).SaveX(ctx)
		db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(player.ID).
			SetSeatIndex(i).SetStatus(esportsservicesegment.StatusInService).
			SetStartedAt(now.Add(-10 * time.Minute)).SaveX(ctx)
	}
	return order
}

// §6.2-04：暂停/恢复与订单/服务段视图必须返回逐成员「已服务量 / 暂停量 / 仍需履约量」
// （仍需履约量 = 承诺量 + 加钟量 − 已上报量），并且暂停后有效时长冻结、恢复后暂停时长固化。
func TestDw2ServiceFulfillmentUnitsView(t *testing.T) {
	db := openTransferDB(t, "esports_dw2_fulfillment")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()

	p1 := seedPlayer(t, db, 8801, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	p2 := seedPlayer(t, db, 8802, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := dw2ServiceOrder(t, db, "DW2-FU-1", 2, 4, 2, p1, p2, now)

	// ① 尚未上报：仍需履约量 = 承诺量 4。
	items, err := listPlayerServiceTimings(ctx, db, p2.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("应返回 1 条进行中服务段，got=%d", len(items))
	}
	item := items[0]
	if item.PlayerID != p2.ID || item.SeatIndex != 2 {
		t.Fatalf("逐成员字段缺失：%+v", item)
	}
	if item.ReportedUnits != 0 || item.PromisedUnits != 2 {
		t.Fatalf("已服务量 / 本席承诺量口径不正确：%+v", item)
	}
	if item.OrderPromisedUnits != 4 || item.OrderServedUnits != 0 || item.UnfulfilledUnits != 4 {
		t.Fatalf("订单级履约量口径不正确：%+v", item)
	}

	// ② 成员 P1 结束服务并上报 2 单位 → 仍需履约量降到 2。
	if _, err := finishPlayerService(ctx, db, p1.ID, order.ID, 2, 600, "", now); err != nil {
		t.Fatalf("结束服务应成功：%v", err)
	}
	items, err = listPlayerServiceTimings(ctx, db, p2.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].OrderServedUnits != 2 || items[0].UnfulfilledUnits != 2 {
		t.Fatalf("已上报量必须实时计入仍需履约量：%+v", items[0])
	}

	// ③ 加钟量计入承诺侧：仍需履约量 = 4 + 1 − 2 = 3。
	db.EsportsOrder.UpdateOneID(order.ID).SetAddonUnits(1).SaveX(ctx)
	items, err = listPlayerServiceTimings(ctx, db, p2.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].OrderPromisedUnits != 4 || items[0].OrderTotalUnits != 5 || items[0].UnfulfilledUnits != 3 {
		t.Fatalf("加钟量必须计入仍需履约量：%+v", items[0])
	}

	// ④ 暂停：响应必须回逐成员履约量；暂停后有效时长冻结、暂停时长随墙钟增长。
	paused, err := setPlayerServicePause(ctx, db, p2.ID, order.ID, true, "客户临时离开", now)
	if err != nil {
		t.Fatalf("暂停应成功：%v", err)
	}
	if paused.Status != "paused" || paused.ReportedUnits != 0 || paused.PromisedUnits != 2 ||
		paused.OrderServedUnits != 2 || paused.UnfulfilledUnits != 3 {
		t.Fatalf("暂停响应必须回逐成员履约量：%+v", paused)
	}
	later := now.Add(90 * time.Second)
	items, err = listPlayerServiceTimings(ctx, db, p2.ID, later)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].EffectiveSeconds != paused.EffectiveSeconds {
		t.Fatalf("暂停期间有效服务量必须冻结：暂停时=%d 90 秒后=%d",
			paused.EffectiveSeconds, items[0].EffectiveSeconds)
	}
	if items[0].PausedSeconds != paused.PausedSeconds+90 {
		t.Fatalf("暂停量必须随墙钟增长：暂停时=%d 90 秒后=%d",
			paused.PausedSeconds, items[0].PausedSeconds)
	}

	// ⑤ 恢复：暂停量固化，有效服务量重新增长。
	resumed, err := setPlayerServicePause(ctx, db, p2.ID, order.ID, false, "", now.Add(180*time.Second))
	if err != nil {
		t.Fatalf("恢复应成功：%v", err)
	}
	if resumed.Status != "in_service" || resumed.PausedAt != nil || resumed.PausedSeconds != paused.PausedSeconds+180 {
		t.Fatalf("恢复后暂停量应固化：%+v", resumed)
	}
	items, err = listPlayerServiceTimings(ctx, db, p2.ID, now.Add(300*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if items[0].PausedSeconds != resumed.PausedSeconds {
		t.Fatalf("恢复后暂停量不得继续增长：恢复时=%d 之后=%d",
			resumed.PausedSeconds, items[0].PausedSeconds)
	}
	if items[0].EffectiveSeconds <= paused.EffectiveSeconds {
		t.Fatal("恢复后有效服务量必须继续增长")
	}
	if items[0].UnfulfilledUnits != 3 {
		t.Fatalf("暂停/恢复不改变已上报量，故仍需履约量保持 3：%+v", items[0])
	}
}

// §6.2-02：客服记名停工（代暂停 / 时长更正登记停工量）后，若整单再无进行中的服务段，
// 必须按剩余履约量把订单转入 pending_close，并把剩余履约量登记到事件。
func TestDw2AdminServiceStopMovesOrderToPendingClose(t *testing.T) {
	db := openTransferDB(t, "esports_dw2_admin_stop_close")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	admin := seedAdminAccount(t, db, "dw2stopadmin")

	// 双席：P1 已结束并上报 1，P2 仍在服务中 → 客服记名停工 P2 后整单应转待结单。
	p1 := seedPlayer(t, db, 8901, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	p2 := seedPlayer(t, db, 8902, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := dw2ServiceOrder(t, db, "DW2-STOP-1", 2, 4, 2, p1, p2, now)
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(admin.ID).SaveX(ctx)
	if _, err := finishPlayerService(ctx, db, p1.ID, order.ID, 1, 600, "", now); err != nil {
		t.Fatalf("P1 结束服务应成功：%v", err)
	}
	if _, err := setServicePauseByAdmin(ctx, db, admin.ID, order.ID, p2.ID, false, true, "陪玩失联", now); err != nil {
		t.Fatalf("客服代暂停应成功：%v", err)
	}
	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.FulfillmentStatus != esportsorder.FulfillmentStatusPendingClose {
		t.Fatalf("记名停工后整单应转待结单，got=%v", got.FulfillmentStatus)
	}
	events := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(order.ID),
		esportsorderevent.EventTypeEQ("order.pending_close_entered"),
	).AllX(ctx)
	if len(events) != 1 {
		t.Fatalf("进入待结单必须写独立事件留痕，got=%d", len(events))
	}
	if events[0].ActorType != "admin" || events[0].ActorID == "" {
		t.Fatalf("进入待结单事件必须记录操作客服：%+v", events[0])
	}
	if units := events[0].Details["unfulfilled_units"]; units != float64(3) {
		t.Fatalf("事件必须登记剩余履约量（4−1=3）：%+v", events[0].Details)
	}

	// 恢复服务：整单必须回到服务中，客服误操作可回退。
	if _, err := setServicePauseByAdmin(ctx, db, admin.ID, order.ID, p2.ID, false, false, "误操作恢复", now.Add(time.Minute)); err != nil {
		t.Fatalf("记名停工后应允许恢复：%v", err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.FulfillmentStatus != esportsorder.FulfillmentStatusInService {
		t.Fatalf("恢复后整单应回到服务中，got=%v", got.FulfillmentStatus)
	}
}

// §6.2-02 反例：仍有其他成员在服务中时，记名停工不得把整单转待结单。
func TestDw2AdminServiceStopKeepsInServiceWhileOthersServing(t *testing.T) {
	db := openTransferDB(t, "esports_dw2_admin_stop_keep")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	admin := seedAdminAccount(t, db, "dw2stopkeep")

	p1 := seedPlayer(t, db, 8911, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	p2 := seedPlayer(t, db, 8912, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := dw2ServiceOrder(t, db, "DW2-STOP-2", 2, 4, 2, p1, p2, now)
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(admin.ID).SaveX(ctx)

	if _, err := setServicePauseByAdmin(ctx, db, admin.ID, order.ID, p1.ID, false, true, "陪玩失联", now); err != nil {
		t.Fatalf("客服代暂停应成功：%v", err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.FulfillmentStatus != esportsorder.FulfillmentStatusInService {
		t.Fatalf("仍有成员在服务中时不得转待结单，got=%v", got.FulfillmentStatus)
	}
	// 本人自行暂停（非记名停工）同样不得把整单转待结单。
	if _, err := setPlayerServicePause(ctx, db, p2.ID, order.ID, true, "自己歇一会", now); err != nil {
		t.Fatalf("本人暂停应成功：%v", err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.FulfillmentStatus != esportsorder.FulfillmentStatusInService {
		t.Fatalf("本人暂停不得触发整单待结单，got=%v", got.FulfillmentStatus)
	}
}
