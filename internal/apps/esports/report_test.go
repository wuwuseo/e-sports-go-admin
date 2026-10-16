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
