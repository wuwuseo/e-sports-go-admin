package esports

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscommissionentry"
	"go-admin/internal/ent/esportscscommissionentry"
	"go-admin/internal/ent/esportscscommissionrule"
	"go-admin/internal/ent/esportsgrowthledger"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/esportsservicesegment"
)

func seedBackfillSku(t *testing.T, db *ent.Client, code string) *ent.EsportsSku {
	t.Helper()
	sku, err := saveSku(t.Context(), db, skuInput{
		Code: code, Title: "补录测试商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 2, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: 10, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	return sku
}

// 管理员历史订单补录（§3.3 / §4.1 / §12.5）：入口固定、不占名额、分成与成长值默认计提、
// 客服提成默认不计、留痕审计且幂等。
func TestAdminBackfillOrderIsolatesCommissionAndQuota(t *testing.T) {
	db := openAfterSaleDB(t, "esports_admin_backfill")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	settled := now.AddDate(0, 0, -30)
	customer := saveCustomerOrFail(t, db, 11)
	sku := seedBackfillSku(t, db, "BACKFILL-1")

	order, err := backfillOrder(ctx, db, 11, backfillOrderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 2,
		SettledAt: settled.Format(time.RFC3339), Reason: "线下微信成交漏报，按客户流水补录",
	}, "backfill-key-0001", now)
	if err != nil {
		t.Fatal(err)
	}
	// 入口、成交金额与历史时间：订单创建时间即业务发生时间，报表按发生期归集。
	if order.EntryPoint != esportsorder.EntryPointAdminBackfill ||
		order.PayableCents != 2000 || order.PromisedServiceUnits != 4 ||
		!order.CreatedAt.Equal(settled) || !order.PaidAt.Equal(settled) {
		t.Fatalf("补录单基本信息不正确：%+v", order)
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusCompleted ||
		order.PaymentStatus != esportsorder.PaymentStatusPaid ||
		order.ClosedAt == nil || !order.ClosedAt.Equal(settled) ||
		order.CloseNetServiceCents == nil || *order.CloseNetServiceCents != 2000 {
		t.Fatalf("补录单履约与结单快照不正确：%+v", order)
	}
	// 审计留痕：补录原因、补录人、补录时刻。
	if order.BackfillReason == "" || order.BackfilledByAdminID == nil ||
		*order.BackfilledByAdminID != 11 || order.BackfilledAt == nil ||
		order.BackfilledAt.Before(now) || order.BackfillCountCommission {
		t.Fatalf("补录审计字段不正确：%+v", order)
	}
	// 隔离规则：不占 SKU 名额。
	got := db.EsportsSku.GetX(ctx, sku.ID)
	if got.QuotaReserved != 0 || got.QuotaSold != 0 {
		t.Fatalf("补录单占用了 SKU 名额：%+v", got)
	}
	// 隔离规则：陪玩分成 / 成长值默认正常计提（补录单无席位成员，分成明细保持 0 条但不被排除），
	// 客服提成默认不计入并写排除留痕。
	if count := db.EsportsCommissionEntry.Query().CountX(ctx); count != 0 {
		t.Fatalf("补录单误计提了陪玩分成：%d", count)
	}
	if count := db.EsportsCsCommissionEntry.Query().CountX(ctx); count != 0 {
		t.Fatalf("补录单默认计提了客服提成：%d", count)
	}
	if n := db.EsportsGrowthLedger.Query().Where(
		esportsgrowthledger.SourceTypeEQ(esportsgrowthledger.SourceTypeOrder),
		esportsgrowthledger.SourceIDEQ(order.OrderNo),
	).CountX(ctx); n != 1 {
		t.Fatalf("补录单默认未计提成长值：%d", n)
	}
	if n := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("order.cs_commission_excluded"),
	).CountX(ctx); n != 1 {
		t.Fatalf("缺少「客服提成排除」留痕外盒：%d", n)
	}
	events := countEventsByType(t, db)
	if events["order.backfilled"] != 1 {
		t.Fatalf("缺少 order.backfilled 事件：%+v", events)
	}
	// 幂等：同键同体返回同一张单；同键不同体视为冲突。
	again, err := backfillOrder(ctx, db, 11, backfillOrderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 2,
		SettledAt: settled.Format(time.RFC3339), Reason: "线下微信成交漏报，按客户流水补录",
	}, "backfill-key-0001", now)
	if err != nil || again.ID != order.ID {
		t.Fatalf("补录幂等失效：%v %+v", err, again)
	}
	if _, err := backfillOrder(ctx, db, 11, backfillOrderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 3,
		SettledAt: settled.Format(time.RFC3339), Reason: "线下微信成交漏报，按客户流水补录",
	}, "backfill-key-0001", now); !errors.Is(err, errConflict) {
		t.Fatalf("同键不同体未判冲突：%v", err)
	}
}

