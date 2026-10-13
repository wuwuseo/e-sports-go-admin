package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscommissionentry"
	"go-admin/internal/ent/esportscommissionrule"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsservicesegment"
	"go-admin/internal/ent/esportssettlement"
	"go-admin/internal/ent/esportswallettx"
)

// segSeed 一条已结束服务段的构造参数。
type segSeed struct {
	playerID  int
	seatIndex int
	seconds   int64
}

// seedCloseableOrder 构造一张待结单订单并挂上指定服务段（均带凭证与系统秒数）。
func seedCloseableOrder(t *testing.T, db *ent.Client, orderNo string, payable int64, segs []segSeed) *ent.EsportsOrder {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC()
	order := db.EsportsOrder.Create().
		SetOrderNo(orderNo).SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(payable).SetPayableCents(payable).
		SetPromisedServiceUnits(1).SetRequiredSeats(len(segs)).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).SaveX(ctx)
	for _, seg := range segs {
		db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(seg.playerID).
			SetSeatIndex(seg.seatIndex).SetStatus(esportsservicesegment.StatusFinished).
			SetStartedAt(now.Add(-10 * time.Minute)).SetEndedAt(now).
			SetSystemSeconds(seg.seconds).SetEvidenceRef("ev.png").SaveX(ctx)
	}
	return order
}

// seedActiveRule 新建并直接生效一条分成规则。
func seedActiveRule(t *testing.T, db *ent.Client, in ruleInput) *ent.EsportsCommissionRule {
	t.Helper()
	ctx := t.Context()
	rule, err := createCommissionRule(ctx, db, in, 11, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	active, err := activateCommissionRule(ctx, db, rule.ID, 11, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return active
}

// 规则版本管理：新建即 draft；同 rule_no 再建得 version=2；生效后旧版转 history、新版 active。
func TestCommissionRuleVersionLifecycle(t *testing.T) {
	db := openAfterSaleDB(t, "esports_commission_rule")
	defer db.Close()
	ctx := t.Context()

	v1, err := createCommissionRule(ctx, db, ruleInput{
		RuleNo: "A", Scope: "global", CalcMode: "percent", ValueBp: 1000, UnitBasis: "sku_quantity",
	}, 11, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if v1.Version != 1 || v1.Status != esportscommissionrule.StatusDraft {
		t.Fatalf("首版规则不正确：%+v", v1)
	}
	active, err := activateCommissionRule(ctx, db, v1.ID, 11, time.Now())
	if err != nil || active.Status != esportscommissionrule.StatusActive || active.EffectiveAt == nil {
		t.Fatalf("生效失败：%v %+v", err, active)
	}
	v2, err := createCommissionRule(ctx, db, ruleInput{
		RuleNo: "A", Scope: "global", CalcMode: "percent", ValueBp: 1200, UnitBasis: "sku_quantity",
	}, 11, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if v2.Version != 2 || v2.Status != esportscommissionrule.StatusDraft {
		t.Fatalf("新版规则应 version=2 draft：%+v", v2)
	}
	if _, err := activateCommissionRule(ctx, db, v2.ID, 11, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsCommissionRule.GetX(ctx, v1.ID); got.Status != esportscommissionrule.StatusHistory {
		t.Fatalf("旧版未转 history：%+v", got)
	}
	if got := db.EsportsCommissionRule.GetX(ctx, v2.ID); got.Status != esportscommissionrule.StatusActive {
		t.Fatalf("新版未转 active：%+v", got)
	}
}

// 结单生成分成：percent 1000bp、payable 2000、单成员 → 一条 service 明细 amount=200；重复结单不重复生成。
func TestCloseOrderAccruesCommission(t *testing.T) {
	db := openAfterSaleDB(t, "esports_commission_accrue")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	rule := seedActiveRule(t, db, ruleInput{
		RuleNo: "ACC", Scope: "global", CalcMode: "percent", ValueBp: 1000, UnitBasis: "sku_quantity",
	})
	order := seedCloseableOrder(t, db, "accrue-1", 2000, []segSeed{{1, 1, 600}})
	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatal(err)
	}
	entries := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.OrderIDEQ(order.ID),
		esportscommissionentry.KindEQ(esportscommissionentry.KindService),
	).AllX(ctx)
	if len(entries) != 1 {
		t.Fatalf("应生成 1 条分成明细：%d", len(entries))
	}
	entry := entries[0]
	if entry.AmountCents != 200 || entry.BaseCents != 2000 ||
		entry.Status != esportscommissionentry.StatusPendingSettlement ||
		entry.RuleID == nil || *entry.RuleID != rule.ID || entry.RuleVersion != rule.Version ||
		entry.RuleNo != "ACC" {
		t.Fatalf("分成明细不正确：%+v", entry)
	}
	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "重复结单"}, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复结单未拦截：%v", err)
	}
	if n := db.EsportsCommissionEntry.Query().CountX(ctx); n != 1 {
		t.Fatalf("重复结单改变了明细数：%d", n)
	}
}

// 多成员按服务秒数分摊，尾差归最后一人；分成池超过可分基数时截断到 payable。
func TestCommissionSplitAndCap(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC()

	// 分摊：pool 200，权重 60/180 → 50/150。
	splitDB := openAfterSaleDB(t, "esports_commission_split")
	defer splitDB.Close()
	seedActiveRule(t, splitDB, ruleInput{
		RuleNo: "SPLIT", Scope: "global", CalcMode: "percent", ValueBp: 1000, UnitBasis: "sku_quantity",
	})
	order := seedCloseableOrder(t, splitDB, "split-1", 2000, []segSeed{{1, 1, 60}, {2, 2, 180}})
	if _, err := closeOrder(ctx, splitDB, order.ID, 11, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatal(err)
	}
	entries := splitDB.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.OrderIDEQ(order.ID),
	).Order(ent.Asc(esportscommissionentry.FieldID)).AllX(ctx)
	if len(entries) != 2 {
		t.Fatalf("应有 2 条明细：%d", len(entries))
	}
	var sum int64
	byPlayer := map[int]int64{}
	for _, e := range entries {
		byPlayer[e.PlayerID] = e.AmountCents
		sum += e.AmountCents
	}
	if sum != 200 || byPlayer[1] != 50 || byPlayer[2] != 150 {
		t.Fatalf("分摊不正确 sum=%d byPlayer=%v", sum, byPlayer)
	}

	// 上限：fixed_per_order 5000 > payable 2000 → 截断到 2000，并注明截断。
	capDB := openAfterSaleDB(t, "esports_commission_cap")
	defer capDB.Close()
	seedActiveRule(t, capDB, ruleInput{
		RuleNo: "CAP", Scope: "global", CalcMode: "fixed_per_order", ValueCents: 5000,
		UnitBasis: "sku_quantity",
	})
	capOrder := seedCloseableOrder(t, capDB, "cap-1", 2000, []segSeed{{1, 1, 600}})
	if _, err := closeOrder(ctx, capDB, capOrder.ID, 11, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatal(err)
	}
	capped := capDB.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.OrderIDEQ(capOrder.ID),
	).OnlyX(ctx)
	if capped.AmountCents != 2000 || capped.Note != "已按可分基数上限截断" {
		t.Fatalf("上限截断不正确：%+v", capped)
	}
}

