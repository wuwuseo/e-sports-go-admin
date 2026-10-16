package esports

import (
	"errors"
	"testing"
	"time"

	"go-admin/internal/ent/esportspriceadjustment"
)

func TestPriceAdjustmentFloorAndIndependentApproval(t *testing.T) {
	db := openAfterSaleDB(t, "esports_price_approval")
	defer db.Close()
	ctx := t.Context()
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	customer := saveCustomerOrFail(t, db, 11)
	sku, err := saveSku(ctx, db, skuInput{Code: "PRICE-APPROVAL", Title: "审批商品", ServiceType: "play",
		UnitPriceCents: 1000, MinUnitPriceCents: 800, AdjustmentApprovalBp: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: 10, Status: "on_sale"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1}, "price-approval-order", now)
	if err != nil {
		t.Fatal(err)
	}
	db.EsportsSku.UpdateOneID(sku.ID).SetMinUnitPriceCents(900).SetAdjustmentApprovalBp(5000).ExecX(ctx)
	belowFloor := repriceInput{ExpectedVersion: order.Version, Quantity: 1, ManualAdjustCents: -201, Reason: "低于底价"}
	if _, err := previewReprice(ctx, db, order.ID, belowFloor); !errors.Is(err, errInvalidInput) {
		t.Fatalf("预览未拦截底价：%v", err)
	}
	if _, err := repriceOrder(ctx, db, order.ID, 11, false, belowFloor); !errors.Is(err, errInvalidInput) {
		t.Fatalf("执行未拦截底价：%v", err)
	}
	in := repriceInput{ExpectedVersion: order.Version, Quantity: 1, ManualAdjustCents: -150, Reason: "长期客户议价"}
	preview, err := previewReprice(ctx, db, order.ID, in)
	if err != nil || !preview.RequiresApproval || preview.MinUnitPriceCents != 800 || preview.ApprovalThresholdBp != 1000 || preview.PayableCents != 850 {
		t.Fatalf("超限预览及快照不正确：%+v %v", preview, err)
	}
	if _, err := repriceOrder(ctx, db, order.ID, 11, false, in); !errors.Is(err, errInvalidInput) {
		t.Fatalf("未经审批的超限改价成功：%v", err)
	}
	if _, err := repriceOrder(ctx, db, order.ID, 22, false, in); !errors.Is(err, errForbidden) {
		t.Fatalf("非负责客服绕过事务内归属校验：%v", err)
	}
	request := priceAdjustmentRequestInput{ExpectedVersion: order.Version, Quantity: 1,
		ManualAdjustCents: -150, Reason: in.Reason, RequestKey: "price-approval-request"}
	item, err := requestPriceAdjustment(ctx, db, order.ID, 11, false, request)
	if err != nil || item.Status != esportspriceadjustment.StatusPending {
		t.Fatalf("发起审批失败：%+v %v", item, err)
	}
	if _, err := decidePriceAdjustment(ctx, db, item.ID, 11, true, true, "同意", now); !errors.Is(err, errForbidden) {
		t.Fatalf("申请人自审成功：%v", err)
	}
	if _, err := decidePriceAdjustment(ctx, db, item.ID, 22, false, true, "同意", now); !errors.Is(err, errForbidden) {
		t.Fatalf("普通管理员审批成功：%v", err)
	}
	approved, err := decidePriceAdjustment(ctx, db, item.ID, 22, true, true, "价格在底价之上", now)
	if err != nil || approved.Status != esportspriceadjustment.StatusApproved {
		t.Fatalf("异人审批失败：%+v %v", approved, err)
	}
	wrong := in
	wrong.ApprovalID = item.ID
	wrong.ManualAdjustCents = -160
	if _, err := repriceOrder(ctx, db, order.ID, 11, false, wrong); !errors.Is(err, errConflict) {
		t.Fatalf("审批单被用于不同报价：%v", err)
	}
	in.ApprovalID = item.ID
	updated, err := repriceOrder(ctx, db, order.ID, 11, false, in)
	if err != nil || updated.PayableCents != 850 || updated.QuoteVersion != 2 {
		t.Fatalf("审批后执行失败：%+v %v", updated, err)
	}
	if got := db.EsportsPriceAdjustment.GetX(ctx, item.ID); got.Status != esportspriceadjustment.StatusConsumed {
		t.Fatalf("审批记录未与改价原子消费：%+v", got)
	}
	if _, err := repriceOrder(ctx, db, order.ID, 11, false, in); !errors.Is(err, errConflict) {
		t.Fatalf("重复执行审批改价：%v", err)
	}
	db.EsportsSku.UpdateOneID(sku.ID).SetStatus("off_sale").ExecX(ctx)
	if _, err := previewReprice(ctx, db, order.ID, repriceInput{
		ExpectedVersion: updated.Version, Quantity: 1,
	}); !errors.Is(err, errConflict) {
		t.Fatalf("已下架商品仍允许生成新报价：%v", err)
	}
}
