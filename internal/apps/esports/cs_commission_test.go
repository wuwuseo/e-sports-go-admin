package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/apps/esports/schema"
	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportscscommissionentry"
	"go-admin/internal/ent/esportscscommissionrule"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsservicesegment"
	"go-admin/internal/ent/esportssettlement"
	"go-admin/internal/ent/migrate"
)

// createAndActivatePlayerRule 建一条生效的陪玩分成规则，用于构造「可分基数被陪玩分成占用」的场景。
func createAndActivatePlayerRule(t *testing.T, db *ent.Client, adminID, valueBp int, now time.Time) error {
	t.Helper()
	rule, err := createCommissionRule(t.Context(), db, ruleInput{
		RuleNo: "CR-CS-TEST", Scope: "global", CalcMode: "percent",
		ValueBp: valueBp, UnitBasis: "sku_quantity",
	}, adminID, now)
	if err != nil {
		return err
	}
	_, err = activateCommissionRule(t.Context(), db, rule.ID, adminID, now)
	return err
}

func openCsCommissionDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	return enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
}

func seedAdmin(t *testing.T, db *ent.Client, uid string) *ent.Admin {
	t.Helper()
	return db.Admin.Create().SetEmail(uid + "@example.com").SetPassword("test").
		SetUID(uid).SetUsername(uid).SetStatus("active").SaveX(t.Context())
}

// 规则配置校验：基数与计费方式的非法组合、阶梯必须严格递增、admin 范围必须给 key。
func TestCsCommissionRuleValidation(t *testing.T) {
	db := openCsCommissionDB(t, "esports_cs_commission_rule")
	ctx := t.Context()
	admin := seedAdmin(t, db, "cc-rule-admin")
	now := time.Now()

	invalid := []csCommissionRuleInput{
		// admin 范围缺 scope_key。
		{Scope: "admin", BaseMode: "order_amount", CalcMode: "percent", ValueBp: 500, Period: "monthly"},
		// 单量基数不能按比例计价。
		{Scope: "global", BaseMode: "order_count", CalcMode: "percent", ValueBp: 500, Period: "monthly"},
		// 无阶梯时必须给 value_bp。
		{Scope: "global", BaseMode: "order_amount", CalcMode: "percent", Period: "monthly"},
		// 阶梯阈值必须严格递增。
		{Scope: "global", BaseMode: "order_amount", CalcMode: "percent", Period: "monthly",
			Tiers: []schema.CsCommissionTier{{ThresholdBaseCents: 10000, ValueBp: 500}, {ThresholdBaseCents: 10000, ValueBp: 800}}},
		// 固定额必须为正。
		{Scope: "global", BaseMode: "order_count", CalcMode: "fixed_per_order", Period: "monthly"},
		// 周期非法。
		{Scope: "global", BaseMode: "order_amount", CalcMode: "percent", ValueBp: 500, Period: "yearly"},
	}
	for i, in := range invalid {
		if _, err := createCsCommissionRule(ctx, db, in, admin.ID, now); !errors.Is(err, errInvalidInput) {
			t.Fatalf("第 %d 组非法配置应被拒，err=%v", i+1, err)
		}
	}

	rule, err := createCsCommissionRule(ctx, db, csCommissionRuleInput{
		RuleNo: "CC-TEST", Scope: "global", BaseMode: "order_amount", CalcMode: "percent",
		ValueBp: 500, Period: "monthly", Note: "全局 5%",
	}, admin.ID, now)
	if err != nil {
		t.Fatalf("合法规则应创建成功，err=%v", err)
	}
	if rule.Version != 1 || rule.Status != esportscscommissionrule.StatusDraft {
		t.Fatalf("新规则应为 v1 draft，got v%d %s", rule.Version, rule.Status)
	}
	// 同一 rule_no 再建一版 → 版本递增。
	second, err := createCsCommissionRule(ctx, db, csCommissionRuleInput{
		RuleNo: "CC-TEST", Scope: "global", BaseMode: "order_amount", CalcMode: "percent",
		ValueBp: 800, Period: "monthly",
	}, admin.ID, now)
	if err != nil || second.Version != 2 {
		t.Fatalf("同编号应递增到 v2，got=%v err=%v", second, err)
	}
	// 生效 v2：v1 不生效（v1 仍是 draft），v2 转 active。
	active, err := activateCsCommissionRule(ctx, db, second.ID, admin.ID, now)
	if err != nil || active.Status != esportscscommissionrule.StatusActive {
		t.Fatalf("生效失败：%v err=%v", active, err)
	}
	if got := db.EsportsCsCommissionRule.GetX(ctx, rule.ID).Status; got != esportscscommissionrule.StatusDraft {
		t.Fatalf("未生效的 draft 行不受影响，got=%s", got)
	}
	// 回滚：以历史版本为模板生成更高版本并直接生效。
	rolled, err := rollbackCsCommissionRule(ctx, db, rule.ID, admin.ID, now)
	if err != nil {
		t.Fatalf("回滚失败：%v", err)
	}
	if rolled.Version != 3 || rolled.Status != esportscscommissionrule.StatusActive || rolled.ValueBp != 500 {
		t.Fatalf("回滚应生成 v3 active 且沿用模板值，got=%+v", rolled)
	}
	// 原 active 转 history。
	if got := db.EsportsCsCommissionRule.GetX(ctx, second.ID).Status; got != esportscscommissionrule.StatusHistory {
		t.Fatalf("原 active 应转 history，got=%s", got)
	}
}

