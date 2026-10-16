package esports

import (
	"errors"
	"strconv"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscommissionentry"
	"go-admin/internal/ent/esportscommissionrule"
	"go-admin/internal/ent/esportscscommissionentry"
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

func TestCommissionUsesEachMembersOwnRule(t *testing.T) {
	db := openAfterSaleDB(t, "esports_commission_member_rules")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	p1 := seedPlayer(t, db, 911, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	p2 := seedPlayer(t, db, 912, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	r1 := seedActiveRule(t, db, ruleInput{
		RuleNo: "P1", Scope: "player", ScopeKey: strconv.Itoa(p1.ID),
		CalcMode: "percent", ValueBp: 2000, UnitBasis: "sku_quantity",
	})
	r2 := seedActiveRule(t, db, ruleInput{
		RuleNo: "P2", Scope: "player", ScopeKey: strconv.Itoa(p2.ID),
		CalcMode: "percent", ValueBp: 3000, UnitBasis: "sku_quantity",
	})
	order := seedCloseableOrder(t, db, "member-rules", 2000,
		[]segSeed{{p1.ID, 1, 60}, {p2.ID, 2, 60}})
	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatal(err)
	}
	entries := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.OrderIDEQ(order.ID),
		esportscommissionentry.KindEQ(esportscommissionentry.KindService),
	).AllX(ctx)
	if len(entries) != 2 {
		t.Fatalf("应有两条成员分成：%d", len(entries))
	}
	byPlayer := map[int]*ent.EsportsCommissionEntry{}
	for _, entry := range entries {
		byPlayer[entry.PlayerID] = entry
	}
	if byPlayer[p1.ID].AmountCents != 200 || byPlayer[p2.ID].AmountCents != 300 ||
		byPlayer[p1.ID].BaseCents != 1000 || byPlayer[p2.ID].BaseCents != 1000 ||
		*byPlayer[p1.ID].RuleID != r1.ID || *byPlayer[p2.ID].RuleID != r2.ID {
		t.Fatalf("成员个体规则未分别生效：%+v", entries)
	}
}

func TestCommissionRuleLockedWhenEachPlayerAccepts(t *testing.T) {
	db := openAfterSaleDB(t, "esports_commission_accept_snapshot")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	p1 := seedPlayer(t, db, 913, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	p2 := seedPlayer(t, db, 914, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	v1 := seedActiveRule(t, db, ruleInput{
		RuleNo: "LOCK", Scope: "global", CalcMode: "percent", ValueBp: 1000,
		UnitBasis: "sku_quantity",
	})
	order := seedPaidOrder(t, db, "member-snapshot", 2)
	seat1, err := assignDispatchSeat(ctx, db, order.ID, 1, p1.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	seat2, err := assignDispatchSeat(ctx, db, order.ID, 2, p2.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := respondPlayerInvite(ctx, db, p1.ID, seat1.ID, true, now); err != nil {
		t.Fatal(err)
	}
	if err := respondPlayerInvite(ctx, db, p2.ID, seat2.ID, true, now); err != nil {
		t.Fatal(err)
	}
	for _, seatID := range []int{seat1.ID, seat2.ID} {
		seat := db.EsportsDispatchSeat.GetX(ctx, seatID)
		if seat.CommissionRuleLockedAt == nil || seat.CommissionRuleID == nil || *seat.CommissionRuleID != v1.ID {
			t.Fatalf("接单未锁定分成规则：%+v", seat)
		}
	}
	v2, err := createCommissionRule(ctx, db, ruleInput{
		RuleNo: "LOCK", Scope: "global", CalcMode: "percent", ValueBp: 4000,
		UnitBasis: "sku_quantity",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := activateCommissionRule(ctx, db, v2.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	for _, playerID := range []int{p1.ID, p2.ID} {
		if _, err := startPlayerService(ctx, db, playerID, order.ID, now); err != nil {
			t.Fatal(err)
		}
		if _, err := finishPlayerService(ctx, db, playerID, order.ID, 1, 60, "ev.png", now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	entries := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.OrderIDEQ(order.ID),
		esportscommissionentry.KindEQ(esportscommissionentry.KindService),
	).AllX(ctx)
	if len(entries) != 2 {
		t.Fatalf("应有两条分成明细：%d", len(entries))
	}
	for _, entry := range entries {
		if entry.RuleID == nil || *entry.RuleID != v1.ID || entry.RuleVersion != v1.Version || entry.AmountCents != 50 {
			t.Fatalf("结单错误地使用了改版后的规则：%+v", entry)
		}
	}
}

func TestNoPlayerRuleAtGrabDoesNotGainLaterRule(t *testing.T) {
	db := openAfterSaleDB(t, "esports_commission_grab_no_rule")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	player := seedPlayer(t, db, 915, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedGrabbingOrder(t, db, "grab-no-rule")
	if _, err := grabPlayerOrder(ctx, db, player.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}
	seat := db.EsportsDispatchSeat.Query().OnlyX(ctx)
	if seat.CommissionRuleLockedAt == nil || seat.CommissionRuleID != nil {
		t.Fatalf("无规则接单未锁定空规则：%+v", seat)
	}
	seedActiveRule(t, db, ruleInput{
		RuleNo: "LATER", Scope: "global", CalcMode: "percent", ValueBp: 2000,
		UnitBasis: "sku_quantity",
	})
	if _, err := startPlayerService(ctx, db, player.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := finishPlayerService(ctx, db, player.ID, order.ID, 1, 60, "ev.png", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if count := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.OrderIDEQ(order.ID),
		esportscommissionentry.KindEQ(esportscommissionentry.KindService),
	).CountX(ctx); count != 0 {
		t.Fatalf("无规则接单被后续激活规则追溯计提：%d", count)
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

func TestRefundAfterPreCloseRefundUsesCloseNetBase(t *testing.T) {
	db := openAfterSaleDB(t, "esports_commission_preclose_refund")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	seedActiveRule(t, db, ruleInput{
		RuleNo: "PRE-CLOSE-REFUND", Scope: "global", CalcMode: "percent",
		ValueBp: 1000, UnitBasis: "sku_quantity",
	})
	admin := seedAdmin(t, db, "preclose-refund-cs")
	csRule, err := createCsCommissionRule(ctx, db, csCommissionRuleInput{
		RuleNo: "PRE-CLOSE-REFUND-CS", Scope: "global", BaseMode: "order_amount",
		CalcMode: "percent", ValueBp: 1000, Period: "monthly",
	}, admin.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activateCsCommissionRule(ctx, db, csRule.ID, admin.ID, now); err != nil {
		t.Fatal(err)
	}
	order := seedBalancePaidOrder(t, db, "preclose-refund")
	// 付款时的客服规则快照；独立于本例的退款比例计算，支付路径另有覆盖。
	db.EsportsOrder.UpdateOneID(order.ID).SetCsCommissionAdminID(admin.ID).
		SetCsCommissionRuleID(csRule.ID).SetCsCommissionRuleLockedAt(now).SaveX(ctx)
	refund := func(cents int64) {
		t.Helper()
		item := newRefundAfterSale(t, db, order.ID, "refund_request", cents, 11, 12, now)
		if _, err := approveAfterSale(ctx, db, item.ID, 12, now); err != nil {
			t.Fatal(err)
		}
		if _, err := executeAfterSale(ctx, db, item.ID, 12, now); err != nil {
			t.Fatal(err)
		}
	}
	refund(1000)
	db.EsportsOrder.UpdateOneID(order.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).SaveX(ctx)
	db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(1).SetSeatIndex(1).
		SetStatus(esportsservicesegment.StatusFinished).
		SetStartedAt(now.Add(-10 * time.Minute)).SetEndedAt(now).
		SetSystemSeconds(600).SetEvidenceRef("ev.png").SaveX(ctx)
	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatal(err)
	}
	closed := db.EsportsOrder.GetX(ctx, order.ID)
	if closed.CloseNetServiceCents == nil || *closed.CloseNetServiceCents != 1000 {
		t.Fatalf("结单净服务额快照错误：%+v", closed.CloseNetServiceCents)
	}
	playerService := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.OrderIDEQ(order.ID),
		esportscommissionentry.KindEQ(esportscommissionentry.KindService)).OnlyX(ctx)
	csService := db.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.OrderIDEQ(order.ID),
		esportscscommissionentry.KindEQ(esportscscommissionentry.KindService)).OnlyX(ctx)
	if playerService.AmountCents != 100 || csService.AmountCents != 100 {
		t.Fatalf("结单应仅按未退的 1000 分计提：陪玩=%d 客服=%d", playerService.AmountCents, csService.AmountCents)
	}
	refund(500)
	checkReversal := func(want int64) {
		t.Helper()
		var playerTotal, csTotal int64
		for _, entry := range db.EsportsCommissionEntry.Query().Where(
			esportscommissionentry.OrderIDEQ(order.ID),
			esportscommissionentry.KindEQ(esportscommissionentry.KindReversal)).AllX(ctx) {
			playerTotal += entry.AmountCents
		}
		for _, entry := range db.EsportsCsCommissionEntry.Query().Where(
			esportscscommissionentry.OrderIDEQ(order.ID),
			esportscscommissionentry.KindEQ(esportscscommissionentry.KindReversal)).AllX(ctx) {
			csTotal += entry.AmountCents
		}
		if playerTotal != want || csTotal != want {
			t.Fatalf("冲回合计应为 %d：陪玩=%d 客服=%d", want, playerTotal, csTotal)
		}
	}
	checkReversal(-50)
	refund(500)
	checkReversal(-100)
}

// 结算：草稿只预留明细；锁定时写入猫粮流水并把明细转 settled；
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
	walletQuery := db.EsportsWalletTx.Query().Where(
		esportswallettx.AccountTypeEQ(esportswallettx.AccountTypePlayerCatfood),
		esportswallettx.OwnerIDEQ(player.ID),
		esportswallettx.SourceTypeEQ("settlement"),
	)
	if n := walletQuery.CountX(ctx); n != 0 {
		t.Fatalf("草稿结算单提前入猫粮钱包：%d", n)
	}
	if _, err := applyWithdrawal(ctx, db, player.ID, validApply("settle-draft-withdraw", 100), now); !errors.Is(err, errInsufficientBalance) {
		t.Fatalf("草稿结算金额不可申请提现，got %v", err)
	}
	for _, entry := range db.EsportsCommissionEntry.Query().AllX(ctx) {
		if entry.Status != esportscommissionentry.StatusPendingSettlement ||
			entry.SettlementID == nil || *entry.SettlementID != settlement.ID {
			t.Fatalf("草稿明细预留状态不正确：%+v", entry)
		}
	}
	locked, err := lockSettlement(ctx, db, settlement.ID, 11, now)
	if err != nil || locked.Status != esportssettlement.StatusLocked || locked.LockedAt == nil {
		t.Fatalf("锁定失败：%v %+v", err, locked)
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
	if _, err := applyWithdrawal(ctx, db, player.ID, validApply("settle-locked-withdraw", 100), now); err != nil {
		t.Fatalf("锁定后应可申请提现：%v", err)
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
