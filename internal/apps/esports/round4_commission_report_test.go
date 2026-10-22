package esports

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportscommissionentry"
	"go-admin/internal/ent/esportscommissionrule"
	"go-admin/internal/ent/esportscscommissionentry"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportssettlement"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"
)

// openRound4DB 打开本文件专用的内存库，每个用例唯一 DSN，避免相互污染。
func openRound4DB(t *testing.T, name string) *ent.Client {
	t.Helper()
	return enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
}

// useRound4ORM 把测试库接到 global.ORM 上（handler 用例需要），并在用例结束后还原。
func useRound4ORM(t *testing.T, db *ent.Client) {
	t.Helper()
	previous := global.ORM
	global.ORM = db
	t.Cleanup(func() { global.ORM = previous })
}

// §12.1-05：退款冲回的舍入尾差必须单列为独立冲回行（而不是并入最后一条冲回明细），
// 明细行不再吸收尾差，冲回合计仍等于精确值。
func TestReversalRoundingTailListedSeparately(t *testing.T) {
	db := openRound4DB(t, "round4_reversal_tail_row")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

	order := db.EsportsOrder.Create().SetOrderNo("R4-TAIL-1").SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(10000).SetPayableCents(10000).
		SetPromisedServiceUnits(1).SetRequiredSeats(2).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).SaveX(ctx)
	// 两条 3 分服务分成，退款 50%：精确冲回 3 分，逐条截断各 1 分、尾差 1 分。
	for i, playerID := range []int{1, 2} {
		db.EsportsCommissionEntry.Create().SetIdemKey("r4tail:service:" + string(rune('a'+i))).
			SetOrderID(order.ID).SetPlayerID(playerID).
			SetKind(esportscommissionentry.KindService).SetSeatIndex(playerID).
			SetBaseCents(10000).SetAmountCents(3).
			SetRuleNo("CR-R4-TAIL").SetRuleVersion(1).
			SetCalcMode(esportscommissionentry.CalcModePercent).
			SetStatus(esportscommissionentry.StatusPendingSettlement).SaveX(ctx)
	}

	item := &ent.EsportsAfterSale{AfterSaleNo: "AS-R4-TAIL-1", RefundCents: 5000}
	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = reverseOrderCommission(ctx, tx, order, item, now); err != nil {
		_ = tx.Rollback()
		t.Fatalf("退款冲回失败：%v", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	rows := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.OrderIDEQ(order.ID),
		esportscommissionentry.KindEQ(esportscommissionentry.KindReversal),
	).AllX(ctx)
	var total int64
	tails := 0
	for _, row := range rows {
		total += row.AmountCents
		if containsTailMark(row.Note) {
			tails++
			continue
		}
		if row.AmountCents != -1 {
			t.Fatalf("舍入尾差仍并入明细行（期望明细行各 -1）：%+v", row)
		}
	}
	if total != -3 {
		t.Fatalf("尾差单列后冲回合计应为 -3，实际 %d（冲回明细 %d 条）", total, len(rows))
	}
	if tails != 1 {
		t.Fatalf("舍入尾差未单列为独立冲回行：尾差行 %d 条，冲回明细 %d 条", tails, len(rows))
	}
}

