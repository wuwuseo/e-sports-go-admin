package esports

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportssettlement"
	"go-admin/internal/ent/esportswallettx"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"
)

// P1-08 报表缺资金维度：账户类型 / 流水类型 / 结算状态必须在 handler 解析后真正下推到查询，
// 不能「收下后忽略」。本用例给定维度后断言结果集按维度收窄（含下钻明细同口径）。
func TestReportFinanceFilterDimensionsPushdown(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report_finance_dims?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)

	// 三个账户桶各一笔：客户充值、陪玩猫粮充值、储值卡消费。
	db.EsportsWalletTx.Create().SetTxNo("dims-tx-1").
		SetAccountType(esportswallettx.AccountTypeCustomerBalance).SetOwnerID(1).
		SetEntryType(esportswallettx.EntryTypeRecharge).SetPrincipalDeltaCents(5000).
		SetSourceType("manual").SetSourceID("dims-r-1").
		SetCreatedAt(day.Add(time.Hour)).SaveX(ctx)
	db.EsportsWalletTx.Create().SetTxNo("dims-tx-2").
		SetAccountType(esportswallettx.AccountTypePlayerCatfood).SetOwnerID(2).
		SetEntryType(esportswallettx.EntryTypeRecharge).SetPrincipalDeltaCents(3000).
		SetSourceType("settlement").SetSourceID("dims-s-1").
		SetCreatedAt(day.Add(2 * time.Hour)).SaveX(ctx)
	db.EsportsWalletTx.Create().SetTxNo("dims-tx-3").
		SetAccountType(esportswallettx.AccountTypeStoredValueCard).SetOwnerID(3).
		SetEntryType(esportswallettx.EntryTypeConsume).SetPrincipalDeltaCents(-1000).
		SetSourceType("order").SetSourceID("dims-o-1").
		SetCreatedAt(day.Add(3 * time.Hour)).SaveX(ctx)
	// 两张结算单：一张 locked、一张 paid。
	db.EsportsSettlement.Create().SetSettlementNo("dims-st-locked").SetOwnerID(2).
		SetPeriodStart(day).SetPeriodEnd(day.AddDate(0, 0, 1)).SetGrossCents(3000).SetNetCents(3000).
		SetEntryCount(1).SetStatus(esportssettlement.StatusLocked).
		SetCreatedAt(day.Add(time.Hour)).SaveX(ctx)
	db.EsportsSettlement.Create().SetSettlementNo("dims-st-paid").SetOwnerID(2).
		SetPeriodStart(day).SetPeriodEnd(day.AddDate(0, 0, 1)).SetGrossCents(2000).SetNetCents(2000).
		SetEntryCount(1).SetStatus(esportssettlement.StatusPaid).
		SetCreatedAt(day.Add(2 * time.Hour)).SaveX(ctx)

	base := reportFilter{Start: day, End: day.AddDate(0, 0, 1)}

	// 账户类型维度：只保留玩家猫粮桶。
	byAccount := base
	byAccount.WalletAccountType = "player_catfood"
	result, err := financeReport(ctx, db, byAccount)
	if err != nil {
		t.Fatal(err)
	}
	wallet := result["wallet"].(map[string]any)
	if len(wallet["customer_balance"].(map[string]int64)) != 0 ||
		len(wallet["stored_value_card"].(map[string]int64)) != 0 {
		t.Fatalf("账户类型维度未下推：%+v", wallet)
	}
	if got := wallet["player_catfood"].(map[string]int64)[string(esportswallettx.EntryTypeRecharge)]; got != 3000 {
		t.Fatalf("账户类型维度下推后金额不正确：%d", got)
	}

	// 流水类型维度：只保留消费流水。
	byEntry := base
	byEntry.WalletEntryType = "consume"
	result, err = financeReport(ctx, db, byEntry)
	if err != nil {
		t.Fatal(err)
	}
	wallet = result["wallet"].(map[string]any)
	if len(wallet["customer_balance"].(map[string]int64)) != 0 ||
		len(wallet["player_catfood"].(map[string]int64)) != 0 {
		t.Fatalf("流水类型维度未下推：%+v", wallet)
	}
	if got := wallet["stored_value_card"].(map[string]int64)[string(esportswallettx.EntryTypeConsume)]; got != -1000 {
		t.Fatalf("流水类型维度下推后金额不正确：%d", got)
	}

	// 结算状态维度：汇总、状态分布与下钻明细必须一起收窄。
	byStatus := base
	byStatus.SettlementStatus = "paid"
	result, err = financeReport(ctx, db, byStatus)
	if err != nil {
		t.Fatal(err)
	}
	settlement := result["settlement"].(map[string]any)
	if settlement["count"].(int) != 1 || settlement["net_cents"].(int64) != 2000 {
		t.Fatalf("结算状态维度未下推：%+v", settlement)
	}
	if status := settlement["by_status"].(map[string]int); len(status) != 1 || status["paid"] != 1 {
		t.Fatalf("结算状态分布未随维度收窄：%+v", status)
	}
	docs, _ := result["details"].([]map[string]any)
	for _, doc := range docs {
		if doc["doc_type"] == "settlement" && fmt.Sprint(doc["status"]) != string(esportssettlement.StatusPaid) {
			t.Fatalf("下钻明细未按结算状态收窄：%+v", doc)
		}
	}
}

// P1-08 维度取值非法时必须显式 400，不能拼错枚举就静默统计成 0 行。
func TestReportFinanceRejectsUnknownDimensionValue(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report_finance_dim_value?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()
	admin := seedAdminAccount(t, db, "report-finance-dim-value")
	app := reportTestApp(admin, true)

	cases := map[string]string{
		"account_type=unknown_account":   "账户类型",
		"entry_type=unknown_entry":       "流水类型",
		"settlement_status=unknown_stat": "结算状态",
	}
	for query, keyword := range cases {
		status, _, payload := getJSON(t, app, "/admin/reports/finance?"+query)
		if status != http.StatusBadRequest {
			t.Fatalf("非法资金维度 %s 应返回 400，实际 %d：%+v", query, status, payload)
		}
		message, _ := payload["message"].(string)
		if !strings.Contains(message, keyword) {
			t.Fatalf("非法资金维度 %s 的中文提示缺少「%s」：%q", query, keyword, message)
		}
	}
}

// P1-08 / P1-09：资金维度只属于财务资金报表，其它报表类型必须显式 400 而不是静默忽略。
func TestReportFundsDimensionsRejectedForOtherReports(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report_dim_scope?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()
	admin := seedAdminAccount(t, db, "report-dim-scope")
	app := reportTestApp(admin, true)

	status, _, payload := getJSON(t, app, "/admin/reports/owner_overview?settlement_status=paid")
	if status != http.StatusBadRequest {
		t.Fatalf("经营总览带结算状态维度应返回 400，实际 %d：%+v", status, payload)
	}
	message, _ := payload["message"].(string)
	if !strings.Contains(message, "经营总览报表") || !strings.Contains(message, "结算状态") {
		t.Fatalf("不支持维度的中文提示不完整：%q", message)
	}

	// 支持清单与不支持清单必须互补且含三个资金维度。
	caps := map[string]bool{}
	for _, key := range supportedReportDimensions("finance") {
		caps[key] = true
	}
	for _, key := range []string{"account_type", "entry_type", "settlement_status"} {
		if !caps[key] {
			t.Fatalf("财务资金报表的可用维度缺失 %s：%+v", key, caps)
		}
	}
	if caps["channel_source"] || caps["responsible_admin_id"] {
		t.Fatalf("财务资金报表不应提供订单归属维度：%+v", caps)
	}
}