// 补录入参与数据边界校验：原因、成交时间、状态枚举、客户归属、单价都被约束。
func TestAdminBackfillOrderGuards(t *testing.T) {
	db := openAfterSaleDB(t, "esports_admin_backfill_guards")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	settled := now.AddDate(0, 0, -3).Format(time.RFC3339)
	customer := saveCustomerOrFail(t, db, 11)
	other := saveCustomerOrFail(t, db, 22)
	sku := seedBackfillSku(t, db, "BACKFILL-2")

	base := backfillOrderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
		SettledAt: settled, Reason: "线下成交漏报补录",
	}
	cases := []struct {
		name string
		in   backfillOrderInput
	}{
		{"原因为空", backfillOrderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1, SettledAt: settled}},
		{"成交时间缺失", backfillOrderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1, Reason: "线下成交漏报补录"}},
		{"成交时间为未来", backfillOrderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
			SettledAt: now.Add(time.Hour).Format(time.RFC3339), Reason: "线下成交漏报补录"}},
		{"成交时间超追溯年限", backfillOrderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
			SettledAt: now.AddDate(-6, 0, 0).Format(time.RFC3339), Reason: "线下成交漏报补录"}},
		{"状态不在终态白名单", backfillOrderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
			SettledAt: settled, Reason: "线下成交漏报补录", FulfillmentStatus: "pending_dispatch"}},
		{"未收款", backfillOrderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
			SettledAt: settled, Reason: "线下成交漏报补录", PaymentStatus: "unpaid"}},
		{"已取消单要求计入客服提成", backfillOrderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
			SettledAt: settled, Reason: "线下成交漏报补录", FulfillmentStatus: "cancelled",
			CountCsCommission: true}},
		{"单价非法", backfillOrderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
			SettledAt: settled, Reason: "线下成交漏报补录", UnitPriceCents: -1}},
		{"数量超上限", backfillOrderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1001,
			SettledAt: settled, Reason: "线下成交漏报补录"}},
		{"无 SKU 未给成交单价", backfillOrderInput{CustomerID: customer.ID, Quantity: 1,
			SettledAt: settled, Reason: "线下成交漏报补录"}},
	}
	for i, tc := range cases {
		if _, err := backfillOrder(ctx, db, 11, tc.in, "backfill-guard-0001", now); !errors.Is(err, errInvalidInput) {
			t.Fatalf("第 %d 组（%s）未拦截：%v", i+1, tc.name, err)
		}
	}
	// 客服只能给本人负责的客户补录。
	if _, err := backfillOrder(ctx, db, 11, backfillOrderInput{
		CustomerID: other.ID, SkuID: sku.ID, Quantity: 1, SettledAt: settled, Reason: "线下成交漏报补录",
	}, "backfill-guard-0002", now); !errors.Is(err, errForbidden) {
		t.Fatalf("跨客服补录未被拦截：%v", err)
	}
	// 幂等键长度受约束（与常规报单同口径）。
	if _, err := backfillOrder(ctx, db, 11, base, "short", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("过短幂等键未被拦截：%v", err)
	}
	// 未传单价时回落 SKU 规格价；显式单价按成交价落账。
	order, err := backfillOrder(ctx, db, 11, base, "backfill-guard-0003", now)
	if err != nil || order.PayableCents != 1000 {
		t.Fatalf("未传单价未回落规格价：%v %+v", err, order)
	}
	priced, err := backfillOrder(ctx, db, 11, backfillOrderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1, UnitPriceCents: 666,
		SettledAt: settled, Reason: "线下成交漏报补录",
	}, "backfill-guard-0004", now)
	if err != nil || priced.PayableCents != 666 || priced.UnitPriceCents != 666 {
		t.Fatalf("显式成交价未落账：%v %+v", err, priced)
	}
	// 已取消的历史单不写结单快照。
	cancelled, err := backfillOrder(ctx, db, 11, backfillOrderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1, SettledAt: settled,
		Reason: "历史取消单补录", FulfillmentStatus: "cancelled",
	}, "backfill-guard-0005", now)
	if err != nil || cancelled.ClosedAt != nil || cancelled.CloseNetServiceCents != nil {
		t.Fatalf("历史取消单不应写结单快照：%v %+v", err, cancelled)
	}
}

