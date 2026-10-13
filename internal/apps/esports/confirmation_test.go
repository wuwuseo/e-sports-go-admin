package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
)

func TestCustomerQuoteConfirmationRequiresVerifiedIdentityAndCurrentVersion(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_quote_confirmation?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	account := db.User.Create().SetEmail("customer@example.com").SetUID("verified-user-001").
		SetStatus("active").SaveX(ctx)
	other := db.User.Create().SetEmail("other@example.com").SetUID("other-user-002").
		SetStatus("active").SaveX(ctx)
	_ = other
	customer := saveCustomerOrFail(t, db, 11)
	db.EsportsCustomer.UpdateOneID(customer.ID).SetUserID(account.ID).SetAgeStatus("adult").ExecX(ctx)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "CONFIRM-SKU", Title: "陪玩一小时", ServiceType: "play", UnitPriceCents: 5000,
		ServiceUnits: 1, ServiceUnitName: "小时", RequiredSeats: 1, QuotaTotal: 2, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1}, "confirm-order-001", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := orderQuote(ctx, db, "verified-user-001", order.ID); !errors.Is(err, errForbidden) {
		t.Fatalf("unverified account read customer quote: %v", err)
	}
	db.EsportsIdentityBinding.Create().SetCustomerID(customer.ID).SetProvider("user").
		SetSubjectScope("").SetExternalSubject("verified-user-001").
		SetVerificationMethod("verified_account_link").SetVerifiedAt(now).SaveX(ctx)
	if _, err := orderQuote(ctx, db, "other-user-002", order.ID); !errors.Is(err, errForbidden) {
		t.Fatalf("other user read customer quote: %v", err)
	}
	quote, err := orderQuote(ctx, db, "verified-user-001", order.ID)
	if err != nil || quote.PayableCents != 5000 || quote.Confirmed {
		t.Fatalf("wrong quote snapshot: %+v %v", quote, err)
	}
	if _, err := confirmQuoteRetry(ctx, db, "verified-user-001", order.ID, 2, true, now); !errors.Is(err, errConflict) {
		t.Fatalf("stale quote version confirmed: %v", err)
	}
	if _, err := confirmQuoteRetry(ctx, db, "verified-user-001", order.ID, 1, false, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("implicit consent accepted: %v", err)
	}
	first, err := confirmQuoteRetry(ctx, db, "verified-user-001", order.ID, 1, true, now)
	if err != nil || first.AmountCents != 5000 || first.VerifiedAt == nil {
		t.Fatalf("confirmation failed: %+v %v", first, err)
	}
	again, err := confirmQuoteRetry(ctx, db, "verified-user-001", order.ID, 1, true, now)
	if err != nil || again.ID != first.ID || db.EsportsOutbox.Query().CountX(ctx) != 2 {
		t.Fatalf("duplicate confirmation created another event: %+v %v", again, err)
	}
	if _, err := confirmQuoteRetry(ctx, db, "verified-user-001", order.ID, 1, true, now.Add(16*time.Minute)); !errors.Is(err, errConflict) {
		t.Fatalf("expired quote reconfirmed: %v", err)
	}
}
