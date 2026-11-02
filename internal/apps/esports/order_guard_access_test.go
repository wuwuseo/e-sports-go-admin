package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/apps/esports/schema"
	"go-admin/internal/ent/esportscloseapproval"
	"go-admin/internal/ent/esportsexternalrefund"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportspaymentreversal"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsservicesegment"
)

// 客服代暂停 / 恢复的写边界：只有订单负责客服或超管可代操作，
// 其他客服即使有权限码也不能暂停他人订单；被拒时服务段保持不变。
func TestServicePauseRequiresOrderOwnership(t *testing.T) {
	db := openPlayerTestDB(t, "esports_order_guard_pause_owner")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	const owner, otherAdmin = 11, 12

	player := seedPlayer(t, db, 8801, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedGrabbingOrder(t, db, "guard-owner-pause")
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(owner).ExecX(ctx)
	if _, err := grabPlayerOrder(ctx, db, player.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}
	mustConfirmGrab(t, db, order.ID, now)
	if _, err := startPlayerService(ctx, db, player.ID, order.ID, now); err != nil {
		t.Fatal(err)
	}

	// 客服 A 代暂停客服 B 的订单 → 403。
	if _, err := setServicePauseByAdmin(ctx, db, otherAdmin, order.ID, player.ID, false, true,
		"非负责客服代暂停", now.Add(5*time.Second)); !errors.Is(err, errForbidden) {
		t.Fatalf("跨客服代暂停应拒绝：%v", err)
	}
	if segment := db.EsportsServiceSegment.Query().OnlyX(ctx); segment.Status != esportsservicesegment.StatusInService {
		t.Fatalf("越权被拒后服务段不应变化：%+v", segment)
	}

	// 超管可代任意客服订单操作。
	if _, err := setServicePauseByAdmin(ctx, db, otherAdmin, order.ID, player.ID, true, true,
		"超管代暂停", now.Add(10*time.Second)); err != nil {
		t.Fatalf("超管代暂停失败：%v", err)
	}
}

// 服务时长纠正的写边界：跨客服更正被拒且不改写计时，负责客服与超管可更正。
func TestServiceTimingCorrectionRequiresOrderOwnership(t *testing.T) {
	db := openPlayerTestDB(t, "esports_order_guard_timing_owner")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	const owner, otherAdmin = 21, 22

	order := seedSimpleCloseableOrder(t, db, "guard-owner-timing", 2000, owner)

	// 客服 A 改客服 B 订单时长 → 403。
	if _, err := correctServiceTiming(ctx, db, otherAdmin, order.ID, 1, false, 500, 0,
		"player", "跨客服更正", now.Add(time.Minute)); !errors.Is(err, errForbidden) {
		t.Fatalf("跨客服时长更正应拒绝：%v", err)
	}
	if segment := db.EsportsServiceSegment.Query().OnlyX(ctx); segment.SystemSeconds != 600 || segment.PausedSeconds != 0 {
		t.Fatalf("越权被拒后计时不应变化：%+v", segment)
	}

	// 负责客服可更正。
	if _, err := correctServiceTiming(ctx, db, owner, order.ID, 1, false, 500, 0,
		"player", "负责客服核实后更正", now.Add(time.Minute)); err != nil {
		t.Fatalf("负责客服更正失败：%v", err)
	}
	// 超管可更正他人订单。
	if _, err := correctServiceTiming(ctx, db, otherAdmin, order.ID, 1, true, 480, 30,
		"player", "超管复核更正", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("超管更正失败：%v", err)
	}
}

// 存单冻结（存单）：外部退款执行被拦截，退款单与订单退款额度均不落账。
func TestExternalRefundBlockedWhenOrderFrozen(t *testing.T) {
	db := openAfterSaleDB(t, "esports_order_guard_external_refund_frozen")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

	order := seedBalancePaidOrder(t, db, "GUARD-ER-FROZEN")
	if _, err := depositOrder(ctx, db, order.ID, 99, now); err != nil {
		t.Fatal(err)
	}
	refund := seedExternalRefundRow(t, db, "ER-GUARD-FROZEN", order.ID, 500, 11, nil)

	if _, err := executeExternalRefund(ctx, db, 12, refund.ID, now); !errors.Is(err, errOrderFrozen) {
		t.Fatalf("冻结单外部退款执行未被拦截：%v", err)
	}
	if got := db.EsportsExternalRefund.GetX(ctx, refund.ID); got.Status != esportsexternalrefund.StatusRegistered {
		t.Fatalf("冻结拦截后外部退款状态不应变化：%+v", got)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundedCents != 0 {
		t.Fatalf("冻结拦截后不应累加已退额：%+v", got)
	}
}

// 存单冻结：渠道撤销 / 拒付的登记与处置均被拦截，订单资金状态与撤销单状态不变。
func TestPaymentReversalBlockedWhenOrderFrozen(t *testing.T) {
	db := openAfterSaleDB(t, "esports_order_guard_reversal_frozen")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	receipt, order := seedReversalFixture(t, db, "GUARD-FROZEN-1")
	reversal, err := registerPaymentReversal(ctx, db, 11, paymentReversalInput{
		ReceiptID: receipt.ID, Kind: "chargeback", AmountCents: receipt.ActualCents,
		Reason: "银行通知拒付，冻结单处置测试",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = depositOrder(ctx, db, order.ID, 99, now); err != nil {
		t.Fatal(err)
	}
	if _, err = decidePaymentReversal(ctx, db, reversal.ID, 12, paymentReversalDecisionInput{
		Action: reversalActionMarkException, Resolution: "冻结期间不应落账",
	}, now); !errors.Is(err, errOrderFrozen) {
		t.Fatalf("冻结单撤销处置未被拦截：%v", err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.PaymentStatus != esportsorder.PaymentStatusPaid {
		t.Fatalf("冻结拦截后订单资金状态不应变化：%+v", got)
	}
	if got := db.EsportsPaymentReversal.GetX(ctx, reversal.ID); got.Status != esportspaymentreversal.StatusPending {
		t.Fatalf("冻结拦截后撤销单应保持待处置：%+v", got)
	}

	// 已冻结订单不再接受新的撤销 / 拒付登记。
	secondReceipt, secondOrder := seedReversalFixture(t, db, "GUARD-FROZEN-2")
	if _, err = depositOrder(ctx, db, secondOrder.ID, 99, now); err != nil {
		t.Fatal(err)
	}
	if _, err = registerPaymentReversal(ctx, db, 11, paymentReversalInput{
		ReceiptID: secondReceipt.ID, AmountCents: secondReceipt.ActualCents,
		Reason: "冻结单登记撤销",
	}, now); !errors.Is(err, errOrderFrozen) {
		t.Fatalf("冻结单撤销登记未被拦截：%v", err)
	}
	if count := db.EsportsPaymentReversal.Query().CountX(ctx); count != 1 {
		t.Fatalf("冻结拦截后不应新增撤销登记：%d", count)
	}
}

// 存单冻结：结单审批超时自动通过被拦截，订单仍待复核、节点仍待审批。
func TestCloseApprovalTimeoutBlockedWhenOrderFrozen(t *testing.T) {
	db := openCloseFormDB(t, "esports_order_guard_close_timeout_frozen")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	seedActiveApprovalFlow(t, db, closeApprovalFlowInput{Code: "AF-GUARD-FROZEN", Name: "冻结单超时",
		Scope: "global", MinCents: 1000, Nodes: []schema.CloseApprovalNode{
			{Seq: 1, Name: "客服主管复核", Role: "admin", TimeoutHours: 1, AutoPassOnTimeout: true},
		}})
	order := seedSimpleCloseableOrder(t, db, "guard-frozen-close", 2000, 11)
	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := depositOrder(ctx, db, order.ID, 99, now); err != nil {
		t.Fatal(err)
	}

	if _, err := checkCloseApprovalTimeouts(ctx, db, now.Add(2*time.Hour), 200); !errors.Is(err, errOrderFrozen) {
		t.Fatalf("冻结单超时自动通过未被拦截：%v", err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.FulfillmentStatus != esportsorder.FulfillmentStatusPendingCloseReview {
		t.Fatalf("冻结拦截后订单不应结单：%+v", got)
	}
	approval := db.EsportsCloseApproval.Query().OnlyX(ctx)
	if approval.Status != esportscloseapproval.StatusPending || approval.Steps[0].AutoPassed {
		t.Fatalf("冻结拦截后审批节点不应自动通过：%+v", approval)
	}
}

// 存单冻结：手动指定订单分成规则被拦截。
func TestManualCommissionRuleBlockedWhenOrderFrozen(t *testing.T) {
	db := openAfterSaleDB(t, "esports_order_guard_commission_frozen")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	rule := seedActiveRule(t, db, ruleInput{
		RuleNo: "GUARD-FROZEN", Scope: "global", CalcMode: "percent", ValueBp: 1000,
		UnitBasis: "sku_quantity",
	})
	order := seedGrabbingOrder(t, db, "guard-frozen-commission")
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(11).ExecX(ctx)
	if _, err := depositOrder(ctx, db, order.ID, 99, now); err != nil {
		t.Fatal(err)
	}

	if _, err := setOrderManualCommissionRule(ctx, db, order.ID, rule.ID, 11, now); !errors.Is(err, errOrderFrozen) {
		t.Fatalf("冻结单手动分成规则未被拦截：%v", err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.ManualCommissionRuleID != nil {
		t.Fatalf("冻结拦截后不应写入手动分成规则：%+v", got)
	}
}
