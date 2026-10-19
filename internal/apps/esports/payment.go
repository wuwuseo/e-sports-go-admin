package esports

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/esportsconfirmation"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportspaymentintent"
	"go-admin/internal/ent/esportspaymentreceipt"
	"go-admin/internal/ent/esportsquotahold"
	"go-admin/internal/ent/esportssku"
)

func externalPaymentChannel(channel string) (esportsorder.PaymentChannel, bool) {
	switch channel {
	case string(esportsorder.PaymentChannelWechatWork):
		return esportsorder.PaymentChannelWechatWork, true
	case string(esportsorder.PaymentChannelMerchantQr):
		return esportsorder.PaymentChannelMerchantQr, true
	case string(esportsorder.PaymentChannelCorporateTransfer):
		return esportsorder.PaymentChannelCorporateTransfer, true
	default:
		return esportsorder.PaymentChannelNone, false
	}
}

// verifiedPayment can only be constructed by a channel adapter after its
// signature and settlement evidence have been checked. No public route calls it.
type verifiedPayment struct {
	IntentNo      string
	Channel       string
	TransactionNo string
	AmountCents   int64
	ConfirmedAt   time.Time
	// AppliedAt 是「核销动作发生时刻」（财务核验 / 渠道回调处理时刻），用于派单优先通知窗口、
	// 入池时间等运营时点；账务时点（paid_at、收据、提成规则归属）仍用 ConfirmedAt（真实到账时刻）。
	// 零值表示与 ConfirmedAt 相同（保持既有调用方与测试口径）。
	AppliedAt time.Time
}

type paymentOutcome struct {
	ReceiptID       int
	AppliedCents    int64
	ExceptionCents  int64
	OrderDispatched bool
}

