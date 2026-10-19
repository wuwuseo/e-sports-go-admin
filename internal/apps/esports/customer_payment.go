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
)

// 客户自助端收款闭环（§4.3）：客户读取启用的收款方式与财务审核通过的收款账户，
// 为订单上传转账/扫码截图凭证生成「待核验收款」，落入既有 offline_payment 财务核验链路；
// 并可查询核验进度（待核验 / 已核销 / 被驳回）。

// customerPaymentMethodName 收款方式展示名（与订单 payment_channel 枚举一一对应）。
var customerPaymentMethodName = map[string]string{
	"customer_balance":   "客户余额",
	"stored_value_card":  "储值卡",
	"wechat_work":        "企微收款",
	"merchant_qr":        "商户码",
	"corporate_transfer": "对公转账",
}

// customerPaymentAccountView 客户侧收款账户视图：只暴露收款所需信息，不含审核人 / 提交人等后台字段。
type customerPaymentAccountView struct {
	ID          int    `json:"id"`
	Channel     string `json:"channel"`
	SubjectName string `json:"subject_name"`
	AccountNo   string `json:"account_no"`
	QrURL       string `json:"qr_url"`
	BankName    string `json:"bank_name"`
}

// customerPaymentMethodView 客户侧收款方式视图：internal 为余额/储值卡等站内方式（无账户），
// external 为需上传凭证的外部收款渠道（携带财务审核通过的收款账户）。
type customerPaymentMethodView struct {
	Channel  string                       `json:"channel"`
	Name     string                       `json:"name"`
	Kind     string                       `json:"kind"`
	Enabled  bool                         `json:"enabled"`
	Accounts []customerPaymentAccountView `json:"accounts"`
}

// listCustomerPaymentMethods 客户读取当前启用的收款方式与财务审核通过的收款账户（§4.3）。
// 后台关闭的外部收款方式不返回；未通过审核（待审核 / 驳回 / 停用）的账户不返回。
func listCustomerPaymentMethods(ctx context.Context, db *ent.Client) ([]customerPaymentMethodView, error) {
	channels, err := listPaymentChannels(ctx, db)
	if err != nil {
		return nil, err
	}
	enabled := make(map[string]bool, len(channels))
	for _, channel := range channels {
		enabled[channel.Channel] = channel.Enabled
	}
	// 站内方式（余额 / 储值卡）同样受后台启停约束（§4.3-08）：停用后不再作为可选收款方式。
	methods := []customerPaymentMethodView{
		{Channel: "customer_balance", Name: customerPaymentMethodName["customer_balance"], Kind: "internal", Enabled: enabled["customer_balance"], Accounts: []customerPaymentAccountView{}},
		{Channel: "stored_value_card", Name: customerPaymentMethodName["stored_value_card"], Kind: "internal", Enabled: enabled["stored_value_card"], Accounts: []customerPaymentAccountView{}},
	}
	for _, channel := range externalPaymentChannelValues {
		if !enabled[channel] {
			continue
		}
		accounts := []customerPaymentAccountView{}
		items, err := listPaymentAccounts(ctx, db, channel, "active", 200)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			accounts = append(accounts, customerPaymentAccountView{
				ID: item.ID, Channel: item.Channel.String(), SubjectName: item.SubjectName,
				AccountNo: item.AccountNo, QrURL: item.QrURL, BankName: item.BankName,
			})
		}
		methods = append(methods, customerPaymentMethodView{
			Channel: channel, Name: customerPaymentMethodName[channel], Kind: "external",
			Enabled: true, Accounts: accounts,
		})
	}
	return methods, nil
}

// paymentProofEvidenceDirectory 客户付款凭证附件的私有目录。
const paymentProofEvidenceDirectory = "private/esports/payment-proof"

// maxPaymentProofAttachments 单次可引用的付款凭证附件上限。
const maxPaymentProofAttachments = 6

// resolvePaymentProofEvidence 校验付款凭证附件：须为本人上传、落在付款凭证私有目录、
// 内容哈希为 64 位 hex、MIME 为图片/PDF。归属不符返回 errForbidden，其余不满足返回 errInvalidInput。
func resolvePaymentProofEvidence(ctx context.Context, db *ent.Client, ids []int, expectedUploadedBy string) ([]*ent.Attachment, error) {
	if len(ids) == 0 {
		return nil, errInvalidInput
	}
	if len(ids) > maxPaymentProofAttachments {
		return nil, errInvalidInput
	}
	if expectedUploadedBy == "" {
		return nil, errForbidden
	}
	attachments := make([]*ent.Attachment, 0, len(ids))
	seen := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		if id < 1 {
			return nil, errInvalidInput
		}
		if _, dup := seen[id]; dup {
			return nil, errInvalidInput
		}
		seen[id] = struct{}{}
		item, err := db.Attachment.Get(ctx, id)
		if err != nil {
			return nil, errInvalidInput
		}
		if !matchesEvidenceUploader(item.UploadedBy, expectedUploadedBy) {
			return nil, errForbidden
		}
		if item.Directory != paymentProofEvidenceDirectory ||
			!validEvidenceHash(item.ContentHash) ||
			!validAftersaleEvidenceMime(item.MimeType) {
			return nil, errInvalidInput
		}
		attachments = append(attachments, item)
	}
	return attachments, nil
}

