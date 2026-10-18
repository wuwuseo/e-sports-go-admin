package esports

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportswallettx"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"
)

// reportTestApp 构造带登录态的最小报表路由：uid / isSuperAdmin 由 Locals 注入，
// 与 PermissionMiddleware 之后的 handler 运行环境一致。
func reportTestApp(admin *ent.Admin, isSuper bool) *fiber.App {
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("uid", admin.UID)
		c.Locals("isSuperAdmin", isSuper)
		return c.Next()
	})
	app.Get("/admin/reports/:type", adminReports)
	return app
}

// 财务总表不具备逐订单归属：渠道 / 入口 / SKU / 游戏 / 负责客服 / 预约 / 席位筛选必须返回 400，
// 不能接受后静默按全量资金统计。
func TestReportFinanceRejectsOrderDimensions(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report_finance_guard?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()
	admin := seedAdminAccount(t, db, "report-finance-guard")
	app := reportTestApp(admin, true)

	for _, query := range []string{
		"channel_source=wecom",
		"entry_point=cs_pc",
		"sku_id=1",
		"game=王者荣耀",
		"responsible_admin_id=12",
		"appointment_type=instant",
		"seat_type=single",
	} {
		status, _, payload := getJSON(t, app, "/admin/reports/finance?"+query)
		if status != http.StatusBadRequest {
			t.Fatalf("财务带订单维度筛选 %s 应返回 400，实际 %d：%+v", query, status, payload)
		}
		message, _ := payload["message"].(string)
		if !strings.Contains(message, "财务资金报表") {
			t.Fatalf("财务筛选错误缺少明确中文提示：%q", message)
		}
	}

	// 不带订单维度时照常返回财务汇总。
	status, _, payload := getJSON(t, app, "/admin/reports/finance")
	if status != http.StatusOK {
		t.Fatalf("超管查看财务总表应成功，实际 %d：%+v", status, payload)
	}
}