func priorReceipt(ctx context.Context, db *ent.Client, p verifiedPayment) (*paymentOutcome, error) {
	intent, err := db.EsportsPaymentIntent.Query().Where(esportspaymentintent.IntentNoEQ(p.IntentNo)).Only(ctx)
	if err != nil {
		return nil, err
	}
	receipt, err := db.EsportsPaymentReceipt.Query().Where(
		esportspaymentreceipt.ChannelEQ(p.Channel),
		esportspaymentreceipt.TransactionNoEQ(p.TransactionNo),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if receipt.IntentID != intent.ID || receipt.ActualCents != p.AmountCents {
		return nil, errConflict
	}
	return &paymentOutcome{
		ReceiptID: receipt.ID, AppliedCents: receipt.AppliedCents,
		ExceptionCents: receipt.ExceptionCents, OrderDispatched: receipt.AppliedCents > 0,
	}, nil
}

// applyVerifiedPayment is the single persistence boundary for a real channel
// receipt. The quote version and order version arbitrate payment versus expiry.
func applyVerifiedPayment(ctx context.Context, db *ent.Client, p verifiedPayment) (*paymentOutcome, error) {
	result, err := applyVerifiedPaymentOnce(ctx, db, p)
	if errors.Is(err, errConflict) {
		prior, lookupErr := priorReceipt(ctx, db, p)
		if lookupErr == nil && prior != nil {
			return prior, nil
		}
	}
	return result, err
}

func applyVerifiedPaymentOnce(ctx context.Context, db *ent.Client, p verifiedPayment) (*paymentOutcome, error) {
	p.IntentNo = strings.TrimSpace(p.IntentNo)
	p.Channel = strings.TrimSpace(p.Channel)
	p.TransactionNo = strings.TrimSpace(p.TransactionNo)
	if p.IntentNo == "" || p.Channel == "" || p.TransactionNo == "" ||
		p.AmountCents <= 0 || p.ConfirmedAt.IsZero() {
		return nil, errInvalidInput
	}
	// §4.3-03：支付意图按所属单据类型分流——订单收款 / 客户充值 / 客户自助购卡。
	// 充值意图与购卡意图的 order_id 是保留段（非真实订单），必须走各自核销入口，
	// 否则 tx.EsportsOrder.Get 必然查不到而导致到账无法入账。
	intent, err := db.EsportsPaymentIntent.Query().Where(
		esportspaymentintent.IntentNoEQ(p.IntentNo)).Only(ctx)
	if err != nil {
		return nil, err
	}
	if intent.Channel != p.Channel {
		return nil, errInvalidInput
	}
	switch paymentDocumentKindOf(intent.OrderID) {
	case paymentDocumentRecharge:
		return settleChannelRecharge(ctx, db, p, intent)
	case paymentDocumentStoredValue:
		return settleChannelStoredValue(ctx, db, p, intent)
	}
	// §7 / 18-02：支付时限未配置时不得核销（明确失败，不返回伪成功）。
	if err := ensurePaymentDeadlineConfigured(ctx, db); err != nil {
		return nil, err
	}
	if previous, err := priorReceipt(ctx, db, p); err != nil || previous != nil {
		return previous, err
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	intent, err = tx.EsportsPaymentIntent.Query().Where(esportspaymentintent.IntentNoEQ(p.IntentNo)).Only(ctx)
	if err != nil {
		return nil, err
	}
	if intent.Channel != p.Channel {
		return nil, errInvalidInput
	}
	order, err := tx.EsportsOrder.Get(ctx, intent.OrderID)
	if err != nil {
		return nil, err
	}
	customer, err := tx.EsportsCustomer.Get(ctx, order.CustomerID)
	if err != nil {
		return nil, err
	}
	sku, err := tx.EsportsSku.Get(ctx, order.SkuID)
	if err != nil {
		return nil, err
	}
	confirmation, err := tx.EsportsConfirmation.Get(ctx, intent.ConfirmationID)
	if ent.IsNotFound(err) {
		confirmation = nil
	} else if err != nil {
		return nil, err
	}
	payable := order.PayableCents
	canApply := customer.AgeStatus == esportscustomer.AgeStatusAdult &&
		sku.Status == esportssku.StatusOnSale &&
		confirmation != nil && confirmation.Status == esportsconfirmation.StatusVerified &&
		confirmation.SubjectType == "order_quote" && confirmation.SubjectID == order.ID &&
		confirmation.SubjectVersion == order.QuoteVersion && confirmation.CustomerID == order.CustomerID &&
		confirmation.SkuID == order.SkuID && confirmation.Quantity == order.Quantity &&
		confirmation.AmountCents == payable && confirmation.VerifiedAt != nil &&
		!p.ConfirmedAt.Before(*confirmation.VerifiedAt) && !p.ConfirmedAt.After(confirmation.ExpiresAt) &&
		intent.Status == esportspaymentintent.StatusPending &&
		intent.QuoteVersion == order.QuoteVersion && intent.AmountCents == payable &&
		!p.ConfirmedAt.After(intent.ExpiresAt) && !p.ConfirmedAt.After(order.PaymentDeadline) &&
		order.FulfillmentStatus == esportsorder.FulfillmentStatusPendingPayment &&
		order.PaymentStatus == esportsorder.PaymentStatusUnpaid && p.AmountCents >= payable
	result := &paymentOutcome{ExceptionCents: p.AmountCents}
	if canApply {
		update := tx.EsportsOrder.Update().Where(
			esportsorder.IDEQ(order.ID), esportsorder.VersionEQ(order.Version),
			esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingPayment),
			esportsorder.PaymentStatusEQ(esportsorder.PaymentStatusUnpaid),
		).SetPaymentStatus(esportsorder.PaymentStatusPaid).
			SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).
			SetPaidAt(p.ConfirmedAt.UTC()).AddVersion(1)
		// 外部到账核销必须回写订单收款方式，供渠道对账/原路退款识别；未知渠道保持 none。
		if channel, ok := externalPaymentChannel(p.Channel); ok {
			update.SetPaymentChannel(channel)
		}
		if err := lockCsCommissionOnPayment(ctx, tx, order, update, p.ConfirmedAt); err != nil {
			return nil, err
		}
		changed, err := update.Save(ctx)
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, errConflict
		}
		// 进入待派单：若该游戏分区有优先派单权生效名单，则只通知名单内陪玩（§5.4 业务口径）。
		// 窗口与「当前可接单」判定必须用核销动作时刻：晚核验的订单若用真实到账时刻，
		// 优先通知窗口会在开窗瞬间就已过期，名单内陪玩收不到通知。
		appliedAt := p.AppliedAt
		if appliedAt.IsZero() {
			appliedAt = p.ConfirmedAt
		}
		if err = onOrderEnteredPendingDispatch(ctx, tx, order.ID, appliedAt.UTC()); err != nil {
			return nil, err
		}
		hold, err := tx.EsportsQuotaHold.Query().Where(esportsquotahold.OrderIDEQ(order.ID)).Only(ctx)
		if err != nil {
			return nil, err
		}
		if hold.Status != esportsquotahold.StatusReserved {
			return nil, errConflict
		}
		changed, err = tx.EsportsQuotaHold.Update().Where(
			esportsquotahold.IDEQ(hold.ID), esportsquotahold.StatusEQ(esportsquotahold.StatusReserved),
		).SetStatus(esportsquotahold.StatusSold).Save(ctx)
		if err != nil || changed != 1 {
			return nil, errConflict
		}
		changed, err = tx.EsportsSku.Update().Where(
			esportssku.IDEQ(hold.SkuID), esportssku.QuotaReservedGTE(hold.Quantity),
		).AddQuotaReserved(-hold.Quantity).AddQuotaSold(hold.Quantity).AddVersion(1).Save(ctx)
		if err != nil || changed != 1 {
			return nil, errConflict
		}
		changed, err = tx.EsportsPaymentIntent.Update().Where(
			esportspaymentintent.IDEQ(intent.ID),
			esportspaymentintent.StatusEQ(esportspaymentintent.StatusPending),
		).SetStatus(esportspaymentintent.StatusPaid).Save(ctx)
		if err != nil || changed != 1 {
			return nil, errConflict
		}
		result.AppliedCents = payable
		result.ExceptionCents = p.AmountCents - payable
		result.OrderDispatched = true
	}
	status := esportspaymentreceipt.StatusException
	if result.AppliedCents > 0 {
		status = esportspaymentreceipt.StatusVerified
	}
	receipt, err := tx.EsportsPaymentReceipt.Create().SetIntentID(intent.ID).
		SetChannel(p.Channel).SetTransactionNo(p.TransactionNo).
		SetActualCents(p.AmountCents).SetAppliedCents(result.AppliedCents).
		SetExceptionCents(result.ExceptionCents).SetStatus(status).
		SetConfirmedAt(p.ConfirmedAt.UTC()).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	result.ReceiptID = receipt.ID
	ref := strconv.Itoa(receipt.ID)
	if _, err = tx.EsportsLedgerEntry.Create().SetAccountType("studio_external_cash").
		SetAccountID("studio").SetSourceType("payment_receipt").SetSourceID(ref).
		SetEntryRole("confirmed_cash").SetDeltaCents(p.AmountCents).Save(ctx); err != nil {
		return nil, err
	}
	if result.AppliedCents > 0 {
		if _, err = tx.EsportsLedgerEntry.Create().SetAccountType("customer_order_prepayment").
			SetAccountID(strconv.Itoa(order.CustomerID)).SetSourceType("payment_receipt").SetSourceID(ref).
			SetEntryRole("applied_order_prepayment").SetDeltaCents(result.AppliedCents).Save(ctx); err != nil {
			return nil, err
		}
	}
	if result.ExceptionCents > 0 {
		if _, err = tx.EsportsLedgerEntry.Create().SetAccountType("suspense_liability").
			SetAccountID("studio").SetSourceType("payment_receipt").SetSourceID(ref).
			SetEntryRole("unapplied_amount").SetDeltaCents(result.ExceptionCents).Save(ctx); err != nil {
			return nil, err
		}
	}
	if result.AppliedCents > 0 {
		if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
			SetOrderVersion(order.Version + 1).SetEventType("order.payment_verified").
			SetActorType("payment_channel").SetActorID(p.Channel).
			SetDetails(map[string]interface{}{"receipt_id": receipt.ID, "applied_cents": payable}).Save(ctx); err != nil {
			return nil, err
		}
	}
	eventType := "payment.receipt_exception"
	if result.AppliedCents > 0 {
		eventType = "payment.receipt_applied"
	}
	if _, err = tx.EsportsOutbox.Create().SetEventKey("payment.receipt:" + p.Channel + ":" + p.TransactionNo).
		SetEventType(eventType).SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{"receipt_id": receipt.ID, "order_no": order.OrderNo}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

