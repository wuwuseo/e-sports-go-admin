package esports

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsconfirmation"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsidentitybinding"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/user"
)

type quoteSnapshot struct {
	OrderID              int       `json:"order_id"`
	OrderNo              string    `json:"order_no"`
	QuoteVersion         int       `json:"quote_version"`
	SkuID                int       `json:"product_id"`
	Quantity             int64     `json:"quantity"`
	UnitPriceCents       int64     `json:"unit_price_cents"`
	DiscountCents        int64     `json:"discount_cents"`
	ManualAdjustCents    int64     `json:"manual_adjust_cents"`
	PayableCents         int64     `json:"payable_cents"`
	PromisedServiceUnits int64     `json:"promised_service_units"`
	RequiredSeats        int       `json:"required_seats"`
	ExpiresAt            time.Time `json:"expires_at"`
	Confirmed            bool      `json:"confirmed"`
}

func confirmedQuote(ctx context.Context, db *ent.Client, orderID, quoteVersion, customerID int) (*ent.EsportsConfirmation, error) {
	confirmation, err := db.EsportsConfirmation.Query().Where(
		esportsconfirmation.SubjectTypeEQ("order_quote"),
		esportsconfirmation.SubjectIDEQ(orderID),
		esportsconfirmation.SubjectVersionEQ(quoteVersion),
		esportsconfirmation.CustomerIDEQ(customerID),
		esportsconfirmation.ChannelEQ("authenticated_self_service"),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	return confirmation, err
}

func customerForVerifiedUser(ctx context.Context, db *ent.Client, uid string) (*ent.EsportsCustomer, error) {
	if uid == "" {
		return nil, errForbidden
	}
	account, err := db.User.Query().Where(user.UIDEQ(uid), user.StatusEQ(user.StatusActive)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, errForbidden
	}
	if err != nil {
		return nil, err
	}
	customer, err := db.EsportsCustomer.Query().Where(esportscustomer.UserIDEQ(account.ID)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, errForbidden
	}
	if err != nil {
		return nil, err
	}
	verified, err := db.EsportsIdentityBinding.Query().Where(
		esportsidentitybinding.CustomerIDEQ(customer.ID),
		esportsidentitybinding.ProviderEQ("user"),
		esportsidentitybinding.SubjectScopeEQ(""),
		esportsidentitybinding.ExternalSubjectEQ(uid),
		esportsidentitybinding.StatusEQ(esportsidentitybinding.StatusActive),
	).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if !verified {
		return nil, errForbidden
	}
	return customer, nil
}

func orderQuote(ctx context.Context, db *ent.Client, userUID string, orderID int) (*quoteSnapshot, error) {
	customer, err := customerForVerifiedUser(ctx, db, userUID)
	if err != nil {
		return nil, err
	}
	order, err := db.EsportsOrder.Query().Where(esportsorder.IDEQ(orderID), esportsorder.CustomerIDEQ(customer.ID)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, errForbidden
	}
	if err != nil {
		return nil, err
	}
	confirmation, err := confirmedQuote(ctx, db, order.ID, order.QuoteVersion, customer.ID)
	if err != nil {
		return nil, err
	}
	return &quoteSnapshot{
		OrderID: order.ID, OrderNo: order.OrderNo, QuoteVersion: order.QuoteVersion,
		SkuID: order.SkuID, Quantity: order.Quantity, UnitPriceCents: order.UnitPriceCents,
		DiscountCents: order.DiscountCents, ManualAdjustCents: order.ManualAdjustCents,
		PayableCents: order.PayableCents, PromisedServiceUnits: order.PromisedServiceUnits,
		RequiredSeats: order.RequiredSeats, ExpiresAt: order.PaymentDeadline,
		Confirmed: confirmation != nil && confirmation.Status == esportsconfirmation.StatusVerified,
	}, nil
}

// confirmQuote records the authenticated customer's explicit consent to the
// complete, versioned price snapshot. A linked but unverified profile cannot
// confirm a staff-created order.
func confirmQuote(ctx context.Context, db *ent.Client, userUID string, orderID, expectedVersion int, accepted bool, now time.Time) (*ent.EsportsConfirmation, error) {
	if orderID < 1 || expectedVersion < 1 || !accepted {
		return nil, errInvalidInput
	}
	customer, err := customerForVerifiedUser(ctx, db, userUID)
	if err != nil {
		return nil, err
	}
	if customer.AgeStatus != esportscustomer.AgeStatusAdult {
		return nil, errAgeUnverified
	}
	// 实名守卫：开关开启且客户未实名时拒绝确认报价（配置默认关闭，行为与改动前一致）。
	if err := ensureRealNameVerified(ctx, db, realnameSubjectTypeCustomer, customer.ID, realNameScopeOrder); err != nil {
		return nil, err
	}
	order, err := db.EsportsOrder.Query().Where(esportsorder.IDEQ(orderID), esportsorder.CustomerIDEQ(customer.ID)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, errForbidden
	}
	if err != nil {
		return nil, err
	}
	if order.QuoteVersion != expectedVersion || order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingPayment ||
		order.PaymentStatus != esportsorder.PaymentStatusUnpaid || !now.UTC().Before(order.PaymentDeadline) {
		return nil, errConflict
	}
	prior, err := confirmedQuote(ctx, db, order.ID, expectedVersion, customer.ID)
	if err != nil {
		return nil, err
	}
	if prior != nil {
		if prior.Status == esportsconfirmation.StatusVerified {
			return prior, nil
		}
		return nil, errConflict
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Recheck mutable fields inside the transaction. The unique confirmation
	// index resolves duplicate requests for the same quote version.
	current, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if err = ensureOrderNotFrozen(current); err != nil {
		return nil, err
	}
	if current.CustomerID != customer.ID || current.Version != order.Version || current.QuoteVersion != expectedVersion ||
		current.FulfillmentStatus != esportsorder.FulfillmentStatusPendingPayment ||
		current.PaymentStatus != esportsorder.PaymentStatusUnpaid || !now.UTC().Before(current.PaymentDeadline) {
		return nil, errConflict
	}
	binding, err := tx.EsportsIdentityBinding.Query().Where(
		esportsidentitybinding.CustomerIDEQ(customer.ID),
		esportsidentitybinding.ProviderEQ("user"),
		esportsidentitybinding.SubjectScopeEQ(""),
		esportsidentitybinding.ExternalSubjectEQ(userUID),
		esportsidentitybinding.StatusEQ(esportsidentitybinding.StatusActive),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, errForbidden
	}
	if err != nil {
		return nil, err
	}
	confirmation, err := tx.EsportsConfirmation.Create().SetCustomerID(customer.ID).
		SetSubjectType("order_quote").SetSubjectID(current.ID).SetSubjectVersion(current.QuoteVersion).
		SetSkuID(current.SkuID).SetQuantity(current.Quantity).SetAmountCents(current.PayableCents).
		SetChannel("authenticated_self_service").SetEvidenceRef("user_binding:" + strconv.Itoa(binding.ID) + ":action:" + uuid.NewString()).
		SetStatus(esportsconfirmation.StatusVerified).SetExpiresAt(current.PaymentDeadline).
		SetVerifiedAt(now.UTC()).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if _, err = tx.EsportsOutbox.Create().SetEventKey("order.quote_confirmed:" + strconv.Itoa(current.ID) + ":" + strconv.Itoa(current.QuoteVersion)).
		SetEventType("order.quote_confirmed").SetAggregateRef(current.OrderNo).
		SetPayload(map[string]interface{}{"order_no": current.OrderNo, "confirmation_id": confirmation.ID}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return confirmation, nil
}

func confirmQuoteRetry(ctx context.Context, db *ent.Client, userUID string, orderID, expectedVersion int, accepted bool, now time.Time) (*ent.EsportsConfirmation, error) {
	result, err := confirmQuote(ctx, db, userUID, orderID, expectedVersion, accepted, now)
	if errors.Is(err, errConflict) && accepted {
		customer, lookupErr := customerForVerifiedUser(ctx, db, userUID)
		if lookupErr == nil {
			order, lookupErr := db.EsportsOrder.Query().Where(
				esportsorder.IDEQ(orderID), esportsorder.CustomerIDEQ(customer.ID),
				esportsorder.QuoteVersionEQ(expectedVersion),
				esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingPayment),
				esportsorder.PaymentStatusEQ(esportsorder.PaymentStatusUnpaid),
			).Only(ctx)
			if lookupErr == nil && now.UTC().Before(order.PaymentDeadline) {
				prior, lookupErr := confirmedQuote(ctx, db, orderID, expectedVersion, customer.ID)
				if lookupErr == nil && prior != nil && prior.Status == esportsconfirmation.StatusVerified {
					return prior, nil
				}
			}
		}
	}
	return result, err
}
