package esports

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportspriceadjustment"
)

type priceAdjustmentRequestInput struct {
	ExpectedVersion   int    `json:"expected_version"`
	Quantity          int64  `json:"quantity"`
	ManualAdjustCents int64  `json:"manual_adjust_cents"`
	Reason            string `json:"reason"`
	RequestKey        string `json:"request_key"`
}

func requestPriceAdjustment(ctx context.Context, db *ent.Client, orderID, adminID int, isSuper bool, in priceAdjustmentRequestInput) (*ent.EsportsPriceAdjustment, error) {
	in.Reason = strings.TrimSpace(in.Reason)
	in.RequestKey = strings.TrimSpace(in.RequestKey)
	if orderID < 1 || adminID < 1 || in.ExpectedVersion < 1 ||
		in.Reason == "" || len([]rune(in.Reason)) > 255 || len(in.RequestKey) < 8 || len(in.RequestKey) > 128 {
		return nil, errInvalidInput
	}
	if _, err := loadOwnedOrder(ctx, db, adminID, isSuper, orderID); err != nil {
		return nil, err
	}
	prior, err := db.EsportsPriceAdjustment.Query().Where(
		esportspriceadjustment.RequestedByAdminIDEQ(adminID), esportspriceadjustment.RequestKeyEQ(in.RequestKey),
	).Only(ctx)
	if err == nil {
		if prior.OrderID == orderID && prior.OrderVersion == in.ExpectedVersion && prior.Quantity == in.Quantity &&
			prior.ManualAdjustCents == in.ManualAdjustCents && prior.Reason == in.Reason {
			return prior, nil
		}
		return nil, errConflict
	}
	if !ent.IsNotFound(err) {
		return nil, err
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	if (!isSuper && (order.ResponsibleAdminID == nil || *order.ResponsibleAdminID != adminID)) ||
		order.Version != in.ExpectedVersion || order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingPayment ||
		order.PaymentStatus != esportsorder.PaymentStatusUnpaid {
		return nil, errConflict
	}
	quote, err := calculateRepriceQuote(ctx, tx.Client(), order, in.Quantity, in.ManualAdjustCents)
	if err != nil {
		return nil, err
	}
	if !quote.RequiresApproval {
		return nil, errInvalidInput
	}
	item, err := tx.EsportsPriceAdjustment.Create().SetOrderID(orderID).SetOrderVersion(order.Version).
		SetQuantity(in.Quantity).SetManualAdjustCents(in.ManualAdjustCents).SetPayableCents(quote.PayableCents).
		SetRequestedByAdminID(adminID).SetRequestKey(in.RequestKey).SetReason(in.Reason).Save(ctx)
	if ent.IsConstraintError(err) {
		return nil, errConflict
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOutbox.Create().SetEventKey(fmt.Sprintf("order.price_adjustment_requested:%d", item.ID)).
		SetEventType("order.price_adjustment_requested").SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{"order_id": order.ID, "adjustment_id": item.ID, "payable_cents": quote.PayableCents}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsPriceAdjustment.Get(ctx, item.ID)
}

func listPriceAdjustments(ctx context.Context, db *ent.Client, adminID int, isSuper bool, status string) ([]*ent.EsportsPriceAdjustment, error) {
	if adminID < 1 {
		return nil, errForbidden
	}
	q := db.EsportsPriceAdjustment.Query().Order(ent.Desc(esportspriceadjustment.FieldID)).Limit(200)
	if !isSuper {
		q.Where(esportspriceadjustment.RequestedByAdminIDEQ(adminID))
	}
	switch status {
	case "":
	case "pending", "approved", "rejected", "consumed":
		q.Where(esportspriceadjustment.StatusEQ(esportspriceadjustment.Status(status)))
	default:
		return nil, errInvalidInput
	}
	return q.All(ctx)
}

func decidePriceAdjustment(ctx context.Context, db *ent.Client, id, reviewerID int, isSuper, approve bool, note string, now time.Time) (*ent.EsportsPriceAdjustment, error) {
	note = strings.TrimSpace(note)
	if id < 1 || reviewerID < 1 || note == "" || len([]rune(note)) > 255 {
		return nil, errInvalidInput
	}
	if !isSuper {
		return nil, errForbidden
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	item, err := tx.EsportsPriceAdjustment.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.RequestedByAdminID == reviewerID {
		return nil, errForbidden
	}
	if item.Status != esportspriceadjustment.StatusPending {
		return nil, errConflict
	}
	order, err := tx.EsportsOrder.Get(ctx, item.OrderID)
	if err != nil {
		return nil, err
	}
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	if order.Version != item.OrderVersion || order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingPayment ||
		order.PaymentStatus != esportsorder.PaymentStatusUnpaid {
		return nil, errConflict
	}
	if approve {
		quote, err := calculateRepriceQuote(ctx, tx.Client(), order, item.Quantity, item.ManualAdjustCents)
		if err != nil || !quote.RequiresApproval || quote.PayableCents != item.PayableCents {
			return nil, errConflict
		}
	}
	status := esportspriceadjustment.StatusRejected
	if approve {
		status = esportspriceadjustment.StatusApproved
	}
	changed, err := tx.EsportsPriceAdjustment.Update().Where(esportspriceadjustment.IDEQ(id),
		esportspriceadjustment.StatusEQ(esportspriceadjustment.StatusPending)).
		SetStatus(status).SetReviewedByAdminID(reviewerID).SetReviewNote(note).SetReviewedAt(now.UTC()).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if _, err = tx.EsportsOutbox.Create().SetEventKey(fmt.Sprintf("order.price_adjustment_decided:%d", id)).
		SetEventType("order.price_adjustment_decided").SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{"order_id": order.ID, "adjustment_id": id, "approved": approve}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsPriceAdjustment.Get(ctx, id)
}