// 结单计提客服提成：基数取报单金额、按比例计提，且受「可分基数减陪玩分成」上限约束。
func TestCsCommissionAccrualOnClose(t *testing.T) {
	db := openCsCommissionDB(t, "esports_cs_commission_accrual")
	ctx := t.Context()
	admin := seedAdmin(t, db, "cc-accrual-admin")
	now := time.Now()

	// 陪玩分成 30%（留出可分余额），客服提成 10%。
	if err := createAndActivatePlayerRule(t, db, admin.ID, 3000, now); err != nil {
		t.Fatal(err)
	}
	csRule, err := createCsCommissionRule(ctx, db, csCommissionRuleInput{
		RuleNo: "CC-ACCRUAL", Scope: "global", BaseMode: "order_amount", CalcMode: "percent",
		ValueBp: 1000, Period: "monthly",
	}, admin.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activateCsCommissionRule(ctx, db, csRule.ID, admin.ID, now); err != nil {
		t.Fatal(err)
	}

	order := seedCloseableOrder(t, db, "CC-ORDER-1", 10000, []segSeed{{playerID: 1, seatIndex: 1, seconds: 600}})
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(admin.ID).SaveX(ctx)
	if _, err = closeOrder(ctx, db, order.ID, admin.ID, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatalf("结单应成功，err=%v", err)
	}
	entry, err := db.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.OrderIDEQ(order.ID)).Only(ctx)
	if err != nil {
		t.Fatalf("结单后应有客服提成明细，err=%v", err)
	}
	// 报单金额 10000 × 10% = 1000。
	if entry.AmountCents != 1000 || entry.BaseCents != 10000 || entry.AdminID != admin.ID {
		t.Fatalf("提成计提不正确：%+v", entry)
	}
	if entry.Status != esportscscommissionentry.StatusPendingSettlement {
		t.Fatalf("新提成应为待结算，got=%s", entry.Status)
	}
	// 幂等：再次结单不会重复计提（订单已 completed，结单本身会冲突）。
	if got := db.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.OrderIDEQ(order.ID)).CountX(ctx); got != 1 {
		t.Fatalf("提成明细应为 1 条，got=%d", got)
	}

	// 上限：客服提成规则改成 90%，则受「可分基数 = 10000 − 陪玩分成 3000 = 7000」截断。
	capRule, err := createCsCommissionRule(ctx, db, csCommissionRuleInput{
		RuleNo: "CC-ACCRUAL", Scope: "global", BaseMode: "order_amount", CalcMode: "percent",
		ValueBp: 9000, Period: "monthly",
	}, admin.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activateCsCommissionRule(ctx, db, capRule.ID, admin.ID, now); err != nil {
		t.Fatal(err)
	}
	second := seedCloseableOrder(t, db, "CC-ORDER-2", 10000, []segSeed{{playerID: 1, seatIndex: 1, seconds: 600}})
	db.EsportsOrder.UpdateOneID(second.ID).SetResponsibleAdminID(admin.ID).SaveX(ctx)
	if _, err = closeOrder(ctx, db, second.ID, admin.ID, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatal(err)
	}
	capped, err := db.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.OrderIDEQ(second.ID)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if capped.AmountCents != 7000 {
		t.Fatalf("应被可分基数上限截断到 7000，got=%d", capped.AmountCents)
	}
	if capped.Note == "" {
		t.Fatal("被截断时应在备注中说明")
	}

	// 无订单负责客服时不计提。
	third := seedCloseableOrder(t, db, "CC-ORDER-3", 10000, []segSeed{{playerID: 1, seatIndex: 1, seconds: 600}})
	db.EsportsOrder.UpdateOneID(third.ID).ClearResponsibleAdminID().SaveX(ctx)
	if _, err = closeOrder(ctx, db, third.ID, admin.ID, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.OrderIDEQ(third.ID)).CountX(ctx); got != 0 {
		t.Fatalf("无业绩归属客服不应计提，got=%d", got)
	}
}