// 退款按比例冲回：结单生成 service 200，退 1000/2000 → reversal -100；重复执行不重复冲回。
func TestRefundReversesCommission(t *testing.T) {
	db := openAfterSaleDB(t, "esports_commission_reversal")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	seedActiveRule(t, db, ruleInput{
		RuleNo: "REV", Scope: "global", CalcMode: "percent", ValueBp: 1000, UnitBasis: "sku_quantity",
	})
	order := seedBalancePaidOrder(t, db, "rev")
	db.EsportsOrder.UpdateOneID(order.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).SaveX(ctx)
	db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(1).SetSeatIndex(1).
		SetStatus(esportsservicesegment.StatusFinished).
		SetStartedAt(now.Add(-10 * time.Minute)).SetEndedAt(now).
		SetSystemSeconds(600).SetEvidenceRef("ev.png").SaveX(ctx)
	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatal(err)
	}
	service := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.KindEQ(esportscommissionentry.KindService),
	).OnlyX(ctx)
	if service.AmountCents != 200 {
		t.Fatalf("service 明细金额不正确：%+v", service)
	}

	item := newRefundAfterSale(t, db, order.ID, "refund_request", 1000, 11, 12, now)
	if _, err := approveAfterSale(ctx, db, item.ID, 12, now); err != nil {
		t.Fatal(err)
	}
	if _, err := executeAfterSale(ctx, db, item.ID, 12, now); err != nil {
		t.Fatal(err)
	}
	reversal := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.KindEQ(esportscommissionentry.KindReversal),
	).OnlyX(ctx)
	if reversal.AmountCents != -100 || reversal.PlayerID != 1 || reversal.AfterSaleID == nil {
		t.Fatalf("冲回明细不正确：%+v", reversal)
	}
	if _, err := executeAfterSale(ctx, db, item.ID, 12, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复执行未拦截：%v", err)
	}
	if n := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.KindEQ(esportscommissionentry.KindReversal),
	).CountX(ctx); n != 1 {
		t.Fatalf("重复执行造成重复冲回：%d", n)
	}
}

