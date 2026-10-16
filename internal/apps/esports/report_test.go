package esports

import (
	"strconv"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportscscommissionentry"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportspaymentreceipt"
	"go-admin/internal/ent/esportssettlement"
	"go-admin/internal/ent/esportswallettx"
	"go-admin/internal/ent/migrate"
)

// seedReportOrder 构造一张带归属、金额与时间的订单，供报表聚合用例使用。
func seedReportOrder(t *testing.T, db *ent.Client, orderNo string, createdAt time.Time, adminID int,
	payable, refunded int64, payment esportsorder.PaymentStatus, fulfillment esportsorder.FulfillmentStatus,
) *ent.EsportsOrder {
	t.Helper()
	return db.EsportsOrder.Create().
		SetOrderNo(orderNo).SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(payable).SetPayableCents(payable).SetRefundedCents(refunded).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetResponsibleAdminID(adminID).SetCreatedByAdminID(adminID).
		SetPaymentDeadline(createdAt.Add(time.Hour)).
		SetPaymentStatus(payment).SetFulfillmentStatus(fulfillment).
		SetCreatedAt(createdAt).SaveX(t.Context())
}

func TestScopeReportFilterIgnoresOtherAdminID(t *testing.T) {
	requested := reportFilter{ResponsibleAdmin: 22}
	if got := scopeReportFilter(requested, 11, false); got.ResponsibleAdmin != 11 {
		t.Fatalf("客服可通过筛选参数读取其他人的报表：%+v", got)
	}
	if got := scopeReportFilter(requested, 11, true); got.ResponsibleAdmin != 22 {
		t.Fatalf("超级管理员的筛选条件不应被覆盖：%+v", got)
	}
}

func TestReportFiltersChannelSourceAndEntryPoint(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report_source?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	ctx := t.Context()
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	manual := seedReportOrder(t, db, "source-manual", day.Add(time.Hour), 11, 1000, 0,
		esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusCompleted)
	wecom := seedReportOrder(t, db, "source-wecom", day.Add(2*time.Hour), 11, 2000, 0,
		esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusCompleted)
	db.EsportsOrder.UpdateOneID(wecom.ID).SetChannelSource(esportsorder.ChannelSourceWecom).
		SetEntryPoint(esportsorder.EntryPointWecomSidebar).SaveX(ctx)
	for _, order := range []*ent.EsportsOrder{manual, wecom} {
		db.EsportsAfterSale.Create().SetAfterSaleNo("source-as-" + order.OrderNo).
			SetSourceID(order.ID).SetKind(esportsaftersale.KindRefundRequest).
			SetChannel(esportsaftersale.ChannelCustomer).SetDemand("测试").
			SetCreatedAt(day.Add(3 * time.Hour)).SaveX(ctx)
	}
	f := reportFilter{Start: day, End: day.AddDate(0, 0, 1), ChannelSource: "wecom", EntryPoint: "wecom_sidebar"}
	overview, err := ownerOverviewReport(ctx, db, f)
	if err != nil {
		t.Fatal(err)
	}
	if overview["summary"].(map[string]any)["order_count"].(int) != 1 ||
		overview["summary"].(map[string]any)["revenue_cents"].(int64) != 2000 {
		t.Fatalf("渠道与入口筛选未作用到订单聚合：%+v", overview)
	}
	after, err := aftersaleQualityReport(ctx, db, f, 0)
	if err != nil {
		t.Fatal(err)
	}
	if after["summary"].(map[string]any)["total"].(int) != 1 {
		t.Fatalf("渠道与入口筛选未作用到关联售后：%+v", after)
	}
}