// 显式选择计入客服提成时按规则计提提成，且不再写客服提成排除留痕；成长值仍默认计提。
func TestAdminBackfillOptInCsCommission(t *testing.T) {
	db := openAfterSaleDB(t, "esports_admin_backfill_optin")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	settled := now.AddDate(0, 0, -1).Format(time.RFC3339)
	customer := saveCustomerOrFail(t, db, 11)
	sku := seedBackfillSku(t, db, "BACKFILL-3")
	// 全局 5% 客服提成，供显式开启时实际产出提成明细。
	db.EsportsCsCommissionRule.Create().
		SetRuleNo("CC-BACKFILL").SetVersion(1).
		SetStatus(esportscscommissionrule.StatusActive).
		SetScope(esportscscommissionrule.ScopeGlobal).SetScopeKey("").
		SetBaseMode(esportscscommissionrule.BaseModeOrderAmount).
		SetCalcMode(esportscscommissionrule.CalcModePercent).
		SetValueBp(500).SetPeriod(esportscscommissionrule.PeriodMonthly).SaveX(ctx)

	order, err := backfillOrder(ctx, db, 11, backfillOrderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1, SettledAt: settled,
		Reason: "历史成交补录并计入客服提成", CountCsCommission: true,
	}, "backfill-optin-0001", now)
	if err != nil {
		t.Fatal(err)
	}
	if !orderCountsCsCommission(order) {
		t.Fatalf("显式计入客服提成后仍被判为不计提：%+v", order)
	}
	view := backfillOrderView(order)
	if counted, ok := view["counted_cs_commission"].(bool); !ok || !counted {
		t.Fatalf("补录结果视图未给出客服提成计提结论：%+v", view)
	}
	if n := db.EsportsCsCommissionEntry.Query().CountX(ctx); n != 1 {
		t.Fatalf("显式计入客服提成后未计提提成：%d", n)
	}
	// 报单金额 1000 × 5% = 50，基数取订单报单金额。
	entry, err := db.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.OrderIDEQ(order.ID),
	).Only(ctx)
	if err != nil || entry.AmountCents != 50 || entry.BaseCents != 1000 || entry.AdminID != 11 {
		t.Fatalf("客服提成金额不符合规则：%v %+v", err, entry)
	}
	if n := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("order.cs_commission_excluded"),
	).CountX(ctx); n != 0 {
		t.Fatalf("计入客服提成的单不应写排除留痕：%d", n)
	}
	if n := db.EsportsGrowthLedger.Query().Where(
		esportsgrowthledger.SourceTypeEQ(esportsgrowthledger.SourceTypeOrder),
		esportsgrowthledger.SourceIDEQ(order.OrderNo),
	).CountX(ctx); n != 1 {
		t.Fatalf("显式计入客服提成后成长值未计提：%d", n)
	}
}

