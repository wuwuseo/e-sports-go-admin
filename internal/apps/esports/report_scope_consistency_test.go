package esports

import (
	"net/http"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportscommissionentry"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportssettlement"
	"go-admin/internal/ent/esportswallettx"
	"go-admin/internal/ent/esportswithdrawal"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"
)

// P1-07 财务报表口径混用：同一请求内钱包 / 结算 / 提现 / 分成对账必须使用同一套口径。
// 此前分成对账会按 VisibleAdmin 单独收敛订单，而钱包 / 结算 / 回执全量，导致「部分全量 + 部分局部」；
// 本次统一为账户级显式全量域（data_scope=full），因此带 VisibleAdmin 的请求结果必须与全量请求完全一致。
func TestReportFinanceScopeIsUniformFull(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report_finance_scope?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)

	// 钱包流水：客户充值与陪玩猫粮各一笔（账户级资金，无订单归属）。
	db.EsportsWalletTx.Create().SetTxNo("scope-tx-1").
		SetAccountType(esportswallettx.AccountTypeCustomerBalance).SetOwnerID(1).
		SetEntryType(esportswallettx.EntryTypeRecharge).SetPrincipalDeltaCents(5000).
		SetSourceType("manual").SetSourceID("scope-r-1").
		SetCreatedAt(day.Add(time.Hour)).SaveX(ctx)
	db.EsportsWalletTx.Create().SetTxNo("scope-tx-2").
		SetAccountType(esportswallettx.AccountTypePlayerCatfood).SetOwnerID(2).
		SetEntryType(esportswallettx.EntryTypeRecharge).SetPrincipalDeltaCents(3000).
		SetSourceType("settlement").SetSourceID("scope-s-1").
		SetCreatedAt(day.Add(2 * time.Hour)).SaveX(ctx)
	// 结算单与提现单。
	db.EsportsSettlement.Create().SetSettlementNo("scope-st-1").SetOwnerID(2).
		SetPeriodStart(day).SetPeriodEnd(day.AddDate(0, 0, 1)).SetGrossCents(3000).SetNetCents(3000).
		SetEntryCount(1).SetStatus(esportssettlement.StatusLocked).
		SetCreatedAt(day.Add(time.Hour)).SaveX(ctx)
	db.EsportsWithdrawal.Create().SetWithdrawalNo("scope-wd-1").SetPlayerID(2).
		SetIdemKey("scope-wd-idem-1").SetAmountCents(8000).SetFeeCents(80).SetTaxCents(120).
		SetNetCents(7800).SetChannel(esportswithdrawal.ChannelBankCard).
		SetAccountRef("6222****0000").SetStatus(esportswithdrawal.StatusPaid).
		SetCreatedAt(day.Add(3 * time.Hour)).SaveX(ctx)

	// 分成明细：两条都挂在不属于客服 11 的订单上（订单 ID 1 / 2 在库里并不存在）。
	for i, orderID := range []int{1, 2} {
		db.EsportsCommissionEntry.Create().SetIdemKey("scope-entry-" + string(rune('a'+i))).
			SetOrderID(orderID).SetPlayerID(2).
			SetKind(esportscommissionentry.KindService).
			SetBaseCents(1000).SetAmountCents(500).
			SetCreatedAt(day.Add(time.Duration(i+1) * time.Hour)).SaveX(ctx)
	}

	full := reportFilter{Start: day, End: day.AddDate(0, 0, 1)}
	// 非超管（财务角色授权）请求：scopeReportFilter 会写入 ResponsibleAdmin / VisibleAdmin。
	scoped := full
	scoped.ResponsibleAdmin = 11
	scoped.VisibleAdmin = 11

	fullResult, err := financeReport(ctx, db, full)
	if err != nil {
		t.Fatal(err)
	}
	scopedResult, err := financeReport(ctx, db, scoped)
	if err != nil {
		t.Fatal(err)
	}
	if fullResult["data_scope"].(string) != financeReportDataScope ||
		scopedResult["data_scope"].(string) != financeReportDataScope {
		t.Fatalf("财务资金报表应恒为显式全量域：%v / %v",
			fullResult["data_scope"], scopedResult["data_scope"])
	}

	// 钱包：客户 5000、陪玩 3000 必须都在，不因 VisibleAdmin 而只看到局部。
	wallet := scopedResult["wallet"].(map[string]any)
	customer := wallet["customer_balance"].(map[string]int64)
	if customer[string(esportswallettx.EntryTypeRecharge)] != 5000 {
		t.Fatalf("钱包资金被局部收敛：%+v", customer)
	}
	if wallet["player_catfood"].(map[string]int64)[string(esportswallettx.EntryTypeRecharge)] != 3000 {
		t.Fatalf("陪玩猫粮资金被局部收敛：%+v", wallet["player_catfood"])
	}
	// 结算 / 提现：同样保持全量。
	if got := scopedResult["settlement"].(map[string]any)["count"].(int); got != 1 {
		t.Fatalf("结算汇总被局部收敛：count=%d", got)
	}
	if got := scopedResult["withdrawals"].(map[string]any)["count"].(int); got != 1 {
		t.Fatalf("提现汇总被局部收敛：count=%d", got)
	}
	if got := scopedResult["withdrawals"].(map[string]any)["net_cents"].(int64); got != 7800 {
		t.Fatalf("提现实结额不正确：%d", got)
	}
	// 分成对账：必须与钱包 / 结算同一口径（全量 2 条），而不是按 VisibleAdmin 收敛成 0 条。
	reconciliation := scopedResult["commission_reconciliation"].(map[string]any)["summary"].(map[string]any)
	if got := reconciliation["entry_count"].(int); got != 2 {
		t.Fatalf("分成对账与钱包/结算口径不一致：entry_count=%d", got)
	}
	if got := reconciliation["gross_cents"].(int64); got != 1000 {
		t.Fatalf("分成对账金额不正确：%d", got)
	}
}

