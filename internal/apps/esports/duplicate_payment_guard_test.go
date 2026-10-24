package esports

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsofflinepaymentproof"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/migrate"
)

// 本文件覆盖 P0-11「重复收款」：同一订单已有待核验的转账凭证时，
// 客户余额核销必须被拒；而核销该凭证本身不得被自己拦住。

func openDuplicateGuardDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	db := enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	t.Cleanup(func() { db.Close() })
	return db
}

// 已有待核验转账凭证时，余额核销被拒（且不产生扣款流水、订单仍待付款）；
// 随后财务核销该凭证本身正常通过——证明防护没有自我阻断。
func TestBalancePaymentBlockedByPendingOfflineProof(t *testing.T) {
	db := openDuplicateGuardDB(t, "esports_dup_guard_balance")
	ctx := t.Context()
	now := time.Now().UTC()

	account := seedActivePaymentAccount(t, db, "wechat_work", "WX-DUP-BAL-0001")
	intent := seedPayableOrderForOffline(t, db, "dupbal", "wechat_work")
	order := db.EsportsOrder.GetX(ctx, intent.OrderID)
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, order.CustomerID,
		walletInput{PrincipalCents: order.PayableCents + 1000, Note: "重复收款防护测试"},
		"dup-balance-recharge"); err != nil {
		t.Fatal(err)
	}
	proof, err := submitOfflinePaymentProof(ctx, db, offlinePaymentProofInput{
		OrderNo: order.OrderNo, AccountID: account.ID, TransactionNo: "WX-DUP-BAL-TXN-1",
		AmountCents: order.PayableCents, ProofRef: "proof/dup-balance-0001.png",
	}, 11, now)
	if err != nil {
		t.Fatalf("提交待核验凭证应成功：%v", err)
	}

	// 余额核销：必须判冲突并给出中文提示。
	if _, err = payOrderWithBalance(ctx, db, 11, order.ID, now); !errors.Is(err, errConflict) ||
		!strings.Contains(err.Error(), "待核验的转账凭证") {
		t.Fatalf("已有待核验凭证时余额核销应被拒，err=%v", err)
	}
	// 被拒后余额与订单状态都不得变化。
	balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, order.CustomerID)
	if err != nil {
		t.Fatal(err)
	}
	if balance.total() != order.PayableCents+1000 {
		t.Fatalf("被拒的余额核销不应扣款：%+v", balance)
	}
	reload := db.EsportsOrder.GetX(ctx, order.ID)
	if reload.PaymentStatus != esportsorder.PaymentStatusUnpaid ||
		reload.FulfillmentStatus != esportsorder.FulfillmentStatusPendingPayment {
		t.Fatalf("被拒的余额核销不应改单：%+v", reload)
	}

	// 核销该凭证本身不被自己拦住。
	verified, err := verifyOfflinePaymentProof(ctx, db, proof.ID, 12, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("核销自身待核验凭证应放行：%v", err)
	}
	if verified.Status != esportsofflinepaymentproof.StatusVerified {
		t.Fatalf("凭证核销后应为 verified：%+v", verified)
	}
	paid := db.EsportsOrder.GetX(ctx, order.ID)
	if paid.PaymentStatus != esportsorder.PaymentStatusPaid ||
		paid.PaymentChannel != esportsorder.PaymentChannelWechatWork {
		t.Fatalf("核销后订单应转已付款：%+v", paid)
	}
	// 已付款订单再走余额核销仍判冲突（既有口径不变）。
	if _, err = payOrderWithBalance(ctx, db, 11, order.ID, now.Add(2*time.Minute)); !errors.Is(err, errConflict) {
		t.Fatalf("已付款订单再余额核销应冲突，err=%v", err)
	}
}

// 反向：已被余额核销的订单，再提交转账凭证应被拒（订单已非待付款）。
func TestOfflineProofRejectedAfterBalancePaid(t *testing.T) {
	db := openDuplicateGuardDB(t, "esports_dup_guard_paid")
	ctx := t.Context()
	now := time.Now().UTC()

	account := seedActivePaymentAccount(t, db, "corporate_transfer", "6222-0000-0000-9911")
	intent := seedPayableOrderForOffline(t, db, "duppaid", "corporate_transfer")
	order := db.EsportsOrder.GetX(ctx, intent.OrderID)
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, order.CustomerID,
		walletInput{PrincipalCents: order.PayableCents}, "dup-paid-recharge"); err != nil {
		t.Fatal(err)
	}
	if _, err := payOrderWithBalance(ctx, db, 11, order.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := submitOfflinePaymentProof(ctx, db, offlinePaymentProofInput{
		OrderNo: order.OrderNo, AccountID: account.ID, TransactionNo: "CORP-DUP-PAID-TXN-1",
		AmountCents: order.PayableCents, ProofRef: "proof/dup-paid-0001.png",
	}, 11, now); !errors.Is(err, errConflict) {
		t.Fatalf("已用余额收款的订单再提交转账凭证应被拒，err=%v", err)
	}
	if count := db.EsportsOfflinePaymentProof.Query().CountX(ctx); count != 0 {
		t.Fatalf("被拒的凭证不应落库：%d", count)
	}
}

