package esports

import (
	"errors"
	"fmt"
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

// 客户被驳回的到账凭证补交回归测试：
// 同一笔转账（channel + transaction_no）被财务驳回后，客户可上传新截图原样补交，
// 唯一键仍保证同一渠道流水不会被核销两次。

// seedResubmitProofAttachment 造一个本人上传的付款凭证附件，内容哈希按 seed 区分，
// 避免与既有附件撞 (content_hash, size, original_name_hash) 唯一索引。
func seedResubmitProofAttachment(t *testing.T, db *ent.Client, uploadedBy, seed string) int {
	t.Helper()
	return db.Attachment.Create().SetName("补交凭证").SetOriginalName("proof-" + seed + ".png").
		SetPath("private/payment-proof/proof-" + seed + ".png").SetMimeType("image/png").SetExtension("png").
		SetDirectory(paymentProofEvidenceDirectory).SetSize(128).SetUploadedBy(uploadedBy).
		SetContentHash(strings.Repeat(seed, 64)).SaveX(t.Context()).ID
}

// seedSecondPayableOrder 为同一客户再建一张待付款订单并补上已核验报价，
// 用于验证「同一渠道流水号换订单补交」必须判冲突。
func seedSecondPayableOrder(t *testing.T, db *ent.Client, customer *ent.EsportsCustomer,
	skuID int, suffix string, now time.Time) *ent.EsportsOrder {
	t.Helper()
	ctx := t.Context()
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: skuID, Quantity: 1},
		"resubmit-order-"+suffix, now)
	if err != nil {
		t.Fatal(err)
	}
	db.EsportsConfirmation.Create().SetCustomerID(customer.ID).
		SetSubjectType("order_quote").SetSubjectID(order.ID).SetSubjectVersion(order.QuoteVersion).
		SetSkuID(skuID).SetQuantity(order.Quantity).SetAmountCents(order.PayableCents).
		SetChannel("authenticated_self_service").SetEvidenceRef("resubmit-evidence-" + suffix).
		SetStatus("verified").SetVerifiedAt(now).SetExpiresAt(order.PaymentDeadline).SaveX(ctx)
	return order
}

// 首次提交成功 → 财务驳回 → 用同一流水号 + 新附件补交：原地重置为待核验，仍是同一条凭证。
func TestCustomerPaymentProofResubmitAfterRejection(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_customer_payment_resubmit?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	account := seedActivePaymentAccount(t, db, "corporate_transfer", "6222-0000-0000-4444")
	customer, order := seedCustomerPaymentFixture(t, db, "resubmit")
	firstAttachment := seedResubmitProofAttachment(t, db, "user:pay-cust-resubmit", "b")
	secondAttachment := seedResubmitProofAttachment(t, db, "user:pay-cust-resubmit", "c")

	proof, err := submitCustomerOfflinePaymentProof(ctx, db, customer, order.ID, customerPaymentProofInput{
		AccountID: account.ID, TransactionNo: "CUST-RESUBMIT-0001", AmountCents: order.PayableCents,
		Note: "首次转账截图", EvidenceAttachmentIDs: []int{firstAttachment},
	}, now)
	if err != nil || proof.Status != esportsofflinepaymentproof.StatusPendingVerification {
		t.Fatalf("首次提交应生成待核验凭证：%+v err=%v", proof, err)
	}

	// 财务以「截图不清」驳回（提交人非财务，可正常驳回）。
	rejected, err := rejectOfflinePaymentProof(ctx, db, proof.ID, 12, "截图不清，请重新上传清晰截图", now)
	if err != nil || rejected.Status != esportsofflinepaymentproof.StatusRejected {
		t.Fatalf("财务驳回应成功：%+v err=%v", rejected, err)
	}

	resubmitted, err := submitCustomerOfflinePaymentProof(ctx, db, customer, order.ID, customerPaymentProofInput{
		AccountID: account.ID, TransactionNo: "CUST-RESUBMIT-0001", AmountCents: order.PayableCents,
		Note: "补交清晰截图", EvidenceAttachmentIDs: []int{secondAttachment},
	}, now)
	if err != nil {
		t.Fatalf("驳回后应可用同一流水号补交：err=%v", err)
	}
	if resubmitted.ID != proof.ID || resubmitted.ProofNo != proof.ProofNo {
		t.Fatalf("补交应原地重置同一条凭证：id=%d/%d no=%s/%s",
			resubmitted.ID, proof.ID, resubmitted.ProofNo, proof.ProofNo)
	}
	if resubmitted.Status != esportsofflinepaymentproof.StatusPendingVerification || resubmitted.Resolution != "" {
		t.Fatalf("补交后应回到待核验且清空驳回原因：%+v", resubmitted)
	}
	if resubmitted.ProofRef != fmt.Sprintf("[%d]", secondAttachment) {
		t.Fatalf("补交后凭证引用应指向新附件 %d：%q", secondAttachment, resubmitted.ProofRef)
	}
	if resubmitted.Version != rejected.Version+1 {
		t.Fatalf("补交后版本号应递增：%d → %d", rejected.Version, resubmitted.Version)
	}
	if resubmitted.VerifiedAt != nil || resubmitted.VerifiedByAdminID != nil || resubmitted.ReceiptID != nil {
		t.Fatalf("补交后应清空驳回 / 核销回填：%+v", resubmitted)
	}
	if count := db.EsportsOfflinePaymentProof.Query().CountX(ctx); count != 1 {
		t.Fatalf("补交不应新增数据行：count=%d", count)
	}
	// 补交不影响订单状态，仍需财务按新截图重新核验。
	reload := db.EsportsOrder.GetX(ctx, order.ID)
	if reload.PaymentStatus != esportsorder.PaymentStatusUnpaid ||
		reload.FulfillmentStatus != esportsorder.FulfillmentStatusPendingPayment {
		t.Fatalf("补交不应变更订单状态：%+v", reload)
	}
}

