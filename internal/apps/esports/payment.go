package esports

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsconfirmation"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportspaymentintent"
	"go-admin/internal/ent/esportspaymentreceipt"
	"go-admin/internal/ent/esportsquotahold"
	"go-admin/internal/ent/esportssku"
)

// verifiedPayment can only be constructed by a channel adapter after its
// signature and settlement evidence have been checked. No public route calls it.
type verifiedPayment struct {
	IntentNo      string
	Channel       string
	TransactionNo string
	AmountCents   int64
	ConfirmedAt   time.Time
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
	if previous, err := priorReceipt(ctx, db, p); err != nil || previous != nil {
		return previous, err
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	intent, err := tx.EsportsPaymentIntent.Query().Where(esportspaymentintent.IntentNoEQ(p.IntentNo)).Only(ctx)
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
		changed, err := tx.EsportsOrder.Update().Where(
			esportsorder.IDEQ(order.ID), esportsorder.VersionEQ(order.Version),
			esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingPayment),
			esportsorder.PaymentStatusEQ(esportsorder.PaymentStatusUnpaid),
		).SetPaymentStatus(esportsorder.PaymentStatusPaid).
			SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).
			SetPaidAt(p.ConfirmedAt.UTC()).AddVersion(1).Save(ctx)
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, errConflict
		}
		// 进入待派单：若该游戏分区有优先派单权生效名单，则只通知名单内陪玩（§5.4 业务口径）。
		if err = onOrderEnteredPendingDispatch(ctx, tx, order.ID, p.ConfirmedAt.UTC()); err != nil {
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

// PaymentGateway is implemented only for an approved studio merchant account.
// Production checkout never falls back to a fake successful gateway.
type PaymentGateway interface {
	CreatePayment(ctx context.Context, intentNo string, amountCents int64, expiresAt time.Time) (string, error)
	VerifyNotification(ctx context.Context, payload []byte, signature string) (verifiedPayment, error)
}