// matchesEvidenceUploader 判断附件上传人是否为期望的归属主体。
// 前台路由写入的上传人带 "user:" / "admin:" 域前缀，管理端与存量数据可能只存裸 uid，两者都视为本人。
func matchesEvidenceUploader(uploadedBy, subject string) bool {
	if uploadedBy == "" || subject == "" {
		return false
	}
	if uploadedBy == subject {
		return true
	}
	for _, prefix := range []string{"user:", "admin:"} {
		if strings.HasPrefix(subject, prefix) && uploadedBy == strings.TrimPrefix(subject, prefix) {
			return true
		}
	}
	return false
}

// adminUploadSubject 返回管理端上传附件的归属标识 "admin:<uid>"（§4.2 客服凭证归属可追溯）。
func adminUploadSubject(ctx context.Context, db *ent.Client, adminID int) (string, error) {
	if adminID < 1 {
		return "", errForbidden
	}
	operator, err := db.Admin.Get(ctx, adminID)
	if err != nil || strings.TrimSpace(operator.UID) == "" {
		return "", errForbidden
	}
	return "admin:" + operator.UID, nil
}

// customerPaymentProofInput 客户自助提交付款凭证入参（§4.3）。
type customerPaymentProofInput struct {
	AccountID             int    `json:"account_id"`
	TransactionNo         string `json:"transaction_no"`
	AmountCents           int64  `json:"amount_cents"`
	Note                  string `json:"note"`
	EvidenceAttachmentIDs []int  `json:"evidence_attachment_ids"`
}

// requireOrderQuoteConfirmation 取订单已核验的报价确认书；客户须先确认报价才可提交付款凭证。
func requireOrderQuoteConfirmation(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder) (*ent.EsportsConfirmation, error) {
	confirmation, err := tx.EsportsConfirmation.Query().Where(
		esportsconfirmation.SubjectTypeEQ("order_quote"),
		esportsconfirmation.SubjectIDEQ(order.ID),
		esportsconfirmation.SubjectVersionEQ(order.QuoteVersion),
		esportsconfirmation.CustomerIDEQ(order.CustomerID),
		esportsconfirmation.StatusEQ(esportsconfirmation.StatusVerified),
	).Order(ent.Desc(esportsconfirmation.FieldID)).First(ctx)
	if ent.IsNotFound(err) {
		return nil, fmt.Errorf("%w：请先确认报价后再提交付款凭证", errInvalidInput)
	}
	if err != nil {
		return nil, err
	}
	return confirmation, nil
}

// ensureOrderPaymentIntent 保障订单在指定收款渠道下存在支付意图：复用既有待支付意图（金额 / 报价版本一致），
// 否则新建；意图到期时间与订单支付时限一致，核销边界 applyVerifiedPayment 会校验其一致性。
func ensureOrderPaymentIntent(ctx context.Context, tx *ent.Tx, order *ent.EsportsOrder,
	channel string, now time.Time) (*ent.EsportsPaymentIntent, error) {
	confirmation, err := requireOrderQuoteConfirmation(ctx, tx, order)
	if err != nil {
		return nil, err
	}
	intent, err := tx.EsportsPaymentIntent.Query().Where(
		esportspaymentintent.OrderIDEQ(order.ID),
		esportspaymentintent.ChannelEQ(channel),
		esportspaymentintent.StatusEQ(esportspaymentintent.StatusPending),
	).Order(ent.Desc(esportspaymentintent.FieldID)).First(ctx)
	if err == nil {
		if intent.ConfirmationID != confirmation.ID || intent.AmountCents != order.PayableCents ||
			intent.QuoteVersion != order.QuoteVersion {
			return nil, errConflict
		}
		return intent, nil
	}
	if !ent.IsNotFound(err) {
		return nil, err
	}
	raw := strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))
	intent, err = tx.EsportsPaymentIntent.Create().
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

