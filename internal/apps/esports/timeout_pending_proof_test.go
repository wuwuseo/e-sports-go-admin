package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsofflinepaymentproof"
	"go-admin/internal/ent/esportspaymentintent"
	"go-admin/internal/ent/esportsquotahold"
	"go-admin/internal/ent/migrate"
)

// 本文件回归两个口径缺陷：
//  1. 超时取消任务 expireOrder 不得抢先取消「客户已转账、财务还没核验」的订单（存在待核验凭证）；
//  2. 外部到账核销的到账时间以凭证提交时间（created_at）为准，财务晚于支付时限核验不算超时。
// 播种复用 offline_payment_test.go 的 seedPayableOrderForOffline / seedActivePaymentAccount，
// 只在需要「已过支付时限」或「指定凭证提交时间」时做最小调整。

// seedExpiredOrderWithPendingProof 造一张已过支付时限的待付款订单，并提交一笔待核验到账凭证，
// 返回订单 ID 与凭证 ID；用于验证超时任务遇到待核验凭证时的行为。
func seedExpiredOrderWithPendingProof(t *testing.T, db *ent.Client, suffix, accountNo string) (int, int) {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC()
	intent := seedPayableOrderForOffline(t, db, suffix, "corporate_transfer")
	account := seedActivePaymentAccount(t, db, "corporate_transfer", accountNo)
	if _, err := submitOfflinePaymentProof(ctx, db, offlinePaymentProofInput{
		OrderNo: intentOrderNoForTest(t, db, intent), AccountID: account.ID, TransactionNo: "CORP-EXPIRE-" + suffix,
		AmountCents: intent.AmountCents, ProofRef: "proof/expire-" + suffix + ".png",
	}, 11, now); err != nil {
		t.Fatalf("提交待核验到账凭证失败：%v", err)
	}
	// 把支付时限推到过去：模拟超时任务扫描到这张单，此时凭证仍为待核验。
	db.EsportsOrder.UpdateOneID(intent.OrderID).SetPaymentDeadline(now.Add(-time.Hour)).ExecX(ctx)
	proof := db.EsportsOfflinePaymentProof.Query().Where(
		esportsofflinepaymentproof.OrderIDEQ(intent.OrderID)).OnlyX(ctx)
	if proof.Status != esportsofflinepaymentproof.StatusPendingVerification {
		t.Fatalf("前置条件不成立：凭证应为待核验，got=%s", proof.Status)
	}
	return intent.OrderID, proof.ID
}

// assertOrderStillAwaitingPayment 断言订单仍为「待付款」：超时任务未抢先取消。
func assertOrderStillAwaitingPayment(t *testing.T, db *ent.Client, orderID int) {
	t.Helper()
	order := db.EsportsOrder.GetX(t.Context(), orderID)
	if order.FulfillmentStatus != "pending_payment" || order.PaymentStatus != "unpaid" {
		t.Fatalf("订单应保持待付款：%+v", order)
	}
}

// 超时取消遇到本订单还有待核验到账凭证时应直接跳过：订单保持待付款、名额不释放，
// 等财务核验（客户可能已实际转账，任务不得抢先取消并释放配额）。
func TestExpireOrderSkipsOrderWithPendingVerificationProof(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_expire_pending_proof?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	orderID, proofID := seedExpiredOrderWithPendingProof(t, db, "skip", "6222-0000-0000-1001")

	// 前置条件：订单确实已过支付时限且仍未付款，且凭证仍待核验，否则本用例没有意义。
	order := db.EsportsOrder.GetX(ctx, orderID)
	if !order.PaymentDeadline.Before(now) {
		t.Fatalf("前置条件不成立：订单支付时限应在过去：deadline=%s now=%s", order.PaymentDeadline, now)
	}
	assertOrderStillAwaitingPayment(t, db, orderID)
	if db.EsportsOfflinePaymentProof.GetX(ctx, proofID).Status !=
		esportsofflinepaymentproof.StatusPendingVerification {
		t.Fatal("前置条件不成立：凭证应为待核验")
	}
	eventsBefore := db.EsportsOrderEvent.Query().CountX(ctx)

	expired, err := expireOrder(ctx, db, orderID, now)
	if err != nil || expired {
		t.Fatalf("有待核验凭证时超时任务不得取消订单：expired=%v err=%v", expired, err)
	}
	assertOrderStillAwaitingPayment(t, db, orderID)
	if db.EsportsOfflinePaymentProof.GetX(ctx, proofID).Status !=
		esportsofflinepaymentproof.StatusPendingVerification {
		t.Fatal("凭证应保持待核验，交由财务处理")
	}
	// 本轮不释放配额：名额占用仍为 reserved，SKU 已占名额不变。
	hold := db.EsportsQuotaHold.Query().Where(esportsquotahold.OrderIDEQ(orderID)).OnlyX(ctx)
	if hold.Status != esportsquotahold.StatusReserved {
		t.Fatalf("有待核验凭证时不应释放名额：%+v", hold)
	}
	if reserved := db.EsportsSku.GetX(ctx, order.SkuID).QuotaReserved; reserved != 1 {
		t.Fatalf("SKU 已占名额应保持不变，got=%d", reserved)
	}
	// 支付意图不置过期，也不落 order.payment_expired 事件。
	if status := db.EsportsPaymentIntent.GetX(ctx, orderID2IntentID(t, db, orderID)).Status; status == "expired" {
		t.Fatal("有待核验凭证时支付意图不应置为过期")
	}
	if events := db.EsportsOrderEvent.Query().CountX(ctx); events != eventsBefore {
		t.Fatalf("有待核验凭证时不应新增订单事件：before=%d after=%d", eventsBefore, events)
	}
}

