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

// §8.2-15 / 附录 B.3-1：客户自助提交转账凭证与客服代提交、余额核销走同一套
// 「同一订单是否已有待核验凭证」判定，因此也必须纳入同一个订单级版本占位。
// 内存 SQLite 无 SELECT ... FOR UPDATE，用 paymentOrderInterleaveHook 复现
// 「客户侧读到待付款且无凭证 → 另一路径完整提交 → 客户侧写入」的同一时序。

// openCustomerProofLockDB 打开唯一 DSN 的内存 SQLite。
func openCustomerProofLockDB(t *testing.T, dsn string) *ent.Client {
	t.Helper()
	db := enttest.Open(t, "sqlite3", "file:"+dsn+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	t.Cleanup(func() { db.Close() })
	return db
}

// 交错：客户侧读到「待付款、无凭证」后，另一路径（余额核销）完整提交 → 客户自助提交必须失败，
// 且不落第二条凭证，避免「订单已付款 + 仍挂一张待核验凭证」的悬账。
func TestCustomerPaymentProofSubmitReservesOrderSlot(t *testing.T) {
	db := openCustomerProofLockDB(t, "esports_customer_proof_order_lock")
	ctx := t.Context()
	now := time.Now().UTC()

	account := seedActivePaymentAccount(t, db, "corporate_transfer", "6222-0000-0000-9901")
	customer, order := seedCustomerPaymentFixture(t, db, "proof-lock")
	attachment := seedPaymentProofAttachment(t, db, "user:pay-cust-proof-lock")
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: order.PayableCents, Note: "客户自助提交凭证占位测试"},
		"cust-proof-lock-recharge"); err != nil {
		t.Fatal(err)
	}

	// 交错点：在「本函数读到订单」与「事务内占位」之间插入另一条收款路径的完整提交。
	var paidErr error
	entered := false
	paymentOrderInterleaveHook = func(orderID int) {
		if entered || orderID != order.ID {
			return
		}
		entered = true
		_, paidErr = payOrderWithBalance(ctx, db, 11, order.ID, now)
	}
	t.Cleanup(func() { paymentOrderInterleaveHook = nil })

	_, err := submitCustomerOfflinePaymentProof(ctx, db, customer, order.ID, customerPaymentProofInput{
		AccountID: account.ID, TransactionNo: "CUST-LOCK-0001", AmountCents: order.PayableCents,
		EvidenceAttachmentIDs: []int{attachment},
	}, now)
	if !errors.Is(err, errConflict) || !strings.Contains(err.Error(), "占单处理") {
		t.Fatalf("交错点后客户自助提交凭证必须判冲突，err=%v", err)
	}
	if paidErr != nil {
		t.Fatalf("交错点内余额核销应成功：%v", paidErr)
	}
	if got := db.EsportsOfflinePaymentProof.Query().CountX(ctx); got != 0 {
		t.Fatalf("被拒的客户自助凭证不得落库：%d", got)
	}
	paid := db.EsportsOrder.GetX(ctx, order.ID)
	if paid.PaymentStatus != esportsorder.PaymentStatusPaid ||
		paid.PaymentChannel != esportsorder.PaymentChannelCustomerBalance {
		t.Fatalf("订单应已由交错路径核销：%+v", paid)
	}
}

// 正常单请求不受影响：无交错时客户自助提交凭证成功、订单级占位推进版本但不改订单状态，
// 随后财务核验仍能走通。
func TestCustomerPaymentProofSubmitSlotKeepsSingleRequestWorking(t *testing.T) {
	db := openCustomerProofLockDB(t, "esports_customer_proof_lock_normal")
	ctx := t.Context()
	now := time.Now().UTC()

	account := seedActivePaymentAccount(t, db, "merchant_qr", "QR-0000-0000-9902")
	customer, order := seedCustomerPaymentFixture(t, db, "proof-lock-normal")
	attachment := seedPaymentProofAttachment(t, db, "user:pay-cust-proof-lock-normal")

	proof, err := submitCustomerOfflinePaymentProof(ctx, db, customer, order.ID, customerPaymentProofInput{
		AccountID: account.ID, TransactionNo: "CUST-LOCK-0002", AmountCents: order.PayableCents,
		EvidenceAttachmentIDs: []int{attachment},
	}, now)
	if err != nil || proof.Status != esportsofflinepaymentproof.StatusPendingVerification {
		t.Fatalf("单请求客户自助提交凭证应成功：%+v err=%v", proof, err)
	}
	reload := db.EsportsOrder.GetX(ctx, order.ID)
	if reload.Version != order.Version+1 {
		t.Fatalf("客户自助提交凭证必须占用订单版本槽：%d -> %d", order.Version, reload.Version)
	}
	if reload.PaymentStatus != esportsorder.PaymentStatusUnpaid ||
		reload.FulfillmentStatus != esportsorder.FulfillmentStatusPendingPayment {
		t.Fatalf("提交凭证不应变更订单状态：%+v", reload)
	}

	// 核验路径的占位读到的是推进后的版本，正常核销不受影响。
	verified, err := verifyOfflinePaymentProof(ctx, db, proof.ID, 12, now.Add(time.Minute))
	if err != nil || verified.Status != esportsofflinepaymentproof.StatusVerified {
		t.Fatalf("财务核验应成功：%+v err=%v", verified, err)
	}
	if paid := db.EsportsOrder.GetX(ctx, order.ID); paid.PaymentStatus != esportsorder.PaymentStatusPaid {
		t.Fatalf("核验后订单应转已付款：%+v", paid)
	}
}
