package esports

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsconfirmation"
	"go-admin/internal/ent/esportsofflinepaymentproof"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportspaymentintent"
	"go-admin/internal/ent/esportspaymentreceipt"
	"go-admin/internal/ent/esportssku"
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
	// EvidenceAttachmentIDs 是客服上传的转账截图附件 ID（§4.2-04）：非空时按本人归属 + 私有目录 +
	// 内容哈希 + 图片/PDF MIME 强校验，并以 JSON 数组落 proof_ref；为空时兼容存量文本 proof_ref。
	EvidenceAttachmentIDs []int `json:"evidence_attachment_ids"`
}

// ensureOrderSkuOnSale 校验收款订单的商品仍在售（§15.7-01）：SKU 下架后不得再发起收款，
// 必须在生成收款单 / 支付意图之前就明确失败，而不是等到核销环节才退回人工。
func ensureOrderSkuOnSale(ctx context.Context, db *ent.Client, order *ent.EsportsOrder) error {
	if order == nil {
		return errInvalidInput
	}
	sku, err := db.EsportsSku.Get(ctx, order.SkuID)
	if err != nil {
		return err
	}
	if sku.Status != esportssku.StatusOnSale {
		return fmt.Errorf("%w：商品已下架，无法发起收款，请更换在售商品或联系客户改单", errConflict)
	}
	return nil
}

// ensurePaymentDeadlineConfigured 发起收款 / 核销前的上线校验（§7 / 18-02）：
// app.esports.payment_deadline_minutes 未配置或非法时，收款入口一律明确失败（不得伪成功）。
func ensurePaymentDeadlineConfigured(ctx context.Context, db *ent.Client) error {
	_, err := paymentDeadlineMinutes(ctx, db)
	if err != nil {
		return fmt.Errorf("%w：订单支付时限尚未配置，暂不可发起收款", err)
	}
	return nil
}