// 并发兜底：凭证已不在驳回态时，条件更新（ID + 驳回态）影响行数为 0，必须判冲突。
// 该分支在正常串行链路里被前置状态判断挡住，此处直接调用 helper 覆盖。
func TestCustomerPaymentProofResetGuardConflictsOnStatusChange(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_customer_payment_reset_guard?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	account := seedActivePaymentAccount(t, db, "corporate_transfer", "6222-0000-0000-8888")
	customer, order := seedCustomerPaymentFixture(t, db, "reset-guard")
	attachment := seedResubmitProofAttachment(t, db, "user:pay-cust-reset-guard", "b")

	// 首次提交后凭证处于待核验（非驳回态）：等价于并发场景下另一请求已把它推进到下一状态。
	proof, err := submitCustomerOfflinePaymentProof(ctx, db, customer, order.ID, customerPaymentProofInput{
		AccountID: account.ID, TransactionNo: "CUST-RESET-GUARD-0001", AmountCents: order.PayableCents,
		EvidenceAttachmentIDs: []int{attachment},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = resetRejectedPaymentProof(ctx, tx, proof, proof.IntentID, proof.AccountID,
		proof.AmountCents, proof.ProofRef, proof.Note); !errors.Is(err, errConflict) {
		t.Fatalf("非驳回态原地重置应判冲突：err=%v", err)
	}
	// SQLite 下事务内条件更新会持有写锁，先回滚再用新连接读取校验。
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	reload := db.EsportsOfflinePaymentProof.GetX(ctx, proof.ID)
	if reload.Status != esportsofflinepaymentproof.StatusPendingVerification || reload.Version != proof.Version {
		t.Fatalf("被拒的重置不得改动凭证：%+v", reload)
	}
}

// 待核验状态下用同一流水号重复提交：判冲突且不产生第二条记录。
func TestCustomerPaymentProofDuplicateResubmitConflicts(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_customer_payment_dup_resubmit?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	account := seedActivePaymentAccount(t, db, "corporate_transfer", "6222-0000-0000-5555")
	customer, order := seedCustomerPaymentFixture(t, db, "dup")
	firstAttachment := seedResubmitProofAttachment(t, db, "user:pay-cust-dup", "b")
	secondAttachment := seedResubmitProofAttachment(t, db, "user:pay-cust-dup", "c")

	proof, err := submitCustomerOfflinePaymentProof(ctx, db, customer, order.ID, customerPaymentProofInput{
		AccountID: account.ID, TransactionNo: "CUST-DUP-0001", AmountCents: order.PayableCents,
		EvidenceAttachmentIDs: []int{firstAttachment},
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	_, err = submitCustomerOfflinePaymentProof(ctx, db, customer, order.ID, customerPaymentProofInput{
		AccountID: account.ID, TransactionNo: "CUST-DUP-0001", AmountCents: order.PayableCents,
		EvidenceAttachmentIDs: []int{secondAttachment},
	}, now)
	if !errors.Is(err, errConflict) {
		t.Fatalf("待核验下重复提交同一流水号应冲突：err=%v", err)
	}
	if !strings.Contains(err.Error(), "已提交或已核销") {
		t.Fatalf("重复提交应给出中文冲突提示：err=%v", err)
	}
	if count := db.EsportsOfflinePaymentProof.Query().CountX(ctx); count != 1 {
		t.Fatalf("重复提交不应新增数据行：count=%d", count)
	}
	reload := db.EsportsOfflinePaymentProof.GetX(ctx, proof.ID)
	if reload.ProofRef != fmt.Sprintf("[%d]", firstAttachment) || reload.Version != proof.Version {
		t.Fatalf("重复提交不得改动原凭证：%+v", reload)
	}
}

// 同一流水号但目标订单不同：判冲突（该笔渠道流水已被其它订单占用）。
func TestCustomerPaymentProofResubmitOtherOrderConflicts(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_customer_payment_other_order?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	account := seedActivePaymentAccount(t, db, "corporate_transfer", "6222-0000-0000-6666")
	customer, order := seedCustomerPaymentFixture(t, db, "other-order")
	secondOrder := seedSecondPayableOrder(t, db, customer, order.SkuID, "other-order", now)
	attachment := seedResubmitProofAttachment(t, db, "user:pay-cust-other-order", "b")

	if _, err := submitCustomerOfflinePaymentProof(ctx, db, customer, order.ID, customerPaymentProofInput{
		AccountID: account.ID, TransactionNo: "CUST-OTHER-0001", AmountCents: order.PayableCents,
		EvidenceAttachmentIDs: []int{attachment},
	}, now); err != nil {
		t.Fatal(err)
	}

	_, err := submitCustomerOfflinePaymentProof(ctx, db, customer, secondOrder.ID, customerPaymentProofInput{
		AccountID: account.ID, TransactionNo: "CUST-OTHER-0001", AmountCents: secondOrder.PayableCents,
		EvidenceAttachmentIDs: []int{attachment},
	}, now)
	if !errors.Is(err, errConflict) {
		t.Fatalf("同一流水号换订单补交应冲突：err=%v", err)
	}
	if !strings.Contains(err.Error(), "其它订单") {
		t.Fatalf("换订单冲突应给出中文提示：err=%v", err)
	}
	if count := db.EsportsOfflinePaymentProof.Query().CountX(ctx); count != 1 {
		t.Fatalf("换订单补交不应新增数据行：count=%d", count)
	}
}

// 已核销（verified）后用同一流水号补交：判冲突。
func TestCustomerPaymentProofResubmitVerifiedConflicts(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_customer_payment_verified?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	account := seedActivePaymentAccount(t, db, "corporate_transfer", "6222-0000-0000-7777")
	customer, order := seedCustomerPaymentFixture(t, db, "verified")
	firstAttachment := seedResubmitProofAttachment(t, db, "user:pay-cust-verified", "b")
	secondAttachment := seedResubmitProofAttachment(t, db, "user:pay-cust-verified", "c")

	proof, err := submitCustomerOfflinePaymentProof(ctx, db, customer, order.ID, customerPaymentProofInput{
		AccountID: account.ID, TransactionNo: "CUST-VERIFIED-0001", AmountCents: order.PayableCents,
		EvidenceAttachmentIDs: []int{firstAttachment},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := verifyOfflinePaymentProof(ctx, db, proof.ID, 12, now)
	if err != nil || verified.Status != esportsofflinepaymentproof.StatusVerified || verified.ReceiptID == nil {
		t.Fatalf("财务核销应成功：%+v err=%v", verified, err)
	}
	// 订单已转已付款，会被订单状态前置校验拦下；这里把订单状态回置为待付款，
	// 以隔离验证「同一流水号已核销不可再补交」这一凭证分支。
	db.EsportsOrder.UpdateOneID(order.ID).
		SetPaymentStatus(esportsorder.PaymentStatusUnpaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingPayment).ExecX(ctx)

	_, err = submitCustomerOfflinePaymentProof(ctx, db, customer, order.ID, customerPaymentProofInput{
		AccountID: account.ID, TransactionNo: "CUST-VERIFIED-0001", AmountCents: order.PayableCents,
		EvidenceAttachmentIDs: []int{secondAttachment},
	}, now)
	if !errors.Is(err, errConflict) {
		t.Fatalf("已核销流水补交应冲突：err=%v", err)
	}
	if !strings.Contains(err.Error(), "已提交或已核销") {
		t.Fatalf("已核销补交应给出中文提示：err=%v", err)
	}
	reload := db.EsportsOfflinePaymentProof.GetX(ctx, proof.ID)
	if reload.Status != esportsofflinepaymentproof.StatusVerified || reload.ReceiptID == nil {
		t.Fatalf("已核销凭证不得被重置：%+v", reload)
	}
	if count := db.EsportsOfflinePaymentProof.Query().CountX(ctx); count != 1 {
		t.Fatalf("已核销后补交不应新增数据行：count=%d", count)
	}
}
