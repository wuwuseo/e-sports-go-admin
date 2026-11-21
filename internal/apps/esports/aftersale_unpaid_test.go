package esports

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsorder"
)

// 未付款订单不允许客户自助发起售后：订单还没产生资金往来，退款 / 赔付都无从谈起。
// 同一条已付款订单仍应放行，避免把门槛做过。
func TestCustomerAfterSaleRejectsUnpaidOrder(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_unpaid")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	customer := saveCustomerOrFail(t, db, 11)

	unpaid := seedCustomerOrder(t, db, "AU-UNPAID-1", customer.ID,
		esportsorder.FulfillmentStatusPendingDispatch)
	if err := db.EsportsOrder.UpdateOneID(unpaid.ID).
		SetPaymentStatus(esportsorder.PaymentStatusUnpaid).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := createCustomerAfterSale(ctx, db, customer, afterSaleInput{
		SourceType: "order", SourceID: unpaid.ID, Kind: "refund_request",
		Demand: "未付款订单也想售后",
	}, now)
	if !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "还未付款") {
		t.Fatalf("未付款订单应拒绝售后，got %v", err)
	}

	paid := seedCustomerOrder(t, db, "AU-PAID-1", customer.ID,
		esportsorder.FulfillmentStatusPendingDispatch)
	if _, err := createCustomerAfterSale(ctx, db, customer, afterSaleInput{
		SourceType: "order", SourceID: paid.ID, Kind: "refund_request",
		Demand: "已付款订单售后",
	}, now); err != nil {
		t.Fatalf("已付款订单应允许售后：%v", err)
	}
}
