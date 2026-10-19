package esports

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsaftersaleaction"
	"go-admin/internal/ent/esportsexternalrefund"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportswallettx"
)

// P0-10 回归：外部渠道（商户码 / 微信 / 对公转账）付款的订单「退原核销款」不再返回未接入，
// 而是生成一笔待执行的外部退款单（registered），由财务核验后走既有 executeExternalRefund 执行；
// 真正落账（累加已退额、核销预占）发生在财务执行外部退款时，而不是售后执行时直接改钱包。
func TestAfterSaleRefundOriginalExternalChannelCreatesPendingExternalRefund(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_external_channel")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	// 外部渠道已核销订单：没有余额 / 储值卡核销流水可原路退回。
	order := seedPaidOrder(t, db, "as-external-channel", 1)
	db.EsportsOrder.UpdateOneID(order.ID).
		SetPaymentChannel(esportsorder.PaymentChannelMerchantQr).ExecX(ctx)

	item := newRefundAfterSale(t, db, order.ID, "refund_request", 1000, 11, 12, now)
	if _, err := approveAfterSale(ctx, db, item.ID, 12, now); err != nil {
		t.Fatalf("外部渠道订单审批失败：%v", err)
	}
	done, err := executeAfterSale(ctx, db, item.ID, 12, now)
	if err != nil {
		t.Fatalf("退原核销款对接收外部渠道退款失败：%v", err)
	}
	if done.Status != esportsaftersale.StatusCompleted {
		t.Fatalf("生成外部退款单后应按已登记结案：%+v", done)
	}
	if !strings.Contains(done.Resolution, "已生成外部渠道退款待执行") {
		t.Fatalf("应返回明确中文提示「已生成外部渠道退款待执行」：%q", done.Resolution)
	}

	refund := db.EsportsExternalRefund.Query().Where(
		esportsexternalrefund.OrderIDEQ(order.ID),
	).OnlyX(ctx)
	if refund.Status != esportsexternalrefund.StatusRegistered ||
		refund.AmountCents != 1000 || refund.Channel != "merchant_qr" ||
		refund.AftersaleID == nil || *refund.AftersaleID != item.ID {
		t.Fatalf("待执行外部退款单不正确：%+v", refund)
	}
	// 钱还没出账：不写钱包、不累加已退额，售后预占额度保留到财务执行时核销。
	if n := db.EsportsWalletTx.Query().Where(
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeRefundReturn),
	).CountX(ctx); n != 0 {
		t.Fatalf("外部退款登记阶段不得直接改钱包，got=%d 条退款流水", n)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundedCents != 0 || got.RefundReservedCents != 1000 {
		t.Fatalf("登记阶段不应累加已退额 / 释放预占：%+v", got)
	}

	// 支付状态异常（渠道撤销 / 拒付）期间禁止执行外部退款（P1）。
	db.EsportsOrder.UpdateOneID(order.ID).
		SetPaymentStatus(esportsorder.PaymentStatusException).ExecX(ctx)
	if _, err = executeExternalRefund(ctx, db, 13, refund.ID, now); !errors.Is(err, errConflict) {
		t.Fatalf("支付状态异常时执行外部退款应被拒，err=%v", err)
	}
	if live := db.EsportsExternalRefund.GetX(ctx, refund.ID); live.Status != esportsexternalrefund.StatusRegistered {
		t.Fatalf("被拒的外部退款不应改变状态：%+v", live)
	}

	// 支付异常处置完成后，财务核验执行：核销预占并累加已退额。
	db.EsportsOrder.UpdateOneID(order.ID).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).ExecX(ctx)
	if _, err = executeExternalRefund(ctx, db, 13, refund.ID, now); err != nil {
		t.Fatalf("财务执行外部退款失败：%v", err)
	}
	final := db.EsportsOrder.GetX(ctx, order.ID)
	if final.RefundedCents != 1000 || final.RefundReservedCents != 0 ||
		final.RefundStatus != esportsorder.RefundStatusFull {
		t.Fatalf("外部退款执行后订单退款额度不正确：%+v", final)
	}
}