// 客服提成计提口径判断：常规单始终计提；补录单默认不计，仅显式开启才计入。
func TestOrderCountsCsCommissionOnlyForBackfill(t *testing.T) {
	normal := &ent.EsportsOrder{EntryPoint: esportsorder.EntryPointCsPc}
	if !orderCountsCsCommission(normal) {
		t.Fatal("常规订单不应被排除客服提成")
	}
	backfill := &ent.EsportsOrder{EntryPoint: esportsorder.EntryPointAdminBackfill}
	if orderCountsCsCommission(backfill) {
		t.Fatal("补录单默认应排除客服提成")
	}
	optedIn := &ent.EsportsOrder{
		EntryPoint: esportsorder.EntryPointAdminBackfill, BackfillCountCommission: true,
	}
	if !orderCountsCsCommission(optedIn) {
		t.Fatal("显式开启后补录单应计提客服提成")
	}
}

// 无 SKU 历史补录（§15.6）：sku_id 可省略，用 unit_price_cents 兜底定价、不绑定 SKU、不占名额，且幂等。
func TestAdminBackfillWithoutSku(t *testing.T) {
	db := openAfterSaleDB(t, "esports_admin_backfill_no_sku")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	settled := now.AddDate(0, 0, -7).Format(time.RFC3339)
	customer := saveCustomerOrFail(t, db, 11)

	in := backfillOrderInput{
		CustomerID: customer.ID, Quantity: 3, UnitPriceCents: 2500,
		SettledAt: settled, Reason: "无 SKU 的历史线下成交补录",
	}
	order, err := backfillOrder(ctx, db, 11, in, "backfill-nosku-0001", now)
	if err != nil {
		t.Fatal(err)
	}
	if order.SkuID != 0 || order.UnitPriceCents != 2500 || order.PayableCents != 7500 {
		t.Fatalf("无 SKU 补录价格不正确：%+v", order)
	}
	if order.PromisedServiceUnits != 3 || order.RequiredSeats != 1 {
		t.Fatalf("无 SKU 补录快照默认不正确：%+v", order)
	}
	// 成长值按应付额正常计提（不绑定 SKU 不影响成长值来源）。
	if n := db.EsportsGrowthLedger.Query().Where(
		esportsgrowthledger.SourceTypeEQ(esportsgrowthledger.SourceTypeOrder),
		esportsgrowthledger.SourceIDEQ(order.OrderNo),
	).CountX(ctx); n != 1 {
		t.Fatalf("无 SKU 补录未计提成长值：%d", n)
	}
	// 幂等：同键同体返回同一张单；同键不同体判冲突。
	again, err := backfillOrder(ctx, db, 11, in, "backfill-nosku-0001", now)
	if err != nil || again.ID != order.ID {
		t.Fatalf("无 SKU 补录幂等失效：%v %+v", err, again)
	}
	other := in
	other.Quantity = 4
	if _, err := backfillOrder(ctx, db, 11, other, "backfill-nosku-0001", now); !errors.Is(err, errConflict) {
		t.Fatalf("同键不同体未判冲突：%v", err)
	}
}

// 补录单与常规单在报表口径上区分：entry_point 与创建时间都按业务发生期落库。
func TestAdminBackfillOrderAppearsInSourceReports(t *testing.T) {
	db := openAfterSaleDB(t, "esports_admin_backfill_report")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	settled := now.AddDate(0, 0, -2)
	customer := saveCustomerOrFail(t, db, 11)
	sku := seedBackfillSku(t, db, "BACKFILL-4")
	if _, err := backfillOrder(ctx, db, 11, backfillOrderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
		SettledAt: settled.Format(time.RFC3339), Reason: "线下成交漏报补录",
	}, "backfill-report-0001", now); err != nil {
		t.Fatal(err)
	}
	matched, err := db.EsportsOrder.Query().Where(
		esportsorder.EntryPointEQ(esportsorder.EntryPointAdminBackfill),
		esportsorder.CreatedAtGTE(settled.Add(-time.Hour)),
		esportsorder.CreatedAtLT(settled.Add(time.Hour)),
	).Count(ctx)
	if err != nil || matched != 1 {
		t.Fatalf("补录单未按业务发生期进入渠道来源筛选：%d %v", matched, err)
	}
}