// 财务资金汇总必须计入赠送权益（principal_delta_cents + bonus_delta_cents），
// 收入 / 支出按流水方向自带正负号，与钱包余额口径一致。
func TestReportFinanceIncludesBonus(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report_finance_bonus?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	seedWalletTx := func(no string, account esportswallettx.AccountType, owner int, entry esportswallettx.EntryType,
		principal, bonus int64, sourceID string,
	) {
		t.Helper()
		db.EsportsWalletTx.Create().SetTxNo(no).SetAccountType(account).SetOwnerID(owner).
			SetEntryType(entry).SetPrincipalDeltaCents(principal).SetBonusDeltaCents(bonus).
			SetSourceType("manual").SetSourceID(sourceID).
			SetCreatedAt(day.Add(time.Hour)).SaveX(ctx)
	}
	// 客户余额：充值 50 元 + 赠送 5 元；消费 20 元 + 赠送 3 元（负数方向）。
	seedWalletTx("bonus-customer-recharge", esportswallettx.AccountTypeCustomerBalance, 1,
		esportswallettx.EntryTypeRecharge, 5000, 500, "bonus-r-1")
	seedWalletTx("bonus-customer-consume", esportswallettx.AccountTypeCustomerBalance, 1,
		esportswallettx.EntryTypeConsume, -2000, -300, "bonus-r-2")
	// 陪玩猫粮与储值卡账户桶同样按本金 + 赠送汇总。
	seedWalletTx("bonus-player-recharge", esportswallettx.AccountTypePlayerCatfood, 2,
		esportswallettx.EntryTypeRecharge, 3000, 200, "bonus-r-3")
	seedWalletTx("bonus-card-consume", esportswallettx.AccountTypeStoredValueCard, 3,
		esportswallettx.EntryTypeConsume, -1000, -100, "bonus-r-4")

	result, err := financeReport(ctx, db, reportFilter{Start: day, End: day.AddDate(0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	wallet := result["wallet"].(map[string]any)
	customer := wallet["customer_balance"].(map[string]int64)
	if customer[string(esportswallettx.EntryTypeRecharge)] != 5500 ||
		customer[string(esportswallettx.EntryTypeConsume)] != -2300 {
		t.Fatalf("客户余额资金未计入赠送权益：%+v", customer)
	}
	// 收入 5500 − 支出 2300 = 净额 3200，方向与钱包扣除一致。
	if customer[string(esportswallettx.EntryTypeRecharge)]+
		customer[string(esportswallettx.EntryTypeConsume)] != 3200 {
		t.Fatalf("客户余额净额不正确：%+v", customer)
	}
	player := wallet["player_catfood"].(map[string]int64)
	if player[string(esportswallettx.EntryTypeRecharge)] != 3200 {
		t.Fatalf("陪玩猫粮未计入赠送权益：%+v", player)
	}
	stored := wallet["stored_value_card"].(map[string]int64)
	if stored[string(esportswallettx.EntryTypeConsume)] != -1100 {
		t.Fatalf("储值卡账户未计入赠送权益：%+v", stored)
	}
}

// 售后质量按渠道 / 入口圈定订单时，必须限定 source_type=order：
// 礼物 / 储值卡 / 充值售后的 ID 可能与订单 ID 相同，不得被误计入。
func TestReportAftersaleQualityScopesOrderSourceType(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_report_aftersale_source?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	order := seedReportOrder(t, db, "aftersale-source-wecom", day.Add(time.Hour), 11, 2000, 0,
		esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusCompleted)
	db.EsportsOrder.UpdateOneID(order.ID).SetChannelSource(esportsorder.ChannelSourceWecom).SaveX(ctx)

	createAftersale := func(no string, sourceType esportsaftersale.SourceType, kind esportsaftersale.Kind) {
		t.Helper()
		db.EsportsAfterSale.Create().SetAfterSaleNo(no).SetSourceType(sourceType).
			SetSourceID(order.ID).SetKind(kind).
			SetChannel(esportsaftersale.ChannelCustomer).SetDemand("测试").
			SetCreatedAt(day.Add(2 * time.Hour)).SaveX(ctx)
	}
	createAftersale("aftersale-source-order", esportsaftersale.SourceTypeOrder,
		esportsaftersale.KindRefundRequest)
	createAftersale("aftersale-source-gift", esportsaftersale.SourceTypeGift,
		esportsaftersale.KindOther)
	createAftersale("aftersale-source-card", esportsaftersale.SourceTypeStoredValueCard,
		esportsaftersale.KindOther)

	filter := reportFilter{Start: day, End: day.AddDate(0, 0, 1), ChannelSource: "wecom"}
	result, err := aftersaleQualityReport(ctx, db, filter, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total := result["summary"].(map[string]any)["total"].(int); total != 1 {
		t.Fatalf("渠道圈定误包含非订单来源售后：total=%d %+v", total, result["by_kind"])
	}
	if len(result["by_kind"].(map[string]int)) != 1 {
		t.Fatalf("渠道圈定应按订单来源过滤：%+v", result["by_kind"])
	}

	// 导出口径必须与查看一致。
	var buf bytes.Buffer
	if _, err := exportReportCSV(ctx, db, "aftersale_quality", filter, 0, &buf); err != nil {
		t.Fatal(err)
	}
	details := 0
	for _, record := range parseExportCSV(t, buf.String()) {
		if len(record) >= 2 && record[0] == "aftersale" && record[1] != "id" {
			details++
			if record[2] != string(esportsaftersale.KindRefundRequest) {
				t.Fatalf("导出明细混入非订单来源售后：%+v", record)
			}
		}
	}
	if details != 1 {
		t.Fatalf("导出明细应只含订单来源售后，实际 %d 条", details)
	}
}

// 自定义区间结束日为日期时应包含结束日整天（按 UTC+8 业务日），
// 单日区间也要涵盖当天 00:00 ~ 24:00。
func TestReportRangeEndDateIncludesWholeDay(t *testing.T) {
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	begin, end, err := reportRange("daily", "2026-10-03", "2026-10-03", now)
	if err != nil {
		t.Fatal(err)
	}
	// 业务 2026-10-03 全天 = UTC [2026-10-02 16:00, 2026-10-03 16:00)。
	if !begin.Equal(time.Date(2026, 10, 2, 16, 0, 0, 0, time.UTC)) ||
		!end.Equal(time.Date(2026, 10, 3, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("单日区间未包含结束日：%v ~ %v", begin, end)
	}
	if end.Sub(begin) != 24*time.Hour {
		t.Fatalf("单日区间长度不正确：%v", end.Sub(begin))
	}

	db := enttest.Open(t, "sqlite3", "file:esports_report_range_end?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	// 结束日 23:00（业务时区）必须计入；次日 00:00 属于右开区间，排除。
	seedReportOrder(t, db, "range-end-in", time.Date(2026, 10, 3, 15, 0, 0, 0, time.UTC), 11, 1000, 0,
		esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusCompleted)
	seedReportOrder(t, db, "range-end-out", time.Date(2026, 10, 3, 16, 0, 0, 0, time.UTC), 11, 2000, 0,
		esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusCompleted)

	result, err := ownerOverviewReport(ctx, db, reportFilter{Start: begin, End: end, Period: "daily"})
	if err != nil {
		t.Fatal(err)
	}
	summary := result["summary"].(map[string]any)
	if summary["order_count"].(int) != 1 || summary["revenue_cents"].(int64) != 1000 {
		t.Fatalf("结束日未包含当天或越界计入次日：%+v", summary)
	}
}
