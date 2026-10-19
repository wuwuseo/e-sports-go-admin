package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsorderaddon"
	"go-admin/internal/ent/migrate"
)

// P1-16 加钟收入必须进营收与结单净额，且与加钟分成同一时点口径：
// 加钟有独立收款记录（EsportsOrderAddon.payment_status=paid 的 paid_cents），
// 未收款 / 已取消的加钟不得计入营收（否则与「收款成功才计提分成」的成本侧不对称）。
func TestReportRevenueIncludesAddonRevenue(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report_addon_revenue?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)

	order := seedReportOrder(t, db, "addon-report-1", day.Add(time.Hour), 11, 10000, 0,
		esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusCompleted)
	// 已确认并已收款：计入营收。
	db.EsportsOrderAddon.Create().SetAddonNo("addon-report-paid").SetOrderID(order.ID).
		SetAddedCents(4000).SetAddedUnits(1).
		SetStatus(esportsorderaddon.StatusConfirmed).
		SetPaymentStatus(esportsorderaddon.PaymentStatusPaid).
		SetPaidCents(4000).SetPaidAt(day.Add(2 * time.Hour)).
		SetCreatedAt(day.Add(time.Hour)).SaveX(ctx)
	// 已确认但未收款：不计入（尚无实收，也未计提分成）。
	db.EsportsOrderAddon.Create().SetAddonNo("addon-report-unpaid").SetOrderID(order.ID).
		SetAddedCents(2000).SetStatus(esportsorderaddon.StatusConfirmed).
		SetPaymentStatus(esportsorderaddon.PaymentStatusUnpaid).
		SetCreatedAt(day.Add(2 * time.Hour)).SaveX(ctx)
	// 已取消：不计入。
	db.EsportsOrderAddon.Create().SetAddonNo("addon-report-cancelled").SetOrderID(order.ID).
		SetAddedCents(5000).SetStatus(esportsorderaddon.StatusCancelled).
		SetPaymentStatus(esportsorderaddon.PaymentStatusUnpaid).
		SetCreatedAt(day.Add(3 * time.Hour)).SaveX(ctx)

	filter := reportFilter{Start: day, End: day.AddDate(0, 0, 1), Period: "daily"}
	result, err := ownerOverviewReport(ctx, db, filter)
	if err != nil {
		t.Fatal(err)
	}
	summary := result["summary"].(map[string]any)
	if got := summary["revenue_cents"].(int64); got != 14000 {
		t.Fatalf("营收未计入已收款加钟（应付 10000 + 加钟 4000）：%d", got)
	}
	if got := summary["addon_revenue_cents"].(int64); got != 4000 {
		t.Fatalf("加钟收入单列值不正确：%d", got)
	}
	if got := summary["net_cents"].(int64); got != 14000 {
		t.Fatalf("结单净额未计入加钟收入：%d", got)
	}
	// 时间轴分段与汇总同口径。
	buckets := result["buckets"].([]reportBucket)
	if len(buckets) != 1 || buckets[0].RevenueCents != 14000 {
		t.Fatalf("时间轴分段未计入加钟收入：%+v", buckets)
	}

	// 客服业绩的营收口径必须与经营总览一致（加钟收入归业绩归属客服）。
	performance, err := csPerformanceReport(ctx, db, filter)
	if err != nil {
		t.Fatal(err)
	}
	rows := performance["rows"].([]csPerformanceRow)
	if len(rows) != 1 || rows[0].AdminID != 11 || rows[0].RevenueCents != 14000 ||
		rows[0].NetCents != 14000 {
		t.Fatalf("客服业绩未计入加钟收入：%+v", rows)
	}
}

// 加钟收入只统计命中订单筛选的订单：区间外的订单上的已收款加钟不得计入。
func TestReportAddonRevenueRespectsOrderScope(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report_addon_scope?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)

	inRange := seedReportOrder(t, db, "addon-scope-in", day.Add(time.Hour), 11, 10000, 0,
		esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusCompleted)
	outOfRange := seedReportOrder(t, db, "addon-scope-out", day.AddDate(0, 0, -3), 11, 9000, 0,
		esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusCompleted)
	for _, item := range []struct {
		no      string
		orderID int
		paid    int64
	}{
		{"addon-scope-paid-in", inRange.ID, 4000},
		{"addon-scope-paid-out", outOfRange.ID, 7000},
	} {
		db.EsportsOrderAddon.Create().SetAddonNo(item.no).SetOrderID(item.orderID).
			SetAddedCents(item.paid).SetStatus(esportsorderaddon.StatusConfirmed).
			SetPaymentStatus(esportsorderaddon.PaymentStatusPaid).
			SetPaidCents(item.paid).SetPaidAt(day.Add(2 * time.Hour)).
			SetCreatedAt(day.Add(time.Hour)).SaveX(ctx)
	}

	result, err := ownerOverviewReport(ctx, db, reportFilter{
		Start: day, End: day.AddDate(0, 0, 1), Period: "daily",
	})
	if err != nil {
		t.Fatal(err)
	}
	summary := result["summary"].(map[string]any)
	if got := summary["revenue_cents"].(int64); got != 14000 {
		t.Fatalf("加钟收入越界计入：%d", got)
	}
	if got := summary["addon_revenue_cents"].(int64); got != 4000 {
		t.Fatalf("加钟收入未按订单筛选收敛：%d", got)
	}
}
