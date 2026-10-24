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

// 按比例扣减（proportional）：本金 / 赠送按剩余额比例分摊，本金向下取整、尾差归赠送，
// 拆分之和恒等于应付额。
func TestSplitPrepayDeductionProportional(t *testing.T) {
	cases := []struct {
		principal, bonus, amount int64
		wantP, wantB             int64
	}{
		{1500, 1000, 2000, 1200, 800}, // 2000×1500/2500=1200，余 800
		{500, 1000, 800, 266, 534},    // 800×500/1500=266（截断），尾差归赠送
		{0, 1000, 500, 0, 500},        // 无本金则全走赠送
		{1000, 0, 400, 400, 0},        // 无赠送则全走本金
		{1000, 1000, 1500, 750, 750},  // 各半
	}
	for _, c := range cases {
		gotP, gotB := splitPrepayDeduction("proportional", c.principal, c.bonus, c.amount)
		if gotP != c.wantP || gotB != c.wantB {
			t.Fatalf("proportional(%d,%d,%d): 期望 %d/%d，实际 %d/%d",
				c.principal, c.bonus, c.amount, c.wantP, c.wantB, gotP, gotB)
		}
		if gotP+gotB != c.amount {
			t.Fatalf("拆分之和应等于应付额 %d，实际 %d", c.amount, gotP+gotB)
		}
	}
}

// 余额核销配置为 proportional 时按比例分摊本金 / 赠送。
func TestWalletPayProportionalDeduction(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_prepay_prop?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	db.SystemConfig.Create().SetKey("app.esports.prepay_deduction_order").SetName("预付款扣减顺序").
		SetValue("proportional").SetType("text").SetGroup("esports").SetStatus(1).SaveX(ctx)

	customer := saveCustomerOrFail(t, db, 11)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "GAME-PROP", Title: "按比例扣减测试", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: 5, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 2}, "prop-key-001", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 1500, BonusCents: 1000, Note: "充值"}, "prop-recharge-001"); err != nil {
		t.Fatal(err)
	}
	if _, err := payOrderWithBalance(ctx, db, 11, order.ID, now); err != nil {
		t.Fatal(err)
	}
	// 应付 2000，余额本金 1500 / 赠送 1000：按比例本金扣 1200、赠送扣 800 → 余本金 300 / 赠送 200。
	balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance.PrincipalCents != 300 || balance.BonusCents != 200 {
		t.Fatalf("按比例扣减不正确：%+v", balance)
	}
}

// 余额核销按配置扣减：principal_first 时先扣本金（可配置扣减顺序）。
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
