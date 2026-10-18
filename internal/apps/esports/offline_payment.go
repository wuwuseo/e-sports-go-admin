package esports

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsconfirmation"
	"go-admin/internal/ent/esportsofflinepaymentproof"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportspaymentintent"
)

// 线下 / 外部到账核销（§4.2 / §4.3）：
// 客服上传转账截图或商户码截图只生成「待核验收款」，财务核验前订单不派单；
// 核销只能落到启用中的工作室收款账户，且收款方式必须处于启用状态；
// 核销复用手续收款落库边界 applyVerifiedPayment（金额校验、订单状态、配额、账本、事件一次完成），
// 提交人不得自审；唯一键 (channel, transaction_no) 保证同一笔渠道流水不会被核销两次。

type offlinePaymentProofInput struct {
	OrderID       int    `json:"order_id"`
	AccountID     int    `json:"account_id"`
	TransactionNo string `json:"transaction_no"`
	AmountCents   int64  `json:"amount_cents"`
	ProofRef      string `json:"proof_ref"`
	Note          string `json:"note"`
}

// submitOfflinePaymentProof 客服提交外部到账凭证（§4.3）：企微收款无回调、商户码截图、对公转账凭证。
func submitOfflinePaymentProof(ctx context.Context, db *ent.Client, in offlinePaymentProofInput,
	adminID int, now time.Time) (*ent.EsportsOfflinePaymentProof, error) {
	in.TransactionNo = strings.TrimSpace(in.TransactionNo)
	in.ProofRef = strings.TrimSpace(in.ProofRef)
	in.Note = strings.TrimSpace(in.Note)
	if in.OrderID < 1 || in.AccountID < 1 || adminID < 1 || in.AmountCents <= 0 ||
		in.TransactionNo == "" || len([]rune(in.TransactionNo)) > 128 ||
		len([]rune(in.ProofRef)) < 8 || len([]rune(in.ProofRef)) > 255 ||
		len([]rune(in.Note)) > 500 {
		return nil, errInvalidInput
	}
	account, err := db.EsportsPaymentAccount.Get(ctx, in.AccountID)
	if err != nil {
		return nil, err
	}
	channel := account.Channel.String()
	if !paymentChannelEnabled(ctx, db, channel) {
		return nil, errForbidden
	}
	if _, err = requireActivePaymentAccount(ctx, db, account.ID, channel); err != nil {
		return nil, err
	}
	order, err := db.EsportsOrder.Get(ctx, in.OrderID)
	if err != nil {
		return nil, err
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingPayment ||
		order.PaymentStatus != esportsorder.PaymentStatusUnpaid {
		return nil, errConflict
	}
	if in.AmountCents < order.PayableCents {
		return nil, errInvalidInput
	}
	intent, err := ensureCSOfflinePaymentIntent(ctx, db, order, channel, adminID, now)
	if err != nil {
		return nil, err
	}
	if intent.Channel != channel {
		return nil, errInvalidInput
	}
	if intent.AmountCents != order.PayableCents {
		return nil, errConflict
	}
	item, err := db.EsportsOfflinePaymentProof.Create().
		SetProofNo("OP" + uuid.NewString()).
		SetOrderID(order.ID).SetIntentID(intent.ID).SetAccountID(account.ID).
		SetChannel(esportsofflinepaymentproof.Channel(channel)).
		SetTransactionNo(in.TransactionNo).SetAmountCents(in.AmountCents).
		SetProofRef(in.ProofRef).SetNote(in.Note).
		SetStatus(esportsofflinepaymentproof.StatusPendingVerification).
		SetSubmittedByAdminID(adminID).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	return item, nil
}

// verifyOfflinePaymentProof 财务核验外部到账并核销（§4.2）：核销成功后订单进入待派单。
// 提交人不得自审；账户已停用 / 渠道已停用 / 订单已不满足核销条件时拒绝，凭证保持待核验以便人工处理。
func verifyOfflinePaymentProof(ctx context.Context, db *ent.Client, id, adminID int,
	now time.Time) (*ent.EsportsOfflinePaymentProof, error) {
	if id < 1 || adminID < 1 {
		return nil, errInvalidInput
	}
	item, err := db.EsportsOfflinePaymentProof.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.Status != esportsofflinepaymentproof.StatusPendingVerification {
		return nil, errConflict
	}
	if item.SubmittedByAdminID != nil && *item.SubmittedByAdminID == adminID {
		return nil, errForbidden
	}
	channel := item.Channel.String()
	if !paymentChannelEnabled(ctx, db, channel) {
		return nil, errForbidden
	}
	if _, err = requireActivePaymentAccount(ctx, db, item.AccountID, channel); err != nil {
		return nil, err
	}
	intent, err := db.EsportsPaymentIntent.Get(ctx, item.IntentID)
	if err != nil {
		return nil, err
	}
	// 复用手续收款落库边界：金额、报价版本、过期时间与配额都在其中强校验。
	outcome, err := applyVerifiedPayment(ctx, db, verifiedPayment{
		IntentNo: intent.IntentNo, Channel: channel, TransactionNo: item.TransactionNo,
		AmountCents: item.AmountCents, ConfirmedAt: now.UTC(),
	})
	if err != nil {
		return nil, err
	}
	if outcome.AppliedCents <= 0 {
		// 金额已收但订单当前不可核销（如报价失效被拒付）：保留待核验，交由财务人工处理。
		return nil, errConflict
	}
	changed, err := db.EsportsOfflinePaymentProof.Update().Where(
		esportsofflinepaymentproof.IDEQ(id),
		esportsofflinepaymentproof.StatusEQ(esportsofflinepaymentproof.StatusPendingVerification),
	).SetStatus(esportsofflinepaymentproof.StatusVerified).
		SetVerifiedByAdminID(adminID).SetVerifiedAt(now.UTC()).
		SetReceiptID(outcome.ReceiptID).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsOfflinePaymentProof.Get(ctx, id)
}

// rejectOfflinePaymentProof 财务驳回外部到账凭证（须填原因）；提交人不得自审。
func rejectOfflinePaymentProof(ctx context.Context, db *ent.Client, id, adminID int,
	resolution string, now time.Time) (*ent.EsportsOfflinePaymentProof, error) {
	resolution = strings.TrimSpace(resolution)
	if id < 1 || adminID < 1 || resolution == "" || len([]rune(resolution)) > 255 {
		return nil, errInvalidInput
	}
	item, err := db.EsportsOfflinePaymentProof.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.Status != esportsofflinepaymentproof.StatusPendingVerification {
		return nil, errConflict
	}
	if item.SubmittedByAdminID != nil && *item.SubmittedByAdminID == adminID {
		return nil, errForbidden
	}
	changed, err := db.EsportsOfflinePaymentProof.Update().Where(
		esportsofflinepaymentproof.IDEQ(id),
		esportsofflinepaymentproof.StatusEQ(esportsofflinepaymentproof.StatusPendingVerification),
	).SetStatus(esportsofflinepaymentproof.StatusRejected).
		SetVerifiedByAdminID(adminID).SetVerifiedAt(now.UTC()).
		SetResolution(resolution).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsOfflinePaymentProof.Get(ctx, id)
}

// listOfflinePaymentProofs 列出待核验收款单，可按状态与订单筛选。
func listOfflinePaymentProofs(ctx context.Context, db *ent.Client, status string, orderID, limit int) ([]*ent.EsportsOfflinePaymentProof, error) {
	if limit < 1 || limit > 500 {
		limit = 200
	}
	q := db.EsportsOfflinePaymentProof.Query()
	if status = strings.TrimSpace(status); status != "" {
		switch status {
		case "pending_verification", "verified", "rejected":
			q.Where(esportsofflinepaymentproof.StatusEQ(esportsofflinepaymentproof.Status(status)))
		default:
			return nil, errInvalidInput
		}
	}
	if orderID > 0 {
		q.Where(esportsofflinepaymentproof.OrderIDEQ(orderID))
	}
	return q.Order(ent.Desc(esportsofflinepaymentproof.FieldID)).Limit(limit).All(ctx)
}

// ensureCSOfflinePaymentIntent 为纯客服报单补建「客服代确认」的报价确认书与支付意图：
// 首次报价（quote_version=1）允许客服在提交外部到账凭证时一并确认；
// 改价后的订单仍要求客户先确认新报价，避免客服绕过改价确认直接收款。
func ensureCSOfflinePaymentIntent(ctx context.Context, db *ent.Client, order *ent.EsportsOrder,
	channel string, adminID int, now time.Time) (*ent.EsportsPaymentIntent, error) {
	intent, err := db.EsportsPaymentIntent.Query().Where(
		esportspaymentintent.OrderIDEQ(order.ID),
		esportspaymentintent.ChannelEQ(channel),
		esportspaymentintent.StatusEQ(esportspaymentintent.StatusPending),
	).Order(ent.Desc(esportspaymentintent.FieldID)).First(ctx)
	if err == nil {
		return intent, nil
	}
	if !ent.IsNotFound(err) {
		return nil, err
	}
	if order.QuoteVersion > 1 {
		return nil, fmt.Errorf("%w：改价后的订单需客户确认新报价后再收款", errConflict)
	}
	confirmation, err := db.EsportsConfirmation.Create().
		SetCustomerID(order.CustomerID).
		SetSubjectType("order_quote").SetSubjectID(order.ID).
		SetSubjectVersion(order.QuoteVersion).SetSkuID(order.SkuID).
		SetQuantity(order.Quantity).SetAmountCents(order.PayableCents).
		SetChannel("cs_offline").SetEvidenceRef(fmt.Sprintf("admin:%d", adminID)).
		SetStatus(esportsconfirmation.StatusVerified).
		SetVerifiedAt(now.UTC()).SetExpiresAt(order.PaymentDeadline).Save(ctx)
	if err != nil {
		return nil, err
	}
	raw := strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))
	intent, err = db.EsportsPaymentIntent.Create().
		SetIntentNo("ORD" + raw[:12]).
		SetOrderID(order.ID).SetConfirmationID(confirmation.ID).
		SetQuoteVersion(order.QuoteVersion).SetAmountCents(order.PayableCents).
		SetChannel(channel).SetStatus(esportspaymentintent.StatusPending).
		SetExpiresAt(order.PaymentDeadline).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	return intent, nil
}
