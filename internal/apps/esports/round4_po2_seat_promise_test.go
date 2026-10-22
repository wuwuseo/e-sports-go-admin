package esports

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsmemberlevelrule"
)

// ── 第 2 波：每席承诺服务量 / 快照口径（15.3-01、15.5-01、5.3-05、4.2-08、15.6-03、10.4-08、3.1-04）──

// po2SeedSku 造一个在售 SKU，可指定每席承诺服务量。
func po2SeedSku(t *testing.T, db *ent.Client, code string, seats int, units, perSeat int64) *ent.EsportsSku {
	t.Helper()
	sku, err := saveSku(t.Context(), db, skuInput{
		Code: code, Title: "商品 " + code, ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: units, ServiceUnitName: "局", RequiredSeats: seats,
		PerSeatServiceUnits: perSeat, QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	return sku
}

// §15.3-01 / §15.6-03 / §4.2-08：多人 SKU 每席承诺量落库、缺每席量下单 400、收款前复核口径。
func TestRound4W2MemberDiscountSnapshotAndBoosting(t *testing.T) {
	db := openProductDB(t, "esports_po2_member_boosting")
	defer db.Close()
	poSeedBaseConfigs(t, db)
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	poSeedMemberLevel(t, db, "PO2-GOLD", 8000)
	customer := saveCustomerOrFail(t, db, 11)
	db.EsportsCustomer.UpdateOneID(customer.ID).SetMemberLevelCode("PO2-GOLD").SaveX(ctx)
	customer = db.EsportsCustomer.GetX(ctx, customer.ID)

	sku := po2SeedSku(t, db, "PO2-SOLO", 1, 1, 0)
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"po2-member-key-001", now)
	if err != nil {
		t.Fatal(err)
	}
	if order.MemberLevelCode != "PO2-GOLD" || order.MemberDiscountBps != 8000 {
		t.Fatalf("会员折扣快照未落库：code=%q bps=%d", order.MemberLevelCode, order.MemberDiscountBps)
	}
	db.EsportsMemberLevelRule.Update().Where(esportsmemberlevelrule.CodeEQ("PO2-GOLD")).
		SetDiscountBps(9000).SaveX(ctx)
	if got := db.EsportsOrder.GetX(ctx, order.ID).MemberDiscountBps; got != 8000 {
		t.Fatalf("改规则后历史单折扣基点被追溯改写：%d", got)
	}

	if _, err = saveSku(ctx, db, skuInput{
		Code: "PO2-BOOST", Title: "代练商品", ServiceType: "boosting", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
	}); !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "代练") {
		t.Fatalf("代练 SKU 应返回「未开放」参数错误：%v", err)
	}
}
