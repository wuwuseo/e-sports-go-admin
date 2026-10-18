package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/migrate"
)

// TestReleaseOrderToPublicPool 覆盖「待派单」订单由负责客服释放到公共池：
// 清空负责人、保留履约状态、写事件；非负责人不能释放。
func TestReleaseOrderToPublicPool(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_release_pool?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("60").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	customer := saveCustomerOrFail(t, db, 11)
	db.EsportsCustomer.UpdateOneID(customer.ID).SetAgeStatus("adult").ExecX(ctx)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "POOL-SKU", Title: "陪玩一小时", ServiceType: "play", UnitPriceCents: 5000,
		ServiceUnits: 1, ServiceUnitName: "小时", RequiredSeats: 1, QuotaTotal: 2, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1}, "pool-order-1", now)
	if err != nil {
		t.Fatal(err)
	}
	order = db.EsportsOrder.UpdateOneID(order.ID).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).
		SaveX(ctx)

	if _, err := releaseOrderToPublicPool(ctx, db, 12, false, order.ID, "非负责人", now); err == nil {
		t.Fatal("非负责人释放应被拒")
	}
	released, err := releaseOrderToPublicPool(ctx, db, 11, false, order.ID, "客服临时离线", now)
	if err != nil {
		t.Fatalf("负责人释放失败: %v", err)
	}
	if released.ResponsibleAdminID != nil {
		t.Fatalf("释放后负责人应为空: %+v", released.ResponsibleAdminID)
	}
	if released.FulfillmentStatus != esportsorder.FulfillmentStatusPendingDispatch {
		t.Fatalf("释放不应改变履约状态: %s", released.FulfillmentStatus)
	}
	if n := db.EsportsOrderEvent.Query().Where().CountX(ctx); n == 0 {
		t.Fatal("释放应写订单事件")
	}
}
