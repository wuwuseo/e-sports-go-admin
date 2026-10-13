package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
)

// splitPrepayDeduction 按顺序拆分应付额：默认先赠送后本金，principal_first 先本金后赠送。
func TestSplitPrepayDeduction(t *testing.T) {
	cases := []struct {
		order        string
		principal    int64
		bonus        int64
		amount       int64
		wantP, wantB int64
	}{
		{"bonus_first", 1500, 1000, 2000, 1000, 1000},
		{"bonus_first", 1500, 1000, 800, 0, 800},
		{"principal_first", 1500, 1000, 2000, 1500, 500},
		{"principal_first", 1500, 1000, 1200, 1200, 0},
		{"principal_first", 500, 1000, 800, 500, 300},
	}
	for _, c := range cases {
		gotP, gotB := splitPrepayDeduction(c.order, c.principal, c.bonus, c.amount)
		if gotP != c.wantP || gotB != c.wantB {
			t.Fatalf("%s: 本金/赠送 期望 %d/%d，实际 %d/%d", c.order, c.wantP, c.wantB, gotP, gotB)
		}
	}
}

// 余额核销按配置扣减：principal_first 时先扣本金（§4.5 可配置扣减顺序）。
func TestWalletPayDeductionOrderConfigurable(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_prepay_order?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	db.SystemConfig.Create().SetKey("app.esports.prepay_deduction_order").SetName("预付款扣减顺序").
		SetValue("principal_first").SetType("text").SetGroup("esports").SetStatus(1).SaveX(ctx)

	customer := saveCustomerOrFail(t, db, 11)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "GAME-PREPAY", Title: "扣减顺序测试", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: 5, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 2}, "prepay-key-001", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 1500, BonusCents: 1000, Note: "充值"}, "prepay-recharge-001"); err != nil {
		t.Fatal(err)
	}
	if _, err := payOrderWithBalance(ctx, db, 11, order.ID, now); err != nil {
		t.Fatal(err)
	}
	// 应付 2000：先本金 1500，再赠送 500，剩赠送 500。
	balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance.PrincipalCents != 0 || balance.BonusCents != 500 {
		t.Fatalf("先本金扣减不正确：%+v", balance)
	}
}
