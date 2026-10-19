package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
)

// ── 第 2 波：无归属客服的客户自助下单进公共池（5.1-02） ──

// po2SeedOrphanCustomer 造一个无归属客服、已开通自助、年龄已核验的客户。
func po2SeedOrphanCustomer(t *testing.T, db *ent.Client) *ent.EsportsCustomer {
	t.Helper()
	ctx := t.Context()
	customer := db.EsportsCustomer.Create().SetDisplayName("无归属客服客户").
		SetAgeStatus("adult").SetSelfOrderEnabled(true).SaveX(ctx)
	return customer
}

func TestRound4W2SelfOrderWithoutOwnerAdmin(t *testing.T) {
	db := openProductDB(t, "esports_po2_self_order_orphan")
	defer db.Close()
	poSeedBaseConfigs(t, db)
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

	customer := po2SeedOrphanCustomer(t, db)
	sku := po2SeedSku(t, db, "PO2-SELF", 1, 1, 0)

	order, err := placeCustomerSelfOrder(ctx, db, customer,
		customerOrderInput{SkuID: sku.ID, Quantity: 1, IdempotencyKey: "po2-self-key-001"}, now)
	if err != nil {
		t.Fatalf("无归属客服的客户自助下单应成功：%v", err)
	}
	if order.ResponsibleAdminID != nil {
		t.Fatalf("无归属客服的自助单负责客服应为 NULL：%+v", order.ResponsibleAdminID)
	}
	if order.CreatedByAdminID != nil {
		t.Fatalf("客户自助单不应记客服建单人：%+v", order.CreatedByAdminID)
	}
	if order.EntryPoint != esportsorder.EntryPointCustomerSelf {
		t.Fatalf("自助单入口不正确：%s", order.EntryPoint)
	}
	// 公共池口径：责任客服为 NULL 的订单可被派单池查询命中。
	pooled := db.EsportsOrder.Query().Where(esportsorder.ResponsibleAdminIDIsNil()).CountX(ctx)
	if pooled != 1 {
		t.Fatalf("自助单未进入公共池：%d", pooled)
	}

	// 有归属客服时仍按归属客服落库（不被本改动放宽）。
	owned := saveCustomerOrFail(t, db, 11)
	ownedOrder, err := placeCustomerSelfOrder(ctx, db, owned,
		customerOrderInput{SkuID: sku.ID, Quantity: 1, IdempotencyKey: "po2-self-key-002"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if ownedOrder.ResponsibleAdminID == nil || *ownedOrder.ResponsibleAdminID != 11 {
		t.Fatalf("有归属客服时负责客服应为其归属：%+v", ownedOrder.ResponsibleAdminID)
	}

	// 客服侧报单仍严格校验归属：他人客户不得代报。
	if _, err = placeOrder(ctx, db, 12, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"po2-self-key-003", now); !errors.Is(err, errForbidden) {
		t.Fatalf("客服代报他人客户应被拒：%v", err)
	}
}