// §12.1-05 / §12.5-02：同一次退款的服务冲回与加钟冲回必须按同一「可退基数」计算比例，
// 否则两类冲回比例不一致，加钟侧被少冲。
func TestReversalUnifiedRefundableBaseForServiceAndAddon(t *testing.T) {
	db := openRound4DB(t, "round4_reversal_unified_base")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

	player := seedPlayer(t, db, 9410, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedInServiceOrder(t, db, "r4-unified-base", player.ID)
	// 应付 1000、结单前已退 200 → 结单净服务额 800；加钟已收款 4000。
	db.EsportsOrder.UpdateOneID(order.ID).SetCloseNetServiceCents(800).SetAddonCents(4000).ExecX(ctx)
	const serviceAccrued, addonAccrued = int64(500), int64(1000)
	db.EsportsCommissionEntry.Create().SetIdemKey("r4base:service:1").
		SetOrderID(order.ID).SetPlayerID(player.ID).
		SetKind(esportscommissionentry.KindService).SetSeatIndex(1).
		SetBaseCents(800).SetAmountCents(serviceAccrued).
		SetStatus(esportscommissionentry.StatusPendingSettlement).SaveX(ctx)
	db.EsportsCommissionEntry.Create().SetIdemKey("r4base:addon:1").
		SetOrderID(order.ID).SetPlayerID(player.ID).
		SetKind(esportscommissionentry.KindAddon).SetSeatIndex(1).
		SetBaseCents(4000).SetAmountCents(addonAccrued).
		SetStatus(esportscommissionentry.StatusPendingSettlement).SaveX(ctx)

	current := db.EsportsOrder.GetX(ctx, order.ID)
	item := &ent.EsportsAfterSale{AfterSaleNo: "AS-R4-BASE-1", RefundCents: 400}
	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = reverseOrderCommission(ctx, tx, current, item, now); err != nil {
		_ = tx.Rollback()
		t.Fatalf("退款冲回失败：%v", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var serviceReversed, addonReversed int64
	for _, row := range db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.OrderIDEQ(order.ID),
		esportscommissionentry.KindEQ(esportscommissionentry.KindReversal),
	).AllX(ctx) {
		if strings.Contains(row.Note, addonReversalNote) {
			addonReversed += -row.AmountCents
			continue
		}
		serviceReversed += -row.AmountCents
	}
	// 统一可退基数 800：服务 500×400/800=250、加钟 1000×400/800=500。
	if serviceReversed*addonAccrued != addonReversed*serviceAccrued {
		t.Fatalf("服务与加钟冲回比例不一致：服务 %d/%d，加钟 %d/%d",
			serviceReversed, serviceAccrued, addonReversed, addonAccrued)
	}
	if serviceReversed != 250 || addonReversed != 500 {
		t.Fatalf("统一可退基数后冲回额不正确（期望服务 250、加钟 500）：服务 %d、加钟 %d",
			serviceReversed, addonReversed)
	}
}

// §15.5-04：折后明细金额 ÷ 承诺服务量得到「每最小服务单位」金额，逐单位分摊，
// 合计恒等于折后金额，舍入尾差固定记在最后一单位。
func TestServiceUnitAmountsKeepTotalAndTailOnLastUnit(t *testing.T) {
	// 承诺量 3、折后 100 分：逐单位 33/33/34（尾差落在最后一单位）。
	units := splitServiceUnits(100, 3)
	if len(units) != 3 {
		t.Fatalf("逐最小服务单位必须拆出承诺量条（期望 3）：%v", units)
	}
	var total int64
	for _, amount := range units {
		total += amount
	}
	if total != 100 {
		t.Fatalf("逐单位分摊合计必须恒等于折后明细金额 100：%v（合计 %d）", units, total)
	}
	if units[0] != 33 || units[1] != 33 || units[2] != 34 {
		t.Fatalf("逐单位金额应为 33/33/34 且尾差落在最后一单位：%v", units)
	}
	if single := splitServiceUnits(100, 1); len(single) != 1 || single[0] != 100 {
		t.Fatalf("承诺量 1 时应整笔归一个单位：%v", single)
	}
	for _, amount := range []int64{7, 1, 0} {
		parts := splitServiceUnits(amount, 3)
		var sum int64
		for _, part := range parts {
			sum += part
		}
		if sum != amount {
			t.Fatalf("金额 %d 逐单位分摊后合计不守恒：%v", amount, parts)
		}
	}
}

// §12.7-01：分成明细接口须返回规则数值（基点 / 固定额）、该明细被冲回的扣减项与到手净额。
func TestCommissionEntryViewExposesRuleValueAndDeduction(t *testing.T) {
	db := openRound4DB(t, "round4_commission_entry_view")
	defer db.Close()
	ctx := t.Context()
	useRound4ORM(t, db)
	admin := seedAdminAccount(t, db, "round4-entry-view")
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

	order := db.EsportsOrder.Create().SetOrderNo("R4-VIEW-1").SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusCompleted).SaveX(ctx)
	rule := db.EsportsCommissionRule.Create().SetRuleNo("CR-R4-VIEW").
		SetStatus(esportscommissionrule.StatusActive).SetScope(esportscommissionrule.ScopeGlobal).
		SetCalcMode(esportscommissionrule.CalcModePercent).SetValueBp(3000).SaveX(ctx)
	db.EsportsCommissionEntry.Create().SetIdemKey("r4view:service:1").
		SetOrderID(order.ID).SetPlayerID(7).
		SetKind(esportscommissionentry.KindService).SetSeatIndex(1).
		SetRuleID(rule.ID).SetRuleNo(rule.RuleNo).SetRuleVersion(2).
		SetCalcMode(esportscommissionentry.CalcModePercent).
		SetBaseCents(1000).SetAmountCents(300).
		SetStatus(esportscommissionentry.StatusPendingSettlement).SaveX(ctx)
	db.EsportsCommissionEntry.Create().SetIdemKey("r4view:reversal:1").
		SetOrderID(order.ID).SetPlayerID(7).
		SetKind(esportscommissionentry.KindReversal).SetSeatIndex(1).
		SetRuleNo(rule.RuleNo).SetRuleVersion(2).
		SetCalcMode(esportscommissionentry.CalcModePercent).
		SetBaseCents(1000).SetAmountCents(-100).
		SetStatus(esportscommissionentry.StatusPendingSettlement).SaveX(ctx)

	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("uid", admin.UID)
		c.Locals("isSuperAdmin", true)
		return c.Next()
	})
	app.Get("/admin/commission-entries", adminCommissionEntries)

	status, _, payload := getJSON(t, app, "/admin/commission-entries?order_id="+itoa(order.ID))
	if status != http.StatusOK {
		t.Fatalf("管理员查询分成明细应成功，实际 %d：%+v", status, payload)
	}
	rows, ok := payload["data"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("分成明细响应不是明细数组：%+v", payload["data"])
	}
	found := false
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		if row["kind"] != string(esportscommissionentry.KindService) {
			continue
		}
		found = true
		if row["rule_value_bp"] != float64(3000) {
			t.Fatalf("分成明细未返回规则数值(value_bp)：%+v", row)
		}
		if row["deducted_cents"] != float64(-100) {
			t.Fatalf("分成明细未返回扣减项(冲回合计)：%+v", row)
		}
		if row["net_cents"] != float64(200) {
			t.Fatalf("分成明细未返回到手净额(扣减后净额)：%+v", row)
		}
	}
	if !found {
		t.Fatalf("响应缺少服务分成明细：%+v", rows)
	}
}