// P1-07 证据：财务拒绝清单漏了 created_by_admin_id，导致该维度被接受后又静默变成全量。
// 财务资金报表不提供任何订单归属维度（含业绩归属客服 / 报单客服），必须显式 400。
func TestReportFinanceRejectsCreatedByAdminDimension(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report_finance_creator?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()
	admin := seedAdminAccount(t, db, "report-finance-creator")
	app := reportTestApp(admin, true)

	status, _, payload := getJSON(t, app, "/admin/reports/finance?created_by_admin_id=12")
	if status != http.StatusBadRequest {
		t.Fatalf("财务带业绩归属客服筛选应返回 400，实际 %d：%+v", status, payload)
	}
	message, _ := payload["message"].(string)
	if !strings.Contains(message, "财务资金报表") || !strings.Contains(message, "业绩归属") {
		t.Fatalf("财务筛选错误缺少明确中文提示：%q", message)
	}
}

// P1-07 归属过滤条件只在一处构建且带 created_by_admin_id 兜底：
// 负责客服为空的订单（历史单 / 补录单）仍归属其报单人。
func TestReportOwnedOrderPredicateIncludesCreatorFallback(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report_owner_fallback?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)

	// 本人负责的单。
	seedReportOrder(t, db, "fallback-owned", day.Add(time.Hour), 11, 1000, 0,
		esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusCompleted)
	// 负责客服为空、由本人报单兜底的单。
	orphan := seedReportOrder(t, db, "fallback-created", day.Add(2*time.Hour), 11, 2000, 0,
		esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusCompleted)
	db.EsportsOrder.UpdateOneID(orphan.ID).ClearResponsibleAdminID().SaveX(ctx)
	// 他人负责且他人报单：对本人不可见。
	other := seedReportOrder(t, db, "fallback-other", day.Add(3*time.Hour), 12, 4000, 0,
		esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusCompleted)
	db.EsportsOrder.UpdateOneID(other.ID).SetCreatedByAdminID(12).SaveX(ctx)

	result, err := ownerOverviewReport(ctx, db, reportFilter{
		Start: day, End: day.AddDate(0, 0, 1), VisibleAdmin: 11, ResponsibleAdmin: 11,
	})
	if err != nil {
		t.Fatal(err)
	}
	summary := result["summary"].(map[string]any)
	if summary["order_count"].(int) != 2 || summary["revenue_cents"].(int64) != 3000 {
		t.Fatalf("归属过滤未包含 created_by_admin_id 兜底：%+v", summary)
	}
	if result["data_scope"].(string) != "owner" {
		t.Fatalf("非超管数据域标记应为 owner：%v", result["data_scope"])
	}
}
