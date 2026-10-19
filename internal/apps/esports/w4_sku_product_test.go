package esports

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/apps/esports/schematier"
	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportssku"
	"go-admin/internal/global"
)

// ── W4：产品 / SKU 层硬缺口（P1-22 / P1-23 / P1-24 + 成本字段语义）──

// w4Ptr 返回字符串指针，便于构造 required_level 入参。
func w4Ptr[T any](value T) *T { return &value }

// w4MountGlobalORM 把内存库挂到 global.ORM 上，供 HTTP handler 走真实入口。
func w4MountGlobalORM(t *testing.T, db *ent.Client) {
	t.Helper()
	previous := global.ORM
	global.ORM = db
	t.Cleanup(func() { global.ORM = previous })
}

// w4AdminApp 构造带操作人 Locals 的测试应用，用于 PUT 类管理端接口。
func w4AdminApp(t *testing.T, db *ent.Client, routes func(app *fiber.App)) *fiber.App {
	t.Helper()
	admin := db.Admin.Create().SetEmail("w4-sku@example.com").SetPassword("test").
		SetUID("w4-admin-sku").SetUsername("w4-admin-sku").SetStatus("active").SaveX(t.Context())
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("uid", admin.UID)
		c.Locals("isSuperAdmin", true)
		return c.Next()
	})
	routes(app)
	return app
}

// w4CallJSON 发一个 JSON 请求并返回状态码与解析后的响应体。
func w4CallJSON(t *testing.T, app *fiber.App, method, path, body string) (int, map[string]any) {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	payload := map[string]any{}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("%s %s 响应不是 JSON：%v", method, path, err)
	}
	return resp.StatusCode, payload
}

