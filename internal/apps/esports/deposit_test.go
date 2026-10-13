package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/migrate"
)

func openDepositDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	db := enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(t.Context())
	return db
}

// seedPaidOrderWithQuota 造一张已付款、待派单、仍占用名额（quota_reserved 非 0）的订单。
func seedPaidOrderWithQuota(t *testing.T, db *ent.Client, code string) (*ent.EsportsOrder, *ent.EsportsSku) {
	t.Helper()
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	sku, err := saveSku(ctx, db, skuInput{
		Code: code, Title: "存单测试商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: 5, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"deposit-key-"+code, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	paid := db.EsportsOrder.UpdateOneID(order.ID).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).SaveX(ctx)
	return paid, sku
}

// 已付款单存单成功：置冻结标记、留时间与操作人，履约状态与名额占用均不变，写 order.deposited 事件。
func TestDepositOrderFreezesWithoutReleasingQuota(t *testing.T) {
	db := openDepositDB(t, "esports_deposit_ok")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	order, sku := seedPaidOrderWithQuota(t, db, "DEPOSIT-OK")
	reservedBefore := db.EsportsSku.GetX(ctx, sku.ID).QuotaReserved
	if reservedBefore == 0 {
		t.Fatal("测试前置：已付款订单应占用名额")
	}
	frozen, err := depositOrder(ctx, db, order.ID, 99, now)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.DepositStatus != esportsorder.DepositStatusFrozen || frozen.DepositedAt == nil {
		t.Fatalf("存单未生效：%+v", frozen)
	}
	if frozen.DepositedByAdminID == nil || *frozen.DepositedByAdminID != 99 {
		t.Fatalf("存单操作人未记录：%+v", frozen.DepositedByAdminID)
	}
	if frozen.FulfillmentStatus != esportsorder.FulfillmentStatusPendingDispatch {
		t.Fatalf("存单改变了履约状态：%+v", frozen)
	}
	if got := db.EsportsSku.GetX(ctx, sku.ID).QuotaReserved; got != reservedBefore {
		t.Fatalf("存单释放了名额占用：%d -> %d", reservedBefore, got)
	}
	if countEventsByType(t, db)["order.deposited"] != 1 {
		t.Fatal("缺少 order.deposited 事件")
	}
	if _, err := depositOrder(ctx, db, order.ID, 99, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复存单未拦截：%v", err)
	}
}

// 未付款单与已取消订单一律不许存单：返回 errConflict 且标记保持 none。
func TestDepositRejectsUnpaidAndCancelledOrders(t *testing.T) {
	db := openDepositDB(t, "esports_deposit_guards")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	customer := saveCustomerOrFail(t, db, 11)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "DEPOSIT-GUARD", Title: "存单守卫商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: 5, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	unpaid, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"guard-unpaid", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := depositOrder(ctx, db, unpaid.ID, 99, now); !errors.Is(err, errConflict) {
		t.Fatalf("未付款单存单被放行：%v", err)
	}
	if got := db.EsportsOrder.GetX(ctx, unpaid.ID); got.DepositStatus != esportsorder.DepositStatusNone {
		t.Fatalf("未付款单被误存：%+v", got)
	}
	cancelled := db.EsportsOrder.UpdateOneID(unpaid.ID).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusCancelled).SaveX(ctx)
	if _, err := depositOrder(ctx, db, cancelled.ID, 99, now); !errors.Is(err, errConflict) {
		t.Fatalf("已取消订单存单被放行：%v", err)
	}
	if _, err := depositOrder(ctx, db, 0, 99, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法订单号未被拦截：%v", err)
	}
}

// 恢复：清掉冻结标记与存单时间，履约状态与资金状态均不变，写 order.restored 事件；未冻结订单恢复被拒。
func TestRestoreOrderClearsFrozenMark(t *testing.T) {
	db := openDepositDB(t, "esports_deposit_restore")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	order, _ := seedPaidOrderWithQuota(t, db, "DEPOSIT-RESTORE")
	if _, err := restoreOrder(ctx, db, order.ID, 99, now); !errors.Is(err, errConflict) {
		t.Fatalf("未冻结订单恢复被放行：%v", err)
	}
	if _, err := depositOrder(ctx, db, order.ID, 99, now); err != nil {
		t.Fatal(err)
	}
	restored, err := restoreOrder(ctx, db, order.ID, 99, now)
	if err != nil {
		t.Fatal(err)
	}
	if restored.DepositStatus != esportsorder.DepositStatusNone || restored.DepositedAt != nil ||
		restored.DepositedByAdminID != nil {
		t.Fatalf("恢复未清空存单标记：%+v", restored)
	}
	if restored.PaymentStatus != esportsorder.PaymentStatusPaid ||
		restored.FulfillmentStatus != esportsorder.FulfillmentStatusPendingDispatch {
		t.Fatalf("恢复改动了资金或履约状态：%+v", restored)
	}
	if countEventsByType(t, db)["order.restored"] != 1 {
		t.Fatal("缺少 order.restored 事件")
	}
}