func TestCsCommissionRuleAndOwnerLockedAtPayment(t *testing.T) {
	db := openCsCommissionDB(t, "esports_cs_payment_snapshot")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	owner := seedAdmin(t, db, "cs-payment-owner")
	other := seedAdmin(t, db, "cs-payment-other")
	customer := saveCustomerOrFail(t, db, owner.ID)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "CS-LOCK-SKU", Title: "客服提成锁定商品", ServiceType: "play",
		UnitPriceCents: 1000, ServiceUnits: 1, ServiceUnitName: "局",
		RequiredSeats: 1, QuotaTotal: 2, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	v1, err := createCsCommissionRule(ctx, db, csCommissionRuleInput{
		RuleNo: "CS-LOCK", Scope: "global", BaseMode: "order_amount",
		CalcMode: "percent", ValueBp: 1000, Period: "monthly",
	}, owner.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := activateCsCommissionRule(ctx, db, v1.ID, owner.ID, now); err != nil {
		t.Fatal(err)
	}
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	order, err := placeOrder(ctx, db, owner.ID,
		orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1}, "cs-rule-lock-order", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := walletRecharge(ctx, db, owner.ID, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 1000, Note: "测试充值"}, "cs-rule-lock-wallet"); err != nil {
		t.Fatal(err)
	}
	paid, err := payOrderWithBalance(ctx, db, owner.ID, order.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if paid.CsCommissionRuleLockedAt == nil || paid.CsCommissionRuleID == nil ||
		*paid.CsCommissionRuleID != v1.ID || paid.CsCommissionAdminID == nil ||
		*paid.CsCommissionAdminID != owner.ID {
		t.Fatalf("支付时未锁定客服与规则：%+v", paid)
	}
	v2, err := createCsCommissionRule(ctx, db, csCommissionRuleInput{
		RuleNo: "CS-LOCK", Scope: "global", BaseMode: "order_amount",
		CalcMode: "percent", ValueBp: 2000, Period: "monthly",
	}, owner.ID, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := activateCsCommissionRule(ctx, db, v2.ID, owner.ID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	db.EsportsOrder.UpdateOneID(order.ID).
		SetResponsibleAdminID(other.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).SaveX(ctx)
	db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(1).SetSeatIndex(1).
		SetStatus(esportsservicesegment.StatusFinished).SetStartedAt(now).
		SetEndedAt(now.Add(time.Minute)).SetSystemSeconds(60).SetEvidenceRef("proof.png").SaveX(ctx)
	if _, err := closeOrder(ctx, db, order.ID, other.ID,
		closeOrderInput{Note: "结单"}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	entry := db.EsportsCsCommissionEntry.Query().OnlyX(ctx)
	if entry.AdminID != owner.ID || entry.RuleID == nil || *entry.RuleID != v1.ID ||
		entry.RuleVersion != v1.Version || entry.AmountCents != 100 {
		t.Fatalf("结单使用了事后客服或新版规则：%+v", entry)
	}
}

// 阶梯分档：按提成周期累计净基数命中最高档。
func TestCsCommissionTierMatching(t *testing.T) {
	db := openCsCommissionDB(t, "esports_cs_commission_tier")
	ctx := t.Context()
	admin := seedAdmin(t, db, "cc-tier-admin")
	now := time.Now()

	rule, err := createCsCommissionRule(ctx, db, csCommissionRuleInput{
		RuleNo: "CC-TIER", Scope: "global", BaseMode: "order_amount", CalcMode: "percent",
		ValueBp: 500, Period: "monthly",
		Tiers: []schema.CsCommissionTier{
			{ThresholdBaseCents: 10000, ValueBp: 500},
			{ThresholdBaseCents: 20000, ValueBp: 800},
		},
	}, admin.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activateCsCommissionRule(ctx, db, rule.ID, admin.ID, now); err != nil {
		t.Fatal(err)
	}
	// 第一单基数 10000 → 命中 500bp。
	first := seedCloseableOrder(t, db, "CC-TIER-1", 10000, []segSeed{{playerID: 1, seatIndex: 1, seconds: 600}})
	db.EsportsOrder.UpdateOneID(first.ID).SetResponsibleAdminID(admin.ID).SaveX(ctx)
	if _, err = closeOrder(ctx, db, first.ID, admin.ID, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatal(err)
	}
	firstEntry := db.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.OrderIDEQ(first.ID)).OnlyX(ctx)
	if firstEntry.MatchedTierBp != 500 || firstEntry.AmountCents != 500 {
		t.Fatalf("首单应命中 500bp（累计 10000），got=%+v", firstEntry)
	}
	// 第二单基数 10000 → 周期累计 20000 → 命中 800bp。
	second := seedCloseableOrder(t, db, "CC-TIER-2", 10000, []segSeed{{playerID: 1, seatIndex: 1, seconds: 600}})
	db.EsportsOrder.UpdateOneID(second.ID).SetResponsibleAdminID(admin.ID).SaveX(ctx)
	if _, err = closeOrder(ctx, db, second.ID, admin.ID, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatal(err)
	}
	secondEntry := db.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.OrderIDEQ(second.ID)).OnlyX(ctx)
	if secondEntry.MatchedTierBp != 800 || secondEntry.AmountCents != 800 {
		t.Fatalf("次单应命中 800bp（累计 20000），got=%+v", secondEntry)
	}
}

// 退款冲回：结单后部分退款按比例冲回，全额退款把未冲回的余额一次冲至零。
func TestCsCommissionReversalOnRefund(t *testing.T) {
	db := openCsCommissionDB(t, "esports_cs_commission_reversal")
	ctx := t.Context()
	admin := seedAdmin(t, db, "cc-reversal-admin")
	now := time.Now()

	rule, err := createCsCommissionRule(ctx, db, csCommissionRuleInput{
		RuleNo: "CC-REV", Scope: "global", BaseMode: "order_amount", CalcMode: "percent",
		ValueBp: 1000, Period: "monthly",
	}, admin.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activateCsCommissionRule(ctx, db, rule.ID, admin.ID, now); err != nil {
		t.Fatal(err)
	}
	order := seedCloseableOrder(t, db, "CC-REV-1", 10000, []segSeed{{playerID: 1, seatIndex: 1, seconds: 600}})
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(admin.ID).SaveX(ctx)
	if _, err = closeOrder(ctx, db, order.ID, admin.ID, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatal(err)
	}
	paid := db.EsportsOrder.GetX(ctx, order.ID)

	// 部分退款 4000：冲回 1000 × 4000 / 10000 = 400。
	paid = db.EsportsOrder.UpdateOneID(paid.ID).SetRefundedCents(4000).
		SetRefundStatus("partial").SaveX(ctx)
	item := &ent.EsportsAfterSale{AfterSaleNo: "AS-CC-1", RefundCents: 4000}
	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = reverseOrderCsCommission(ctx, tx, paid, item, now); err != nil {
		_ = tx.Rollback()
		t.Fatalf("部分退款冲回应成功，err=%v", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	partial := db.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.OrderIDEQ(paid.ID),
		esportscscommissionentry.KindEQ(esportscscommissionentry.KindReversal)).OnlyX(ctx)
	if partial.AmountCents != -400 {
		t.Fatalf("部分退款应冲回 -400，got=%d", partial.AmountCents)
	}

	// 全额退款：把剩余 600 一次冲至零。
	paid = db.EsportsOrder.UpdateOneID(paid.ID).SetRefundedCents(10000).
		SetRefundStatus("full").SaveX(ctx)
	full := &ent.EsportsAfterSale{AfterSaleNo: "AS-CC-2", RefundCents: 6000}
	tx, err = db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = reverseOrderCsCommission(ctx, tx, paid, full, now); err != nil {
		_ = tx.Rollback()
		t.Fatalf("全额退款冲回应成功，err=%v", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, entry := range db.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.OrderIDEQ(paid.ID)).AllX(ctx) {
		total += entry.AmountCents
	}
	if total != 0 {
		t.Fatalf("全额退款后提成净额应为 0，got=%d", total)
	}
}

// 提成结算单：周期汇总待结算明细、锁定后登记发放并留外部凭证；不写猫粮钱包。
func TestCsCommissionSettleAndPayout(t *testing.T) {
	db := openCsCommissionDB(t, "esports_cs_commission_settle")
	ctx := t.Context()
	admin := seedAdmin(t, db, "cc-settle-admin")
	other := seedAdmin(t, db, "cc-settle-other")
	now := time.Now()

	rule, err := createCsCommissionRule(ctx, db, csCommissionRuleInput{
		RuleNo: "CC-SETTLE", Scope: "global", BaseMode: "order_amount", CalcMode: "percent",
		ValueBp: 1000, Period: "monthly",
	}, admin.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activateCsCommissionRule(ctx, db, rule.ID, admin.ID, now); err != nil {
		t.Fatal(err)
	}
	order := seedCloseableOrder(t, db, "CC-SETTLE-1", 10000, []segSeed{{playerID: 1, seatIndex: 1, seconds: 600}})
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(admin.ID).SaveX(ctx)
	if _, err = closeOrder(ctx, db, order.ID, admin.ID, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatal(err)
	}

	start := now.UTC().Add(-time.Hour)
	end := now.UTC().Add(time.Hour)
	settlement, err := settleCsCommission(ctx, db, admin.ID, start, end, admin.ID, now)
	if err != nil {
		t.Fatalf("生成客服提成结算单应成功，err=%v", err)
	}
	if settlement.OwnerType != esportssettlement.OwnerTypeCs || settlement.OwnerID != admin.ID {
		t.Fatalf("结算单主体应为客服：%+v", settlement)
	}
	if settlement.NetCents != 1000 || settlement.EntryCount != 1 || settlement.Status != esportssettlement.StatusDraft {
		t.Fatalf("结算单金额不正确：%+v", settlement)
	}
	// 客服提成结算不写猫粮钱包。
	if got := db.EsportsWalletTx.Query().CountX(ctx); got != 0 {
		t.Fatalf("客服提成结算不应写钱包流水，got=%d", got)
	}
	// P0-6：草稿阶段明细只挂单，仍为待结算（不发钱就不算已结算）。
	entry := db.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.OrderIDEQ(order.ID)).OnlyX(ctx)
	if entry.Status != esportscscommissionentry.StatusPendingSettlement || entry.SettlementID == nil ||
		*entry.SettlementID != settlement.ID {
		t.Fatalf("草稿阶段明细应待结算并挂结算单：%+v", entry)
	}
	// 无待结算明细再结算被拒。
	if _, err = settleCsCommission(ctx, db, admin.ID, start, end, admin.ID, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("无待结算明细应被拒，err=%v", err)
	}
	// 未锁定不得发放。
	if _, err = markSettlementPaid(ctx, db, settlement.ID, admin.ID, "PAY-1", now); !errors.Is(err, errConflict) {
		t.Fatalf("未锁定不得发放，err=%v", err)
	}
	locked, err := lockSettlement(ctx, db, settlement.ID, admin.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	// 发放必须留外部凭证。
	if _, err = markSettlementPaid(ctx, db, settlement.ID, admin.ID, "  ", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("空凭证应被拒，err=%v", err)
	}
	paid, err := markSettlementPaid(ctx, db, settlement.ID, other.ID, "WX-PAY-2026-01", now)
	if err != nil {
		t.Fatalf("登记发放应成功，err=%v", err)
	}
	if paid.Status != esportssettlement.StatusPaid || paid.PayoutRef != "WX-PAY-2026-01" ||
		paid.PaidByAdminID == nil || *paid.PaidByAdminID != other.ID {
		t.Fatalf("发放留痕不正确：%+v", paid)
	}
	if _, err = markSettlementPaid(ctx, db, settlement.ID, admin.ID, "PAY-2", now); !errors.Is(err, errConflict) {
		t.Fatalf("重复发放应冲突，err=%v", err)
	}
	// 发放成功后明细才转已结算（P0-6）。
	if got := db.EsportsCsCommissionEntry.GetX(ctx, entry.ID).Status; got != esportscscommissionentry.StatusSettled {
		t.Fatalf("发放后明细应转已结算，got=%s", got)
	}
	_ = locked
}

// 提成概览：待结算净额与本月累计。
func TestCsCommissionSummary(t *testing.T) {
	db := openCsCommissionDB(t, "esports_cs_commission_summary")
	ctx := t.Context()
	admin := seedAdmin(t, db, "cc-summary-admin")
	now := time.Now()

	rule, err := createCsCommissionRule(ctx, db, csCommissionRuleInput{
		RuleNo: "CC-SUM", Scope: "global", BaseMode: "order_amount", CalcMode: "percent",
		ValueBp: 1000, Period: "monthly",
	}, admin.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activateCsCommissionRule(ctx, db, rule.ID, admin.ID, now); err != nil {
		t.Fatal(err)
	}
	order := seedCloseableOrder(t, db, "CC-SUM-1", 10000, []segSeed{{playerID: 1, seatIndex: 1, seconds: 600}})
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(admin.ID).SaveX(ctx)
	if _, err = closeOrder(ctx, db, order.ID, admin.ID, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatal(err)
	}

	view, err := csCommissionSummary(ctx, db, admin.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if view.PendingSettlementCents != 1000 || view.MonthlyCents != 1000 || view.SettledCents != 0 {
		t.Fatalf("概览不正确：%+v", view)
	}
	if !view.MonthStart.Before(view.MonthEnd) || view.NextSettlementAt.IsZero() {
		t.Fatalf("周期字段不正确：%+v", view)
	}
	// 其他客服看不到该提成。
	otherView, err := csCommissionSummary(ctx, db, seedAdmin(t, db, "cc-summary-other").ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if otherView.PendingSettlementCents != 0 || otherView.MonthlyCents != 0 {
		t.Fatalf("他人不应看到该提成：%+v", otherView)
	}
}
