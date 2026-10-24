package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsledgerentry"
)

// 批次级逐笔扣减：充值写批次账本，余额消费按批次分摊并记录明细。
func TestLedgerBatchAllocation(t *testing.T) {
	db := openWalletTestDB(t, "esports_ledger_allocation")
	ctx := t.Context()
	now := time.Now()
	customer := saveCustomerOrFail(t, db, 11)

	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 10000, Note: "充值"}, "recharge-key-1"); err != nil {
		t.Fatal(err)
	}
	batches, err := db.EsportsLedgerEntry.Query().Where(
		esportsledgerentry.EntryRoleEQ("recharge_batch")).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 1 || batches[0].DeltaCents != 10000 {
		t.Fatalf("充值应写一条批次账本：%+v", batches)
	}

	sku, err := saveSku(ctx, db, skuInput{
		Code: "ALLOC", Title: "分摊商品", ServiceType: "play", UnitPriceCents: 10000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: 5, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"alloc-order-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := payOrderWithBalance(ctx, db, 11, order.ID, now); err != nil {
		t.Fatal(err)
	}
	allocs, err := db.EsportsLedgerAllocation.Query().All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(allocs) != 1 || allocs[0].AmountCents != 10000 {
		t.Fatalf("消费应分摊到唯一批次：%+v", allocs)
	}
	if allocs[0].AccountType != walletCustomerBalance ||
		allocs[0].BatchSourceType != "wallet_tx" || allocs[0].ConsumeSourceType != "order" {
		t.Fatalf("分摊来源不正确：%+v", allocs[0])
	}

	// 再充值 2000 + 小额报单核销：第一个批次已用尽，应分摊到第二个批次。
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 2000, Note: "充值"}, "recharge-key-2"); err != nil {
		t.Fatal(err)
	}
	smallSku, err := saveSku(ctx, db, skuInput{
		Code: "ALLOC2", Title: "分摊商品2", ServiceType: "play", UnitPriceCents: 2000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: 5, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	order2, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: smallSku.ID, Quantity: 1},
		"alloc-order-2", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := payOrderWithBalance(ctx, db, 11, order2.ID, now); err != nil {
		t.Fatal(err)
	}
	allocs2, err := db.EsportsLedgerAllocation.Query().
		Order().All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(allocs2) != 2 {
		t.Fatalf("第二次消费应新增一条分摊：%+v", allocs2)
	}
	// 第二次分摊应指向第二个批次（wallet_tx / recharge-key-2 对应的 txNo）。
	second := allocs2[1]
	if second.AmountCents != 2000 || second.ConsumeSourceID != order2.OrderNo {
		t.Fatalf("第二次分摊不正确：%+v", second)
	}
	if second.BatchSourceID == allocs2[0].BatchSourceID {
		t.Fatalf("第二次分摊应指向另一个批次：%+v", allocs2)
	}
}