// 同一订单的待核验凭证被财务驳回后，其余条件不变时应恢复超时取消：订单转 cancelled 并释放配额。
func TestExpireOrderCancelsOrderAfterProofRejected(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_expire_proof_rejected?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	orderID, proofID := seedExpiredOrderWithPendingProof(t, db, "reject", "6222-0000-0000-1002")
	eventsBefore := db.EsportsOrderEvent.Query().CountX(ctx)

	// 财务驳回凭证后本订单不再有待核验凭证，超时任务照常取消并释放配额。
	if _, err := rejectOfflinePaymentProof(ctx, db, proofID, 12, "流水号与订单不符", now); err != nil {
		t.Fatalf("驳回到账凭证失败：%v", err)
	}
	if db.EsportsOfflinePaymentProof.GetX(ctx, proofID).Status !=
		esportsofflinepaymentproof.StatusRejected {
		t.Fatal("前置条件不成立：凭证应已被驳回")
	}

	expired, err := expireOrder(ctx, db, orderID, now)
	if err != nil || !expired {
		t.Fatalf("无待核验凭证时超时任务应取消订单：expired=%v err=%v", expired, err)
	}
	order := db.EsportsOrder.GetX(ctx, orderID)
	if order.FulfillmentStatus != "cancelled" || order.PaymentStatus != "unpaid" {
		t.Fatalf("订单应转为已取消：%+v", order)
	}
	hold := db.EsportsQuotaHold.Query().Where(esportsquotahold.OrderIDEQ(orderID)).OnlyX(ctx)
	if hold.Status != esportsquotahold.StatusReleased {
		t.Fatalf("取消后名额占用应释放：%+v", hold)
	}
	if reserved := db.EsportsSku.GetX(ctx, order.SkuID).QuotaReserved; reserved != 0 {
		t.Fatalf("取消后 SKU 已占名额应退回，got=%d", reserved)
	}
	if status := db.EsportsPaymentIntent.GetX(ctx, orderID2IntentID(t, db, orderID)).Status; status != "expired" {
		t.Fatalf("取消后支付意图应置为过期，got=%s", status)
	}
	if events := db.EsportsOrderEvent.Query().CountX(ctx); events != eventsBefore+1 {
		t.Fatalf("取消应恰好落一条 order.payment_expired 事件：before=%d after=%d", eventsBefore, events)
	}
}

// orderID2IntentID 取订单当前有效的支付意图 ID（测试内播种时一单一意图，取最新一条）。
func orderID2IntentID(t *testing.T, db *ent.Client, orderID int) int {
	t.Helper()
	intent, err := db.EsportsPaymentIntent.Query().Where(
		esportspaymentintent.OrderIDEQ(orderID)).Order(ent.Desc(esportspaymentintent.FieldID)).First(t.Context())
	if err != nil {
		t.Fatalf("查询订单支付意图失败：%v", err)
	}
	return intent.ID
}