// §12.6-06：结算单详情须可下钻本期明细并带 rule_version，规则版本可从结算单追溯。
func TestSettlementDetailInlinesEntriesWithRuleVersion(t *testing.T) {
	db := openRound4DB(t, "round4_settlement_detail")
	defer db.Close()
	ctx := t.Context()
	useRound4ORM(t, db)
	admin := seedAdminAccount(t, db, "round4-settlement-detail")
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

	settlement := db.EsportsSettlement.Create().SetSettlementNo("ST-R4-DETAIL").
		SetOwnerType(esportssettlement.OwnerTypePlayer).SetOwnerID(7).
		SetPeriodStart(now.AddDate(0, 0, -7)).SetPeriodEnd(now).
		SetGrossCents(300).SetAdjustmentCents(-100).SetNetCents(200).SetEntryCount(2).
		SetStatus(esportssettlement.StatusDraft).SaveX(ctx)
	db.EsportsCommissionEntry.Create().SetIdemKey("r4detail:service:1").
		SetOrderID(1).SetPlayerID(7).
		SetKind(esportscommissionentry.KindService).SetSeatIndex(1).
		SetRuleNo("CR-R4-DETAIL").SetRuleVersion(3).
		SetCalcMode(esportscommissionentry.CalcModePercent).
		SetBaseCents(1000).SetAmountCents(300).
		SetSettlementID(settlement.ID).
		SetStatus(esportscommissionentry.StatusPendingSettlement).SaveX(ctx)
	db.EsportsCommissionEntry.Create().SetIdemKey("r4detail:reversal:1").
		SetOrderID(1).SetPlayerID(7).
		SetKind(esportscommissionentry.KindReversal).SetSeatIndex(1).
		SetRuleNo("CR-R4-DETAIL").SetRuleVersion(3).
		SetCalcMode(esportscommissionentry.CalcModePercent).
		SetBaseCents(1000).SetAmountCents(-100).
		SetSettlementID(settlement.ID).
		SetStatus(esportscommissionentry.StatusPendingSettlement).SaveX(ctx)

	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("uid", admin.UID)
		c.Locals("isSuperAdmin", true)
		return c.Next()
	})
	app.Get("/admin/settlements/:id", adminSettlementDetail)

	status, _, payload := getJSON(t, app, "/admin/settlements/"+itoa(settlement.ID))
	if status != http.StatusOK {
		t.Fatalf("结算单详情应成功，实际 %d：%+v", status, payload)
	}
	data, _ := payload["data"].(map[string]any)
	entries, ok := data["entries"].([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("结算单详情未内联本期可下钻明细：%+v", data)
	}
	for _, raw := range entries {
		entry, _ := raw.(map[string]any)
		if entry["rule_version"] != float64(3) {
			t.Fatalf("结算单下钻明细未带规则版本号：%+v", entry)
		}
		if entry["settlement_id"] != float64(settlement.ID) {
			t.Fatalf("结算单下钻明细未关联结算单：%+v", entry)
		}
	}
	if data["entry_count"] != float64(2) || data["detail_truncated"] != false {
		t.Fatalf("结算单详情缺少本期明细计数 / 截断标记：%+v", data)
	}
}

