package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsexternalrefund"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportswithdrawal"
	"go-admin/internal/ent/esportswithdrawalpayout"
)

// 审核通过 → 打款执行 → 成功：提现单在打款执行阶段保持 approved，成功后转 paid 并留到账留痕。
func TestWithdrawalPayoutStateMachine(t *testing.T) {
	db := openWithdrawalDB(t, "esports_withdrawal_payout_sm")
	ctx := t.Context()
	player := seedCatfoodPlayer(t, db, 9101, 10000)
	now := time.Now()

	item, err := applyWithdrawal(ctx, db, player.ID, validApply("wd-payout-0001", 4000), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reviewWithdrawal(ctx, db, 7, item.ID, withdrawalReviewInput{Approve: true}, now); err != nil {
		t.Fatal(err)
	}
	// 审核人不得自己提交打款。
	if _, err = requestWithdrawalPayout(ctx, db, 7, item.ID, requestWithdrawalPayoutInput{
		Channel: "bank_card", TransactionNo: "BANK-SELF", AmountCents: 4000,
	}, now); !errors.Is(err, errForbidden) {
		t.Fatalf("审核人提交打款应被拒，err=%v", err)
	}

	// 提交打款执行：落 pay_requested，提现单仍 approved。
	req, err := requestWithdrawalPayout(ctx, db, 8, item.ID, requestWithdrawalPayoutInput{
		Channel: "bank_card", TransactionNo: "BANK-PAY-1", AmountCents: 4000,
	}, now)
	if err != nil {
		t.Fatalf("提交打款执行应成功，err=%v", err)
	}
	if req.Status != esportswithdrawalpayout.StatusPayRequested ||
		req.Channel != esportswithdrawalpayout.ChannelBankCard ||
		req.TransactionNo != "BANK-PAY-1" || req.AmountCents != 4000 ||
		req.SubmittedByAdminID == nil || *req.SubmittedByAdminID != 8 {
		t.Fatalf("打款执行单不正确：%+v", req)
	}
	if got := db.EsportsWithdrawal.GetX(ctx, item.ID); got.Status != esportswithdrawal.StatusApproved {
		t.Fatalf("打款执行阶段提现单应保持 approved，got=%s", got.Status)
	}

	// 提交人不得自己确认到账。
	if _, err = markWithdrawalPayoutPaid(ctx, db, 8, req.ID, "TAX-SELF", now); !errors.Is(err, errForbidden) {
		t.Fatalf("打款提交人自确认应被拒，err=%v", err)
	}
	// 标记打款成功：提现单转 paid，留到账时间与打款人。
	paid, err := markWithdrawalPayoutPaid(ctx, db, 9, req.ID, "TAX-1", now)
	if err != nil {
		t.Fatalf("标记打款成功应成功，err=%v", err)
	}
	if paid.Status != esportswithdrawalpayout.StatusPaid || paid.TaxRef != "TAX-1" ||
		paid.PaidByAdminID == nil || *paid.PaidByAdminID != 9 || paid.PaidAt == nil {
		t.Fatalf("打款成功留痕不正确：%+v", paid)
	}
	wd := db.EsportsWithdrawal.GetX(ctx, item.ID)
	if wd.Status != esportswithdrawal.StatusPaid || wd.PayoutRef != "BANK-PAY-1" ||
		wd.TaxRef != "TAX-1" || wd.PaidByAdminID == nil || *wd.PaidByAdminID != 9 || wd.PaidAt == nil {
		t.Fatalf("提现单打款留痕不正确：%+v", wd)
	}
	// 重复标记成功冲突。
	if _, err = markWithdrawalPayoutPaid(ctx, db, 9, req.ID, "TAX-1", now); !errors.Is(err, errConflict) {
		t.Fatalf("重复标记打款成功应冲突，err=%v", err)
	}
}

// 打款失败退回可重试：失败后提现单保持 approved，可用新流水号重新提交并成功。
func TestWithdrawalPayoutFailAndRetry(t *testing.T) {
	db := openWithdrawalDB(t, "esports_withdrawal_payout_retry")
	ctx := t.Context()
	player := seedCatfoodPlayer(t, db, 9102, 10000)
	now := time.Now()

	item, err := applyWithdrawal(ctx, db, player.ID, validApply("wd-retry-0001", 4000), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reviewWithdrawal(ctx, db, 7, item.ID, withdrawalReviewInput{Approve: true}, now); err != nil {
		t.Fatal(err)
	}
	req, err := requestWithdrawalPayout(ctx, db, 8, item.ID, requestWithdrawalPayoutInput{
		Channel: "wechat", TransactionNo: "WX-PAY-1", AmountCents: 4000,
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	// 失败须填原因。
	if _, err = markWithdrawalPayoutFailed(ctx, db, 8, req.ID, failWithdrawalPayoutInput{}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("失败未填原因应被拒，err=%v", err)
	}
	failed, err := markWithdrawalPayoutFailed(ctx, db, 8, req.ID, failWithdrawalPayoutInput{Reason: "收款账号异常"})
	if err != nil {
		t.Fatalf("标记打款失败应成功，err=%v", err)
	}
	if failed.Status != esportswithdrawalpayout.StatusPayFailed || failed.FailReason != "收款账号异常" {
		t.Fatalf("打款失败留痕不正确：%+v", failed)
	}
	if got := db.EsportsWithdrawal.GetX(ctx, item.ID); got.Status != esportswithdrawal.StatusApproved {
		t.Fatalf("失败后提现单应保持 approved，got=%s", got.Status)
	}

	// 重试：新流水号重新提交，成功后提现单转 paid。
	retry, err := requestWithdrawalPayout(ctx, db, 8, item.ID, requestWithdrawalPayoutInput{
		Channel: "wechat", TransactionNo: "WX-PAY-2", AmountCents: 4000,
	}, now)
	if err != nil {
		t.Fatalf("失败后重试应成功，err=%v", err)
	}
	if _, err = markWithdrawalPayoutPaid(ctx, db, 9, retry.ID, "", now); err != nil {
		t.Fatalf("重试后打款成功应成功，err=%v", err)
	}
	if got := db.EsportsWithdrawal.GetX(ctx, item.ID); got.Status != esportswithdrawal.StatusPaid || got.PayoutRef != "WX-PAY-2" {
		t.Fatalf("重试成功后提现单留痕不正确：%+v", got)
	}
}

// 同一打款流水号不重复落账。
func TestWithdrawalPayoutIdempotency(t *testing.T) {
	db := openWithdrawalDB(t, "esports_withdrawal_payout_idem")
	ctx := t.Context()
	player := seedCatfoodPlayer(t, db, 9103, 10000)
	now := time.Now()

	item, err := applyWithdrawal(ctx, db, player.ID, validApply("wd-idem-0001", 4000), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reviewWithdrawal(ctx, db, 7, item.ID, withdrawalReviewInput{Approve: true}, now); err != nil {
		t.Fatal(err)
	}
	if _, err = requestWithdrawalPayout(ctx, db, 8, item.ID, requestWithdrawalPayoutInput{
		Channel: "bank_card", TransactionNo: "BANK-IDEM-1", AmountCents: 4000,
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err = requestWithdrawalPayout(ctx, db, 8, item.ID, requestWithdrawalPayoutInput{
		Channel: "bank_card", TransactionNo: "BANK-IDEM-1", AmountCents: 4000,
	}, now); !errors.Is(err, errConflict) {
		t.Fatalf("同一流水号重复提交应冲突，err=%v", err)
	}
}

// 外部退款：登记 → 执行对齐订单退款状态，流水号幂等与超退拦截。
func TestExternalRefundRegisterExecuteAndIdempotency(t *testing.T) {
	db := openWithdrawalDB(t, "esports_external_refund")
	ctx := t.Context()
	now := time.Now()
	order := seedPaidOrder(t, db, "ext-refund-1", 1)

	reg, err := registerExternalRefund(ctx, db, 8, registerExternalRefundInput{
		Channel: "wechat_work", TransactionNo: "WX-REFUND-1", AmountCents: 300, OrderID: order.ID,
	}, now)
	if err != nil {
		t.Fatalf("登记外部退款应成功，err=%v", err)
	}
	if reg.Status != esportsexternalrefund.StatusRegistered || reg.Channel != "wechat_work" ||
		reg.TransactionNo != "WX-REFUND-1" || reg.AmountCents != 300 || reg.OrderID != order.ID {
		t.Fatalf("外部退款登记不正确：%+v", reg)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundedCents != 0 || got.RefundStatus != esportsorder.RefundStatusNone {
		t.Fatalf("登记阶段不应改订单退款状态：%+v", got)
	}
	// 同一流水号重复登记冲突。
	if _, err = registerExternalRefund(ctx, db, 8, registerExternalRefundInput{
		Channel: "wechat_work", TransactionNo: "WX-REFUND-1", AmountCents: 300, OrderID: order.ID,
	}, now); !errors.Is(err, errConflict) {
		t.Fatalf("同一退款流水号重复登记应冲突，err=%v", err)
	}

	// 执行：订单累计已退额与退款状态对齐。
	exec, err := executeExternalRefund(ctx, db, 9, reg.ID, now)
	if err != nil {
		t.Fatalf("执行外部退款应成功，err=%v", err)
	}
	if exec.Status != esportsexternalrefund.StatusExecuted || exec.ExecutedByAdminID == nil ||
		*exec.ExecutedByAdminID != 9 || exec.ExecutedAt == nil {
		t.Fatalf("外部退款执行留痕不正确：%+v", exec)
	}
	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.RefundedCents != 300 || got.RefundStatus != esportsorder.RefundStatusPartial {
		t.Fatalf("执行后订单退款状态未对齐：%+v", got)
	}
	// 重复执行冲突。
	if _, err = executeExternalRefund(ctx, db, 9, reg.ID, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复执行应冲突，err=%v", err)
	}

	// 超退拦截：再登记 800 分退款，已退 300 分 + 本次 800 分已超过应付额 1000 分，
	// 登记阶段即按累计可退额度拦截，避免待执行登记虚占/透支订单额度。
	if _, err = registerExternalRefund(ctx, db, 8, registerExternalRefundInput{
		Channel: "wechat_work", TransactionNo: "WX-REFUND-2", AmountCents: 800, OrderID: order.ID,
	}, now); !errors.Is(err, errConflict) {
		t.Fatalf("登记超退退款应冲突，err=%v", err)
	}
	refunds, err := listExternalRefunds(ctx, db, order.ID, "registered", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(refunds) != 0 {
		t.Fatalf("超退登记不应落库：%+v", refunds)
	}
	// 兜底：即使绕过登记校验直接落一条 registered 记录，执行阶段仍须拦截超退。
	forced, err := db.EsportsExternalRefund.Create().SetRefundNo(externalRefundNo()).
		SetChannel("wechat_work").SetTransactionNo("WX-REFUND-3").SetAmountCents(800).
		SetOrderID(order.ID).SetStatus(esportsexternalrefund.StatusRegistered).
		SetReason("超退兜底").SetRegisteredByAdminID(8).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = executeExternalRefund(ctx, db, 9, forced.ID, now); !errors.Is(err, errConflict) {
		t.Fatalf("超退执行应冲突，err=%v", err)
	}
}
