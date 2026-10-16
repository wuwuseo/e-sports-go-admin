package esports

import (
	"context"
	"strconv"
	"strings"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportspaymentintent"
)

// cancelCustomerUnpaidOrder 仅允许客户本人取消尚未付款的订单；名额、支付意图与状态在同一事务内处理。
func cancelCustomerUnpaidOrder(ctx context.Context, db *ent.Client, customerID, orderID int, reason string) (*ent.EsportsOrder, error) {
	reason = strings.TrimSpace(reason)
	if customerID < 1 || orderID < 1 || len([]rune(reason)) > 255 {
		return nil, errInvalidInput
	}
	if reason == "" {
		reason = "客户自行取消"
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Query().Where(
		esportsorder.IDEQ(orderID), esportsorder.CustomerIDEQ(customerID),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, errForbidden
	}
	if err != nil {
		return nil, err
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingPayment ||
		order.PaymentStatus != esportsorder.PaymentStatusUnpaid {
		return nil, errConflict
	}
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(orderID), esportsorder.CustomerIDEQ(customerID),
		esportsorder.VersionEQ(order.Version),
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingPayment),
		esportsorder.PaymentStatusEQ(esportsorder.PaymentStatusUnpaid),
	).SetFulfillmentStatus(esportsorder.FulfillmentStatusCancelled).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if err := releaseReservedQuota(ctx, tx, order.ID); err != nil {
		return nil, err
	}
	if _, err := tx.EsportsPaymentIntent.Update().Where(
		esportspaymentintent.OrderIDEQ(order.ID),
		esportspaymentintent.StatusEQ(esportspaymentintent.StatusPending),
	).SetStatus(esportspaymentintent.StatusInvalidated).Save(ctx); err != nil {
		return nil, err
	}
	if _, err := tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(order.Version + 1).SetEventType("order.cancelled").
		SetActorType("customer").SetActorID(strconv.Itoa(customerID)).
		SetDetails(map[string]interface{}{"reason": reason}).Save(ctx); err != nil {
		return nil, err
	}
	if _, err := tx.EsportsOutbox.Create().SetEventKey("order.cancelled:" + order.OrderNo).
		SetEventType("order.cancelled").SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{"order_no": order.OrderNo}).Save(ctx); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}