// §12.7-03：财务总表须给出按订单 / 陪玩的分成明细级对账（含规则版本）。
func TestFinanceReportCommissionReconciliation(t *testing.T) {
	db := openRound4DB(t, "round4_finance_reconciliation")
	defer db.Close()
	ctx := t.Context()
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	order := seedReportOrder(t, db, "recon-1", day.Add(time.Hour), 11, 2000, 500,
		esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusCompleted)
	db.EsportsCommissionEntry.Create().SetIdemKey("r4recon:service:1").
		SetOrderID(order.ID).SetPlayerID(7).
		SetKind(esportscommissionentry.KindService).SetSeatIndex(1).
		SetRuleNo("CR-R4-RECON").SetRuleVersion(2).
		SetCalcMode(esportscommissionentry.CalcModePercent).
		SetBaseCents(2000).SetAmountCents(500).
		SetCreatedAt(day.Add(time.Hour)).
		SetStatus(esportscommissionentry.StatusPendingSettlement).SaveX(ctx)
	db.EsportsCommissionEntry.Create().SetIdemKey("r4recon:reversal:1").
		SetOrderID(order.ID).SetPlayerID(7).
		SetKind(esportscommissionentry.KindReversal).SetSeatIndex(1).
		SetRuleNo("CR-R4-RECON").SetRuleVersion(2).
		SetCalcMode(esportscommissionentry.CalcModePercent).
		SetBaseCents(2000).SetAmountCents(-125).
		SetCreatedAt(day.Add(2 * time.Hour)).
		SetStatus(esportscommissionentry.StatusPendingSettlement).SaveX(ctx)

	result, err := financeReport(ctx, db, reportFilter{Start: day, End: day.AddDate(0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	section, ok := result["commission_reconciliation"].(map[string]any)
	if !ok {
		t.Fatalf("财务总表缺少按订单/陪玩的分成明细级对账：%v", mapKeys(result))
	}
	rows, ok := section["rows"].([]commissionReconciliationRow)
	if !ok || len(rows) != 1 {
		t.Fatalf("对账报表未按订单+陪玩聚合成行：%+v", section["rows"])
	}
	row := rows[0]
	if row.OrderNo != order.OrderNo || row.PlayerID != 7 {
		t.Fatalf("对账行未带原单据引用：%+v", row)
	}
	if row.RuleNo != "CR-R4-RECON" || row.RuleVersion != 2 {
		t.Fatalf("对账行未带规则版本：%+v", row)
	}
	if row.GrossCents != 500 || row.ReversalCents != -125 || row.NetCents != 375 {
		t.Fatalf("对账行金额口径不正确：%+v", row)
	}
	details, ok := section["details"].([]map[string]any)
	if !ok || len(details) != 2 {
		t.Fatalf("对账报表缺少逐笔明细：%+v", section["details"])
	}
	if _, has := section["detail_truncated"]; !has {
		t.Fatalf("对账报表缺少 detail_truncated 标记：%+v", mapKeys(section))
	}
	for _, detail := range details {
		if detail["order_no"] != order.OrderNo || detail["rule_version"] != 2 {
			t.Fatalf("对账逐笔明细未带原单据 / 规则版本：%+v", detail)
		}
	}

	// 导出必须与查看同口径（含逐笔明细）。
	var buf bytes.Buffer
	if _, err := exportReportCSV(ctx, db, "finance", reportFilter{Start: day, End: day.AddDate(0, 0, 1)}, 0, &buf); err != nil {
		t.Fatal(err)
	}
	exported := 0
	for _, record := range parseExportCSV(t, buf.String()) {
		if len(record) >= 2 && record[0] == "commission_reconciliation" {
			exported++
		}
	}
	if exported != 3 { // 表头 + 2 条逐笔明细
		t.Fatalf("导出缺少分成明细级对账（期望表头 + 2 条）：%d 行\n%s", exported, buf.String())
	}
}

// §12.7-04：财务总表须给出按周期聚合工作室剩余（studio_remainder_cents）的抽成汇总。
func TestFinanceReportStudioRemainderSummary(t *testing.T) {
	db := openRound4DB(t, "round4_finance_studio")
	defer db.Close()
	ctx := t.Context()
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	seed := func(no string, at time.Time, adminID int, amount, studio int64) {
		t.Helper()
		db.EsportsCsCommissionEntry.Create().SetIdemKey("r4studio:" + no).
			SetAdminID(adminID).SetKind(esportscscommissionentry.KindService).
			SetBaseCents(amount + studio).SetAmountCents(amount).
			SetStudioRemainderCents(studio).SetCreatedAt(at).SaveX(ctx)
	}
	seed("a", day.Add(time.Hour), 11, 300, 700)
	seed("b", day.Add(2*time.Hour), 22, 200, 500)

	result, err := financeReport(ctx, db, reportFilter{Start: day, End: day.AddDate(0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	studio, ok := result["studio"].(map[string]any)
	if !ok {
		t.Fatalf("财务总表缺少工作室抽成汇总：%v", mapKeys(result))
	}
	summary, ok := studio["summary"].(map[string]any)
	if !ok || summary["studio_remainder_cents"] != int64(1200) ||
		summary["cs_commission_cents"] != int64(500) || summary["entry_count"] != 2 {
		t.Fatalf("工作室抽成汇总口径不正确：%+v", studio["summary"])
	}
	buckets, ok := studio["buckets"].([]studioRemainderBucket)
	if !ok || len(buckets) != 1 {
		t.Fatalf("工作室抽成汇总未按周期聚合：%+v", studio["buckets"])
	}
	if buckets[0].Bucket != "2026-10-11" || buckets[0].StudioRemainderCents != 1200 ||
		buckets[0].CsCommissionCents != 500 || buckets[0].EntryCount != 2 {
		t.Fatalf("工作室抽成周期桶不正确：%+v", buckets[0])
	}

	var buf bytes.Buffer
	if _, err := exportReportCSV(ctx, db, "finance", reportFilter{Start: day, End: day.AddDate(0, 0, 1)}, 0, &buf); err != nil {
		t.Fatal(err)
	}
	exportedStudio := false
	for _, record := range parseExportCSV(t, buf.String()) {
		if len(record) >= 3 && record[0] == "summary" && record[1] == "studio.studio_remainder_cents" {
			exportedStudio = true
		}
	}
	if !exportedStudio {
		t.Fatalf("导出缺少工作室抽成汇总：\n%s", buf.String())
	}
}

// §17-06：客服业绩 / 派单效率 / 财务三类报表须返回可下钻明细（单据 id + 单号 + detail_truncated），
// 售后质量明细须带来源订单号。
func TestReportDetailsCarryOriginalDocumentRefs(t *testing.T) {
	db := openRound4DB(t, "round4_report_detail_refs")
	defer db.Close()
	ctx := t.Context()
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	order := seedReportOrder(t, db, "detail-1", day.Add(time.Hour), 11, 2000, 0,
		esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusCompleted)
	db.EsportsDispatchSeat.Create().SetOrderID(order.ID).SetSeatIndex(1).
		SetInvitedByAdminID(11).SetStatus(esportsdispatchseat.StatusAccepted).
		SetCreatedAt(day.Add(time.Hour)).SaveX(ctx)
	db.EsportsAfterSale.Create().SetAfterSaleNo("detail-as-1").
		SetSourceType(esportsaftersale.SourceTypeOrder).SetSourceID(order.ID).
		SetKind(esportsaftersale.KindRefundRequest).SetChannel(esportsaftersale.ChannelCustomer).
		SetDemand("测试").SetRefundCents(200).
		SetCreatedAt(day.Add(2 * time.Hour)).SaveX(ctx)
	db.EsportsSettlement.Create().SetSettlementNo("ST-R4-REF").
		SetOwnerType(esportssettlement.OwnerTypePlayer).SetOwnerID(7).
		SetPeriodStart(day).SetPeriodEnd(day.AddDate(0, 0, 1)).
		SetGrossCents(300).SetNetCents(300).SetEntryCount(1).
		SetCreatedAt(day.Add(3 * time.Hour)).SaveX(ctx)

	filter := reportFilter{Start: day, End: day.AddDate(0, 0, 1)}
	cs, err := csPerformanceReport(ctx, db, filter)
	if err != nil {
		t.Fatal(err)
	}
	assertReportDetailRefs(t, cs, "cs_performance")
	dispatch, err := dispatchEfficiencyReport(ctx, db, filter)
	if err != nil {
		t.Fatal(err)
	}
	assertReportDetailRefs(t, dispatch, "dispatch_efficiency")
	finance, err := financeReport(ctx, db, filter)
	if err != nil {
		t.Fatal(err)
	}
	assertReportDetailRefs(t, finance, "finance")

	after, err := aftersaleQualityReport(ctx, db, filter, 0)
	if err != nil {
		t.Fatal(err)
	}
	details, ok := after["details"].([]map[string]any)
	if !ok || len(details) == 0 {
		t.Fatalf("售后质量报表缺少下钻明细：%+v", after)
	}
	if details[0]["order_no"] != order.OrderNo || details[0]["source_id"] != order.ID {
		t.Fatalf("售后质量明细未带来源订单号：%+v", details[0])
	}
}

// assertReportDetailRefs 校验报表响应存在可定位原单据的引用字段与截断标记。
func assertReportDetailRefs(t *testing.T, result map[string]any, reportType string) {
	t.Helper()
	details, ok := result["details"].([]map[string]any)
	if !ok || len(details) == 0 {
		t.Fatalf("%s 报表缺少可下钻明细：%v", reportType, mapKeys(result))
	}
	if _, has := result["detail_truncated"]; !has {
		t.Fatalf("%s 报表缺少 detail_truncated 标记：%v", reportType, mapKeys(result))
	}
	if id, ok := details[0]["id"].(int); !ok || id <= 0 {
		t.Fatalf("%s 报表下钻明细缺少单据 id：%+v", reportType, details[0])
	}
	ref := ""
	for _, key := range []string{"order_no", "settlement_no", "receipt_no", "tx_no"} {
		if value, ok := details[0][key].(string); ok && value != "" {
			ref = key
		}
	}
	if ref == "" {
		t.Fatalf("%s 报表下钻明细缺少原单据引用字段：%+v", reportType, details[0])
	}
}

// §17-07：服务端须按报表类型拒绝不支持的筛选维度（返回 400，而不是静默按全量统计）。
func TestReportRejectsUnsupportedDimensions(t *testing.T) {
	db := openRound4DB(t, "round4_report_reject_dimensions")
	defer db.Close()
	useRound4ORM(t, db)
	admin := seedAdminAccount(t, db, "round4-report-reject")
	app := reportTestApp(admin, true)

	cases := []struct{ path, message string }{
		{"/admin/reports/aftersale_quality?product_id=1", "售后质量报表"},
		{"/admin/reports/aftersale_quality?game=%E7%8E%8B%E8%80%85", "售后质量报表"},
		{"/admin/reports/aftersale_quality?payment_channel=wechat", "售后质量报表"},
		{"/admin/reports/aftersale_quality?appointment_type=instant", "售后质量报表"},
		{"/admin/reports/aftersale_quality?seat_type=single", "售后质量报表"},
		{"/admin/reports/owner_overview?kind=refund_request", "经营总览报表"},
		{"/admin/reports/cs_performance?fault_party=player", "客服业绩报表"},
		{"/admin/reports/dispatch_efficiency?kind=refund_request", "派单效率报表"},
		{"/admin/reports/finance?kind=refund_request", "财务资金报表"},
		{"/admin/reports/finance?fault_party=player", "财务资金报表"},
	}
	for _, item := range cases {
		status, _, payload := getJSON(t, app, item.path)
		if status != http.StatusBadRequest {
			t.Fatalf("%s 不支持的维度应返回 400，实际 %d：%+v", item.path, status, payload)
		}
		message, _ := payload["message"].(string)
		if !strings.Contains(message, item.message) {
			t.Fatalf("%s 拒绝提示缺少报表名（期望含 %q）：%q", item.path, item.message, message)
		}
	}
	// 支持的维度不得被误拒。
	for _, path := range []string{
		"/admin/reports/aftersale_quality?kind=refund_request&fault_party=player",
		"/admin/reports/owner_overview?product_id=1&channel_source=wecom",
	} {
		if status, _, payload := getJSON(t, app, path); status != http.StatusOK {
			t.Fatalf("%s 支持的维度被误拒：%d %+v", path, status, payload)
		}
	}
}

// §17-07：响应须给出该报表类型可用的筛选项清单（supported / unsupported）。
func TestReportFilterCapabilitiesExposed(t *testing.T) {
	db := openRound4DB(t, "round4_report_capabilities")
	defer db.Close()
	useRound4ORM(t, db)
	admin := seedAdminAccount(t, db, "round4-report-caps")
	app := reportTestApp(admin, true)

	status, _, payload := getJSON(t, app, "/admin/reports/aftersale_quality")
	if status != http.StatusOK {
		t.Fatalf("售后质量报表应成功，实际 %d：%+v", status, payload)
	}
	data, _ := payload["data"].(map[string]any)
	caps, ok := data["filter_capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("报表响应缺少可用筛选项清单：%v", mapKeys(data))
	}
	supported := anyStrings(caps["supported"])
	for _, key := range []string{"kind", "fault_party", "channel_source", "entry_point"} {
		if !containsString(supported, key) {
			t.Fatalf("售后质量报表可用维度缺少 %s：%v", key, supported)
		}
	}
	unsupported := anyStrings(caps["unsupported"])
	for _, key := range []string{"product_id", "game", "payment_channel", "appointment_type", "seat_type"} {
		if !containsString(unsupported, key) {
			t.Fatalf("售后质量报表不支持维度缺少 %s：%v", key, unsupported)
		}
	}

	status, _, payload = getJSON(t, app, "/admin/reports/finance")
	if status != http.StatusOK {
		t.Fatalf("财务总表应成功，实际 %d：%+v", status, payload)
	}
	data, _ = payload["data"].(map[string]any)
	caps, _ = data["filter_capabilities"].(map[string]any)
	if !containsString(anyStrings(caps["unsupported"]), "kind") ||
		!containsString(anyStrings(caps["unsupported"]), "fault_party") {
		t.Fatalf("财务总表不支持维度缺少 kind/fault_party：%v", caps["unsupported"])
	}
}

// §17-09 / §3.5-06：报表与导出的可见性谓词须与订单列表同口径
// （本人负责 OR 存在协作授权），协作客服在报表与导出中不得丢失被授权订单。
func TestReportVisibilityIncludesCollaborators(t *testing.T) {
	db := openRound4DB(t, "round4_report_collaborator_scope")
	defer db.Close()
	ctx := t.Context()
	useRound4ORM(t, db)
	me := seedAdminAccount(t, db, "round4-collab-me")
	other := seedAdminAccount(t, db, "round4-collab-other")
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	mine := seedReportOrder(t, db, "collab-mine", day.Add(time.Hour), me.ID, 1000, 0,
		esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusCompleted)
	collaborated := seedReportOrder(t, db, "collab-other", day.Add(2*time.Hour), other.ID, 2000, 0,
		esportsorder.PaymentStatusPaid, esportsorder.FulfillmentStatusCompleted)
	db.EsportsOrderCollaborator.Create().SetOrderID(collaborated.ID).
		SetAdminID(me.ID).SetGrantedByAdminID(other.ID).SaveX(ctx)

	// 与只读订单边界同口径：两笔订单都在 loadOwnedOrder 可见集合内。
	for _, id := range []int{mine.ID, collaborated.ID} {
		if _, err := loadOwnedOrder(ctx, db, me.ID, false, id); err != nil {
			t.Fatalf("订单 %d 应属于协作可见集合：%v", id, err)
		}
	}

	start := day.Format(time.RFC3339)
	end := day.AddDate(0, 0, 1).Format(time.RFC3339)
	app := reportTestApp(me, false)
	status, _, payload := getJSON(t, app, "/admin/reports/owner_overview?start="+start+"&end="+end)
	if status != http.StatusOK {
		t.Fatalf("协作客服查看报表应成功，实际 %d：%+v", status, payload)
	}
	data, _ := payload["data"].(map[string]any)
	summary, _ := data["summary"].(map[string]any)
	if summary["order_count"] != float64(2) || summary["revenue_cents"] != float64(3000) {
		t.Fatalf("协作客服报表未纳入被授权协作订单：%+v", summary)
	}

	// 导出与查看必须同口径。
	scoped := scopeReportFilter(reportFilter{Start: day, End: day.AddDate(0, 0, 1)}, me.ID, false)
	var buf bytes.Buffer
	if _, err := exportReportCSV(ctx, db, "owner_overview", scoped, 0, &buf); err != nil {
		t.Fatal(err)
	}
	exported := map[string]bool{}
	for _, record := range parseExportCSV(t, buf.String()) {
		if len(record) >= 3 && record[0] == "order" && record[1] != "id" {
			exported[record[2]] = true
		}
	}
	if !exported[mine.OrderNo] || !exported[collaborated.OrderNo] {
		t.Fatalf("导出未纳入被授权协作订单：%v\n%s", exported, buf.String())
	}
}

func containsTailMark(note string) bool { return strings.Contains(note, "尾差") }

func anyStrings(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	}
	return nil
}

func mapKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	return keys
}

func itoa(value int) string { return strconv.Itoa(value) }
