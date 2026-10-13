package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsservicesegment"
	"go-admin/internal/ent/migrate"
)

// seedGrabbingOrder 直接构造一张已付款、抢单中的单席位订单。
func seedGrabbingOrder(t *testing.T, db *ent.Client, orderNo string) *ent.EsportsOrder {
	t.Helper()
	return db.EsportsOrder.Create().
		SetOrderNo(orderNo).SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetPaymentDeadline(time.Now().UTC().Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusGrabbing).
		SaveX(t.Context())
}

func openPlayerTestDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	return enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
}

// 管理端改档与陪玩自助上下线：字段生效、版本递增，非法枚举被拦截。
func TestPlayerUpdateAndPresence(t *testing.T) {
	db := openPlayerTestDB(t, "esports_player_update")
	defer db.Close()
	ctx := t.Context()
	player := seedPlayer(t, db, 301, esportsplayer.QualificationQualified, esportsplayer.PresenceOffline)

	updated, err := updatePlayer(ctx, db, player.ID, playerUpdateInput{
		DisplayName: "高手", Qualification: "suspended", Presence: "protected_abnormal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.DisplayName != "高手" || updated.Qualification != esportsplayer.QualificationSuspended ||
		updated.Presence != esportsplayer.PresenceProtectedAbnormal || updated.Version != player.Version+1 {
		t.Fatalf("改档未生效：%+v", updated)
	}
	if _, err := updatePlayer(ctx, db, player.ID, playerUpdateInput{
		DisplayName: "高手", Qualification: "vip", Presence: "online",
	}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法资格枚举未拦截：%v", err)
	}

	online, err := setPlayerPresence(ctx, db, player.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if online.Presence != esportsplayer.PresenceOnline || online.Version != updated.Version+1 {
		t.Fatalf("上线未生效：%+v", online)
	}
	offline, err := setPlayerPresence(ctx, db, player.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if offline.Presence != esportsplayer.PresenceOffline {
		t.Fatalf("下线未生效：%+v", offline)
	}
}

// 陪玩回应邀请：接受使订单接满转 accepted，拒绝在无剩余席位时退回待派单；越权与重复回应被拦截。
func TestPlayerInviteRespond(t *testing.T) {
	db := openPlayerTestDB(t, "esports_player_invite")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	player := seedPlayer(t, db, 401, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	other := seedPlayer(t, db, 402, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	order := seedPaidOrder(t, db, "player-invite-accept", 1)
	seat, err := assignDispatchSeat(ctx, db, order.ID, 1, player.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := respondPlayerInvite(ctx, db, other.ID, seat.ID, true, now); !errors.Is(err, errForbidden) {
		t.Fatalf("非本人席位未拦截：%v", err)
	}
	if err := respondPlayerInvite(ctx, db, player.ID, seat.ID, true, now); err != nil {
		t.Fatal(err)
	}
	gotSeat := db.EsportsDispatchSeat.GetX(ctx, seat.ID)
	if gotSeat.Status != esportsdispatchseat.StatusAccepted || gotSeat.RespondedAt == nil {
		t.Fatalf("席位未转接单：%+v", gotSeat)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.FulfillmentStatus != esportsorder.FulfillmentStatusAccepted {
		t.Fatalf("订单未转已接单：%+v", got)
	}
	if countEventsByType(t, db)["order.seat_accepted"] != 1 {
		t.Fatal("缺少 order.seat_accepted 事件")
	}
	if err := respondPlayerInvite(ctx, db, player.ID, seat.ID, true, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复回应未拦截：%v", err)
	}

	// 拒绝：唯一席位被拒后订单退回待派单。
	rejectOrder := seedPaidOrder(t, db, "player-invite-reject", 1)
	rejectSeat, err := assignDispatchSeat(ctx, db, rejectOrder.ID, 1, player.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := respondPlayerInvite(ctx, db, player.ID, rejectSeat.ID, false, now); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsDispatchSeat.GetX(ctx, rejectSeat.ID); got.Status != esportsdispatchseat.StatusRejected {
		t.Fatalf("席位未转拒单：%+v", got)
	}
	if got := db.EsportsOrder.GetX(ctx, rejectOrder.ID); got.FulfillmentStatus != esportsorder.FulfillmentStatusPendingDispatch {
		t.Fatalf("订单未退回待派单：%+v", got)
	}
	if countEventsByType(t, db)["order.seat_rejected"] != 1 {
		t.Fatal("缺少 order.seat_rejected 事件")
	}

	// 过期邀请不可再回应。
	expireOrder := seedPaidOrder(t, db, "player-invite-expired", 1)
	expireSeat, err := assignDispatchSeat(ctx, db, expireOrder.ID, 1, player.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := respondPlayerInvite(ctx, db, player.ID, expireSeat.ID, true, now.Add(20*time.Minute)); !errors.Is(err, errConflict) {
		t.Fatalf("过期邀请未拦截：%v", err)
	}
}

// 抢单：合格陪玩抢下单席位订单转 accepted，重复抢单与非合格陪玩被拦截。
func TestPlayerGrabOrder(t *testing.T) {
	db := openPlayerTestDB(t, "esports_player_grab")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	// 人工下线仍可抢单，故 presence 设为 offline。
	player := seedPlayer(t, db, 501, esportsplayer.QualificationQualified, esportsplayer.PresenceOffline)
	pending := seedPlayer(t, db, 502, esportsplayer.QualificationPending, esportsplayer.PresenceOnline)

	order := seedGrabbingOrder(t, db, "player-grab-1")
	grabbed, err := grabPlayerOrder(ctx, db, player.ID, order.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if grabbed.FulfillmentStatus != esportsorder.FulfillmentStatusAccepted {
		t.Fatalf("订单未转已接单：%+v", grabbed)
	}
	seat := db.EsportsDispatchSeat.Query().OnlyX(ctx)
	if seat.Status != esportsdispatchseat.StatusAccepted || seat.Source != esportsdispatchseat.SourceGrab ||
		seat.PlayerID == nil || *seat.PlayerID != player.ID {
		t.Fatalf("席位抢单结果不正确：%+v", seat)
	}
	if countEventsByType(t, db)["order.grabbed"] != 1 {
		t.Fatal("缺少 order.grabbed 事件")
	}
	if _, err := grabPlayerOrder(ctx, db, player.ID, order.ID, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复抢单未拦截：%v", err)
	}

	otherOrder := seedGrabbingOrder(t, db, "player-grab-2")
	if _, err := grabPlayerOrder(ctx, db, pending.ID, otherOrder.ID, now); !errors.Is(err, errForbidden) {
		t.Fatalf("非合格陪玩抢单未拦截：%v", err)
	}
}

// 服务闭环：accepted→in_service→pending_close，服务段记下上报服务量与系统秒数；重复开始被拦截。
func TestPlayerServiceFlow(t *testing.T) {
	db := openPlayerTestDB(t, "esports_player_service")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	player := seedPlayer(t, db, 601, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	order := seedGrabbingOrder(t, db, "player-service-1")
	if _, err := grabPlayerOrder(ctx, db, player.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}
	started, err := startPlayerService(ctx, db, player.ID, order.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if started.FulfillmentStatus != esportsorder.FulfillmentStatusInService {
		t.Fatalf("订单未转服务中：%+v", started)
	}
	segment := db.EsportsServiceSegment.Query().OnlyX(ctx)
	if segment.Status != esportsservicesegment.StatusInService || segment.SeatIndex != 1 {
		t.Fatalf("服务段未创建：%+v", segment)
	}
	if _, err := startPlayerService(ctx, db, player.ID, order.ID, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复开始未拦截：%v", err)
	}

	finished, err := finishPlayerService(ctx, db, player.ID, order.ID, 3, "ref-1", now.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if finished.FulfillmentStatus != esportsorder.FulfillmentStatusPendingClose {
		t.Fatalf("订单未转待结单：%+v", finished)
	}
	segment = db.EsportsServiceSegment.GetX(ctx, segment.ID)
	if segment.Status != esportsservicesegment.StatusFinished || segment.EndedAt == nil ||
		segment.ReportedUnits != 3 || segment.SystemSeconds != 30 {
		t.Fatalf("服务段结束记录不正确：%+v", segment)
	}
	if countEventsByType(t, db)["order.service_finished"] != 1 {
		t.Fatal("缺少 order.service_finished 事件")
	}
	if _, err := finishPlayerService(ctx, db, player.ID, order.ID, 1, "", now); !errors.Is(err, errConflict) {
		t.Fatalf("重复结束未拦截：%v", err)
	}
}

// 多人单：两席位成员都能各自开始服务段（第二人开工时订单已是 in_service），
// 且只有全部成员结束后订单才转待结单。
func TestPlayerMultiSeatServiceFlow(t *testing.T) {
	db := openPlayerTestDB(t, "esports_player_multi_service")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	a := seedPlayer(t, db, 701, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	b := seedPlayer(t, db, 702, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	order := seedPaidOrder(t, db, "player-multi-service", 2)
	seatA, err := assignDispatchSeat(ctx, db, order.ID, 1, a.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	seatB, err := assignDispatchSeat(ctx, db, order.ID, 2, b.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := respondPlayerInvite(ctx, db, a.ID, seatA.ID, true, now); err != nil {
		t.Fatal(err)
	}
	if err := respondPlayerInvite(ctx, db, b.ID, seatB.ID, true, now); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.FulfillmentStatus != esportsorder.FulfillmentStatusAccepted {
		t.Fatalf("两席位接满后订单未转已接单：%+v", got)
	}
	if _, err := startPlayerService(ctx, db, a.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := startPlayerService(ctx, db, b.ID, order.ID, now); err != nil {
		t.Fatalf("第二席位成员无法开始服务：%v", err)
	}
	if n := db.EsportsServiceSegment.Query().CountX(ctx); n != 2 {
		t.Fatalf("服务段数量应为 2，实际 %d", n)
	}
	if _, err := finishPlayerService(ctx, db, a.ID, order.ID, 1, "", now); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.FulfillmentStatus != esportsorder.FulfillmentStatusInService {
		t.Fatalf("尚有成员未结束就转待结单：%+v", got)
	}
	if _, err := finishPlayerService(ctx, db, b.ID, order.ID, 1, "", now); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.FulfillmentStatus != esportsorder.FulfillmentStatusPendingClose {
		t.Fatalf("全部成员结束后未转待结单：%+v", got)
	}
}
