package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
)

// 余额由流水派生；核销按"先赠送后本金"扣减，余额不足与重复核销都必须被拦住。
func TestWalletBalanceDerivesFromLedgerAndPaysOrder(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_wallet?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	customer := saveCustomerOrFail(t, db, 11)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "GAME-WALLET", Title: "钱包测试商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: 5, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 2}, "wallet-key-001", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := payOrderWithBalance(ctx, db, 11, order.ID, now); !errors.Is(err, errInsufficientBalance) {
		t.Fatalf("零余额订单被放行：%v", err)
	}
	view, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 1500, BonusCents: 1000, Note: "开卡充值"}, "recharge-key-001")
	if err != nil {
		t.Fatal(err)
	}
	if view.BalanceCents != 2500 || view.PrincipalCents != 1500 || view.BonusCents != 1000 {
		t.Fatalf("充值后余额不正确：%+v", view)
	}
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 100, Note: "重复提交"}, "recharge-key-001"); !errors.Is(err, errConflict) {
		t.Fatalf("重复充值键未拦截：%v", err)
	}
	paid, err := payOrderWithBalance(ctx, db, 11, order.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if paid.PaymentStatus != "paid" || paid.FulfillmentStatus != "pending_dispatch" {
		t.Fatalf("余额核销未转待派单：%+v", paid)
	}
	balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 应付 2000：先扣赠送 1000，再扣本金 1000，剩本金 500。
	if balance.PrincipalCents != 500 || balance.BonusCents != 0 {
		t.Fatalf("扣减顺序或金额不正确：%+v", balance)
	}
	stored := db.EsportsSku.GetX(ctx, sku.ID)
	if stored.QuotaSold != 2 || stored.QuotaReserved != 0 {
		t.Fatalf("核销未把名额从占用转已售：%+v", stored)
	}
	if _, err := payOrderWithBalance(ctx, db, 11, order.ID, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复核销被放行：%v", err)
	}
	if _, err := walletAdjust(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: -1000, Note: "调账为负"}, "adjust-key-001"); !errors.Is(err, errInsufficientBalance) {
		t.Fatalf("调整导致负余额未被拦截：%v", err)
	}
	if _, err := walletAdjust(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 1}, "adjust-key-002"); !errors.Is(err, errInvalidInput) {
		t.Fatalf("无原因调账未被拦截：%v", err)
	}
	if _, err := walletRecharge(ctx, db, 11, walletPlayerCatfood, 999,
		walletInput{PrincipalCents: 100}, "catfood-key-001"); !ent.IsNotFound(err) {
		t.Fatalf("不存在的陪玩钱包被接受：%v", err)
	}
	if _, err := walletRecharge(ctx, db, 11, "unknown_wallet", customer.ID,
		walletInput{PrincipalCents: 100}, "invalid-type-key"); !errors.Is(err, errInvalidInput) {
		t.Fatalf("未知钱包类型被接受：%v", err)
	}
}
