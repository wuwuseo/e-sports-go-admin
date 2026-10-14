package esports

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportscustomer"
)

// 客户风险标记（§13.7）：标记须填原因、解除清空原因与时间、风险名单只含被标记客户。
func TestCustomerRiskMarking(t *testing.T) {
	db := openAfterSaleDB(t, "esports_risk_marking")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	customer := saveCustomerOrFail(t, db, 11)
	if _, err := markCustomerRisk(ctx, db, customer.ID, "watch", "  ", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("标记风险须填原因：%v", err)
	}
	if _, err := markCustomerRisk(ctx, db, customer.ID, "unknown", "x", now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法状态应被拒：%v", err)
	}
	marked, err := markCustomerRisk(ctx, db, customer.ID, "restricted", "频繁恶意退款", now)
	if err != nil || marked.RiskStatus != esportscustomer.RiskStatusRestricted ||
		marked.RiskMarkedAt == nil {
		t.Fatalf("标记失败：%v %+v", err, marked)
	}
	items, err := listRiskCustomers(ctx, db, 100)
	if err != nil || len(items) != 1 {
		t.Fatalf("风险名单应含该客户：%v %d", err, len(items))
	}
	cleared, err := markCustomerRisk(ctx, db, customer.ID, "normal", "", now)
	if err != nil || cleared.RiskStatus != esportscustomer.RiskStatusNormal ||
		cleared.RiskReason != "" || cleared.RiskMarkedAt != nil {
		t.Fatalf("解除失败：%v %+v", err, cleared)
	}
	if items, _ = listRiskCustomers(ctx, db, 100); len(items) != 0 {
		t.Fatalf("解除后不应在风险名单：%d", len(items))
	}
}

// 自助入口拦截（§13.7）：只有「限制自助下单」拦截；「疑似滥用复核」不拦截、不自动处罚。
func TestEnsureCustomerSelfServiceAllowed(t *testing.T) {
	cases := []struct {
		status  esportscustomer.RiskStatus
		blocked bool
	}{
		{esportscustomer.RiskStatusNormal, false},
		{esportscustomer.RiskStatusWatch, false},
		{esportscustomer.RiskStatusRestricted, true},
	}
	for _, tc := range cases {
		customer := &ent.EsportsCustomer{RiskStatus: tc.status, RiskReason: "存在退款争议"}
		err := ensureCustomerSelfServiceAllowed(customer)
		if tc.blocked {
			if !errors.Is(err, errForbidden) {
				t.Fatalf("%s 应被拦截：%v", tc.status, err)
			}
		} else if err != nil {
			t.Fatalf("%s 不应被拦截：%v", tc.status, err)
		}
	}
}

// 高频退款自动标记（§13.7）：达阈值标记为「疑似滥用复核」；重复运行幂等，且不降级已限制客户。
func TestFlagHighRiskCustomers(t *testing.T) {
	db := openAfterSaleDB(t, "esports_risk_flag")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	customer := saveCustomerOrFail(t, db, 11)
	for i := 0; i < 3; i++ {
		db.EsportsAfterSale.Create().SetAfterSaleNo(fmt.Sprintf("AS-RISK-%d", i)).
			SetSourceType(esportsaftersale.SourceTypeOrder).SetSourceID(i + 1).
			SetKind(esportsaftersale.KindRefundRequest).SetChannel(esportsaftersale.ChannelCustomer).
			SetDemand("客户申请退款").SetStatus(esportsaftersale.StatusCompleted).
			SetCustomerID(customer.ID).SetRefundCents(1000).
			SetCreatedAt(now.Add(-time.Hour)).SaveX(ctx)
	}
	if flagged, err := flagHighRiskCustomers(ctx, db, now, 200); err != nil || flagged != 1 {
		t.Fatalf("应按默认阈值（3 次）标记 1 个客户：%v %d", err, flagged)
	}
	item := db.EsportsCustomer.GetX(ctx, customer.ID)
	if item.RiskStatus != esportscustomer.RiskStatusWatch || item.RiskReason == "" {
		t.Fatalf("应标记为 watch 并写入原因：%+v", item)
	}
	if again, err := flagHighRiskCustomers(ctx, db, now, 200); err != nil || again != 0 {
		t.Fatalf("重复运行应幂等：%v %d", err, again)
	}
	// 人工限制后不被自动标记降级。
	if _, err := markCustomerRisk(ctx, db, customer.ID, "restricted", "人工限制", now); err != nil {
		t.Fatal(err)
	}
	if again, err := flagHighRiskCustomers(ctx, db, now, 200); err != nil || again != 0 {
		t.Fatalf("已限制客户不应被重新标记：%v %d", err, again)
	}
	if db.EsportsCustomer.GetX(ctx, customer.ID).RiskStatus != esportscustomer.RiskStatusRestricted {
		t.Fatal("restricted 被降级")
	}
}

// 客户自助下单（§13.7）：被限制的客户直接拦截；无归属客服时提示联系客服。
func TestPlaceCustomerSelfOrderGuard(t *testing.T) {
	db := openAfterSaleDB(t, "esports_risk_self_order")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	customer := saveCustomerOrFail(t, db, 11)
	db.EsportsCustomer.UpdateOneID(customer.ID).
		SetAgeStatus(esportscustomer.AgeStatusAdult).SaveX(ctx)
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	restricted, err := markCustomerRisk(ctx, db, customer.ID, "restricted", "频繁恶意退款", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = placeCustomerSelfOrder(ctx, db, restricted, customerOrderInput{
		SkuID: 1, Quantity: 1, IdempotencyKey: "self-order-key-1"}, now); !errors.Is(err, errForbidden) ||
		!strings.Contains(err.Error(), "限制自助下单") {
		t.Fatalf("被限制客户自助下单应被风控拦截：%v", err)
	}
	if n := db.EsportsOrder.Query().CountX(ctx); n != 0 {
		t.Fatalf("被拦截时不应产生订单：%d", n)
	}
	normal, err := markCustomerRisk(ctx, db, customer.ID, "normal", "", now)
	if err != nil {
		t.Fatal(err)
	}
	// 解除限制后不再被风控位拦截（后续因 SKU 不存在而失败，属业务校验）。
	if _, err = placeCustomerSelfOrder(ctx, db, normal, customerOrderInput{
		SkuID: 1, Quantity: 1, IdempotencyKey: "self-order-key-2"}, now); err != nil &&
		strings.Contains(err.Error(), "限制自助下单") {
		t.Fatalf("解除限制后不应再被风控拦截：%v", err)
	}
}