// 结算：正负明细汇总为 gross/adjustment/net，写入猫粮流水，明细转 settled；锁定后状态变 locked；
// 重复结算同周期因无待结算明细被拦截。
func TestSettlePlayerCommission(t *testing.T) {
	db := openAfterSaleDB(t, "esports_commission_settle")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	player := seedPlayer(t, db, 201, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	seedActiveRule(t, db, ruleInput{
		RuleNo: "SET", Scope: "global", CalcMode: "percent", ValueBp: 1000, UnitBasis: "sku_quantity",
	})
	order := seedBalancePaidOrder(t, db, "settle")
	db.EsportsOrder.UpdateOneID(order.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).SaveX(ctx)
	db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(player.ID).SetSeatIndex(1).
		SetStatus(esportsservicesegment.StatusFinished).
		SetStartedAt(now.Add(-10 * time.Minute)).SetEndedAt(now).
		SetSystemSeconds(600).SetEvidenceRef("ev.png").SaveX(ctx)
	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatal(err)
	}
	// 追加一笔冲回 -100，覆盖 adjustment 分支。
	item := newRefundAfterSale(t, db, order.ID, "refund_request", 1000, 11, 12, now)
	if _, err := approveAfterSale(ctx, db, item.ID, 12, now); err != nil {
		t.Fatal(err)
	}
	if _, err := executeAfterSale(ctx, db, item.ID, 12, now); err != nil {
		t.Fatal(err)
	}

	settlement, err := settlePlayerCommission(ctx, db, player.ID,
		now.Add(-time.Hour), now.Add(time.Hour), 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if settlement.GrossCents != 200 || settlement.AdjustmentCents != -100 ||
		settlement.NetCents != 100 || settlement.EntryCount != 2 ||
		settlement.Status != esportssettlement.StatusDraft {
		t.Fatalf("结算单汇总不正确：%+v", settlement)
	}
	walletTx := db.EsportsWalletTx.Query().Where(
		esportswallettx.AccountTypeEQ(esportswallettx.AccountTypePlayerCatfood),
		esportswallettx.OwnerIDEQ(player.ID),
		esportswallettx.SourceTypeEQ("settlement"),
	).OnlyX(ctx)
	if walletTx.PrincipalDeltaCents != 100 || walletTx.EntryType != esportswallettx.EntryTypeRecharge ||
		walletTx.SourceID != settlement.SettlementNo {
		t.Fatalf("猫粮结算流水不正确：%+v", walletTx)
	}
	for _, entry := range db.EsportsCommissionEntry.Query().AllX(ctx) {
		if entry.Status != esportscommissionentry.StatusSettled ||
			entry.SettlementID == nil || *entry.SettlementID != settlement.ID {
			t.Fatalf("明细未转结算：%+v", entry)
		}
	}
	locked, err := lockSettlement(ctx, db, settlement.ID, 11, now)
	if err != nil || locked.Status != esportssettlement.StatusLocked || locked.LockedAt == nil {
		t.Fatalf("锁定失败：%v %+v", err, locked)
	}
	if _, err := settlePlayerCommission(ctx, db, player.ID,
		now.Add(-time.Hour), now.Add(time.Hour), 11, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("重复结算未被拦截：%v", err)
	}
}

// 未配置任何规则时结单不生成分成且不报错。
func TestCloseOrderWithoutRule(t *testing.T) {
	db := openAfterSaleDB(t, "esports_commission_norule")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	order := seedCloseableOrder(t, db, "norule-1", 2000, []segSeed{{1, 1, 600}})
	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatal(err)
	}
	if n := db.EsportsCommissionEntry.Query().CountX(ctx); n != 0 {
		t.Fatalf("无规则却生成了明细：%d", n)
	}
}
