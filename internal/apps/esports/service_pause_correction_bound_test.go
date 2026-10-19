package esports

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsorderevent"
	"go-admin/internal/ent/esportsplayer"
)

// P1-14 服务时长更正上界：更正后的有效秒数必须满足 0 < 新时长 <= min(服务段墙钟跨度, 订单时长上限×系数)；
// 越界一律拒绝且错误指明上界，正常范围内的更正成功并留痕（前后值、操作人、上下界进事件）。
func TestServiceTimingCorrectionBoundRejectsOutOfRange(t *testing.T) {
	db := openPlayerTestDB(t, "esports_service_timing_bound")
	defer db.Close()
	ctx := t.Context()
	base := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	const adminID = 91

	player := seedPlayer(t, db, 801, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedGrabbingOrder(t, db, "service-timing-bound")
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(adminID).ExecX(ctx)
	if _, err := grabPlayerOrder(ctx, db, player.ID, order.ID, base); err != nil {
		t.Fatal(err)
	}
	if _, err := startPlayerService(ctx, db, player.ID, order.ID, base); err != nil {
		t.Fatal(err)
	}
	// 墙钟跨度 = 600 秒；订单未配置标准时长（seedGrabbingOrder 默认 0），故只受墙钟约束。
	if _, err := finishPlayerService(ctx, db, player.ID, order.ID, 1, 600, "proof", base.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	correctAt := base.Add(11 * time.Minute)

	// 0 秒不再被接受（原实现允许 0）。
	if _, err := correctServiceTiming(ctx, db, adminID, order.ID, player.ID, false, 0, 0, "player", "改成零时长", correctAt); !errors.Is(err, errInvalidInput) {
		t.Fatalf("有效秒数为 0 应拒绝：%v", err)
	}
	// 超过墙钟跨度被拒，且错误里给出上界 600。
	_, err := correctServiceTiming(ctx, db, adminID, order.ID, player.ID, false, 601, 0, "player", "超过墙钟跨度", correctAt)
	if !errors.Is(err, errInvalidInput) {
		t.Fatalf("超过墙钟跨度应拒绝：%v", err)
	}
	if !strings.Contains(err.Error(), "600") {
		t.Fatalf("错误未给出墙钟上界 600 秒：%v", err)
	}
	// 暂停秒数超过墙钟跨度同样不可能发生。
	if _, err := correctServiceTiming(ctx, db, adminID, order.ID, player.ID, false, 300, 601, "player", "暂停超墙钟", correctAt); !errors.Is(err, errInvalidInput) {
		t.Fatalf("暂停秒数超过墙钟跨度应拒绝：%v", err)
	}

	// 订单标准时长 2 分钟 → 上界 = min(600, 120×1.2=144) = 144 秒。
	db.EsportsOrder.UpdateOneID(order.ID).SetServiceDurationMinutes(2).ExecX(ctx)
	_, err = correctServiceTiming(ctx, db, adminID, order.ID, player.ID, false, 200, 0, "player", "超过订单时长上限", correctAt)
	if !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "144") {
		t.Fatalf("超过订单时长上限应拒绝并给出 144 秒上界：%v", err)
	}

	// 范围内的更正成功并留痕。
	result, err := correctServiceTiming(ctx, db, adminID, order.ID, player.ID, false, 120, 10, "player", "按实际服务时长更正", correctAt)
	if err != nil {
		t.Fatalf("范围内更正应成功：%v", err)
	}
	if result.EffectiveAfter != 120 || result.PausedAfter != 10 || result.Reason != "按实际服务时长更正" {
		t.Fatalf("更正结果不正确：%+v", result)
	}
	event := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(order.ID),
		esportsorderevent.EventTypeEQ("order.service_timing_corrected"),
	).OnlyX(ctx)
	if event.ActorType != "admin" || event.ActorID != strconv.Itoa(adminID) {
		t.Fatalf("更正事件未按管理员留痕：%+v", event)
	}
	if event.Details["effective_seconds_before"] != float64(600) ||
		event.Details["effective_seconds_after"] != float64(120) ||
		event.Details["wall_clock_span_seconds"] != float64(600) ||
		event.Details["effective_seconds_upper_bound"] != float64(144) ||
		event.Details["reason"] != "按实际服务时长更正" {
		t.Fatalf("更正事件未记录前后值与上界：%+v", event.Details)
	}
}