var errPaymentGatewayUnavailable = errors.New("收款通道尚未配置")

// paymentDocumentKind 是支付意图所属单据类型（§4.3-03）：订单 / 客户充值 / 客户自助购卡。
// 三类意图共用 EsportsPaymentIntent 表，用 order_id 的保留段区分。
type paymentDocumentKind int

const (
	paymentDocumentOrder paymentDocumentKind = iota
	paymentDocumentRecharge
	paymentDocumentStoredValue
)

// paymentDocumentKindOf 按 order_id 所属保留段判定单据类型（真实订单 ID 不落在保留段内）。
func paymentDocumentKindOf(orderID int) paymentDocumentKind {
	if rechargeIntentCustomerID(orderID) > 0 {
		return paymentDocumentRecharge
	}
	if customerStoredValueIntentCardID(orderID) > 0 {
		return paymentDocumentStoredValue
	}
	return paymentDocumentOrder
}

// settleChannelRecharge 已验签回调核销客户充值意图（§4.3-03）：转 settleRechargeIntent，
// 渠道回调没有管理员操作人（OperatorAdminID=0），到账依据是渠道流水号。
func settleChannelRecharge(ctx context.Context, db *ent.Client, p verifiedPayment,
	intent *ent.EsportsPaymentIntent) (*paymentOutcome, error) {
	if _, err := settleRechargeIntent(ctx, db, rechargeSettlementInput{
		IntentNo:      p.IntentNo,
		Channel:       p.Channel,
		TransactionNo: p.TransactionNo,
		ReceivedCents: p.AmountCents,
		ConfirmedAt:   p.ConfirmedAt.UTC(),
		EvidenceNote: fmt.Sprintf("已验签渠道回调自动核销：%s 流水 %s（支付意图 %s）",
			p.Channel, p.TransactionNo, intent.IntentNo),
	}, p.ConfirmedAt.UTC()); err != nil {
		return nil, err
	}
	return channelReceiptOutcome(ctx, db, p)
}

