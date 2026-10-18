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

// 客服代操作（§3.3）：订单须在服务中、目标陪玩须为本单服务成员、理由必填；
// 事件以 actor_type=admin 留痕并记录被代操作的陪玩。
func TestAdminServicePauseResumeOnBehalfOfPlayer(t *testing.T) {
	db := openPlayerTestDB(t, "esports_service_pause_admin")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	const adminID = 77

	player := seedPlayer(t, db, 701, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	other := seedPlayer(t, db, 702, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedGrabbingOrder(t, db, "admin-service-pause-1")
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(adminID).ExecX(ctx)
	if _, err := grabPlayerOrder(ctx, db, player.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := startPlayerService(ctx, db, player.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}

	// 理由必填。
	if _, err := setServicePauseByAdmin(ctx, db, adminID, order.ID, player.ID, false, true, "  ", now.Add(5*time.Second)); !errors.Is(err, errInvalidInput) {
		t.Fatalf("空理由应拒绝：%v", err)
	}
	// 非本单成员被拒（该陪玩在本单没有服务段）。
	if _, err := setServicePauseByAdmin(ctx, db, adminID, order.ID, other.ID, false, true, "越权代操作", now.Add(5*time.Second)); !errors.Is(err, errForbidden) {
		t.Fatalf("非本单成员应拒绝：%v", err)
	}

	paused, err := setServicePauseByAdmin(ctx, db, adminID, order.ID, player.ID, false, true, "客户报障，客服代暂停", now.Add(10*time.Second))
	if err != nil || paused.Status != "paused" || paused.EffectiveSeconds != 10 {
		t.Fatalf("客服代暂停失败：%+v, %v", paused, err)
	}
	// 重复暂停应冲突，避免重复计停工。
	if _, err := setServicePauseByAdmin(ctx, db, adminID, order.ID, player.ID, false, true, "重复", now.Add(11*time.Second)); !errors.Is(err, errConflict) {
		t.Fatalf("重复暂停应冲突：%v", err)
	}

	resumed, err := setServicePauseByAdmin(ctx, db, adminID, order.ID, player.ID, false, false, "客户已恢复", now.Add(40*time.Second))
	if err != nil || resumed.Status != "in_service" || resumed.PausedSeconds != 30 {
		t.Fatalf("客服代恢复失败：%+v, %v", resumed, err)
	}

	events := countEventsByType(t, db)
	if events["order.service_paused"] != 1 || events["order.service_resumed"] != 1 {
		t.Fatalf("代操作事件缺失：%+v", events)
	}
	pausedEvent := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(order.ID),
		esportsorderevent.EventTypeEQ("order.service_paused"),
	).OnlyX(ctx)
	if pausedEvent.ActorType != "admin" || pausedEvent.ActorID != strconv.Itoa(adminID) ||
		seatIndexOf(pausedEvent.Details) != 1 ||
		pausedEvent.Details["on_behalf_of_player_id"] != float64(player.ID) {
		t.Fatalf("代操作事件未按管理员留痕：%+v", pausedEvent)
	}
}

// 争议更正（§3.3）：已结束服务段的有效 / 暂停秒数被留痕更正，事件记录前后值、停工量、责任方与原因；
// 非法入参被拒，进行中的服务段不可直接更正。
func TestServiceTimingCorrectionWritesBeforeAfterAndResponsibility(t *testing.T) {
	db := openPlayerTestDB(t, "esports_service_timing_correction")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	const adminID = 88

	player := seedPlayer(t, db, 703, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedGrabbingOrder(t, db, "admin-service-correct-1")
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(adminID).ExecX(ctx)
	if _, err := grabPlayerOrder(ctx, db, player.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := startPlayerService(ctx, db, player.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}

	// 进行中的服务段不允许直接更正，应走客服代暂停 / 恢复路径。
	if _, err := correctServiceTiming(ctx, db, adminID, order.ID, player.ID, false, 100, 10, "player", "越权更正", now.Add(10*time.Second)); !errors.Is(err, errConflict) {
		t.Fatalf("进行中服务段更正应冲突：%v", err)
	}

	if _, err := setPlayerServicePause(ctx, db, player.ID, order.ID, true, "中场暂停", now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := setPlayerServicePause(ctx, db, player.ID, order.ID, false, "", now.Add(40*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := finishPlayerService(ctx, db, player.ID, order.ID, 1, 40, "proof", now.Add(70*time.Second)); err != nil {
		t.Fatal(err)
	}

	// 非法入参。
	if _, err := correctServiceTiming(ctx, db, adminID, order.ID, player.ID, false, -1, 0, "player", "负值", now.Add(80*time.Second)); !errors.Is(err, errInvalidInput) {
		t.Fatalf("负有效秒数应拒绝：%v", err)
	}
	if _, err := correctServiceTiming(ctx, db, adminID, order.ID, player.ID, false, 100, 0, "player", "", now.Add(80*time.Second)); !errors.Is(err, errInvalidInput) {
		t.Fatalf("空原因应拒绝：%v", err)
	}
	if _, err := correctServiceTiming(ctx, db, adminID, order.ID, player.ID, false, 100, 0, "boss", "责任方非法", now.Add(80*time.Second)); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法责任方应拒绝：%v", err)
	}

	result, err := correctServiceTiming(ctx, db, adminID, order.ID, player.ID, false, 500, 120, "player", "客户投诉时长，核实后更正", now.Add(90*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if result.EffectiveBefore != 40 || result.EffectiveAfter != 500 ||
		result.PausedBefore != 30 || result.PausedAfter != 120 ||
		result.DowntimeSeconds != 120 || result.DowntimeCount != 1 ||
		result.Responsibility != "player" {
		t.Fatalf("更正结果不正确：%+v", result)
	}
	segment := db.EsportsServiceSegment.Query().OnlyX(ctx)
	if segment.SystemSeconds != 500 || segment.PausedSeconds != 120 || segment.Status != esportsservicesegment.StatusFinished {
		t.Fatalf("更正未落库：%+v", segment)
	}
	corrected := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(order.ID),
		esportsorderevent.EventTypeEQ("order.service_timing_corrected"),
	).OnlyX(ctx)
	if corrected.ActorType != "admin" || corrected.ActorID != strconv.Itoa(adminID) {
		t.Fatalf("更正事件未按管理员留痕：%+v", corrected)
	}
	if corrected.Details["effective_seconds_before"] != float64(40) ||
		corrected.Details["effective_seconds_after"] != float64(500) ||
		corrected.Details["paused_seconds_before"] != float64(30) ||
		corrected.Details["paused_seconds_after"] != float64(120) ||
		corrected.Details["downtime_seconds"] != float64(120) ||
		corrected.Details["downtime_count"] != float64(1) ||
		corrected.Details["responsibility"] != "player" ||
		corrected.Details["reason"] != "客户投诉时长，核实后更正" {
		t.Fatalf("更正事件 details 不完整：%+v", corrected.Details)
	}
}

// 断线超阈值自动暂停（§3.3）：配置关闭或阈值内不处理；超阈值时服务段被置 paused 并写系统事件；
// 重复运行幂等。
func TestDisconnectAutoPauseInServiceSegments(t *testing.T) {
	db := openPlayerTestDB(t, "esports_service_disconnect_pause")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	stale := seedPlayer(t, db, 704, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	fresh := seedPlayer(t, db, 705, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	staleOrder := seedGrabbingOrder(t, db, "disconnect-pause-stale")
	freshOrder := seedGrabbingOrder(t, db, "disconnect-pause-fresh")
	if _, err := grabPlayerOrder(ctx, db, stale.ID, staleOrder.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := startPlayerService(ctx, db, stale.ID, staleOrder.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := grabPlayerOrder(ctx, db, fresh.ID, freshOrder.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := startPlayerService(ctx, db, fresh.ID, freshOrder.ID, now); err != nil {
		t.Fatal(err)
	}
	db.EsportsPlayer.UpdateOneID(stale.ID).SetLastHeartbeatAt(now.Add(-30 * time.Minute)).SaveX(ctx)
	db.EsportsPlayer.UpdateOneID(fresh.ID).SetLastHeartbeatAt(now.Add(-1 * time.Minute)).SaveX(ctx)

	// 阈值未配置（默认 0）即关闭，不处理。
	if count, err := autoPauseDisconnectedPlayers(ctx, db, now, 100); err != nil || count != 0 {
		t.Fatalf("关闭阈值时不应处理：%d, %v", count, err)
	}

	db.SystemConfig.Create().SetKey(serviceDisconnectPauseKey).SetName("断线自动暂停阈值（分钟）").
		SetValue("5").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)

	count, err := autoPauseDisconnectedPlayers(ctx, db, now, 100)
	if err != nil || count != 1 {
		t.Fatalf("断线自动暂停应处理 1 段：%d, %v", count, err)
	}
	staleSegment := db.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(staleOrder.ID)).OnlyX(ctx)
	if staleSegment.Status != esportsservicesegment.StatusPaused ||
		staleSegment.PausedAt == nil || !staleSegment.PausedAt.UTC().Equal(now) {
		t.Fatalf("断线服务段未暂停：%+v", staleSegment)
	}
	// 阈值内的陪玩不受影响。
	freshSegment := db.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(freshOrder.ID)).OnlyX(ctx)
	if freshSegment.Status != esportsservicesegment.StatusInService {
		t.Fatalf("阈值内服务段不应暂停：%+v", freshSegment)
	}

	autoEvent := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(staleOrder.ID),
		esportsorderevent.EventTypeEQ("order.service_paused"),
	).OnlyX(ctx)
	if autoEvent.ActorType != "system" || autoEvent.ActorID != serviceDisconnectActor ||
		autoEvent.Details["reason"] != serviceDisconnectReason ||
		autoEvent.Details["on_behalf_of_player_id"] != float64(stale.ID) {
		t.Fatalf("断线自动暂停事件不正确：%+v", autoEvent)
	}

	// 重复运行幂等：服务段已 paused，不再处理。
	if count, err := autoPauseDisconnectedPlayers(ctx, db, now, 100); err != nil || count != 0 {
		t.Fatalf("重复运行应幂等：%d, %v", count, err)
	}
	// 订单状态变化后（如已结束）不再处理。
	if _, err := finishPlayerService(ctx, db, stale.ID, staleOrder.ID, 1, 30, "proof", now.Add(time.Minute)); !errors.Is(err, errConflict) {
		t.Fatalf("暂停中结束应冲突：%v", err)
	}
	if order := db.EsportsOrder.GetX(ctx, staleOrder.ID); order.FulfillmentStatus != esportsorder.FulfillmentStatusInService {
		t.Fatalf("暂停不应改变订单履约状态：%+v", order.FulfillmentStatus)
	}
}