// 客户自助提交付款凭证：同一订单已有待核验凭证时，换一个流水号再提交同样被拒（P0-11 提交侧补缺）。
func TestCustomerProofRejectedWhenOrderHasPendingProof(t *testing.T) {
	db := openDuplicateGuardDB(t, "esports_dup_guard_customer_submit")
	ctx := t.Context()
	now := time.Now().UTC()

	account := seedActivePaymentAccount(t, db, "corporate_transfer", "6222-0000-0000-1201")
	customer, order := seedCustomerPaymentFixture(t, db, "dup-submit")
	firstAttachment := seedResubmitProofAttachment(t, db, "user:pay-cust-dup-submit", "d")
	secondAttachment := seedResubmitProofAttachment(t, db, "user:pay-cust-dup-submit", "e")

	if _, err := submitCustomerOfflinePaymentProof(ctx, db, customer, order.ID, customerPaymentProofInput{
		AccountID: account.ID, TransactionNo: "CUST-DUP-SUBMIT-0001", AmountCents: order.PayableCents,
		EvidenceAttachmentIDs: []int{firstAttachment},
	}, now); err != nil {
		t.Fatalf("首次提交凭证应成功：%v", err)
	}
	// 换流水号的第二张凭证：同一订单只允许一条待核验凭证。
	_, err := submitCustomerOfflinePaymentProof(ctx, db, customer, order.ID, customerPaymentProofInput{
		AccountID: account.ID, TransactionNo: "CUST-DUP-SUBMIT-0002", AmountCents: order.PayableCents,
		EvidenceAttachmentIDs: []int{secondAttachment},
	}, now)
	if !errors.Is(err, errConflict) || !strings.Contains(err.Error(), "待核验的转账凭证") {
		t.Fatalf("已有待核验凭证时再提交应被拒，err=%v", err)
	}
	if count := db.EsportsOfflinePaymentProof.Query().CountX(ctx); count != 1 {
		t.Fatalf("被拒的凭证不应落库：%d", count)
	}
}

// 同一订单已挂两张待核验凭证时，核销任一张都先明确拒绝，
// 提示财务先驳回重复凭证——避免两张凭证各自核销一次。
func TestOfflineVerifyRejectsDuplicatePendingProof(t *testing.T) {
	db := openDuplicateGuardDB(t, "esports_dup_guard_multi")
	ctx := t.Context()
	now := time.Now().UTC()

	account := seedActivePaymentAccount(t, db, "wechat_work", "WX-DUP-MULTI-0001")
	intent := seedPayableOrderForOffline(t, db, "dupmulti", "wechat_work")
	order := db.EsportsOrder.GetX(ctx, intent.OrderID)
	first, err := submitOfflinePaymentProof(ctx, db, offlinePaymentProofInput{
		OrderNo: order.OrderNo, AccountID: account.ID, TransactionNo: "WX-DUP-MULTI-TXN-1",
		AmountCents: order.PayableCents, ProofRef: "proof/dup-multi-0001.png",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := submitOfflinePaymentProof(ctx, db, offlinePaymentProofInput{
		OrderNo: order.OrderNo, AccountID: account.ID, TransactionNo: "WX-DUP-MULTI-TXN-2",
		AmountCents: order.PayableCents, ProofRef: "proof/dup-multi-0002.png",
	}, 11, now)
	if err != nil {
		t.Fatalf("第二张凭证当前仍允许提交（提交不可重复由渠道流水唯一键兜底）：%v", err)
	}
	if _, err = verifyOfflinePaymentProof(ctx, db, first.ID, 12, now); !errors.Is(err, errConflict) ||
		!strings.Contains(err.Error(), "另有一张待核验的转账凭证") {
		t.Fatalf("存在重复待核验凭证时核销应被拒，err=%v", err)
	}
	// 驳回重复凭证后可正常核销保留的那张。
	if _, err = rejectOfflinePaymentProof(ctx, db, second.ID, 12, "同一笔到账重复上传", now); err != nil {
		t.Fatalf("驳回重复凭证应成功：%v", err)
	}
	if _, err = verifyOfflinePaymentProof(ctx, db, first.ID, 12, now.Add(time.Minute)); err != nil {
		t.Fatalf("驳回重复凭证后核销应放行：%v", err)
	}
	paid := db.EsportsOrder.GetX(ctx, order.ID)
	if paid.PaymentStatus != esportsorder.PaymentStatusPaid {
		t.Fatalf("核销后订单应转已付款：%+v", paid)
	}
}