// settleChannelStoredValue 已验签回调核销客户自助购卡意图（§4.3-03）：转 settleCustomerStoredValuePurchase。
// 购卡核验边界要求非空记账管理员（开卡申请人不得自审），渠道回调没有登录身份，
// 因此取「客户归属客服 → 任一启用中管理员」作为记账人；都取不到时明确失败，不用 0 冒充。
func settleChannelStoredValue(ctx context.Context, db *ent.Client, p verifiedPayment,
	intent *ent.EsportsPaymentIntent) (*paymentOutcome, error) {
	card, err := db.EsportsStoredValueCard.Get(ctx, customerStoredValueIntentCardID(intent.OrderID))
	if err != nil {
		return nil, err
	}
	operatorID, err := channelSettlementOperatorAdminID(ctx, db, card.CustomerID)
	if err != nil {
		return nil, err
	}
	if _, err = settleCustomerStoredValuePurchase(ctx, db, customerStoredValueSettlementInput{
		IntentNo:        p.IntentNo,
		Channel:         p.Channel,
		TransactionNo:   p.TransactionNo,
		ReceivedCents:   p.AmountCents,
		ConfirmedAt:     p.ConfirmedAt.UTC(),
		OperatorAdminID: operatorID,
		EvidenceNote: fmt.Sprintf("已验签渠道回调自动核销：%s 流水 %s（支付意图 %s）",
			p.Channel, p.TransactionNo, intent.IntentNo),
	}, p.ConfirmedAt.UTC()); err != nil {
		return nil, err
	}
	return channelReceiptOutcome(ctx, db, p)
}

// channelSettlementOperatorAdminID 渠道自动回调的记账管理员（§4.3-03）：
// 优先客户归属客服，其次任一启用中管理员；取不到时明确报错（不假成功、不用 0 占位）。
func channelSettlementOperatorAdminID(ctx context.Context, db *ent.Client, customerID int) (int, error) {
	if customerID > 0 {
		customer, err := db.EsportsCustomer.Get(ctx, customerID)
		if err != nil {
			return 0, err
		}
		if customer.OwnerAdminID != nil && *customer.OwnerAdminID > 0 {
			return *customer.OwnerAdminID, nil
		}
	}
	operator, err := db.Admin.Query().Where(admin.StatusEQ("active")).
		Order(ent.Asc(admin.FieldID)).First(ctx)
	if err != nil {
		return 0, fmt.Errorf("%w：渠道回调核销缺少可用的记账管理员，请先配置归属客服",
			errPaymentGatewayUnavailable)
	}
	return operator.ID, nil
}

// channelReceiptOutcome 按 (channel, transaction_no) 读回核销回执，构造与订单核销同口径的 outcome。
func channelReceiptOutcome(ctx context.Context, db *ent.Client, p verifiedPayment) (*paymentOutcome, error) {
	receipt, err := db.EsportsPaymentReceipt.Query().Where(
		esportspaymentreceipt.ChannelEQ(p.Channel),
		esportspaymentreceipt.TransactionNoEQ(p.TransactionNo),
	).Only(ctx)
	if err != nil {
		return nil, err
	}
	return &paymentOutcome{
		ReceiptID: receipt.ID, AppliedCents: receipt.AppliedCents,
		ExceptionCents: receipt.ExceptionCents, OrderDispatched: receipt.AppliedCents > 0,
	}, nil
}

// PaymentGateway is implemented only for an approved studio merchant account.
// Production checkout never falls back to a fake successful gateway.
//
// CreatePayment 返回渠道侧收银台地址（pay_url）；该地址由渠道按 intent_no 可重取，
// 因此本系统不做本地持久化。VerifyNotification 的 signature 入参是回调签名头的原值：
// 既接受渠道直接发来的 "<t=unix>,<v1=hex>" 复合串，也接受由 fiber handler 从
// X-Pay-Timestamp / X-Pay-Signature 两个头拼装出的同格式串。
type PaymentGateway interface {
	CreatePayment(ctx context.Context, intentNo string, amountCents int64, expiresAt time.Time, subject string) (string, error)
	VerifyNotification(ctx context.Context, payload []byte, signature string) (verifiedPayment, error)
}
