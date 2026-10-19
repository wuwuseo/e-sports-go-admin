package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsofflinepaymentproof"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/migrate"
)

// 本文件回归 T1 评审提出的两个下游风险：
//  1. 超时扫描窗口被「待核验凭证订单」占满导致任务停摆（订单永远满足过期条件且排在最前）；
//  2. 已过支付时限仍可提交**新的**到账凭证 → 客户可长期把订单与名额挂在待付款。

// 待核验凭证订单不得占用超时扫描的 LIMIT 名额：limit=1 时也必须能取消到另一张真正超时的订单。
func TestExpireDueOrdersSkipsPendingProofWithoutStarvingLimit(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_expire_scan_window?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	// 待核验凭证订单：过期最久（deadline 最早），旧实现会一直占住 limit=1 的唯一名额。
	heldOrderID, _ := seedExpiredOrderWithPendingProof(t, db, "scan", "6222-SCAN-0001")
	db.EsportsOrder.UpdateOneID(heldOrderID).SetPaymentDeadline(now.Add(-2 * time.Hour)).ExecX(ctx)
	held := db.EsportsOrder.GetX(ctx, heldOrderID)

	// 另一张同样超时、但没有待核验凭证的订单（同客户同商品，deadline 稍晚）。
	plain, err := placeOrder(ctx, db, 11, orderInput{
		CustomerID: held.CustomerID, SkuID: held.SkuID, Quantity: 1,
	}, "offline-order-scan-plain", now)
	if err != nil {
		t.Fatalf("补造普通超时订单失败：%v", err)
	}
	db.EsportsOrder.UpdateOneID(plain.ID).SetPaymentDeadline(now.Add(-time.Hour)).ExecX(ctx)

	count, err := expireDueOrders(ctx, db, now, 1)
	if err != nil {
		t.Fatalf("超时扫描应成功，err=%v", err)
	}
	if count != 1 {
		t.Fatalf("待核验订单不应占用扫描名额，应取消另一张超时订单，got=%d", count)
	}
	assertOrderStillAwaitingPayment(t, db, heldOrderID)
	if reload := db.EsportsOrder.GetX(ctx, plain.ID); reload.FulfillmentStatus != esportsorder.FulfillmentStatusCancelled {
		t.Fatalf("未被占用的超时订单应被取消：%+v", reload)
	}
}

// 已过支付时限：客户不得再提交**新的**到账凭证；被驳回凭证的补交不受限（锚定首次提交时间）。
func TestCustomerPaymentProofAfterDeadlineOnlyAllowsResubmit(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_customer_payment_deadline_guard?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	account := seedActivePaymentAccount(t, db, "corporate_transfer", "6222-DEADLINE-0001")
	customer, order := seedCustomerPaymentFixture(t, db, "deadline")
	attachmentID := seedPaymentProofAttachment(t, db, "user:pay-cust-deadline")

	// 先提交一笔凭证（时限内），随后由财务驳回。
	proof, err := submitCustomerOfflinePaymentProof(ctx, db, customer, order.ID, customerPaymentProofInput{
		AccountID: account.ID, TransactionNo: "CUST-DEADLINE-0001", AmountCents: order.PayableCents,
		EvidenceAttachmentIDs: []int{attachmentID},
	}, now)
	if err != nil {
		t.Fatalf("时限内提交凭证应成功：%v", err)
	}
	if _, err = rejectOfflinePaymentProof(ctx, db, proof.ID, 12, "截图不清", now); err != nil {
		t.Fatalf("财务驳回凭证应成功：%v", err)
	}

	// 把订单推到已过期：新的转账凭证一律拒绝（避免用假凭证长期占用订单与名额）。
	db.EsportsOrder.UpdateOneID(order.ID).SetPaymentDeadline(now.Add(-time.Minute)).ExecX(ctx)
	if _, err = submitCustomerOfflinePaymentProof(ctx, db, customer, order.ID, customerPaymentProofInput{
		AccountID: account.ID, TransactionNo: "CUST-DEADLINE-0002", AmountCents: order.PayableCents,
		EvidenceAttachmentIDs: []int{attachmentID},
	}, now); !errors.Is(err, errConflict) {
		t.Fatalf("超过支付时限后提交新凭证应被拒，err=%v", err)
	}
	if total := db.EsportsOfflinePaymentProof.Query().Where(
		esportsofflinepaymentproof.OrderIDEQ(order.ID)).CountX(ctx); total != 1 {
		t.Fatalf("被拒的新凭证不应落库，got=%d 条", total)
	}

	// 同一笔转账（原流水号）的补交仍允许：首次提交时间在时限内，属同一笔已发生的转账。
	resubmitted, err := submitCustomerOfflinePaymentProof(ctx, db, customer, order.ID, customerPaymentProofInput{
		AccountID: account.ID, TransactionNo: "CUST-DEADLINE-0001", AmountCents: order.PayableCents,
		EvidenceAttachmentIDs: []int{attachmentID},
	}, now)
	if err != nil {
		t.Fatalf("超时后补交被驳回凭证应允许，err=%v", err)
	}
	if resubmitted.ID != proof.ID || resubmitted.Status != esportsofflinepaymentproof.StatusPendingVerification {
		t.Fatalf("补交应原地重置为待核验：%+v", resubmitted)
	}
}
