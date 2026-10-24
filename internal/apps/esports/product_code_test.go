package esports

import (
	"testing"

	"go-admin/internal/ent/systemconfig"
)

// 商品编码自动生成：未传 code 时按「前缀-全局流水」生成，连续新建序号递增；
// 显式传 code 时不覆盖。
func TestSaveSkuAutoGeneratesCode(t *testing.T) {
	db := openCloseRuleDB(t, "esports_product_code_auto")
	defer db.Close()
	ctx := t.Context()

	base := skuInput{
		Title: "自动编码商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "小时", RequiredSeats: 1,
		QuotaTotal: -1, Status: "draft",
	}
	first, err := saveSku(ctx, db, base)
	if err != nil {
		t.Fatal(err)
	}
	if first.Code != "SKU-000001" {
		t.Fatalf("首个自动编码异常：%s", first.Code)
	}
	base.Title = "自动编码商品二"
	second, err := saveSku(ctx, db, base)
	if err != nil {
		t.Fatal(err)
	}
	if second.Code != "SKU-000002" {
		t.Fatalf("流水应递增：%s", second.Code)
	}

	// 前缀与位数取系统配置。
	db.SystemConfig.Create().SetKey(skuCodePrefixKey).SetName("前缀").
		SetValue("GAME").SetGroup("esports").SetType("string").SetStatus(1).SaveX(ctx)
	db.SystemConfig.Create().SetKey(skuCodeWidthKey).SetName("位数").
		SetValue("3").SetGroup("esports").SetType("number").SetStatus(1).SaveX(ctx)
	base.Title = "自动编码商品三"
	third, err := saveSku(ctx, db, base)
	if err != nil {
		t.Fatal(err)
	}
	if third.Code != "GAME-001" {
		t.Fatalf("应按配置前缀与位数生成：%s", third.Code)
	}

	// 非法位数回落默认 6 位。
	db.SystemConfig.Update().Where(systemconfig.KeyEQ(skuCodeWidthKey)).
		SetValue("99").SaveX(ctx)
	base.Title = "自动编码商品四"
	fourth, err := saveSku(ctx, db, base)
	if err != nil {
		t.Fatal(err)
	}
	if fourth.Code != "GAME-000002" {
		t.Fatalf("非法位数应回落 6 位且流水继续递增：%s", fourth.Code)
	}

	// 显式传 code 时按原样落库。
	base.Title = "手工编码商品"
	base.Code = "MANUAL-1"
	manual, err := saveSku(ctx, db, base)
	if err != nil {
		t.Fatal(err)
	}
	if manual.Code != "MANUAL-1" {
		t.Fatalf("显式编码不应被覆盖：%s", manual.Code)
	}
}