// P0-10（多动作）回归：多动作方案里的退款动作项遇到外部渠道时，登记待执行的外部退款单，
// 而非返回未接入导致 execution_failed；动作项落「已登记待执行」说明，钱未出账不改订单已退额；
// 人工重试（动作项回退后再次执行）复用同一幂等键，不产生第二张退款单。
func TestAfterSaleActionRefundExternalChannelRegistersPendingExternalRefund(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_actions_external")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	// 外部渠道已核销订单（无余额 / 储值卡核销流水）。
	order := seedPaidOrder(t, db, "as-actions-external", 1)
	db.EsportsOrder.UpdateOneID(order.ID).
		SetPaymentChannel(esportsorder.PaymentChannelMerchantQr).ExecX(ctx)
	player := seedPlayer(t, db, 7601, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	planned := newMultiActionAfterSale(t, db, order.ID, "refund_request", 11, []afterSaleActionInput{
		{ActionType: "refund", AmountCents: 500, SourceRef: "order:" + order.OrderNo},
		{ActionType: "makeup", PlayerID: player.ID, SeatIndex: 1, Units: 1, CompanyPayCents: 200},
	}, now)
	if _, err := approveAfterSale(ctx, db, planned.ID, 12, now); err != nil {
		t.Fatalf("多动作方案审批失败：%v", err)
	}
	done, err := executeAfterSale(ctx, db, planned.ID, 12, now)
	if err != nil {
		t.Fatalf("多动作外部渠道退款执行失败：%v", err)
	}
	if done.Status != esportsaftersale.StatusCompleted {
		t.Fatalf("外部渠道退款动作项已登记，售后不应落 execution_failed：%+v", done)
	}

	refund := db.EsportsExternalRefund.Query().Where(
		esportsexternalrefund.OrderIDEQ(order.ID),
	).OnlyX(ctx)
	if refund.Status != esportsexternalrefund.StatusRegistered || refund.AmountCents != 500 ||
		refund.Channel != "merchant_qr" || refund.AftersaleID == nil || *refund.AftersaleID != planned.ID {
		t.Fatalf("待执行外部退款单不正确：%+v", refund)
	}
	if want := "AS-EXT-" + planned.AfterSaleNo + ":a1"; refund.TransactionNo != want {
		t.Fatalf("外部退款幂等键应按动作项拆分：got %s want %s", refund.TransactionNo, want)
	}
	refundAction := db.EsportsAfterSaleAction.Query().Where(
		esportsaftersaleaction.AfterSaleIDEQ(planned.ID),
		esportsaftersaleaction.SeqEQ(1),
	).OnlyX(ctx)
	if refundAction.Status != esportsaftersaleaction.StatusSucceeded ||
		!strings.Contains(refundAction.ResultSummary, "已生成外部渠道退款待执行") {
		t.Fatalf("退款动作项应落「已登记待执行」说明：%+v", refundAction)
	}
	// 钱还没出账：不写钱包、不累加已退额，预占额度留到财务执行外部退款时核销。
	if n := db.EsportsWalletTx.Query().Where(
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeRefundReturn),
	).CountX(ctx); n != 0 {
		t.Fatalf("外部退款登记阶段不得直接改钱包，got=%d 条退款流水", n)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundedCents != 0 || got.RefundReservedCents != 500 {
		t.Fatalf("登记阶段不应累加已退额 / 释放预占：%+v", got)
	}

	// 幂等：模拟人工重试（动作项回退为 failed、售后回到待执行）再次执行，不产生第二张退款单。
	db.EsportsAfterSaleAction.UpdateOneID(refundAction.ID).
		SetStatus(esportsaftersaleaction.StatusFailed).ExecX(ctx)
	db.EsportsAfterSale.UpdateOneID(planned.ID).
		SetStatus(esportsaftersale.StatusPendingExecution).ExecX(ctx)
	retried, err := executeAfterSale(ctx, db, planned.ID, 13, now)
	if err != nil || retried.Status != esportsaftersale.StatusCompleted {
		t.Fatalf("重试执行失败：%v %+v", err, retried)
	}
	if n := db.EsportsExternalRefund.Query().Where(
		esportsexternalrefund.OrderIDEQ(order.ID),
	).CountX(ctx); n != 1 {
		t.Fatalf("重复执行产生了第二张外部退款单，got=%d", n)
	}
}

// P1 回归：订单支付状态异常（渠道撤销 / 拒付 → payment_status=exception）时，
// 售后审批与退款执行两个入口都必须拦截并留待人工处置，与已付款取消路径同口径。
func TestAfterSaleRefundBlockedWhenPaymentStatusException(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_payment_exception")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	order := seedBalancePaidOrder(t, db, "payment-exception")
	item := newRefundAfterSale(t, db, order.ID, "refund_request", 500, 11, 12, now)

	// 审批入口：支付状态异常时拒绝审批（不预占额度）。
	db.EsportsOrder.UpdateOneID(order.ID).
		SetPaymentStatus(esportsorder.PaymentStatusException).ExecX(ctx)
	_, err := approveAfterSale(ctx, db, item.ID, 12, now)
	if !errors.Is(err, errConflict) || !strings.Contains(err.Error(), "支付状态异常") {
		t.Fatalf("支付状态异常时审批退款未拦截：%v", err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundReservedCents != 0 {
		t.Fatalf("被拒的审批不得预占额度：%+v", got)
	}

	// 执行入口：审批通过后被渠道撤销，执行同样必须拦截，且不改订单额度 / 不写钱包。
	db.EsportsOrder.UpdateOneID(order.ID).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).ExecX(ctx)
	if _, err = approveAfterSale(ctx, db, item.ID, 12, now); err != nil {
		t.Fatalf("支付状态恢复后审批失败：%v", err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundReservedCents != 500 {
		t.Fatalf("审批未预占额度：%+v", got)
	}
	db.EsportsOrder.UpdateOneID(order.ID).
		SetPaymentStatus(esportsorder.PaymentStatusException).ExecX(ctx)
	if _, err = executeAfterSale(ctx, db, item.ID, 12, now); !errors.Is(err, errConflict) ||
		!strings.Contains(err.Error(), "支付状态异常") {
		t.Fatalf("支付状态异常时执行退款未拦截：%v", err)
	}
	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.RefundedCents != 0 || got.RefundReservedCents != 500 {
		t.Fatalf("被拒的执行不得改订单退款额度：%+v", got)
	}
	if n := db.EsportsWalletTx.Query().Where(
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeRefundReturn),
	).CountX(ctx); n != 0 {
		t.Fatalf("被拒的执行不得写退款流水，got=%d", n)
	}
	if live := db.EsportsAfterSale.GetX(ctx, item.ID); live.Status != esportsaftersale.StatusPendingExecution {
		t.Fatalf("被拒的执行不应改变售后状态：%+v", live)
	}
}
