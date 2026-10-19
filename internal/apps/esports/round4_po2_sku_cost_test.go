package esports

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/global"
)

// ── 第 2 波：SKU 成本字段与按角色只读可见（15.8-03） ──

func po2SkuCostApp(admin *ent.Admin, isSuper bool) *fiber.App {
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("uid", admin.UID)
		c.Locals("isSuperAdmin", isSuper)
		return c.Next()
	})
	app.Get("/admin/skus", adminSkus)
	return app
}

func po2FetchSkus(t *testing.T, app *fiber.App) []map[string]any {
	t.Helper()
	req := httptest.NewRequest("GET", "/admin/skus", nil)
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("商品列表状态码 %d", resp.StatusCode)
	}
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("商品列表响应不是 JSON：%v", err)
	}
	return payload.Data
}

func TestRound4W2SkuCostVisibility(t *testing.T) {
	db := openProductDB(t, "esports_po2_sku_cost")
	defer db.Close()
	ctx := t.Context()

	cost := int64(600)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "PO2-COST", Title: "成本商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1,
		Status: "on_sale", CostCents: &cost,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sku.CostCents == nil || *sku.CostCents != 600 {
		t.Fatalf("SKU 成本未落库：%+v", sku.CostCents)
	}

	negative := int64(-1)
	if _, err = saveSku(ctx, db, skuInput{
		Code: "PO2-COST-NEG", Title: "负成本商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1,
		Status: "on_sale", CostCents: &negative,
	}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("负成本应被拒：%v", err)
	}

	// 显式不传成本（nil）表示清空已登记成本。
	cleared, err := updateSku(ctx, db, sku.ID, skuUpdateInput{
		Title: "成本商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.CostCents != nil {
		t.Fatalf("未传成本时应清空：%+v", cleared.CostCents)
	}
	db.EsportsSku.UpdateOneID(sku.ID).SetCostCents(600).SaveX(ctx)

	previousORM := global.ORM
	global.ORM = db
	defer func() { global.ORM = previousORM }()
	admin := db.Admin.Create().SetEmail("po2-cost@example.com").SetPassword("test").
		SetUID("po2-admin-cost").SetUsername("po2-admin-cost").SetStatus("active").SaveX(ctx)

	// 超级管理员可见成本。
	superItems := po2FetchSkus(t, po2SkuCostApp(admin, true))
	if len(superItems) != 1 {
		t.Fatalf("商品列表条数不正确：%d", len(superItems))
	}
	if got, _ := superItems[0]["cost_cents"].(float64); int64(got) != 600 {
		t.Fatalf("超管应可见 SKU 成本：%+v", superItems[0])
	}

	// 未授权角色（未配置 sku_cost_view_roles）：成本字段不可见。
	staffItems := po2FetchSkus(t, po2SkuCostApp(admin, false))
	if len(staffItems) != 1 {
		t.Fatalf("商品列表条数不正确：%d", len(staffItems))
	}
	if staffItems[0]["cost_cents"] != nil {
		t.Fatalf("未授权角色不应看到 SKU 成本：%+v", staffItems[0])
	}
	if got, _ := staffItems[0]["unit_price_cents"].(float64); int64(got) != 1000 {
		t.Fatalf("未授权角色仍应看到售价：%+v", staffItems[0])
	}
}