// P1-22：required_level 必须可以由创建/更新入口写入并落库；未传保持默认 novice，
// 更新未传不得清空既有门槛；非法枚举值返回参数错误；下单时快照到订单。
func TestW4SkuRequiredLevelWritePath(t *testing.T) {
	db := openProductDB(t, "esports_w4_sku_required_level")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)

	senior := "senior"
	sku, err := saveSku(ctx, db, skuInput{
		Code: "W4-LV-SENIOR", Title: "高等级商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
		RequiredLevel: &senior,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sku.RequiredLevel != esportssku.RequiredLevelSenior {
		t.Fatalf("技能等级门槛未落库：%q", sku.RequiredLevel)
	}

	// 未传 required_level：回落 novice（向后兼容旧前端）。
	plain, err := saveSku(ctx, db, skuInput{
		Code: "W4-LV-DEFAULT", Title: "默认等级商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plain.RequiredLevel != esportssku.RequiredLevelNovice {
		t.Fatalf("未传技能等级时应回落 novice：%q", plain.RequiredLevel)
	}

	// 非法枚举值必须被拒，且错误里点名字段取值范围。
	invalid := "grandmaster"
	if _, err = saveSku(ctx, db, skuInput{
		Code: "W4-LV-BAD", Title: "非法等级商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
		RequiredLevel: &invalid,
	}); !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "技能等级门槛") {
		t.Fatalf("非法技能等级应返回参数错误：%v", err)
	}

	// 更新未传 required_level：保持既有 senior（整表回传不得静默降级）。
	kept, err := updateSku(ctx, db, sku.ID, skuUpdateInput{
		Title: "高等级商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	if kept.RequiredLevel != esportssku.RequiredLevelSenior {
		t.Fatalf("更新未传技能等级时不应降级：%q", kept.RequiredLevel)
	}

	// 显式更新到 mentor 生效；再次非法值被拒。
	mentor := "mentor"
	updated, err := updateSku(ctx, db, sku.ID, skuUpdateInput{
		Title: "高等级商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
		RequiredLevel: &mentor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.RequiredLevel != esportssku.RequiredLevelMentor {
		t.Fatalf("技能等级门槛未更新：%q", updated.RequiredLevel)
	}
	if _, err = updateSku(ctx, db, sku.ID, skuUpdateInput{
		Title: "高等级商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
		RequiredLevel: &invalid,
	}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("更新时非法技能等级应被拒：%v", err)
	}

	// P1-22 下半段：报单把 SKU 门槛快照到订单，派单链路据此拦截低等级陪玩。
	customer := saveCustomerOrFail(t, db, 11)
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"w4-level-key-001", now)
	if err != nil {
		t.Fatal(err)
	}
	if order.RequiredLevel != esportsorder.RequiredLevelMentor {
		t.Fatalf("报单未把 SKU 技能等级快照到订单：%q", order.RequiredLevel)
	}
}

// P1-22：SKU 上的技能等级门槛必须真正生效——等级不足的陪玩不进派单候选，指派被拒；满足者通过。
func TestW4SkuRequiredLevelDispatchGuard(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_w4_sku_level_dispatch")
	defer db.Close()
	ctx := t.Context()
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	now := time.Now().UTC()

	// 通过 SKU 创建入口写入 senior 门槛（不再手工改库），验证「写进去 → 真的生效」闭环。
	senior := "senior"
	sku, err := saveSku(ctx, db, skuInput{
		Code: "W4-GL", Title: "高等级陪玩", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: 10, Status: "on_sale",
		RequiredLevel: &senior,
	})
	if err != nil {
		t.Fatal(err)
	}
	customer := saveCustomerOrFail(t, db, 11)
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"w4-level-key-101", now)
	if err != nil {
		t.Fatal(err)
	}
	db.EsportsOrder.UpdateOneID(order.ID).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).SaveX(ctx)
	order = db.EsportsOrder.GetX(ctx, order.ID)

	// games 为空表示不限分区，因此这里只考察等级门槛。
	lowLevel := seedPlayer(t, db, 9801, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(lowLevel.ID).SetLevel(esportsplayer.LevelNovice).SaveX(ctx)
	eligible := seedPlayer(t, db, 9802, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(eligible.ID).SetLevel(esportsplayer.LevelMentor).SaveX(ctx)

	candidates, err := dispatchCandidates(ctx, db, order, 0, 20, now)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, candidate := range candidates {
		if candidate.PlayerID == lowLevel.ID {
			t.Fatalf("等级不足的陪玩不应进入候选池：%+v", candidates)
		}
		if candidate.PlayerID == eligible.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("满足等级的陪玩应进入候选池：%+v", candidates)
	}
	if _, err = assignDispatchSeat(ctx, db, order.ID, 1, lowLevel.ID, 11, now); !errors.Is(err, errForbidden) {
		t.Fatalf("等级不足应被拒绝接单：%v", err)
	}
	if _, err = assignDispatchSeat(ctx, db, order.ID, 1, eligible.ID, 11, now); err != nil {
		t.Fatalf("满足等级应可接单：%v", err)
	}
}

// SKU 挂到商品父实体与规格组：可选字段落库；引用不存在 / 已下架商品 / 停用规格组 / 席位不匹配都拒绝；
// 更新未传保持既有挂载，传 0 显式解除。
func TestW4SkuMountProductAndSpecGroup(t *testing.T) {
	db := openProductDB(t, "esports_w4_sku_mount")
	defer db.Close()
	ctx := t.Context()

	product, err := saveProduct(ctx, db, productInput{Name: "双人陪玩商品", Status: "on_sale"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := saveSpecGroup(ctx, db, specGroupInput{
		Code: "W4-DUO", Name: "双人规格", PerSeatServiceUnits: 1,
		Seats: []schematier.SeatSpec{
			{SeatIndex: 0, Role: "输出"}, {SeatIndex: 1, Role: "辅助"},
		},
		Status: "enabled",
	})
	if err != nil {
		t.Fatal(err)
	}

	sku, err := saveSku(ctx, db, skuInput{
		Code: "W4-MOUNT", Title: "双人档", ServiceType: "play", UnitPriceCents: 3000,
		ServiceUnits: 2, ServiceUnitName: "局", RequiredSeats: 2, QuotaTotal: -1, Status: "on_sale",
		ProductID: &product.ID, SpecGroupID: &group.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sku.ProductID == nil || *sku.ProductID != product.ID {
		t.Fatalf("SKU 未挂到商品：%+v", sku.ProductID)
	}
	if sku.SpecGroupID == nil || *sku.SpecGroupID != group.ID {
		t.Fatalf("SKU 未挂到规格组：%+v", sku.SpecGroupID)
	}
	// 规格组已声明每席量时 SKU 未传则跟随，避免同规格两套口径。
	if sku.PerSeatServiceUnits != 1 {
		t.Fatalf("每席承诺量应跟随规格组：%d", sku.PerSeatServiceUnits)
	}

	missing := 99999
	if _, err = saveSku(ctx, db, skuInput{
		Code: "W4-MOUNT-BADP", Title: "坏商品挂载", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
		ProductID: &missing,
	}); !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "商品不存在") {
		t.Fatalf("引用不存在的商品应被拒：%v", err)
	}
	if _, err = saveSku(ctx, db, skuInput{
		Code: "W4-MOUNT-BADG", Title: "坏规格挂载", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
		SpecGroupID: &missing,
	}); !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "规格组不存在") {
		t.Fatalf("引用不存在的规格组应被拒：%v", err)
	}

	// 商品下架后不允许再挂载新 SKU。
	offSale, err := updateProduct(ctx, db, product.ID, productInput{Name: "双人陪玩商品", Status: "off_sale"})
	if err != nil {
		t.Fatal(err)
	}
	if offSale.Status != "off_sale" {
		t.Fatalf("商品未下架：%+v", offSale.Status)
	}
	if _, err = saveSku(ctx, db, skuInput{
		Code: "W4-MOUNT-OFF", Title: "下架商品挂载", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
		ProductID: &product.ID,
	}); !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "已下架") {
		t.Fatalf("下架商品不应再接受 SKU 挂载：%v", err)
	}
	if _, err = updateProduct(ctx, db, product.ID, productInput{Name: "双人陪玩商品", Status: "on_sale"}); err != nil {
		t.Fatal(err)
	}

	// 停用规格组不允许挂载。
	disabled, err := updateSpecGroup(ctx, db, group.ID, specGroupInput{
		Code: "W4-DUO", Name: "双人规格", PerSeatServiceUnits: 1,
		Seats:  []schematier.SeatSpec{{SeatIndex: 0, Role: "输出"}, {SeatIndex: 1, Role: "辅助"}},
		Status: "disabled",
	})
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Status != "disabled" {
		t.Fatalf("规格组未停用：%+v", disabled.Status)
	}
	if _, err = saveSku(ctx, db, skuInput{
		Code: "W4-MOUNT-GOFF", Title: "停用规格挂载", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
		SpecGroupID: &group.ID,
	}); !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "已停用") {
		t.Fatalf("停用规格组不应再接受 SKU 挂载：%v", err)
	}

	// 重新启用规格组，继续验证更新路径的挂载语义。
	if _, err = updateSpecGroup(ctx, db, group.ID, specGroupInput{
		Code: "W4-DUO", Name: "双人规格", PerSeatServiceUnits: 1,
		Seats:  []schematier.SeatSpec{{SeatIndex: 0, Role: "输出"}, {SeatIndex: 1, Role: "辅助"}},
		Status: "enabled",
	}); err != nil {
		t.Fatal(err)
	}

	// 更新未传挂载字段：保持既有挂载。
	kept, err := updateSku(ctx, db, sku.ID, skuUpdateInput{
		Title: "双人档", ServiceType: "play", UnitPriceCents: 3000, ServiceUnits: 2,
		ServiceUnitName: "局", RequiredSeats: 2, PerSeatServiceUnits: 1, QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	if kept.ProductID == nil || *kept.ProductID != product.ID || kept.SpecGroupID == nil ||
		*kept.SpecGroupID != group.ID {
		t.Fatalf("更新未传挂载字段时应保持既有挂载：product=%+v group=%+v", kept.ProductID, kept.SpecGroupID)
	}

	// 传 0 显式解除挂载。
	zero := 0
	detached, err := updateSku(ctx, db, sku.ID, skuUpdateInput{
		Title: "双人档", ServiceType: "play", UnitPriceCents: 3000, ServiceUnits: 2,
		ServiceUnitName: "局", RequiredSeats: 2, PerSeatServiceUnits: 1, QuotaTotal: -1, Status: "on_sale",
		ProductID: &zero, SpecGroupID: &zero,
	})
	if err != nil {
		t.Fatal(err)
	}
	if detached.ProductID != nil || detached.SpecGroupID != nil {
		t.Fatalf("传 0 时应解除挂载：product=%+v group=%+v", detached.ProductID, detached.SpecGroupID)
	}
}

// P1-24：多人（required_seats > 1）SKU 创建/更新必须提供正的每席承诺服务量，单人允许缺省。
func TestW4SkuPerSeatPromiseValidation(t *testing.T) {
	db := openProductDB(t, "esports_w4_per_seat")
	defer db.Close()
	ctx := t.Context()

	if _, err := saveSku(ctx, db, skuInput{
		Code: "W4-SEAT-NONE", Title: "缺每席量", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 3, ServiceUnitName: "局", RequiredSeats: 3, QuotaTotal: -1, Status: "on_sale",
	}); !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "每席承诺服务量") {
		t.Fatalf("多人 SKU 缺每席量应在创建入口被拒：%v", err)
	}
	// 单人 SKU 允许缺省（下单回落整单服务量，存量口径不变）。
	solo, err := saveSku(ctx, db, skuInput{
		Code: "W4-SEAT-SOLO", Title: "单人档", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatalf("单人 SKU 应允许缺省每席量：%v", err)
	}
	if solo.PerSeatServiceUnits != 0 {
		t.Fatalf("单人 SKU 缺省时每席量应为 0：%d", solo.PerSeatServiceUnits)
	}

	multi, err := saveSku(ctx, db, skuInput{
		Code: "W4-SEAT-MULTI", Title: "三人档", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 3, ServiceUnitName: "局", RequiredSeats: 3, PerSeatServiceUnits: 1,
		QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	// 更新把每席量清成 0 也必须被拒（避免「改坏」成下单必拒）。
	if _, err = updateSku(ctx, db, multi.ID, skuUpdateInput{
		Title: "三人档", ServiceType: "play", UnitPriceCents: 1000, ServiceUnits: 3,
		ServiceUnitName: "局", RequiredSeats: 3, PerSeatServiceUnits: 0, QuotaTotal: -1, Status: "on_sale",
	}); !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "每席承诺服务量") {
		t.Fatalf("更新清空每席量应被拒：%v", err)
	}

	// 商品子 SKU 入口同样校验：多人子规格缺每席量直接拒。
	if _, err = saveProduct(ctx, db, productInput{
		Name: "多人商品", Status: "on_sale",
		Skus: []productChildSkuInput{{
			Code: "W4-CHILD-NONE", Title: "多人子档", ServiceType: "play", UnitPriceCents: 1000,
			ServiceUnits: 2, ServiceUnitName: "局", RequiredSeats: 2, QuotaTotal: -1, Status: "on_sale",
		}},
	}); !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "每席承诺服务量") {
		t.Fatalf("多人子 SKU 缺每席量应被拒：%v", err)
	}
}

// 成本字段（资金相关，P1-22 回归）：PUT 未传 cost_cents 保持不变，显式 null 才清空，
// 商品入口的子 SKU 成本语义与 SKU 入口一致。
func TestW4CostCentsNullKeepsValue(t *testing.T) {
	db := openProductDB(t, "esports_w4_cost_cents")
	defer db.Close()
	ctx := t.Context()
	w4MountGlobalORM(t, db)

	cost := int64(800)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "W4-COST", Title: "成本商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
		CostCents: &cost,
	})
	if err != nil {
		t.Fatal(err)
	}
	app := w4AdminApp(t, db, func(app *fiber.App) {
		app.Put("/admin/skus/:id", updateSkuHandler)
		app.Put("/admin/products/:id", updateProductHandler)
	})
	baseBody := `"title":"成本商品","service_type":"play","unit_price_cents":1000,` +
		`"service_units":1,"service_unit_name":"局","required_seats":1,"quota_total":-1,"status":"on_sale"`

	// 未传 cost_cents：保持既有成本，不得静默清空。
	status, payload := w4CallJSON(t, app, "PUT", "/admin/skus/1", "{"+baseBody+"}")
	if status != fiber.StatusOK {
		t.Fatalf("未传成本应更新成功，status=%d payload=%+v", status, payload)
	}
	if got := db.EsportsSku.GetX(ctx, sku.ID).CostCents; got == nil || *got != 800 {
		t.Fatalf("未传 cost_cents 时成本被改动：%+v", got)
	}

	// 显式 null：清空成本。
	status, payload = w4CallJSON(t, app, "PUT", "/admin/skus/1", `{"cost_cents":null,`+baseBody+`}`)
	if status != fiber.StatusOK {
		t.Fatalf("显式传 null 应更新成功，status=%d payload=%+v", status, payload)
	}
	if got := db.EsportsSku.GetX(ctx, sku.ID).CostCents; got != nil {
		t.Fatalf("显式传 null 时成本应清空：%+v", got)
	}

	// 显式传数值：更新成本。
	status, payload = w4CallJSON(t, app, "PUT", "/admin/skus/1", `{"cost_cents":500,`+baseBody+`}`)
	if status != fiber.StatusOK {
		t.Fatalf("传数值应更新成功，status=%d payload=%+v", status, payload)
	}
	if got := db.EsportsSku.GetX(ctx, sku.ID).CostCents; got == nil || *got != 500 {
		t.Fatalf("显式传数值时成本应更新：%+v", got)
	}

	// 商品入口的子 SKU 同理：PUT /admin/products 不带子 SKU 成本时保持原值。
	childCost := int64(700)
	product, err := saveProduct(ctx, db, productInput{
		Name: "成本商品组", Status: "on_sale",
		Skus: []productChildSkuInput{{
			Code: "W4-COST-CHILD", Title: "成本子档", ServiceType: "play", UnitPriceCents: 1000,
			ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
			CostCents: optionalInt64{Value: &childCost, Present: true},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	child := db.EsportsSku.Query().Where(esportssku.ProductIDEQ(product.ID)).OnlyX(ctx)
	childBody := `{"name":"成本商品组","status":"on_sale","skus":[{"id":` +
		strings.TrimSpace(jsonInt(child.ID)) + `,"code":"W4-COST-CHILD","title":"成本子档",` +
		`"service_type":"play","unit_price_cents":1000,"service_units":1,"service_unit_name":"局",` +
		`"required_seats":1,"quota_total":-1,"status":"on_sale"}]}`
	status, payload = w4CallJSON(t, app, "PUT", "/admin/products/1", childBody)
	if status != fiber.StatusOK {
		t.Fatalf("商品入口未传子 SKU 成本应成功，status=%d payload=%+v", status, payload)
	}
	if got := db.EsportsSku.GetX(ctx, child.ID).CostCents; got == nil || *got != 700 {
		t.Fatalf("商品入口未传子 SKU 成本时应保持原值：%+v", got)
	}

	childBody = strings.Replace(childBody, `"title":"成本子档",`, `"title":"成本子档","cost_cents":null,`, 1)
	status, payload = w4CallJSON(t, app, "PUT", "/admin/products/1", childBody)
	if status != fiber.StatusOK {
		t.Fatalf("商品入口显式传 null 应成功，status=%d payload=%+v", status, payload)
	}
	if got := db.EsportsSku.GetX(ctx, child.ID).CostCents; got != nil {
		t.Fatalf("商品入口显式传 null 时应清空子 SKU 成本：%+v", got)
	}
}

// jsonInt 把整数渲染成 JSON 数字字面量。
func jsonInt(value int) string {
	data, _ := json.Marshal(value)
	return string(data)
}

// P1-23：单个 SKU 配置缺失只降级该 SKU（not_orderable + 中文原因），其它 SKU 报价正常返回；
// 报价请求明确只针对该 SKU（quote_sku_id）时才返回明确错误。
func TestW4AdminSkusQuoteGracefulDegrade(t *testing.T) {
	db := openProductDB(t, "esports_w4_quote_degrade")
	defer db.Close()
	ctx := t.Context()
	w4MountGlobalORM(t, db)

	good, err := saveSku(ctx, db, skuInput{
		Code: "W4-QUOTE-OK", Title: "正常商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	// 存量坏数据：多人 SKU 未配置每席承诺量（P1-24 起创建入口已拦截，故直接落库模拟）。
	bad := db.EsportsSku.Create().SetCode("W4-QUOTE-BAD").SetTitle("坏商品").
		SetServiceType(esportssku.ServiceTypePlay).SetUnitPriceCents(2000).
		SetServiceUnits(2).SetServiceUnitName("局").SetRequiredSeats(2).
		SetPerSeatServiceUnits(0).SetQuotaTotal(-1).SetStatus(esportssku.StatusOnSale).SaveX(ctx)
	customer := saveCustomerOrFail(t, db, 11)

	app := fiber.New()
	app.Get("/admin/skus", adminSkus)

	status, payload := w4CallJSON(t, app, "GET",
		"/admin/skus?quote_customer_id="+jsonInt(customer.ID)+"&quote_quantity=1", "")
	if status != fiber.StatusOK {
		t.Fatalf("存在坏 SKU 时整页报价不应 400，status=%d payload=%+v", status, payload)
	}
	data, _ := payload["data"].(map[string]any)
	quotes, _ := data["quotes"].(map[string]any)
	if len(quotes) != 2 {
		t.Fatalf("应返回两个 SKU 的报价：%+v", quotes)
	}
	goodQuote, _ := quotes[jsonInt(good.ID)].(map[string]any)
	if goodQuote == nil || goodQuote["not_orderable"] == true {
		t.Fatalf("正常 SKU 应可下单：%+v", goodQuote)
	}
	if payable, _ := goodQuote["payable_cents"].(float64); int64(payable) != 1000 {
		t.Fatalf("正常 SKU 报价不正确：%+v", goodQuote)
	}
	badQuote, _ := quotes[jsonInt(bad.ID)].(map[string]any)
	if badQuote == nil || badQuote["not_orderable"] != true {
		t.Fatalf("配置缺失的 SKU 应标记不可下单：%+v", badQuote)
	}
	if reason, _ := badQuote["not_orderable_reason"].(string); !strings.Contains(reason, "每席承诺服务量") {
		t.Fatalf("不可下单原因应指明缺失项：%q", reason)
	}

	// 明确只针对该 SKU 报价：仍返回明确错误（400）而不是静默降级。
	status, payload = w4CallJSON(t, app, "GET",
		"/admin/skus?quote_customer_id="+jsonInt(customer.ID)+"&quote_quantity=1&quote_sku_id="+jsonInt(bad.ID), "")
	if status != fiber.StatusBadRequest {
		t.Fatalf("指定坏 SKU 报价应返回 400，status=%d payload=%+v", status, payload)
	}
	if message, _ := payload["message"].(string); !strings.Contains(message, "每席承诺服务量") {
		t.Fatalf("指定坏 SKU 的错误应点名缺失项：%q", message)
	}

	// 客户不存在属于请求级错误，仍整体失败。
	if status, _ = w4CallJSON(t, app, "GET", "/admin/skus?quote_customer_id=99999", ""); status == fiber.StatusOK {
		t.Fatalf("客户不存在时不应返回成功")
	}
}

// P1-22：HTTP 入口同样接线——POST/PUT /admin/skus 的 required_level 能落库，
// 非法值返回 400 中文提示，避免「只改了服务层、入口没接」。
func TestW4SkuHandlerRequiredLevel(t *testing.T) {
	db := openProductDB(t, "esports_w4_sku_handler_level")
	defer db.Close()
	w4MountGlobalORM(t, db)
	app := w4AdminApp(t, db, func(app *fiber.App) {
		app.Post("/admin/skus", createSku)
		app.Put("/admin/skus/:id", updateSkuHandler)
	})

	body := `{"code":"W4-H-LEVEL","title":"入口等级","service_type":"play","unit_price_cents":1000,` +
		`"service_units":1,"service_unit_name":"局","required_seats":1,"quota_total":-1,` +
		`"status":"on_sale","required_level":"skilled"}`
	status, payload := w4CallJSON(t, app, "POST", "/admin/skus", body)
	if status != fiber.StatusOK {
		t.Fatalf("创建 SKU 应成功，status=%d payload=%+v", status, payload)
	}
	data, _ := payload["data"].(map[string]any)
	id, _ := data["id"].(float64)
	if id < 1 {
		t.Fatalf("创建响应未返回商品 ID：%+v", payload)
	}
	stored := db.EsportsSku.GetX(t.Context(), int(id))
	if stored.RequiredLevel != esportssku.RequiredLevelSkilled {
		t.Fatalf("创建入口未落库 required_level：%q", stored.RequiredLevel)
	}

	updateBody := `{"title":"入口等级","service_type":"play","unit_price_cents":1000,"service_units":1,` +
		`"service_unit_name":"局","required_seats":1,"quota_total":-1,"status":"on_sale",`
	status, payload = w4CallJSON(t, app, "PUT", "/admin/skus/"+jsonInt(int(id)), updateBody+`"required_level":"mentor"}`)
	if status != fiber.StatusOK {
		t.Fatalf("更新 SKU 应成功，status=%d payload=%+v", status, payload)
	}
	if got := db.EsportsSku.GetX(t.Context(), int(id)).RequiredLevel; got != esportssku.RequiredLevelMentor {
		t.Fatalf("更新入口未落库 required_level：%q", got)
	}

	status, payload = w4CallJSON(t, app, "PUT", "/admin/skus/"+jsonInt(int(id)), updateBody+`"required_level":"dragon"}`)
	if status != fiber.StatusBadRequest {
		t.Fatalf("非法技能等级应返回 400，status=%d payload=%+v", status, payload)
	}
	if message, _ := payload["message"].(string); !strings.Contains(message, "技能等级门槛") {
		t.Fatalf("非法技能等级应给出中文提示：%q", message)
	}
}

// schematierSeat 说明：席位定义直接复用 schematier.SeatSpec，保持与生产入参一致。
