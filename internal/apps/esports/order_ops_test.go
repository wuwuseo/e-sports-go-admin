package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
)

// 改价、取消必须与名额占用同事务联动，且旧支付意图在改价时立即失效。
func TestRepriceAndCancelAdjustQuotaExactlyOnce(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_order_ops?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	customer := saveCustomerOrFail(t, db, 11)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "GAME-OPS", Title: "操作测试商品", ServiceType: "play", UnitPriceCents: 1000, MinUnitPriceCents: 800,
		ServiceUnits: 2, ServiceUnitName: "小时", RequiredSeats: 1, QuotaTotal: 5, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1}, "ops-key-001", now)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := repriceOrder(ctx, db, order.ID, 11, false, repriceInput{
		ExpectedVersion: order.Version, Quantity: 3, ManualAdjustCents: -500, Reason: "客户加量并议价",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.PayableCents != 2500 || updated.PromisedServiceUnits != 6 || updated.QuoteVersion != 2 {
		t.Fatalf("改价未按新规格重算：%+v", updated)
	}
	if stored := db.EsportsSku.GetX(ctx, sku.ID); stored.QuotaReserved != 3 {
		t.Fatalf("改份数未同步占用名额：%+v", stored)
	}
	if hold := db.EsportsQuotaHold.Query().OnlyX(ctx); hold.Quantity != 3 {
		t.Fatalf("名额占用记录未同步：%+v", hold)
	}
	if _, err := repriceOrder(ctx, db, order.ID, 11, false, repriceInput{ExpectedVersion: 1, Quantity: 2, Reason: "旧版本"}); !errors.Is(err, errConflict) {
		t.Fatalf("旧版本改价被接受：%v", err)
	}
	if _, err := repriceOrder(ctx, db, order.ID, 11, false, repriceInput{ExpectedVersion: updated.Version, Quantity: 1, ManualAdjustCents: -1000, Reason: "零价"}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("零价改价被接受：%v", err)
	}
	if _, err := rescheduleOrder(ctx, db, order.ID, 11, rescheduleInput{
		ExpectedVersion: updated.Version, StartAt: now.Add(time.Hour), Reason: "客户改期",
	}, now); err != nil {
		t.Fatalf("待付款改期失败：%v", err)
	}
	cancelled, err := cancelOrder(ctx, db, order.ID, 11, "客户放弃", now)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.FulfillmentStatus != "cancelled" {
		t.Fatalf("取消未生效：%+v", cancelled)
	}
	if stored := db.EsportsSku.GetX(ctx, sku.ID); stored.QuotaReserved != 0 ||
		db.EsportsQuotaHold.Query().OnlyX(ctx).Status != "released" {
		t.Fatalf("取消未释放名额：%+v", stored)
	}
	if _, err := cancelOrder(ctx, db, order.ID, 11, "重复取消", now); !errors.Is(err, errConflict) {
		t.Fatalf("重复取消被接受：%v", err)
	}
	for _, event := range db.EsportsOrderEvent.Query().AllX(ctx) {
		switch event.EventType {
		case "order.repriced", "order.rescheduled", "order.cancelled":
			if event.ActorType != "admin" || event.ActorID != "11" {
				t.Fatalf("订单操作审计记录了错误操作人：%+v", event)
			}
		}
	}
}