// 补录入参 JSON 契约：只认 count_cs_commission。旧名 count_commission 已废弃，
// 且**出现即返回参数错误**（而非静默按 false 处理），避免「前端字段名写错 → 开关点了没用」
// 再次变成隐性财务错误。
func TestBackfillOrderInputJSONFieldContract(t *testing.T) {
	var current backfillOrderInput
	if err := json.Unmarshal([]byte(`{"count_cs_commission":true}`), &current); err != nil ||
		!current.CountCsCommission || current.LegacyCountCommission != nil {
		t.Fatalf("count_cs_commission 未正确解析：%v %+v", err, current)
	}
	var legacy backfillOrderInput
	if err := json.Unmarshal([]byte(`{"count_commission":true}`), &legacy); err != nil ||
		legacy.LegacyCountCommission == nil || !*legacy.LegacyCountCommission {
		t.Fatalf("旧字段名应被识别为废弃字段：%v %+v", err, legacy)
	}
	// 旧字段出现必须在入口显式失败（此处用 nil db：校验发生在任何数据库访问之前）。
	if _, err := backfillOrder(t.Context(), nil, 11, legacy, "backfill-legacy-0001", time.Now()); !errors.Is(err, errInvalidInput) {
		t.Fatalf("旧字段名应返回参数错误，err=%v", err)
	}
	// 新旧字段同时出现同样拒绝，避免「新字段 true + 旧字段 false」这类歧义被静默吞掉。
	var mixed backfillOrderInput
	if err := json.Unmarshal([]byte(`{"count_cs_commission":true,"count_commission":false}`), &mixed); err != nil {
		t.Fatal(err)
	}
	if _, err := backfillOrder(t.Context(), nil, 11, mixed, "backfill-legacy-0002", time.Now()); !errors.Is(err, errInvalidInput) {
		t.Fatalf("新旧字段同时出现应返回参数错误，err=%v", err)
	}
}

