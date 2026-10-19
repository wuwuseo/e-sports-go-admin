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
	// 客户补交口径：唯一键 (channel, transaction_no) 保住「同一笔渠道流水不会被核销两次」，
	// 因此被财务驳回后不新增数据行，而是按流水号找回原凭证单原地上传新截图：
	// 保留凭证单号 / 创建时间 / 提交人归属（驳回原因已清空，但驳回动作本身在管理端审计日志里可回溯）。
	//
	// 到账时间口径（有意为之，勿按「放宽时限」理解）：核销时 applyVerifiedPayment 用 CreatedAt
	// 当作到账时间，补交不重置 CreatedAt，因此「客户在原支付时限内已实际付款、只是截图不清被驳回」
	// 的订单仍可核销；财务核验的是真实到账，客户此时改的是凭证清晰度而不是付款时点。
	// 订单若已被超时任务取消，则本函数前面的订单状态校验会拒绝补交，不会出现「取消后又付款」。
	// 先按唯一键定位目标凭证行：命中已存在行时走「原地补交」（复用被驳回的那条），否则新建一条待核验凭证。
	existing, lookupErr := tx.EsportsOfflinePaymentProof.Query().Where(
		esportsofflinepaymentproof.ChannelEQ(esportsofflinepaymentproof.Channel(channel)),
		esportsofflinepaymentproof.TransactionNoEQ(in.TransactionNo),
	).Only(ctx)
	reusingProofID := 0
	switch {
	case lookupErr == nil:
		if existing.OrderID != order.ID {
			return nil, fmt.Errorf("%w：该渠道流水号已被其它订单占用，请核实后改用正确的转账单号", errConflict)
		}
		if existing.Status != esportsofflinepaymentproof.StatusRejected {
			return nil, fmt.Errorf("%w：该流水号已提交或已核销，请勿重复提交", errConflict)
		}
		// 补交的是同一条凭证行：自身不计入「其它待核验凭证」。
		reusingProofID = existing.ID
	case !ent.IsNotFound(lookupErr):
		return nil, lookupErr
	// 已过支付时限不再接受**新的**转账凭证：否则客户可用一张假凭证让订单无限期停在待付款、
	// 长期占住 SKU 名额（超时任务会因「有待核验凭证」而跳过该单）。
	// 被驳回凭证的补交不受此限（命中已存在行的分支）：它锚定的是首次提交时间，属同一笔已发生的转账。
	case !now.UTC().Before(order.PaymentDeadline):
		return nil, fmt.Errorf("%w：该订单已超过支付时限，无法再提交新的付款凭证，请联系归属客服核实", errConflict)
	}
	// P0-11 重复收款防护：同一订单只允许一条待核验的转账凭证。不同流水号并存时，财务核销一张、
	// 另一张只能挂悬账，订单还存在被两种收款方式各收一次的风险；判定与写入同事务，避免并发穿插。
	if duplicated, guardErr := otherPendingPaymentProofExists(ctx, tx.EsportsOfflinePaymentProof.Query(),
		order.ID, reusingProofID); guardErr != nil {
		return nil, guardErr
	} else if duplicated {
		return nil, fmt.Errorf("%w：该订单已有待核验的转账凭证，请等待财务核验；凭证被驳回后再按驳回结论补交", errConflict)
	}
	var item *ent.EsportsOfflinePaymentProof
	if reusingProofID > 0 {
		item, err = resetRejectedPaymentProof(ctx, tx, existing, intent.ID, account.ID,
			in.AmountCents, string(proofRef), in.Note)
	} else {
		item, err = tx.EsportsOfflinePaymentProof.Create().
			SetProofNo("OP" + uuid.NewString()).
			SetOrderID(order.ID).SetIntentID(intent.ID).SetAccountID(account.ID).
			SetChannel(esportsofflinepaymentproof.Channel(channel)).
			SetTransactionNo(in.TransactionNo).SetAmountCents(in.AmountCents).
			SetProofRef(string(proofRef)).SetNote(in.Note).
			SetStatus(esportsofflinepaymentproof.StatusPendingVerification).Save(ctx)
		if ent.IsConstraintError(err) {
			// 并发补交同一流水号（或凭证单号撞键）：唯一键兜底判冲突，不产生第二条记录。
			return nil, fmt.Errorf("%w：该流水号已提交或凭证单号冲突，请勿重复提交", errConflict)
		}
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}

// resetRejectedPaymentProof 把被驳回的到账凭证原地重置为待核验（§4.3 客户补交同一笔转账）：
// 只刷新支付意图、收款账户、金额与凭证引用，并清空核验结论与核销回填，
// 凭证单号、创建时间与提交人归属（submitted_by_admin_id，客服提交时保留）保持不变；
// 驳回结论本身从凭证行上清空，但驳回动作已进管理端审计日志，可按凭证单号回溯。
// 更新条件带上「驳回态」并校验影响行数，避免并发重复补交绕过财务核验。
func resetRejectedPaymentProof(ctx context.Context, tx *ent.Tx, existing *ent.EsportsOfflinePaymentProof,
	intentID, accountID int, amountCents int64, proofRef, note string) (*ent.EsportsOfflinePaymentProof, error) {
	changed, err := tx.EsportsOfflinePaymentProof.Update().Where(
		esportsofflinepaymentproof.IDEQ(existing.ID),
		esportsofflinepaymentproof.StatusEQ(esportsofflinepaymentproof.StatusRejected),
	).SetIntentID(intentID).SetAccountID(accountID).SetAmountCents(amountCents).
		SetProofRef(proofRef).SetNote(note).
		SetStatus(esportsofflinepaymentproof.StatusPendingVerification).
		SetResolution("").ClearVerifiedByAdminID().ClearVerifiedAt().ClearReceiptID().
		AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, fmt.Errorf("%w：该流水号已提交或已核销，请勿重复提交", errConflict)
	}
	return tx.EsportsOfflinePaymentProof.Get(ctx, existing.ID)
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
