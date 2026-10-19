package esports

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsorderevent"
	"go-admin/internal/ent/esportsoutbox"
)

// P1-13 价格偏离原因：超过阈值却未填 price_deviation_reason 时必须拒绝，且错误里点名该字段
// 与当前阈值；补上原因后放行，并把原因留存进订单快照（订单事件 + 偏离提醒外盒）。
func TestPriceDeviationReasonRequiredNamesFieldAndThreshold(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_price_deviation_reason")
	defer db.Close()
	ctx := t.Context()
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	// 关闭重复报单提醒，隔离价格偏离这一项校验。
	db.SystemConfig.Create().SetKey(duplicateOrderWindowKey).SetName("重复报单提醒时间窗").
		SetValue("0").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)

	customer := saveCustomerOrFail(t, db, 11)
	// 标准价 10000 分，免审额度放到 100%，人工议价 -6000 → 偏离 6000 基点（> 默认阈值 3000）。
	sku, err := saveSku(ctx, db, skuInput{
		Code: "DEV-REASON", Title: "偏离原因商品", ServiceType: "play", UnitPriceCents: 10000,
		MinUnitPriceCents: 1, AdjustmentApprovalBp: 10000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	in := orderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
		ManualAdjustCents: -6000, ManualAdjustReason: "老客户议价",
	}
	_, err = placeOrder(ctx, db, 11, in, "deviation-no-reason", now)
	if !errors.Is(err, errPriceDeviation) {
		t.Fatalf("超阈值未填偏离原因应拒绝：%v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "price_deviation_reason") {
		t.Fatalf("错误未指明字段名 price_deviation_reason：%s", msg)
	}
	if !strings.Contains(msg, "3000") {
		t.Fatalf("错误未指明当前阈值 3000 基点：%s", msg)
	}
	if got := db.EsportsOrder.Query().CountX(ctx); got != 0 {
		t.Fatalf("被拦下的报单不应落单，当前订单数 %d", got)
	}

	// 填上原因后放行，并留存进订单快照。
	in.PriceDeviationReason = "老客户议价补偿"
	order, err := placeOrder(ctx, db, 11, in, "deviation-with-reason", now)
	if err != nil {
		t.Fatalf("填写偏离原因后应放行：%v", err)
	}
	event := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(order.ID),
		esportsorderevent.EventTypeEQ("order.created"),
	).OnlyX(ctx)
	if got := event.Details["price_deviation_reason"]; got != "老客户议价补偿" {
		t.Fatalf("订单事件未留存偏离原因：%+v", event.Details)
	}
	if gap := event.Details["price_deviation_bp"]; gap != float64(6000) {
		t.Fatalf("订单事件未留存偏离度：%+v", event.Details)
	}
	flagged := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("order.price_deviation_flagged"),
		esportsoutbox.AggregateRefEQ(order.OrderNo),
	).OnlyX(ctx)
	if got := flagged.Payload["reason"]; got != "老客户议价补偿" {
		t.Fatalf("偏离提醒外盒未留存原因：%+v", flagged.Payload)
	}
}

// P1-13 关闭「原因必填」后仍只提醒不拦截，但要能看出偏离度；超过 255 字的原因被点名拒绝。
func TestPriceDeviationReasonOptionalSwitchAndLengthGuard(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_price_deviation_optional")
	defer db.Close()
	ctx := t.Context()
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	db.SystemConfig.Create().SetKey(duplicateOrderWindowKey).SetName("重复报单提醒时间窗").
		SetValue("0").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	db.SystemConfig.Create().SetKey(priceDeviationReasonRequiredKey).SetName("偏离强制填写原因").
		SetValue("false").SetType("string").SetGroup("esports").SetStatus(1).SaveX(ctx)

	customer := saveCustomerOrFail(t, db, 11)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "DEV-OPTIONAL", Title: "偏离可选原因商品", ServiceType: "play", UnitPriceCents: 10000,
		MinUnitPriceCents: 1, AdjustmentApprovalBp: 10000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err = placeOrder(ctx, db, 11, orderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
		ManualAdjustCents: -6000, ManualAdjustReason: "老客户议价",
	}, "deviation-optional-1", now); err != nil {
		t.Fatalf("关闭原因必填后应放行：%v", err)
	}

	longReason := strings.Repeat("长", 256)
	_, err = placeOrder(ctx, db, 11, orderInput{
		CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1,
		ManualAdjustCents: -6000, ManualAdjustReason: "老客户议价",
		PriceDeviationReason: longReason,
	}, "deviation-optional-2", now)
	if !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "price_deviation_reason") {
		t.Fatalf("超长偏离原因应点名字段被拒：%v", err)
	}
}