// count_cs_commission=false：客服提成明细 0 条并写排除留痕，但成长值照常计提；
// 陪玩分成通道同样不受该开关影响（补录单不建派单席位，故补一条服务段后直接验证计提函数）。
func TestAdminBackfillCsCommissionOffKeepsPlayerShareAndGrowth(t *testing.T) {
	db := openAfterSaleDB(t, "esports_admin_backfill_cs_off")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	settledTime := now.AddDate(0, 0, -5)
	settled := settledTime.Format(time.RFC3339)
	customer := saveCustomerOrFail(t, db, 11)
	sku := seedBackfillSku(t, db, "BACKFILL-CS-OFF")
	// 陪玩分成 30%：若开关误伤分成，应在这里直接暴露为 0 条。
	seedActiveRule(t, db, ruleInput{
		RuleNo: "CR-BACKFILL-OFF", Scope: "global", CalcMode: "percent",
		ValueBp: 3000, UnitBasis: "sku_quantity",
	})

	order, err := backfillOrder(ctx, db, 11, backfillOrderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 2, SettledAt: settled,
		Reason: "历史成交补录且不计入客服提成", CountCsCommission: false,
	}, "backfill-cs-off-0001", now)
	if err != nil {
		t.Fatal(err)
	}
	if orderCountsCsCommission(order) || order.BackfillCountCommission {
		t.Fatalf("关闭时补录单不应计入客服提成：%+v", order)
	}
	if n := db.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.OrderIDEQ(order.ID),
	).CountX(ctx); n != 0 {
		t.Fatalf("关闭时仍计提了客服提成：%d", n)
	}
	if n := db.EsportsGrowthLedger.Query().Where(
		esportsgrowthledger.SourceTypeEQ(esportsgrowthledger.SourceTypeOrder),
		esportsgrowthledger.SourceIDEQ(order.OrderNo),
	).CountX(ctx); n != 1 {
		t.Fatalf("关闭客服提成不应影响成长值：%d", n)
	}
	// 补录单本身没有陪玩席位（历史单不建派单席位 / 服务段），陪玩分成天然为 0 条；
	// 补一条服务段后走同一计提函数，验证开关只作用于客服提成、不阻断陪玩分成。
	db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(7).SetSeatIndex(1).
		SetStatus(esportsservicesegment.StatusFinished).
		SetStartedAt(settledTime.Add(-20 * time.Minute)).SetEndedAt(settledTime).
		SetSystemSeconds(1200).SetEvidenceRef("backfill.png").SaveX(ctx)
	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = accrueOrderCommission(ctx, tx, order, now); err != nil {
		t.Fatalf("补录单陪玩分成计提失败：%v", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	entry, err := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.OrderIDEQ(order.ID),
	).Only(ctx)
	if err != nil {
		t.Fatalf("补录单未计提陪玩分成：%v", err)
	}
	// 应付 2000 × 30% = 600，单人独占全部份额。
	if entry.AmountCents != 600 || entry.PlayerID != 7 {
		t.Fatalf("陪玩分成金额不正确：%+v", entry)
	}
	// 开关关闭时，补席位也不应补出客服提成。
	if n := db.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.OrderIDEQ(order.ID),
	).CountX(ctx); n != 0 {
		t.Fatalf("关闭时仍计提了客服提成：%d", n)
	}
	if n := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("order.cs_commission_excluded"),
	).CountX(ctx); n != 1 {
		t.Fatalf("缺少「客服提成排除」留痕外盒：%d", n)
	}
}

// 幂等 hash 覆盖 count_cs_commission：仅该字段不同的同一 Idempotency-Key 必须判冲突，不能复用旧单。
func TestAdminBackfillIdempotencyHashCoversCsCommission(t *testing.T) {
	db := openAfterSaleDB(t, "esports_admin_backfill_cs_hash")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	settled := now.AddDate(0, 0, -6).Format(time.RFC3339)
	customer := saveCustomerOrFail(t, db, 11)
	sku := seedBackfillSku(t, db, "BACKFILL-CS-HASH")

	in := backfillOrderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1, SettledAt: settled,
		Reason: "线下成交漏报补录", CountCsCommission: false,
	}
	order, err := backfillOrder(ctx, db, 11, in, "backfill-cs-hash-0001", now)
	if err != nil {
		t.Fatal(err)
	}
	// 同键同体：命中幂等，返回同一张单。
	again, err := backfillOrder(ctx, db, 11, in, "backfill-cs-hash-0001", now)
	if err != nil || again.ID != order.ID {
		t.Fatalf("补录幂等失效：%v %+v", err, again)
	}
	// 仅开关不同：请求 hash 不同，必须冲突而不是复用旧单。
	optedIn := in
	optedIn.CountCsCommission = true
	if _, err := backfillOrder(ctx, db, 11, optedIn, "backfill-cs-hash-0001", now); !errors.Is(err, errConflict) {
		t.Fatalf("仅 count_cs_commission 不同未判冲突：%v", err)
	}
	// 未复用旧单，也未多建单，且旧单仍保持「不计提客服提成」。
	if n := db.EsportsOrder.Query().Where(
		esportsorder.EntryPointEQ(esportsorder.EntryPointAdminBackfill),
	).CountX(ctx); n != 1 {
		t.Fatalf("冲突请求误建了补录单：%d", n)
	}
	if n := db.EsportsCsCommissionEntry.Query().CountX(ctx); n != 0 {
		t.Fatalf("冲突请求误计提了客服提成：%d", n)
	}
}
