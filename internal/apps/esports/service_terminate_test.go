package esports

import (
	"errors"
	"strconv"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsorderevent"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsservicesegment"
)

// 终止服务段：客服侧提前结束服务段，必须登记「已履约量 / 未履约量 / 责任方」，
// 服务段保留已履约量并置 finished，责任认定以只增事件 order.service_terminated 留痕。
func TestTerminatePlayerServiceRecordsFulfilledUnfulfilledAndResponsibility(t *testing.T) {
	db := openPlayerTestDB(t, "esports_service_terminate")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	const adminID, otherAdminID = 88, 89

	player := seedPlayer(t, db, 811, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedGrabbingOrder(t, db, "admin-service-terminate-1") // 承诺服务量 1、单席位
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(adminID).ExecX(ctx)
	if _, err := grabPlayerOrder(ctx, db, player.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := startPlayerService(ctx, db, player.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}

	// 入参校验：责任方必填且须在枚举内，原因必填，已履约量不得为负。
	invalid := []struct {
		name           string
		units          int64
		responsibility string
		reason         string
	}{
		{"责任方留空", 1, "", "陪玩失联"},
		{"责任方非法", 1, "boss", "陪玩失联"},
		{"原因为空", 1, "player", "  "},
		{"已履约量为负", -1, "player", "陪玩失联"},
	}
	for _, item := range invalid {
		if _, err := terminatePlayerService(ctx, db, adminID, order.ID, player.ID, false,
			item.units, item.responsibility, item.reason, now.Add(time.Minute)); !errors.Is(err, errInvalidInput) {
			t.Fatalf("%s 应拒绝：%v", item.name, err)
		}
	}
	// 归属边界：非负责客服不可认定他人订单的责任。
	if _, err := terminatePlayerService(ctx, db, otherAdminID, order.ID, player.ID, false,
		1, "player", "越权终止", now.Add(time.Minute)); !errors.Is(err, errForbidden) {
		t.Fatalf("非负责客服应被拒：%v", err)
	}
	if got := db.EsportsServiceSegment.Query().OnlyX(ctx); got.Status != esportsservicesegment.StatusInService {
		t.Fatalf("越权终止不得改动服务段：%+v", got)
	}

	// 提前终止：陪玩实际完成 0 个单位 → 未履约量 = 承诺 1 − 已履约 0。
	result, err := terminatePlayerService(ctx, db, adminID, order.ID, player.ID, false,
		0, "player", "陪玩失联，提前结束", now.Add(30*time.Minute))
	if err != nil {
		t.Fatalf("终止服务段失败：%v", err)
	}
	if result.TerminatedUnits != 0 || result.PromisedUnits != 1 || result.UnfulfilledUnits != 1 ||
		result.FaultParty != "player" || result.SeatIndex != 1 || result.EffectiveSeconds != 1800 {
		t.Fatalf("终止结果不符：%+v", result)
	}

	// 服务段：置 finished、保留已履约量与有效时长、记录结束时间。
	segment := db.EsportsServiceSegment.Query().OnlyX(ctx)
	if segment.Status != esportsservicesegment.StatusFinished || segment.ReportedUnits != 0 ||
		segment.EndedAt == nil || segment.SystemSeconds != 1800 {
		t.Fatalf("服务段未被正确终止：%+v", segment)
	}

	// 只增事件留痕：三项产出 + 责任方 + 操作人。
	event := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(order.ID),
		esportsorderevent.EventTypeEQ("order.service_terminated"),
	).OnlyX(ctx)
	if event.ActorType != "admin" || event.ActorID != strconv.Itoa(adminID) {
		t.Fatalf("终止事件未按管理员留痕：%+v", event)
	}
	if event.Details["terminated_units"] != float64(0) ||
		event.Details["unfulfilled_units"] != float64(1) ||
		event.Details["promised_service_units"] != float64(1) ||
		event.Details["fault_party"] != "player" ||
		event.Details["reason"] != "陪玩失联，提前结束" {
		t.Fatalf("终止事件 details 不完整：%+v", event.Details)
	}

	// 唯一席位服务段结束后订单进入待结单：沿用既有口径，订单保留已履约量。
	reloaded := db.EsportsOrder.GetX(ctx, order.ID)
	if reloaded.FulfillmentStatus != esportsorder.FulfillmentStatusPendingClose {
		t.Fatalf("全部席位结束后应进入待结单：%s", reloaded.FulfillmentStatus)
	}

	// 已结束的服务段不可重复终止（重复登记会让责任认定与已履约量被二次改写）。
	if _, err := terminatePlayerService(ctx, db, adminID, order.ID, player.ID, false,
		1, "player", "重复终止", now.Add(time.Hour)); !errors.Is(err, errConflict) {
		t.Fatalf("重复终止应冲突：%v", err)
	}
}

// 终止也须走统一存单守卫：冻结中的订单不得被改写履约量与责任认定。
func TestTerminatePlayerServiceRejectsFrozenOrder(t *testing.T) {
	db := openPlayerTestDB(t, "esports_service_terminate_frozen")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 11, 0, 0, 0, time.UTC)
	const adminID = 90

	player := seedPlayer(t, db, 812, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedGrabbingOrder(t, db, "admin-service-terminate-2")
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(adminID).ExecX(ctx)
	if _, err := grabPlayerOrder(ctx, db, player.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := startPlayerService(ctx, db, player.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}
	db.EsportsOrder.UpdateOneID(order.ID).SetDepositStatus(esportsorder.DepositStatusFrozen).ExecX(ctx)

	if _, err := terminatePlayerService(ctx, db, adminID, order.ID, player.ID, false,
		1, "player", "冻结期终止", now.Add(time.Minute)); !errors.Is(err, errOrderFrozen) {
		t.Fatalf("冻结单应拒绝终止服务段：%v", err)
	}
	if got := db.EsportsServiceSegment.Query().OnlyX(ctx); got.Status != esportsservicesegment.StatusInService {
		t.Fatalf("冻结期终止不得改动服务段：%+v", got)
	}
}

// 暂停中终止：在途暂停时长必须一并落库。服务段一旦不再是 paused，serviceTimingAt 就不再补这段时长，
// 漏写会让有效时长凭空变长、暂停时长丢失。
func TestTerminatePlayerServiceFromPausedKeepsPausedSeconds(t *testing.T) {
	db := openPlayerTestDB(t, "esports_service_terminate_paused")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	const adminID = 91

	player := seedPlayer(t, db, 813, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedGrabbingOrder(t, db, "admin-service-terminate-3")
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(adminID).ExecX(ctx)
	if _, err := grabPlayerOrder(ctx, db, player.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := startPlayerService(ctx, db, player.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}
	// 服务 10 分钟后暂停，暂停 20 分钟后终止。
	if _, err := setPlayerServicePause(ctx, db, player.ID, order.ID, true, "中场暂停", now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	result, err := terminatePlayerService(ctx, db, adminID, order.ID, player.ID, false,
		1, "customer", "客户要求结束", now.Add(30*time.Minute))
	if err != nil {
		t.Fatalf("暂停中终止失败：%v", err)
	}
	if result.EffectiveSeconds != 600 || result.PausedSeconds != 1200 {
		t.Fatalf("暂停中终止的时长口径不符：%+v", result)
	}
	if result.UnfulfilledUnits != 0 || result.TerminatedUnits != 1 {
		t.Fatalf("已履约量应满足承诺量：%+v", result)
	}
	segment := db.EsportsServiceSegment.Query().OnlyX(ctx)
	if segment.Status != esportsservicesegment.StatusFinished || segment.PausedSeconds != 1200 ||
		segment.SystemSeconds != 600 || segment.PausedAt != nil || segment.ReportedUnits != 1 {
		t.Fatalf("暂停中终止未正确落库：%+v", segment)
	}
}