// submitCustomerOfflinePaymentProof 客户为本人订单提交外部到账凭证（§4.3）：
// 校验订单归属与待付款状态 → 校验收款账户（渠道启用 + 账户审核通过）→ 保障支付意图 →
// 生成「待核验收款」落入既有 offline_payment 财务核验链路。提交人记为客户侧（无归属客服自审限制）。
func submitCustomerOfflinePaymentProof(ctx context.Context, db *ent.Client, customer *ent.EsportsCustomer,
	orderID int, in customerPaymentProofInput, now time.Time) (*ent.EsportsOfflinePaymentProof, error) {
	in.TransactionNo = strings.TrimSpace(in.TransactionNo)
	in.Note = strings.TrimSpace(in.Note)
	if customer == nil || customer.ID < 1 || orderID < 1 || in.AccountID < 1 || in.AmountCents <= 0 ||
		in.TransactionNo == "" || len([]rune(in.TransactionNo)) > 128 ||
		len([]rune(in.Note)) > 500 || len(in.EvidenceAttachmentIDs) == 0 ||
		len(in.EvidenceAttachmentIDs) > maxPaymentProofAttachments {
		return nil, errInvalidInput
	}
	if err := ensurePaymentDeadlineConfigured(ctx, db); err != nil {
		return nil, err
	}
	subject, err := customerUploadSubject(ctx, db, customer)
	if err != nil {
		return nil, err
	}
	if _, err = resolvePaymentProofEvidence(ctx, db, in.EvidenceAttachmentIDs, subject); err != nil {
		return nil, err
	}
	proofRef, err := json.Marshal(in.EvidenceAttachmentIDs)
	if err != nil {
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
	order, err := db.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.CustomerID != customer.ID {
		return nil, errForbidden
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingPayment ||
		order.PaymentStatus != esportsorder.PaymentStatusUnpaid {
		return nil, errConflict
	}
	if in.AmountCents < order.PayableCents {
		return nil, errInvalidInput
	}
	// §15.7-01：商品下架后直接拒绝发起收款，不生成收款单 / 支付意图。
	if err = ensureOrderSkuOnSale(ctx, db, order); err != nil {
		return nil, err
	}
	// §4.3-05：大额订单只允许对公转账。
	if err = ensureCorporateChannelForAmount(ctx, db, channel, in.AmountCents); err != nil {
		return nil, err
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	intent, err := ensureOrderPaymentIntent(ctx, tx, order, channel, now)
	if err != nil {
		return nil, err
	}
	item, err := tx.EsportsOfflinePaymentProof.Create().
		SetProofNo("OP" + uuid.NewString()).
		SetOrderID(order.ID).SetIntentID(intent.ID).SetAccountID(account.ID).
		SetChannel(esportsofflinepaymentproof.Channel(channel)).
		SetTransactionNo(in.TransactionNo).SetAmountCents(in.AmountCents).
		SetProofRef(string(proofRef)).SetNote(in.Note).
		SetStatus(esportsofflinepaymentproof.StatusPendingVerification).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}

// customerPaymentProofView 客户侧付款凭证核验进度视图。
type customerPaymentProofView struct {
	ID            int        `json:"id"`
	ProofNo       string     `json:"proof_no"`
	OrderID       int        `json:"order_id"`
	Channel       string     `json:"channel"`
	TransactionNo string     `json:"transaction_no"`
	AmountCents   int64      `json:"amount_cents"`
	Status        string     `json:"status"`
	Resolution    string     `json:"resolution"`
	CreatedAt     time.Time  `json:"created_at"`
	VerifiedAt    *time.Time `json:"verified_at"`
}

// listCustomerPaymentProofs 客户查询本人订单的收款核验进度（§4.3）：他人订单返回 403。
func listCustomerPaymentProofs(ctx context.Context, db *ent.Client, customerID, orderID int) ([]customerPaymentProofView, error) {
	if customerID < 1 || orderID < 1 {
		return nil, errInvalidInput
	}
	order, err := db.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.CustomerID != customerID {
		return nil, errForbidden
	}
	items, err := db.EsportsOfflinePaymentProof.Query().Where(
		esportsofflinepaymentproof.OrderIDEQ(orderID),
	).Order(ent.Desc(esportsofflinepaymentproof.FieldID)).Limit(50).All(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]customerPaymentProofView, 0, len(items))
	for _, item := range items {
		views = append(views, customerPaymentProofView{
			ID: item.ID, ProofNo: item.ProofNo, OrderID: item.OrderID,
			Channel: item.Channel.String(), TransactionNo: item.TransactionNo,
			AmountCents: item.AmountCents, Status: item.Status.String(),
			Resolution: item.Resolution, CreatedAt: item.CreatedAt, VerifiedAt: item.VerifiedAt,
		})
	}
	return views, nil
}
