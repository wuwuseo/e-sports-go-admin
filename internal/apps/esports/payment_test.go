package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
)

func TestVerifiedReceiptAppliesOnceAndSeparatesOverpayment(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_payment?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	customer := saveCustomerOrFail(t, db, 11)
	db.EsportsCustomer.UpdateOneID(customer.ID).SetAgeStatus("adult").ExecX(ctx)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "PAY-SKU", Title: "陪玩一小时", ServiceType: "play", UnitPriceCents: 5000,
		ServiceUnits: 1, ServiceUnitName: "小时", RequiredSeats: 1, QuotaTotal: 2, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	first, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1}, "payment-order-001", now)
	if err != nil {
		t.Fatal(err)
	}
	firstConfirmation := db.EsportsConfirmation.Create().SetCustomerID(customer.ID).
		SetSubjectType("order_quote").SetSubjectID(first.ID).SetSubjectVersion(first.QuoteVersion).
		SetSkuID(sku.ID).SetQuantity(first.Quantity).SetAmountCents(first.PayableCents).
		SetChannel("verified_customer_message").SetEvidenceRef("test-evidence-001").
		SetStatus("verified").SetVerifiedAt(now).SetExpiresAt(first.PaymentDeadline).SaveX(ctx)
	firstIntent := db.EsportsPaymentIntent.Create().SetIntentNo("intent-001").SetOrderID(first.ID).
		SetConfirmationID(firstConfirmation.ID).
		SetQuoteVersion(first.QuoteVersion).SetAmountCents(first.PayableCents).
		SetChannel("approved_test_channel").SetExpiresAt(first.PaymentDeadline).SaveX(ctx)
	notice := verifiedPayment{
		IntentNo: firstIntent.IntentNo, Channel: firstIntent.Channel,
		TransactionNo: "provider-transaction-001", AmountCents: 5200,
		ConfirmedAt: now.Add(time.Minute),
	}
	result, err := applyVerifiedPayment(ctx, db, notice)
	if err != nil || result.AppliedCents != 5000 || result.ExceptionCents != 200 || !result.OrderDispatched {
		t.Fatalf("overpayment split: %+v, %v", result, err)
	}
	again, err := applyVerifiedPayment(ctx, db, notice)
	if err != nil || again.ReceiptID != result.ReceiptID || db.EsportsPaymentReceipt.Query().CountX(ctx) != 1 {
		t.Fatalf("duplicate receipt was not idempotent: %+v, %v", again, err)
	}
	if _, err := applyVerifiedPayment(ctx, db, verifiedPayment{
		IntentNo: firstIntent.IntentNo, Channel: firstIntent.Channel,
		TransactionNo: notice.TransactionNo, AmountCents: 5100, ConfirmedAt: notice.ConfirmedAt,
	}); !errors.Is(err, errConflict) {
		t.Fatalf("same transaction with different amount: %v", err)
	}
	paid := db.EsportsOrder.GetX(ctx, first.ID)
	if paid.CsCommissionRuleLockedAt == nil || paid.CsCommissionAdminID == nil ||
		*paid.CsCommissionAdminID != 11 || paid.CsCommissionRuleID != nil {
		t.Fatalf("外部付款未锁定当时客服提成口径：%+v", paid)
	}
	stock := db.EsportsSku.GetX(ctx, sku.ID)
	if paid.PaymentStatus != "paid" || paid.FulfillmentStatus != "pending_dispatch" ||
		stock.QuotaReserved != 0 || stock.QuotaSold != 1 || db.EsportsLedgerEntry.Query().CountX(ctx) != 3 {
		t.Fatalf("payment did not update order, quota and ledgers together: %+v %+v", paid, stock)
	}
	ledgerID := db.EsportsLedgerEntry.Query().FirstIDX(ctx)
	if err := db.EsportsLedgerEntry.UpdateOneID(ledgerID).SetDeltaCents(1).Exec(ctx); err == nil {
		t.Fatal("append-only ledger allowed mutation")
	}
	if err := db.EsportsLedgerEntry.DeleteOneID(ledgerID).Exec(ctx); err == nil {
		t.Fatal("append-only ledger allowed deletion")
	}

	second, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1}, "payment-order-002", now)
	if err != nil {
		t.Fatal(err)
	}
	secondConfirmation := db.EsportsConfirmation.Create().SetCustomerID(customer.ID).
		SetSubjectType("order_quote").SetSubjectID(second.ID).SetSubjectVersion(second.QuoteVersion).
		SetSkuID(sku.ID).SetQuantity(second.Quantity).SetAmountCents(second.PayableCents).
		SetChannel("verified_customer_message").SetEvidenceRef("test-evidence-002").
		SetStatus("verified").SetVerifiedAt(now).SetExpiresAt(second.PaymentDeadline).SaveX(ctx)
	secondIntent := db.EsportsPaymentIntent.Create().SetIntentNo("intent-002").SetOrderID(second.ID).
		SetConfirmationID(secondConfirmation.ID).
		SetQuoteVersion(second.QuoteVersion).SetAmountCents(second.PayableCents).
		SetChannel("approved_test_channel").SetExpiresAt(second.PaymentDeadline).SaveX(ctx)
	if expired, err := expireOrder(ctx, db, second.ID, now.Add(16*time.Minute)); err != nil || !expired {
		t.Fatalf("order expiration: %v, %v", expired, err)
	}
	late, err := applyVerifiedPayment(ctx, db, verifiedPayment{
		IntentNo: secondIntent.IntentNo, Channel: secondIntent.Channel,
		TransactionNo: "provider-transaction-002", AmountCents: 5000,
		ConfirmedAt: now.Add(17 * time.Minute),
	})
	if err != nil || late.AppliedCents != 0 || late.ExceptionCents != 5000 || late.OrderDispatched {
		t.Fatalf("late payment revived cancelled order: %+v, %v", late, err)
	}
	if db.EsportsOrder.GetX(ctx, second.ID).FulfillmentStatus != "cancelled" ||
		db.EsportsSku.GetX(ctx, sku.ID).QuotaSold != 1 {
		t.Fatal("late payment changed fulfillment or sold quantity")
	}
	third, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1}, "payment-order-003", now)
	if err != nil {
		t.Fatal(err)
	}
	thirdConfirmation := db.EsportsConfirmation.Create().SetCustomerID(customer.ID).
		SetSubjectType("order_quote").SetSubjectID(third.ID).SetSubjectVersion(third.QuoteVersion).
		SetSkuID(sku.ID).SetQuantity(third.Quantity).SetAmountCents(third.PayableCents).
		SetChannel("verified_customer_message").SetEvidenceRef("test-evidence-003").
		SetExpiresAt(third.PaymentDeadline).SaveX(ctx)
	thirdIntent := db.EsportsPaymentIntent.Create().SetIntentNo("intent-003").SetOrderID(third.ID).
		SetConfirmationID(thirdConfirmation.ID).SetQuoteVersion(third.QuoteVersion).
		SetAmountCents(third.PayableCents).SetChannel("approved_test_channel").
		SetExpiresAt(third.PaymentDeadline).SaveX(ctx)
	unconfirmed, err := applyVerifiedPayment(ctx, db, verifiedPayment{
		IntentNo: thirdIntent.IntentNo, Channel: thirdIntent.Channel,
		TransactionNo: "provider-transaction-003", AmountCents: 5000,
		ConfirmedAt: now.Add(time.Minute),
	})
	if err != nil || unconfirmed.AppliedCents != 0 || unconfirmed.ExceptionCents != 5000 {
		t.Fatalf("unconfirmed quote payment was applied: %+v %v", unconfirmed, err)
	}
	db.EsportsConfirmation.UpdateOneID(thirdConfirmation.ID).SetStatus("verified").SetVerifiedAt(now).ExecX(ctx)
	db.EsportsSku.UpdateOneID(sku.ID).SetStatus("off_sale").ExecX(ctx)
	offSale, err := applyVerifiedPayment(ctx, db, verifiedPayment{
		IntentNo: thirdIntent.IntentNo, Channel: thirdIntent.Channel,
		TransactionNo: "provider-transaction-004", AmountCents: 5000,
		ConfirmedAt: now.Add(2 * time.Minute),
	})
	if err != nil || offSale.AppliedCents != 0 || offSale.ExceptionCents != 5000 ||
		db.EsportsOrder.GetX(ctx, third.ID).PaymentStatus != "unpaid" {
		t.Fatalf("off-sale SKU payment was applied: %+v %v", offSale, err)
	}
}