// 核验时间晚于支付时限、但凭证提交时间在时限内时应核销成功：
// 到账时间按凭证提交时间（created_at）记录，财务晚核验本身不算超时。
func TestVerifyOfflinePaymentProofAcceptsProofSubmittedBeforeDeadline(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_verify_late_confirm?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	account := seedActivePaymentAccount(t, db, "corporate_transfer", "6222-0000-0000-1003")
	intent := seedPayableOrderForOffline(t, db, "lateverify", "corporate_transfer")

	// 支付时限压到 1 分钟：凭证此刻提交（created_at ≈ now，落在时限内），
	// 财务却拖到 2 小时后才核验。旧实现把核验时刻当到账时间，必然判为超时。
	deadline := now.Add(time.Minute)
	db.EsportsOrder.UpdateOneID(intent.OrderID).SetPaymentDeadline(deadline).ExecX(ctx)
	db.EsportsPaymentIntent.UpdateOneID(intent.ID).SetExpiresAt(deadline).ExecX(ctx)
	db.EsportsConfirmation.UpdateOneID(intent.ConfirmationID).SetExpiresAt(deadline).ExecX(ctx)

	proof, err := submitOfflinePaymentProof(ctx, db, offlinePaymentProofInput{
		OrderNo: intentOrderNoForTest(t, db, intent), AccountID: account.ID, TransactionNo: "CORP-LATE-VERIFY-0001",
		AmountCents: intent.AmountCents, ProofRef: "proof/corp-late-verify.png",
	}, 11, now)
	if err != nil {
		t.Fatalf("提交到账凭证失败：%v", err)
	}
	if !proof.CreatedAt.Before(deadline) {
		t.Fatalf("前置条件不成立：凭证提交时间应在支付时限内：created_at=%s deadline=%s",
			proof.CreatedAt, deadline)
	}

	late := now.Add(2 * time.Hour)
	verified, err := verifyOfflinePaymentProof(ctx, db, proof.ID, 12, late)
	if err != nil || verified.Status != esportsofflinepaymentproof.StatusVerified ||
		verified.ReceiptID == nil {
		t.Fatalf("提交时间在时限内的凭证应核销成功：%+v err=%v", verified, err)
	}
	order := db.EsportsOrder.GetX(ctx, intent.OrderID)
	if order.PaymentStatus != "paid" || order.FulfillmentStatus != "pending_dispatch" {
		t.Fatalf("核销后订单应转已付款待派单：%+v", order)
	}
	// 收据的到账时间应是凭证提交时间（落在支付时限内），而不是 2 小时后的核验时刻。
	receipt := db.EsportsPaymentReceipt.GetX(ctx, *verified.ReceiptID)
	if receipt.ConfirmedAt.After(deadline) {
		t.Fatalf("收据到账时间应按凭证提交时间落在支付时限内：confirmed_at=%s deadline=%s",
			receipt.ConfirmedAt, deadline)
	}
	if !receipt.ConfirmedAt.Equal(verified.CreatedAt) {
		t.Fatalf("收据到账时间应等于凭证提交时间：confirmed_at=%s created_at=%s",
			receipt.ConfirmedAt, verified.CreatedAt)
	}
}

// 凭证提交时间晚于支付时限时仍应拒绝核销（保持既有保守口径：超时后提交的凭证不自动核销），
// 凭证留在待核验交人工处理。
func TestVerifyOfflinePaymentProofRejectsProofSubmittedAfterDeadline(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_verify_late_proof?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	account := seedActivePaymentAccount(t, db, "corporate_transfer", "6222-0000-0000-1004")
	intent := seedPayableOrderForOffline(t, db, "lateproof", "corporate_transfer")
	order := db.EsportsOrder.GetX(ctx, intent.OrderID)

	// created_at 是 Immutable 字段，只能在创建时给值、不能事后改；因此这里不走
	// submitOfflinePaymentProof，而是直接建档并显式写入晚于支付时限的提交时间。
	lateCreatedAt := order.PaymentDeadline.Add(time.Minute)
	proof := db.EsportsOfflinePaymentProof.Create().
		SetProofNo("OP-LATE-SUBMITTED-0001").
		SetOrderID(order.ID).SetIntentID(intent.ID).SetAccountID(account.ID).
		SetChannel(esportsofflinepaymentproof.Channel("corporate_transfer")).
		SetTransactionNo("CORP-LATE-SUBMITTED-0001").
		SetAmountCents(order.PayableCents).SetProofRef("proof/corp-late-submitted.png").
		SetStatus(esportsofflinepaymentproof.StatusPendingVerification).
		SetSubmittedByAdminID(11).SetCreatedAt(lateCreatedAt).SaveX(ctx)

	if _, err := verifyOfflinePaymentProof(ctx, db, proof.ID, 12, now.Add(2*time.Hour)); !errors.Is(err, errConflict) {
		t.Fatalf("凭证提交时间晚于支付时限时应拒绝核销：err=%v", err)
	}
	if status := db.EsportsOfflinePaymentProof.GetX(ctx, proof.ID).Status; status !=
		esportsofflinepaymentproof.StatusPendingVerification {
		t.Fatalf("被拒后凭证应保持待核验交人工处理，got=%s", status)
	}
	assertOrderStillAwaitingPayment(t, db, order.ID)
}