// resolveOperatorProofRef 解析客服提交凭证的落库值（§4.2-04）：
// 传了附件 ID 就落 JSON 数组（可解析出附件与上传人），否则沿用存量文本 proof_ref。
func resolveOperatorProofRef(ctx context.Context, db *ent.Client, in offlinePaymentProofInput,
	adminID int) (string, error) {
	if len(in.EvidenceAttachmentIDs) == 0 {
		return in.ProofRef, nil
	}
	subject, err := adminUploadSubject(ctx, db, adminID)
	if err != nil {
		return "", err
	}
	if _, err = resolvePaymentProofEvidence(ctx, db, in.EvidenceAttachmentIDs, subject); err != nil {
		return "", err
	}
	data, err := json.Marshal(in.EvidenceAttachmentIDs)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// otherPendingPaymentProofExists 是同一订单「重复收款」防护的判定核心（P0-11）：
// 一张订单只要还有待核验的到账凭证，就说明这笔钱可能已经在途，其它收款方式（客户余额 / 储值卡）
// 不得再把订单转已付款——否则同一订单被收两次钱，后到的凭证只能挂成悬账。
//
// 调用方须在**同一事务内**传事务里的凭证查询（query），把判定与随后的「订单转已付款」条件更新
// 放在同一事务，避免并发穿插。allowProofID > 0 表示本次动作正是核销这张凭证（同一笔收款），
// 不计入「其它凭证」，因此凭证核销路径不会自我阻断。
func otherPendingPaymentProofExists(ctx context.Context, query *ent.EsportsOfflinePaymentProofQuery,
	orderID, allowProofID int) (bool, error) {
	if orderID < 1 {
		return false, nil
	}
	pending := query.Where(
		esportsofflinepaymentproof.OrderIDEQ(orderID),
		esportsofflinepaymentproof.StatusEQ(esportsofflinepaymentproof.StatusPendingVerification),
	)
	if allowProofID > 0 {
		pending = pending.Where(esportsofflinepaymentproof.IDNEQ(allowProofID))
	}
	return pending.Exist(ctx)
}

// submitOfflinePaymentProof 客服提交外部到账凭证（§4.3）：企微收款无回调、商户码截图、对公转账凭证。
func submitOfflinePaymentProof(ctx context.Context, db *ent.Client, in offlinePaymentProofInput,
	adminID int, now time.Time) (*ent.EsportsOfflinePaymentProof, error) {
	in.TransactionNo = strings.TrimSpace(in.TransactionNo)
	in.ProofRef = strings.TrimSpace(in.ProofRef)
	in.Note = strings.TrimSpace(in.Note)
	if in.OrderID < 1 || in.AccountID < 1 || adminID < 1 || in.AmountCents <= 0 ||
		in.TransactionNo == "" || len([]rune(in.TransactionNo)) > 128 ||
		len([]rune(in.Note)) > 500 ||
		(len(in.EvidenceAttachmentIDs) == 0 &&
			(len([]rune(in.ProofRef)) < 8 || len([]rune(in.ProofRef)) > 255)) {
		return nil, errInvalidInput
	}
	if err := ensurePaymentDeadlineConfigured(ctx, db); err != nil {
		return nil, err
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
	// §15.7-01：商品下架后直接拒绝发起收款，不生成收款单 / 支付意图。
	if err = ensureOrderSkuOnSale(ctx, db, order); err != nil {
		return nil, err
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingPayment ||
		order.PaymentStatus != esportsorder.PaymentStatusUnpaid {
		return nil, errConflict
	}
	if in.AmountCents < order.PayableCents {
		return nil, errInvalidInput
	}
	// §4.3-05：大额订单只允许对公转账。
	if err = ensureCorporateChannelForAmount(ctx, db, channel, in.AmountCents); err != nil {
		return nil, err
	}
	// §4.2-04：客服转账凭证须落附件并校验归属与 MIME。
	proofRef, err := resolveOperatorProofRef(ctx, db, in, adminID)
	if err != nil {
		return nil, err
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
		SetProofRef(proofRef).SetNote(in.Note).
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
	// P0-11 重复收款防护：本次动作正是核销这张待核验凭证，属规则的「同一笔收款」放行分支
	// （按自身 ID 排除），不会自我阻断；仅当同一订单还另挂一张待核验凭证时才拒绝，
	// 避免两张凭证各自被核销一次、或核销后另一张只能挂悬账。
	duplicated, err := otherPendingPaymentProofExists(ctx, db.EsportsOfflinePaymentProof.Query(),
		item.OrderID, item.ID)
	if err != nil {
		return nil, err
	}
	if duplicated {
		return nil, fmt.Errorf("%w：该订单另有一张待核验的转账凭证，请先驳回重复凭证后再核销本笔", errConflict)
	}
	// 复用手续收款落库边界：金额、报价版本、过期时间与配额都在其中强校验。
	// 到账时间以凭证提交时间（item.CreatedAt）为准，不取核验动作发生的 now：
	// applyVerifiedPayment 用它比对报价确认时间、支付意图到期时间与订单支付时限，
	// 客户在时限内转账并上传凭证、财务晚些时候才核验的，不应因为「核验发生在时限之后」
	// 被判为超时；核验动作本身发生在时限之后只是人工处理延迟，不是付款延迟。
	// 真实核验时间仍由下方 SetVerifiedAt(now.UTC()) 记录，两者含义不同、都要保留；
	// AppliedAt 传核验动作时刻，供派单优先通知窗口等运营时点使用。
	outcome, err := applyVerifiedPayment(ctx, db, verifiedPayment{
		IntentNo: intent.IntentNo, Channel: channel, TransactionNo: item.TransactionNo,
		AmountCents: item.AmountCents, ConfirmedAt: item.CreatedAt, AppliedAt: now.UTC(),
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

// ── §4.3-04 收款自动对账 ──

// paymentReconcileMismatch 是一条对账差异：按渠道流水号 / 金额比对得出。
type paymentReconcileMismatch struct {
	ProofNo       string `json:"proof_no"`
	TransactionNo string `json:"transaction_no"`
	Channel       string `json:"channel"`
	// Kind 差异类型：channel_unmatched（渠道无此回执）/ amount_mismatch（与渠道回执金额不一致）/
	// order_amount_mismatch（与订单应付金额不一致）。
	Kind          string `json:"kind"`
	ProofCents    int64  `json:"proof_cents"`
	ChannelCents  int64  `json:"channel_cents"`
	ExpectedCents int64  `json:"expected_cents"`
	Detail        string `json:"detail"`
}

// paymentReconcileReport 是订单维度的收款自动对账结果：逐笔比对收款单与渠道回执 / 订单应付。
type paymentReconcileReport struct {
	OrderID      int                        `json:"order_id"`
	PayableCents int64                      `json:"payable_cents"`
	Compared     int                        `json:"compared"`
	Matched      int                        `json:"matched"`
	Mismatches   []paymentReconcileMismatch `json:"mismatches"`
}

// reconcileOrderPaymentProofs 对订单已提交的收款单做自动对账（§4.3-04）：
//  1. 按「渠道 + 渠道流水号」在渠道回执（EsportsPaymentReceipt）中找同一笔到账；
//     找不到 → channel_unmatched（本地有凭证、渠道账单无此流水）；
//  2. 找到但金额不一致 → amount_mismatch；
//  3. 收款单金额与订单应付金额不一致 → order_amount_mismatch。
//
// 三项全部一致才计入 matched，供财务只处理差异笔，而不是逐笔人工核验。
func reconcileOrderPaymentProofs(ctx context.Context, db *ent.Client, orderID int) (*paymentReconcileReport, error) {
	if orderID < 1 {
		return nil, errInvalidInput
	}
	order, err := db.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	items, err := db.EsportsOfflinePaymentProof.Query().Where(
		esportsofflinepaymentproof.OrderIDEQ(orderID),
	).Order(ent.Asc(esportsofflinepaymentproof.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	report := &paymentReconcileReport{
		OrderID: order.ID, PayableCents: order.PayableCents,
		Compared: len(items), Mismatches: []paymentReconcileMismatch{},
	}
	for _, item := range items {
		channel := item.Channel.String()
		mismatch := paymentReconcileMismatch{
			ProofNo: item.ProofNo, TransactionNo: item.TransactionNo, Channel: channel,
			ProofCents: item.AmountCents, ExpectedCents: order.PayableCents,
		}
		receipt, receiptErr := db.EsportsPaymentReceipt.Query().Where(
			esportspaymentreceipt.ChannelEQ(channel),
			esportspaymentreceipt.TransactionNoEQ(item.TransactionNo),
		).Only(ctx)
		switch {
		case ent.IsNotFound(receiptErr):
			mismatch.Kind = "channel_unmatched"
			mismatch.Detail = "渠道账单中未找到该流水号对应的到账回执"
		case receiptErr != nil:
			return nil, receiptErr
		case receipt.ActualCents != item.AmountCents:
			mismatch.Kind = "amount_mismatch"
			mismatch.ChannelCents = receipt.ActualCents
			mismatch.Detail = fmt.Sprintf("收款单 %d 分与渠道回执 %d 分不一致", item.AmountCents, receipt.ActualCents)
		case item.AmountCents != order.PayableCents:
			mismatch.Kind = "order_amount_mismatch"
			mismatch.ChannelCents = receipt.ActualCents
			mismatch.Detail = fmt.Sprintf("收款单 %d 分与订单应付 %d 分不一致", item.AmountCents, order.PayableCents)
		default:
			report.Matched++
			continue
		}
		report.Mismatches = append(report.Mismatches, mismatch)
	}
	return report, nil
}

// ensureCSOfflinePaymentIntent 为纯客服报单补建「客服代确认」的报价确认书与支付意图：// 首次报价（quote_version=1）允许客服在提交外部到账凭证时一并确认；
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
