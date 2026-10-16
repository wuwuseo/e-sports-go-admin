package esports

import (
	"context"
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscommissionentry"
	"go-admin/internal/ent/esportscscommissionentry"
	"go-admin/internal/ent/esportsgiftdispute"
	"go-admin/internal/ent/esportsgiftorder"
)

// seedGiftWithDisputeScene 建客户（归属客服 11）、开启礼物客服提成、送礼，返回订单与客户。
func seedGiftWithDisputeScene(t *testing.T, db *ent.Client, suffix string) (*ent.EsportsGiftOrder, *ent.EsportsCustomer) {
	t.Helper()
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	customer := seedGiftCustomer(t, db, 101)
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 30000, Note: "礼物争议测试"}, "gd-recharge-"+suffix); err != nil {
		t.Fatal(err)
	}
	rule, err := createCsCommissionRule(ctx, db, csCommissionRuleInput{
		RuleNo: "GD-CS-RULE", Scope: "global", BaseMode: "order_amount", CalcMode: "percent",
		ValueBp: 500, Period: "monthly", IncludeGifts: true,
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activateCsCommissionRule(ctx, db, rule.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	player := seedPlayer(t, db, 202, "qualified", "online")
	gift := seedGift(t, db, giftInput{
		Code: "GIFT-GD", Name: "火箭", PriceCents: 10000,
		CommissionMode: "percent", CommissionBp: 6000,
	})
	order, err := sendGift(ctx, db, 11, giftSendInput{
		CustomerID: customer.ID, GiftID: gift.ID, PlayerID: player.ID,
		Quantity: 1, ClientKey: "gd-key-" + suffix,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	return order, customer
}

func giftReversalTotal(ctx context.Context, db *ent.Client, kind esportscommissionentry.Kind) int64 {
	var total int64
	for _, entry := range db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.KindEQ(kind),
	).AllX(ctx) {
		total += entry.AmountCents
	}
	return total
}

// 礼物争议部分退款闭环（§9.5）：申请 → 审核 → 执行，按比例冲回陪玩分成 / 客服提成 / 成长值；
// 二次退款补齐剩余额度后转全额退款，累计冲回不超过已计提金额。
func TestGiftDisputePartialRefundLifecycle(t *testing.T) {
	db := openGiftDB(t, "esports_gift_dispute_flow")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)

	order, customer := seedGiftWithDisputeScene(t, db, "flow")
	if order.IncomeCents != 6000 || order.RefundCents != 0 {
		t.Fatalf("礼物计提口径异常：%+v", order)
	}

	// 客户自助申请 4000（40%）。
	first, err := createGiftDispute(ctx, db, giftDisputeInput{
		GiftOrderID: order.ID, Kind: "quality", Demand: "礼物与说明不符，申请退 40%",
		RequestedRefundCents: 4000,
	}, "customer", customer.ID, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != esportsgiftdispute.StatusPendingReview ||
		first.OwnerAdminID == nil || *first.OwnerAdminID != 11 {
		t.Fatalf("争议单初始状态 / 归属异常：%+v", first)
	}
	// 客服审核通过并确认 4000。
	approved, err := reviewGiftDispute(ctx, db, first.ID, 11, false, giftDisputeReviewInput{
		Approve: true, ApprovedRefundCents: 4000, Resolution: "与说明不符，按 40% 退款",
	}, now)
	if err != nil || approved.Status != esportsgiftdispute.StatusApproved || approved.ApprovedRefundCents != 4000 {
		t.Fatalf("审核通过失败：%+v err=%v", approved, err)
	}
	// 执行人不得为审核人。
	if _, err = executeGiftDispute(ctx, db, first.ID, 11, now); !errors.Is(err, errForbidden) {
		t.Fatalf("执行人不得为审核人，err=%v", err)
	}
	done, err := executeGiftDispute(ctx, db, first.ID, 12, now)
	if err != nil || done.Status != esportsgiftdispute.StatusCompleted {
		t.Fatalf("执行争议退款失败：%+v err=%v", done, err)
	}
	if _, err = executeGiftDispute(ctx, db, first.ID, 12, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复执行应冲突，err=%v", err)
	}

	got := db.EsportsGiftOrder.GetX(ctx, order.ID)
	if got.RefundCents != 4000 || got.RefundStatus != esportsgiftorder.RefundStatusPartial ||
		got.Status != esportsgiftorder.StatusPaid {
		t.Fatalf("部分退款后礼物单据状态异常：%+v", got)
	}
	if total := giftReversalTotal(ctx, db, esportscommissionentry.KindGiftReversal); total != -2400 {
		t.Fatalf("陪玩分成应按四成比例冲回 -2400，got=%d", total)
	}
	var csTotal int64
	for _, entry := range db.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.KindEQ(esportscscommissionentry.KindGiftReversal),
	).AllX(ctx) {
		csTotal += entry.AmountCents
	}
	if csTotal != -200 {
		t.Fatalf("客服提成应按四成比例冲回 -200，got=%d", csTotal)
	}
	if growth, err := customerGrowthTotal(ctx, db, customer.ID); err != nil || growth != 6000 {
		t.Fatalf("成长值应按退款额反冲，got=%d err=%v", growth, err)
	}
	balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, customer.ID)
	if err != nil || balance.PrincipalCents != 24000 {
		t.Fatalf("余额应退回 4000（充值 30000 - 送礼 10000 + 退款 4000），got=%+v err=%v", balance, err)
	}

	// 二次申请剩余 6000 并执行，转为全额退款。
	second, err := createGiftDispute(ctx, db, giftDisputeInput{
		GiftOrderID: order.ID, Kind: "not_received", Demand: "剩余金额一并申请退款",
		RequestedRefundCents: 6000,
	}, "customer", customer.ID, 0, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// 跨归属审核需超级管理员身份；审核人 13 与执行人 11 分离。
	if _, err = reviewGiftDispute(ctx, db, second.ID, 13, true, giftDisputeReviewInput{
		Approve: true, ApprovedRefundCents: 6000, Resolution: "同意全额退款",
	}, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = executeGiftDispute(ctx, db, second.ID, 11, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got = db.EsportsGiftOrder.GetX(ctx, order.ID)
	if got.RefundCents != 10000 || got.RefundStatus != esportsgiftorder.RefundStatusFull ||
		got.Status != esportsgiftorder.StatusRefunded {
		t.Fatalf("补齐退款后应转全额退款：%+v", got)
	}
	if total := giftReversalTotal(ctx, db, esportscommissionentry.KindGiftReversal); total != -6000 {
		t.Fatalf("陪玩分成累计冲回应封顶在 -6000，got=%d", total)
	}
	if growth, err := customerGrowthTotal(ctx, db, customer.ID); err != nil || growth != 0 {
		t.Fatalf("全额退款后成长值应为 0，got=%d err=%v", growth, err)
	}
	// 已退满后不可再申请。
	if _, err = createGiftDispute(ctx, db, giftDisputeInput{
		GiftOrderID: order.ID, Kind: "other", Demand: "再退一次", RequestedRefundCents: 100,
	}, "customer", customer.ID, 0, now.Add(2*time.Hour)); !errors.Is(err, errConflict) {
		t.Fatalf("已退满礼物不应再受理争议，err=%v", err)
	}
	if _, err = refundGift(ctx, db, 11, false, order.ID, "重复退款", now); !errors.Is(err, errConflict) {
		t.Fatalf("已退满礼物不应再全额退款，err=%v", err)
	}
}

// 礼物争议的权限与额度约束：越权客户、申请人自审、非归属客服审核、超申请额确认、进行中重复受理。
func TestGiftDisputeGuards(t *testing.T) {
	db := openGiftDB(t, "esports_gift_dispute_guards")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)

	order, customer := seedGiftWithDisputeScene(t, db, "guards")
	other := seedGiftCustomer(t, db, 102)

	// 越权客户不可对他人礼物订单发起争议。
	if _, err := createGiftDispute(ctx, db, giftDisputeInput{
		GiftOrderID: order.ID, Kind: "other", Demand: "非本人订单", RequestedRefundCents: 100,
	}, "customer", other.ID, 0, now); !errors.Is(err, errForbidden) {
		t.Fatalf("越权客户发起争议应被拒，err=%v", err)
	}
	// 申请额不得超过可退余额。
	if _, err := createGiftDispute(ctx, db, giftDisputeInput{
		GiftOrderID: order.ID, Kind: "other", Demand: "超额申请", RequestedRefundCents: 20000,
	}, "customer", customer.ID, 0, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("超额申请应被拒，err=%v", err)
	}
	// 客服代建记录申请人，供自审拦截。
	byAdmin, err := createGiftDispute(ctx, db, giftDisputeInput{
		GiftOrderID: order.ID, Kind: "wrong_gift", Demand: "客服代客户建单", RequestedRefundCents: 3000,
	}, "admin", customer.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = createGiftDispute(ctx, db, giftDisputeInput{
		GiftOrderID: order.ID, Kind: "other", Demand: "重复受理", RequestedRefundCents: 100,
	}, "customer", customer.ID, 0, now); !errors.Is(err, errConflict) {
		t.Fatalf("进行中争议不应重复受理，err=%v", err)
	}
	if _, err = reviewGiftDispute(ctx, db, byAdmin.ID, 11, false, giftDisputeReviewInput{
		Approve: true, ApprovedRefundCents: 3000, Resolution: "自审",
	}, now); !errors.Is(err, errForbidden) {
		t.Fatalf("申请人不得自审，err=%v", err)
	}
	if _, err = reviewGiftDispute(ctx, db, byAdmin.ID, 12, false, giftDisputeReviewInput{
		Approve: true, ApprovedRefundCents: 3000, Resolution: "非归属客服审核",
	}, now); !errors.Is(err, errForbidden) {
		t.Fatalf("非归属客服不得审核，err=%v", err)
	}
	if _, err = reviewGiftDispute(ctx, db, byAdmin.ID, 12, true, giftDisputeReviewInput{
		Approve: true, ApprovedRefundCents: 5000, Resolution: "确认额超申请额",
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("确认额超过申请额应被拒，err=%v", err)
	}
	if _, err = reviewGiftDispute(ctx, db, byAdmin.ID, 12, true, giftDisputeReviewInput{
		Approve: false, Resolution: "",
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("驳回必须给原因，err=%v", err)
	}
	// 驳回后争议终结，可重新申请。
	rejected, err := reviewGiftDispute(ctx, db, byAdmin.ID, 12, true, giftDisputeReviewInput{
		Approve: false, Resolution: "证据不足",
	}, now)
	if err != nil || rejected.Status != esportsgiftdispute.StatusRejected {
		t.Fatalf("驳回失败：%+v err=%v", rejected, err)
	}
	again, err := createGiftDispute(ctx, db, giftDisputeInput{
		GiftOrderID: order.ID, Kind: "quality", Demand: "补充证据后重新申请", RequestedRefundCents: 3000,
	}, "customer", customer.ID, 0, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reviewGiftDispute(ctx, db, again.ID, 11, false, giftDisputeReviewInput{
		Approve: true, ApprovedRefundCents: 3000, Resolution: "同意",
	}, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// 执行完成（审核人 11、执行人 12）。
	if _, err = executeGiftDispute(ctx, db, again.ID, 12, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// 客户可撤回自己的进行中争议。
	third, err := createGiftDispute(ctx, db, giftDisputeInput{
		GiftOrderID: order.ID, Kind: "other", Demand: "申请后主动撤回", RequestedRefundCents: 1000,
	}, "customer", customer.ID, 0, now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	withdrawn, err := withdrawGiftDispute(ctx, db, third.ID, other.ID, 0, now.Add(2*time.Hour))
	if !errors.Is(err, errForbidden) {
		t.Fatalf("非本人不可撤回争议，err=%v", err)
	}
	withdrawn, err = withdrawGiftDispute(ctx, db, third.ID, customer.ID, 0, now.Add(2*time.Hour))
	if err != nil || withdrawn.Status != esportsgiftdispute.StatusWithdrawn {
		t.Fatalf("撤回失败：%+v err=%v", withdrawn, err)
	}
	if _, err = withdrawGiftDispute(ctx, db, third.ID, customer.ID, 0, now.Add(3*time.Hour)); !errors.Is(err, errConflict) {
		t.Fatalf("已终结争议不可重复撤回，err=%v", err)
	}
}
