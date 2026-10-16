package esports

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportspaymentintent"
)

func TestCustomerCancelUnpaidOrderReleasesQuotaAndIntent(t *testing.T) {
	db := openAfterSaleDB(t, "esports_customer_cancel_unpaid")
	defer db.Close()
	ctx := t.Context()
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	customer := saveCustomerOrFail(t, db, 11)
	other := saveCustomerOrFail(t, db, 12)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "SELF-CANCEL", Title: "自助取消测试商品", ServiceType: "play",
		UnitPriceCents: 1000, ServiceUnits: 1, ServiceUnitName: "局",
		RequiredSeats: 1, QuotaTotal: 2, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1}, "self-cancel-001", now)
	if err != nil {
		t.Fatal(err)
	}
	intent := db.EsportsPaymentIntent.Create().SetIntentNo("self-cancel-intent").
		SetOrderID(order.ID).SetConfirmationID(1).SetQuoteVersion(order.QuoteVersion).
		SetAmountCents(order.PayableCents).SetChannel("wecom").SetExpiresAt(now.Add(time.Hour)).SaveX(ctx)
	if _, err := cancelCustomerUnpaidOrder(ctx, db, other.ID, order.ID, "越权"); !errors.Is(err, errForbidden) {
		t.Fatalf("其他客户取消未被拦截：%v", err)
	}
	cancelled, err := cancelCustomerUnpaidOrder(ctx, db, customer.ID, order.ID, "客户行程变化")
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.FulfillmentStatus != esportsorder.FulfillmentStatusCancelled ||
		db.EsportsSku.GetX(ctx, sku.ID).QuotaReserved != 0 ||
		db.EsportsPaymentIntent.GetX(ctx, intent.ID).Status != esportspaymentintent.StatusInvalidated {
		t.Fatalf("取消未原子释放名额或支付意图：%+v", cancelled)
	}
	events := db.EsportsOrderEvent.Query().AllX(ctx)
	found := false
	for _, event := range events {
		if event.EventType == "order.cancelled" {
			found = event.ActorType == "customer" && event.ActorID == strconv.Itoa(customer.ID)
		}
	}
	if !found {
		t.Fatalf("客户取消事件操作人错误：%+v", events)
	}
	if _, err := cancelCustomerUnpaidOrder(ctx, db, customer.ID, order.ID, "重复"); !errors.Is(err, errConflict) {
		t.Fatalf("重复取消未拦截：%v", err)
	}
	paid := seedCustomerOrder(t, db, "self-cancel-paid", customer.ID, esportsorder.FulfillmentStatusPendingDispatch)
	if _, err := cancelCustomerUnpaidOrder(ctx, db, customer.ID, paid.ID, "已付款"); !errors.Is(err, errConflict) {
		t.Fatalf("已付款订单不应直接取消：%v", err)
	}
}
