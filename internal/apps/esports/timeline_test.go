package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/migrate"

	"go-admin/internal/ent/enttest"
)

// §5.9 时间线埋点：入池 / 首次派出 / 首次接单 / 开工各自只写一次，重复流转不覆盖首个时间点。
func TestOrderTimelineMarksWrittenOnce(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_order_timeline")
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	order := seedPaidOrder(t, db, "timeline-1", 1)
	player := seedPlayer(t, db, 9201, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	// 首次进入待派单池。
	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = onOrderEnteredPendingDispatch(ctx, tx, order.ID, now); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.FirstPooledAt == nil || !got.FirstPooledAt.Equal(now.UTC()) {
		t.Fatalf("首次入池时间未写入：%+v", got.FirstPooledAt)
	}

	// 二次入池（例如撤回后回退）不覆盖首个时间点。
	tx2, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = onOrderEnteredPendingDispatch(ctx, tx2, order.ID, now.Add(time.Hour)); err != nil {
		_ = tx2.Rollback()
		t.Fatal(err)
	}
	if err = tx2.Commit(); err != nil {
		t.Fatal(err)
	}
	if got = db.EsportsOrder.GetX(ctx, order.ID); !got.FirstPooledAt.Equal(now.UTC()) {
		t.Fatalf("首次入池时间被覆盖：%+v", got.FirstPooledAt)
	}

	// 首次派出邀请。
	if _, err = assignDispatchSeat(ctx, db, order.ID, 1, player.ID, 11, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got = db.EsportsOrder.GetX(ctx, order.ID)
	if got.FirstInvitedAt == nil || !got.FirstInvitedAt.Equal(now.Add(time.Minute).UTC()) {
		t.Fatalf("首次派出时间未写入：%+v", got.FirstInvitedAt)
	}

	// 首次有效接单（陪玩接受邀请）。
	seat := db.EsportsDispatchSeat.Query().OnlyX(ctx)
	if err = respondPlayerInvite(ctx, db, player.ID, seat.ID, true, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got = db.EsportsOrder.GetX(ctx, order.ID)
	if got.FirstAcceptedAt == nil || !got.FirstAcceptedAt.Equal(now.Add(2*time.Minute).UTC()) {
		t.Fatalf("首次接单时间未写入：%+v", got.FirstAcceptedAt)
	}

	// 开工。
	if _, err = startPlayerService(ctx, db, player.ID, order.ID, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got = db.EsportsOrder.GetX(ctx, order.ID)
	if got.ServiceStartedAt == nil || !got.ServiceStartedAt.Equal(now.Add(3*time.Minute).UTC()) {
		t.Fatalf("开工时间未写入：%+v", got.ServiceStartedAt)
	}
}

// 派单效率报表按时间线埋点派生四类平均等待时长（缺埋点的订单不计入样本）。
func TestDispatchEfficiencyTimelineAverages(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_efficiency_timeline")
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	// 报表按 created_at 过滤，创建时显式对齐到测试时基（UTC），避免时区导致的区间失配。
	order := db.EsportsOrder.Create().SetOrderNo("eff-timeline-1").SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).
		SetCreatedAt(now).SaveX(ctx)
	player := seedPlayer(t, db, 9301, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = onOrderEnteredPendingDispatch(ctx, tx, order.ID, now); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = assignDispatchSeat(ctx, db, order.ID, 1, player.ID, 11, now.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	seat := db.EsportsDispatchSeat.Query().OnlyX(ctx)
	if err = respondPlayerInvite(ctx, db, player.ID, seat.ID, true, now.Add(90*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = startPlayerService(ctx, db, player.ID, order.ID, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}

	report, err := dispatchEfficiencyReport(ctx, db, reportFilter{
		Start: now.Add(-time.Hour), End: now.Add(time.Hour), Period: "daily",
	})
	if err != nil {
		t.Fatal(err)
	}
	summary, ok := report["summary"].(map[string]any)
	if !ok {
		t.Fatalf("报表缺少 summary：%+v", report)
	}
	checks := map[string]int64{
		"avg_pool_to_invite_seconds":   30,
		"avg_invite_to_accept_seconds": 60,
		"avg_accept_to_start_seconds":  90,
		"avg_pool_to_start_seconds":    180,
	}
	for key, want := range checks {
		got, ok := summary[key].(int64)
		if !ok {
			t.Fatalf("summary 缺少 %s：%+v", key, summary)
		}
		if got != want {
			t.Fatalf("%s 期望 %d，实际 %d", key, want, got)
		}
	}
}

// 保证报表结构在无数据时也含四类等待时长键（前端可稳定渲染）。
func TestDispatchEfficiencySummaryKeysOnEmptyData(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_dispatch_efficiency_empty?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()

	report, err := dispatchEfficiencyReport(t.Context(), db, reportFilter{
		Start: time.Now().Add(-time.Hour), End: time.Now().Add(time.Hour), Period: "daily",
	})
	if err != nil {
		t.Fatal(err)
	}
	summary := report["summary"].(map[string]any)
	for _, key := range []string{
		"avg_pool_to_invite_seconds", "avg_invite_to_accept_seconds",
		"avg_accept_to_start_seconds", "avg_pool_to_start_seconds",
	} {
		if _, ok := summary[key]; !ok {
			t.Fatalf("空数据下 summary 缺少 %s：%+v", key, summary)
		}
	}
}
