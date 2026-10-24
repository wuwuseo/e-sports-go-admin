package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/migrate"
)

func TestPlaceOrderReservesSKUUnitsOnce(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_place_order?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	customer := saveCustomerOrFail(t, db, 11)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "GAME-HOUR", Title: "游戏陪玩", ServiceType: "play", UnitPriceCents: 5000,
		ServiceUnits: 1, ServiceUnitName: "小时", RequiredSeats: 1, QuotaTotal: 2, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	in := orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 2}
	first, err := placeOrder(ctx, db, 11, in, "request-key-001", now)
	if err != nil {
		t.Fatal(err)
	}
	if first.PayableCents != 10000 || first.PromisedServiceUnits != 2 || !first.PaymentDeadline.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("wrong quote snapshot: %+v", first)
	}
	again, err := placeOrder(ctx, db, 11, in, "request-key-001", now)
	if err != nil || again.ID != first.ID {
		t.Fatalf("idempotent retry created a different order: %+v, %v", again, err)
	}
	if _, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1}, "request-key-001", now); !errors.Is(err, errConflict) {
		t.Fatalf("changed request reused key: %v", err)
	}
	if _, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1}, "request-key-002", now); !errors.Is(err, errConflict) {
		t.Fatalf("oversold final SKU unit: %v", err)
	}
	stored := db.EsportsSku.GetX(ctx, sku.ID)
	if stored.QuotaReserved != 2 || db.EsportsOrder.Query().CountX(ctx) != 1 ||
		db.EsportsQuotaHold.Query().CountX(ctx) != 1 || db.EsportsOrderEvent.Query().CountX(ctx) != 1 ||
		db.EsportsOutbox.Query().CountX(ctx) != 1 {
		t.Fatalf("transaction did not leave one consistent reservation: %+v", stored)
	}
	expired, err := expireDueOrders(ctx, db, now.Add(16*time.Minute), 100)
	if err != nil || expired != 1 {
		t.Fatalf("expiration failed: %v, %v", expired, err)
	}
	expired, err = expireDueOrders(ctx, db, now.Add(17*time.Minute), 100)
	if err != nil || expired != 0 {
		t.Fatalf("duplicate expiration changed state: %v, %v", expired, err)
	}
	stored = db.EsportsSku.GetX(ctx, sku.ID)
	if stored.QuotaReserved != 0 || db.EsportsOrder.GetX(ctx, first.ID).FulfillmentStatus != "cancelled" ||
		db.EsportsOrderEvent.Query().CountX(ctx) != 2 {
		t.Fatalf("expiration did not release exactly once: %+v", stored)
	}
}

func TestOrderRequiresConfiguredDeadlineAndOwner(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_order_config?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "GAME-SESSION", Title: "一局陪玩", ServiceType: "play", UnitPriceCents: 5000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	in := orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1}
	if _, err := placeOrder(ctx, db, 11, in, "request-key-003", time.Now()); !errors.Is(err, errNotConfigured) {
		t.Fatalf("unconfigured deadline: %v", err)
	}
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	if _, err := placeOrder(ctx, db, 12, in, "request-key-004", time.Now()); !errors.Is(err, errForbidden) {
		t.Fatalf("different customer owner: %v", err)
	}
	unverifiedAge, err := saveCustomer(ctx, db, 11, customerInput{DisplayName: "未核验年龄"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := placeOrder(ctx, db, 11, orderInput{CustomerID: unverifiedAge.ID, SkuID: sku.ID, Quantity: 1},
		"request-key-005", time.Now()); !errors.Is(err, errAgeUnverified) {
		t.Fatalf("unverified age placed order: %v", err)
	}
	if db.EsportsOrder.Query().CountX(ctx) != 0 {
		t.Fatal("blocked request persisted an order")
	}
}

func saveCustomerOrFail(t *testing.T, db *ent.Client, ownerID int) *ent.EsportsCustomer {
	t.Helper()
	customer, err := saveCustomer(t.Context(), db, ownerID, customerInput{DisplayName: "测试客户"})
	if err != nil {
		t.Fatal(err)
	}
	// 自助服务（下单/购卡/充值）默认开启：self_order_enabled 缺省 false，测试客户需显式开启，
	// 否则 ensureCustomerSelfServiceAllowed 会拦截（自助入口总开关）。
	customer, err = db.EsportsCustomer.UpdateOneID(customer.ID).
		SetAgeStatus("adult").SetSelfOrderEnabled(true).Save(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return customer
}

// 报单指定陪玩须资质合格：未通过考核的陪玩被拒，合格陪玩写入订单，未指定时字段为空。
func TestPlaceOrderValidatesPreferredPlayer(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_place_order_preferred?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	customer := saveCustomerOrFail(t, db, 11)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "GAME-PREF", Title: "指定陪玩商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: 5, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	pending := seedPlayer(t, db, 9101, esportsplayer.QualificationPending, esportsplayer.PresenceOnline)
	qualified := seedPlayer(t, db, 9102, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	if _, err = placeOrder(ctx, db, 11, orderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1, PreferredPlayerID: pending.ID,
	}, "preferred-order-001", time.Now()); !errors.Is(err, errInvalidInput) {
		t.Fatalf("未合格陪玩应被拒，err=%v", err)
	}
	order, err := placeOrder(ctx, db, 11, orderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1, PreferredPlayerID: qualified.ID,
	}, "preferred-order-002", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if order.PreferredPlayerID == nil || *order.PreferredPlayerID != qualified.ID {
		t.Fatalf("指定陪玩未写入订单：%+v", order)
	}
	plain, err := placeOrder(ctx, db, 11, orderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
	}, "preferred-order-003", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if plain.PreferredPlayerID != nil {
		t.Fatalf("未指定陪玩时不应写入字段：%+v", plain)
	}
}