func TestCsPerformanceScopesCommissionEntriesToResponsibleAdmin(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report_cs_entry_scope?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	ctx := t.Context()
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	for _, id := range []int{11, 22} {
		db.EsportsCsCommissionEntry.Create().SetIdemKey("report-cs-" + strconv.Itoa(id)).
			SetAdminID(id).SetKind(esportscscommissionentry.KindGift).
			SetGiftOrderID(id).SetAmountCents(500).SetCreatedAt(day.Add(time.Hour)).SaveX(ctx)
	}
	result, err := csPerformanceReport(ctx, db, reportFilter{
		Start: day, End: day.AddDate(0, 0, 1), ResponsibleAdmin: 11,
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := result["rows"].([]csPerformanceRow)
	if len(rows) != 1 || rows[0].AdminID != 11 || rows[0].CommissionCents != 500 {
		t.Fatalf("客服报表不得包含他人提成：%+v", rows)
	}
}

// reportRange 按日 / 周 / 月推导的自然周期区间必须首尾对齐且长度正确。
func TestReportRange(t *testing.T) {
	now := time.Date(2026, 10, 11, 15, 30, 0, 0, time.UTC)

	begin, end, err := reportRange("", "", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if begin.Hour() != 0 || end.Sub(begin) != 24*time.Hour {
		t.Fatalf("日区间不正确：%v ~ %v", begin, end)
	}

	begin, end, err = reportRange("weekly", "", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if begin.Weekday() != time.Monday || end.Sub(begin) != 7*24*time.Hour {
		t.Fatalf("周区间不正确：%v ~ %v", begin, end)
	}

	begin, end, err = reportRange("monthly", "", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if begin.Day() != 1 || end.Month() != begin.Month()+1 || end.Day() != 1 {
		t.Fatalf("月区间不正确：%v ~ %v", begin, end)
	}

	begin, end, err = reportRange("daily", "2026-10-01", "2026-10-03", now)
	if err != nil || !begin.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) ||
		!end.Equal(time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("自定义区间不正确：%v ~ %v err=%v", begin, end, err)
	}

	if _, _, err = reportRange("yearly", "", "", now); err == nil {
		t.Fatal("非法周期未拦截")
	}
	if _, _, err = reportRange("daily", "2026-10-03", "2026-10-01", now); err == nil {
		t.Fatal("倒置区间未拦截")
	}
}

// 五类报表的核心聚合口径必须与区间内的订单、席位、售后、流水、结算、回执一致。
func TestReportAggregations(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()

	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	order1 := seedReportOrder(t, db, "report-1", day.Add(time.Hour), 11, 10000, 0,
		esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusCompleted)
	seedReportOrder(t, db, "report-2", day.Add(2*time.Hour), 12, 5000, 1000,
		esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusPendingDispatch)
	// 区间外订单必须被剔除。
	seedReportOrder(t, db, "report-out", day.AddDate(0, 0, -1), 11, 999, 0,
		esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusCompleted)

	filter := reportFilter{Start: day, End: day.AddDate(0, 0, 1), Period: "daily", Limit: 100}

	// 经营总览：营收／退款／净额、履约计数与下钻明细。
	overview, err := ownerOverviewReport(ctx, db, filter)
	if err != nil {
		t.Fatal(err)
	}
	summary := overview["summary"].(map[string]any)
	if summary["order_count"].(int) != 2 || summary["revenue_cents"].(int64) != 15000 ||
		summary["refund_cents"].(int64) != 1000 || summary["net_cents"].(int64) != 14000 ||
		summary["completed_count"].(int) != 1 {
		t.Fatalf("经营总览汇总不正确：%+v", summary)
	}
	if len(overview["details"].([]map[string]any)) != 2 {
		t.Fatalf("经营总览下钻明细条数不正确：%d", len(overview["details"].([]map[string]any)))
	}

	// 客服业绩：按负责客服分组并按净额倒序。
	performance, err := csPerformanceReport(ctx, db, filter)
	if err != nil {
		t.Fatal(err)
	}
	rows := performance["rows"].([]csPerformanceRow)
	if len(rows) != 2 || rows[0].AdminID != 11 || rows[0].NetCents != 10000 ||
		rows[1].AdminID != 12 || rows[1].NetCents != 4000 {
		t.Fatalf("客服业绩分组不正确：%+v", rows)
	}

	// 派单效率：邀请／接单／过期计数与平均接单时长。
	invitedAt := day.Add(time.Hour)
	db.EsportsDispatchSeat.Create().SetOrderID(order1.ID).SetSeatIndex(1).
		SetStatus(esportsdispatchseat.StatusAccepted).SetSource(esportsdispatchseat.SourceAssign).
		SetInvitedByAdminID(11).SetCreatedAt(invitedAt).
		SetRespondedAt(invitedAt.Add(30 * time.Second)).SaveX(ctx)
	db.EsportsDispatchSeat.Create().SetOrderID(order1.ID).SetSeatIndex(2).
		SetStatus(esportsdispatchseat.StatusExpired).SetSource(esportsdispatchseat.SourceGrab).
		SetInvitedByAdminID(12).SetCreatedAt(invitedAt.Add(time.Hour)).SaveX(ctx)
	dispatch, err := dispatchEfficiencyReport(ctx, db, filter)
	if err != nil {
		t.Fatal(err)
	}
	dSummary := dispatch["summary"].(map[string]any)
	if dSummary["invite_count"].(int) != 2 || dSummary["accepted_count"].(int) != 1 ||
		dSummary["expired_count"].(int) != 1 || dSummary["avg_accept_seconds"].(int64) != 30 {
		t.Fatalf("派单效率汇总不正确：%+v", dSummary)
	}

	// 售后质量：状态分布、赔付金额与按类型 / 责任方拆分。
	db.EsportsAfterSale.Create().SetAfterSaleNo("as-1").SetSourceID(order1.ID).
		SetKind(esportsaftersale.KindServiceMismatch).SetChannel(esportsaftersale.ChannelCustomer).
		SetDemand("服务不符").SetStatus(esportsaftersale.StatusCompleted).
		SetFaultParty(esportsaftersale.FaultPartyPlayer).SetRefundCents(500).
		SetCreatedAt(day.Add(time.Hour)).SaveX(ctx)
	aftersale, err := aftersaleQualityReport(ctx, db, filter, 0)
	if err != nil {
		t.Fatal(err)
	}
	aSummary := aftersale["summary"].(map[string]any)
	if aSummary["total"].(int) != 1 || aSummary["completed"].(int) != 1 || aSummary["refund_cents"].(int64) != 500 {
		t.Fatalf("售后质量汇总不正确：%+v", aSummary)
	}
	if aftersale["by_kind"].(map[string]int)[string(esportsaftersale.KindServiceMismatch)] != 1 {
		t.Fatalf("售后类型拆分不正确：%+v", aftersale["by_kind"])
	}

	// 财务资金：钱包流水、结算单与按支付方式的回执聚合。
	db.EsportsWalletTx.Create().SetTxNo("tx-1").SetAccountType(esportswallettx.AccountTypeCustomerBalance).
		SetOwnerID(1).SetEntryType(esportswallettx.EntryTypeRecharge).SetPrincipalDeltaCents(5000).
		SetSourceType("manual").SetSourceID("r-1").SetCreatedAt(day.Add(time.Hour)).SaveX(ctx)
	db.EsportsWalletTx.Create().SetTxNo("tx-2").SetAccountType(esportswallettx.AccountTypeCustomerBalance).
		SetOwnerID(1).SetEntryType(esportswallettx.EntryTypeConsume).SetPrincipalDeltaCents(-2000).
		SetSourceType("order").SetSourceID("report-1").SetCreatedAt(day.Add(2 * time.Hour)).SaveX(ctx)
	db.EsportsSettlement.Create().SetSettlementNo("st-1").SetOwnerID(1).
		SetPeriodStart(day).SetPeriodEnd(day.AddDate(0, 0, 1)).SetGrossCents(3000).SetNetCents(3000).
		SetEntryCount(1).SetStatus(esportssettlement.StatusLocked).SetCreatedAt(day.Add(time.Hour)).SaveX(ctx)
	db.EsportsPaymentReceipt.Create().SetIntentID(1).SetChannel("wechat").SetTransactionNo("wx-1").
		SetActualCents(8000).SetStatus(esportspaymentreceipt.StatusVerified).
		SetConfirmedAt(day.Add(time.Hour)).SetCreatedAt(day.Add(time.Hour)).SaveX(ctx)
	finance, err := financeReport(ctx, db, filter)
	if err != nil {
		t.Fatal(err)
	}
	wallet := finance["wallet"].(map[string]any)["customer_balance"].(map[string]int64)
	if wallet[string(esportswallettx.EntryTypeRecharge)] != 5000 ||
		wallet[string(esportswallettx.EntryTypeConsume)] != -2000 {
		t.Fatalf("钱包资金聚合不正确：%+v", wallet)
	}
	settlement := finance["settlement"].(map[string]any)
	if settlement["count"].(int) != 1 || settlement["net_cents"].(int64) != 3000 {
		t.Fatalf("结算聚合不正确：%+v", settlement)
	}
	channels := finance["payment_channels"].([]financeChannelRow)
	if len(channels) != 1 || channels[0].Channel != "wechat" || channels[0].AmountCents != 8000 {
		t.Fatalf("支付方式聚合不正确：%+v", channels)
	}
}

// bulkReportOrder 是大样本报表用例的订单构造参数。
type bulkReportOrder struct {
	OrderNo       string
	CreatedAt     time.Time
	AdminID       int
	PayableCents  int64
	RefundedCents int64
	Payment       esportsorder.PaymentStatus
	Fulfillment   esportsorder.FulfillmentStatus
}

// seedOrdersBulk 分批批量写入订单：单条 INSERT 的参数个数受 SQLite / MySQL 上限约束，
// 因此按批聚合而不是逐条创建。
func seedOrdersBulk(t *testing.T, db *ent.Client, specs []bulkReportOrder) {
	t.Helper()
	const batchSize = 250
	ctx := t.Context()
	for start := 0; start < len(specs); start += batchSize {
		end := start + batchSize
		if end > len(specs) {
			end = len(specs)
		}
		batch := make([]*ent.EsportsOrderCreate, 0, end-start)
		for _, spec := range specs[start:end] {
			batch = append(batch, db.EsportsOrder.Create().
				SetOrderNo(spec.OrderNo).SetCustomerID(1).SetSkuID(1).
				SetQuantity(1).SetUnitPriceCents(spec.PayableCents).SetPayableCents(spec.PayableCents).
				SetRefundedCents(spec.RefundedCents).SetPromisedServiceUnits(1).SetRequiredSeats(1).
				SetResponsibleAdminID(spec.AdminID).SetCreatedByAdminID(spec.AdminID).
				SetPaymentDeadline(spec.CreatedAt.Add(time.Hour)).
				SetPaymentStatus(spec.Payment).SetFulfillmentStatus(spec.Fulfillment).
				SetCreatedAt(spec.CreatedAt))
		}
		if _, err := db.EsportsOrder.CreateBulk(batch...).Save(ctx); err != nil {
			t.Fatalf("批量写入订单失败（%d~%d）：%v", start, end, err)
		}
	}
}

// §17 大样本：汇总与分组统计走数据库端聚合，订单量超过明细上限时也不得整体报错，
// 且总量 / 营收 / 净额与时间轴分段之和都必须与样本一致。
func TestReportLargeSampleOwnerOverview(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report_large_sample?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)

	// 样本量刻意超过下钻明细上限，覆盖「聚合不再受行数限制」这一诉求。
	const sample = reportDetailLimit + 137
	specs := make([]bulkReportOrder, 0, sample)
	var wantOrders, wantPaid, wantCompleted, wantCancelled int
	var wantRevenue, wantRefund int64
	for i := 0; i < sample; i++ {
		paid := i%3 != 0
		payable := int64(1000 + i%97)
		refunded := int64(0)
		if i%5 == 0 {
			refunded = 10 + int64(i%7)
		}
		payment := esportsorder.PaymentStatusUnpaid
		if paid {
			payment = esportsorder.PaymentStatusPaid
		}
		fulfillment := esportsorder.FulfillmentStatusPendingDispatch
		switch i % 4 {
		case 0:
			fulfillment = esportsorder.FulfillmentStatusCompleted
		case 1:
			fulfillment = esportsorder.FulfillmentStatusCancelled
		}
		specs = append(specs, bulkReportOrder{
			OrderNo: "large-" + strconv.Itoa(i),
			// 均匀落在连续三天内，用于校验时间轴分段之和等于总量。
			CreatedAt:    day.Add(time.Duration(i%3)*24*time.Hour + time.Duration(i%3600)*time.Second),
			AdminID:      11,
			PayableCents: payable, RefundedCents: refunded,
			Payment: payment, Fulfillment: fulfillment,
		})
		wantOrders++
		wantRefund += refunded
		if paid {
			wantPaid++
			wantRevenue += payable
		}
		switch fulfillment {
		case esportsorder.FulfillmentStatusCompleted:
			wantCompleted++
		case esportsorder.FulfillmentStatusCancelled:
			wantCancelled++
		}
	}
	seedOrdersBulk(t, db, specs)

	report, err := ownerOverviewReport(ctx, db, reportFilter{Start: day, End: day.AddDate(0, 0, 3), Period: "daily"})
	if err != nil {
		t.Fatalf("大样本经营总览不得整体报错：%v", err)
	}
	summary := report["summary"].(map[string]any)
	if summary["order_count"].(int) != wantOrders ||
		summary["paid_count"].(int) != wantPaid ||
		summary["revenue_cents"].(int64) != wantRevenue ||
		summary["refund_cents"].(int64) != wantRefund ||
		summary["net_cents"].(int64) != wantRevenue-wantRefund ||
		summary["completed_count"].(int) != wantCompleted ||
		summary["cancelled_count"].(int) != wantCancelled {
		t.Fatalf("大样本汇总不正确：%+v", summary)
	}

	// 下钻明细受上限约束，必须显式标记截断，避免静默漏数。
	if report["detail_truncated"].(bool) != true {
		t.Fatalf("明细超过上限时未标记截断：%+v", report["detail_truncated"])
	}
	if got := len(report["details"].([]map[string]any)); got != reportDetailLimit {
		t.Fatalf("下钻明细条数不正确：%d", got)
	}

	// 时间轴分段之和必须等于汇总口径。
	if got := report["bucket_period"].(string); got != "daily" {
		t.Fatalf("时间轴粒度不正确：%s", got)
	}
	buckets := report["buckets"].([]reportBucket)
	if len(buckets) != 3 {
		t.Fatalf("时间轴分段数不正确：%d", len(buckets))
	}
	var bucketOrders int
	var bucketRevenue, bucketRefund int64
	for _, bucket := range buckets {
		bucketOrders += bucket.OrderCount
		bucketRevenue += bucket.RevenueCents
		bucketRefund += bucket.RefundCents
	}
	if bucketOrders != wantOrders || bucketRevenue != wantRevenue || bucketRefund != wantRefund {
		t.Fatalf("时间轴分段之和与汇总不一致：orders=%d/%d revenue=%d/%d refund=%d/%d",
			bucketOrders, wantOrders, bucketRevenue, wantRevenue, bucketRefund, wantRefund)
	}
}

// 下钻明细上限按入参收敛：超过上限截断并标记，未超过时不标记。
func TestReportOwnerOverviewDetailTruncated(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report_detail_limit?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		seedReportOrder(t, db, "detail-"+strconv.Itoa(i), day.Add(time.Duration(i+1)*time.Hour), 11, 1000, 0,
			esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusCompleted)
	}
	truncated, err := ownerOverviewReport(ctx, db, reportFilter{
		Start: day, End: day.AddDate(0, 0, 1), Period: "daily", Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if truncated["detail_truncated"].(bool) != true || len(truncated["details"].([]map[string]any)) != 2 {
		t.Fatalf("明细上限未生效：%+v", truncated["details"])
	}
	if truncated["summary"].(map[string]any)["order_count"].(int) != 3 {
		t.Fatalf("明细截断不得影响汇总：%+v", truncated["summary"])
	}
	full, err := ownerOverviewReport(ctx, db, reportFilter{
		Start: day, End: day.AddDate(0, 0, 1), Period: "daily", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if full["detail_truncated"].(bool) != false || len(full["details"].([]map[string]any)) != 3 {
		t.Fatalf("明细未超限时不应标记截断：%+v", full["details"])
	}
}

// 售后下钻明细同样受上限约束并显式标记，汇总仍为全量聚合。
func TestReportAftersaleQualityDetailTruncated(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report_aftersale_detail?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		db.EsportsAfterSale.Create().SetAfterSaleNo("aftersale-detail-" + strconv.Itoa(i)).
			SetSourceID(i + 1).SetKind(esportsaftersale.KindRefundRequest).
			SetChannel(esportsaftersale.ChannelCustomer).SetDemand("测试").
			SetRefundCents(100).SetCreatedAt(day.Add(time.Duration(i+1) * time.Hour)).SaveX(ctx)
	}
	result, err := aftersaleQualityReport(ctx, db, reportFilter{
		Start: day, End: day.AddDate(0, 0, 1), Limit: 2,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result["summary"].(map[string]any)["total"].(int) != 3 ||
		result["summary"].(map[string]any)["refund_cents"].(int64) != 300 {
		t.Fatalf("售后汇总不正确：%+v", result["summary"])
	}
	if result["detail_truncated"].(bool) != true || len(result["details"].([]map[string]any)) != 2 {
		t.Fatalf("售后明细未按上限截断：%+v", result["details"])
	}
}

// §5.9 时间线埋点的四类平均等待时长必须由 SQL 聚合得出，且只统计时间点齐全的订单。
func TestReportDispatchEfficiencyTimelineAverages(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report_timeline?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	pooled := day.Add(time.Hour)
	full := seedReportOrder(t, db, "timeline-full", pooled.Add(-time.Minute), 11, 5000, 0,
		esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusCompleted)
	db.EsportsOrder.UpdateOneID(full.ID).
		SetFirstPooledAt(pooled).
		SetFirstInvitedAt(pooled.Add(120 * time.Second)).
		SetFirstAcceptedAt(pooled.Add(180 * time.Second)).
		SetServiceStartedAt(pooled.Add(480 * time.Second)).SaveX(ctx)
	// 第二单只埋了「入池 → 派出」：只影响该段的样本数与均值。
	partial := seedReportOrder(t, db, "timeline-partial", pooled.Add(2*time.Hour), 11, 5000, 0,
		esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusCompleted)
	db.EsportsOrder.UpdateOneID(partial.ID).
		SetFirstPooledAt(pooled.Add(time.Hour)).
		SetFirstInvitedAt(pooled.Add(time.Hour + 60*time.Second)).SaveX(ctx)

	result, err := dispatchEfficiencyReport(ctx, db, reportFilter{Start: day, End: day.AddDate(0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	summary := result["summary"].(map[string]any)
	if summary["avg_pool_to_invite_seconds"].(int64) != 90 ||
		summary["avg_invite_to_accept_seconds"].(int64) != 60 ||
		summary["avg_accept_to_start_seconds"].(int64) != 300 ||
		summary["avg_pool_to_start_seconds"].(int64) != 480 {
		t.Fatalf("时间线平均等待时长不正确：%+v", summary)
	}
}

// 订单维度筛选生效时，客服提成只计入关联订单命中筛选的明细（分片归集路径）。
func TestReportCsPerformanceCommissionOrderScope(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report_cs_commission?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	wecom := seedReportOrder(t, db, "cs-commission-wecom", day.Add(time.Hour), 11, 8000, 0,
		esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusCompleted)
	manual := seedReportOrder(t, db, "cs-commission-manual", day.Add(2*time.Hour), 11, 6000, 0,
		esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusCompleted)
	db.EsportsOrder.UpdateOneID(wecom.ID).SetChannelSource(esportsorder.ChannelSourceWecom).SaveX(ctx)
	db.EsportsOrder.UpdateOneID(manual.ID).SetChannelSource(esportsorder.ChannelSourceManual).SaveX(ctx)
	for i, order := range []*ent.EsportsOrder{wecom, manual} {
		db.EsportsCsCommissionEntry.Create().SetIdemKey("cs-commission-" + strconv.Itoa(i)).
			SetAdminID(11).SetOrderID(order.ID).SetGiftOrderID(order.ID).
			SetKind(esportscscommissionentry.KindGift).
			SetAmountCents(int64(300 + 400*i)).
			SetCreatedAt(day.Add(time.Duration(i+1) * time.Hour)).SaveX(ctx)
	}
	result, err := csPerformanceReport(ctx, db, reportFilter{
		Start: day, End: day.AddDate(0, 0, 1), ChannelSource: "wecom",
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := result["rows"].([]csPerformanceRow)
	if len(rows) != 1 || rows[0].OrderCount != 1 || rows[0].RevenueCents != 8000 {
		t.Fatalf("渠道筛选未作用到客服分组：%+v", rows)
	}
	if rows[0].CommissionCents != 300 {
		t.Fatalf("提成未按订单维度筛选归集：%+v", rows)
	}
}

// 时间轴分段在数据跨度过大时自动放粗粒度，保证分段查询次数有界且区间首尾相接。
func TestReportBucketPlanWidensPeriod(t *testing.T) {
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)

	period, ranges := reportBucketPlan(day, day.AddDate(0, 0, 2).Add(23*time.Hour), "daily")
	if period != "daily" || len(ranges) != 3 {
		t.Fatalf("短区间不应放粗粒度：period=%s ranges=%d", period, len(ranges))
	}
	period, ranges = reportBucketPlan(day, day.AddDate(5, 0, 0), "daily")
	if period == "daily" || len(ranges) > reportBucketLimit {
		t.Fatalf("长区间未按上限放粗粒度：period=%s ranges=%d", period, len(ranges))
	}
	for i := 1; i < len(ranges); i++ {
		if !ranges[i].Start.Equal(ranges[i-1].End) {
			t.Fatalf("分段区间未首尾相接：%v 与 %v", ranges[i-1], ranges[i])
		}
	}
}
