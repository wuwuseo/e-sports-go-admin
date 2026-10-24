package esports

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/gofiber/fiber/v3"

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
		app.Put("/admin/products/:id", updateSkuHandler)
	})
	baseBody := `"title":"成本商品","service_type":"play","unit_price_cents":1000,` +
		`"service_units":1,"service_unit_name":"局","required_seats":1,"quota_total":-1,"status":"on_sale"`

	// 未传 cost_cents：保持既有成本，不得静默清空。
	status, payload := w4CallJSON(t, app, "PUT", "/admin/products/1", "{"+baseBody+"}")
	if status != fiber.StatusOK {
		t.Fatalf("未传成本应更新成功，status=%d payload=%+v", status, payload)
	}
	if got := db.EsportsSku.GetX(ctx, sku.ID).CostCents; got == nil || *got != 800 {
		t.Fatalf("未传 cost_cents 时成本被改动：%+v", got)
	}

	// 显式 null：清空成本。
	status, payload = w4CallJSON(t, app, "PUT", "/admin/products/1", `{"cost_cents":null,`+baseBody+`}`)
	if status != fiber.StatusOK {
		t.Fatalf("显式传 null 应更新成功，status=%d payload=%+v", status, payload)
	}
	if got := db.EsportsSku.GetX(ctx, sku.ID).CostCents; got != nil {
		t.Fatalf("显式传 null 时成本应清空：%+v", got)
	}

	// 显式传数值：更新成本。
	status, payload = w4CallJSON(t, app, "PUT", "/admin/products/1", `{"cost_cents":500,`+baseBody+`}`)
	if status != fiber.StatusOK {
		t.Fatalf("传数值应更新成功，status=%d payload=%+v", status, payload)
	}
	if got := db.EsportsSku.GetX(ctx, sku.ID).CostCents; got == nil || *got != 500 {
		t.Fatalf("显式传数值时成本应更新：%+v", got)
	}

}

// jsonInt 把整数渲染成 JSON 数字字面量。
func jsonInt(value int) string {
	data, _ := json.Marshal(value)
	return string(data)
}

// P1-23：单个 SKU 配置缺失只降级该 SKU（not_orderable + 中文原因），其它 SKU 报价正常返回；
// 报价请求明确只针对该 SKU（quote_product_id）时才返回明确错误。
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
	app.Get("/admin/products", adminSkus)

	status, payload := w4CallJSON(t, app, "GET",
		"/admin/products?quote_customer_id="+jsonInt(customer.ID)+"&quote_quantity=1", "")
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
	if reason, _ := badQuote["not_orderable_reason"].(string); !strings.Contains(reason, "历史多人商品缺少每人服务量") {
		t.Fatalf("不可下单原因应指明缺失项：%q", reason)
	}

	// 明确只针对该 SKU 报价：仍返回明确错误（400）而不是静默降级。
	status, payload = w4CallJSON(t, app, "GET",
		"/admin/products?quote_customer_id="+jsonInt(customer.ID)+"&quote_quantity=1&quote_product_id="+jsonInt(bad.ID), "")
	if status != fiber.StatusBadRequest {
		t.Fatalf("指定坏 SKU 报价应返回 400，status=%d payload=%+v", status, payload)
	}
	if message, _ := payload["message"].(string); !strings.Contains(message, "历史多人商品缺少每人服务量") {
		t.Fatalf("指定坏 SKU 的错误应点名缺失项：%q", message)
	}

	// 客户不存在属于请求级错误，仍整体失败。
	if status, _ = w4CallJSON(t, app, "GET", "/admin/products?quote_customer_id=99999", ""); status == fiber.StatusOK {
		t.Fatalf("客户不存在时不应返回成功")
	}
}

// P1-22：HTTP 入口同样接线——POST/PUT /admin/products 的 required_level 能落库，
// 非法值返回 400 中文提示，避免「只改了服务层、入口没接」。
func TestW4SkuHandlerRequiredLevel(t *testing.T) {
	db := openProductDB(t, "esports_w4_sku_handler_level")
	defer db.Close()
	w4MountGlobalORM(t, db)
	app := w4AdminApp(t, db, func(app *fiber.App) {
		app.Post("/admin/products", createSku)
		app.Put("/admin/products/:id", updateSkuHandler)
	})

	body := `{"code":"W4-H-LEVEL","title":"入口等级","service_type":"play","unit_price_cents":1000,` +
		`"service_units":1,"service_unit_name":"局","quota_total":-1,` +
		`"status":"on_sale","required_level":"skilled"}`
	status, payload := w4CallJSON(t, app, "POST", "/admin/products", body)
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
	if stored.RequiredSeats != 1 || stored.PerSeatServiceUnits != 0 {
		t.Fatalf("新商品应固定单人服务：%d/%d", stored.RequiredSeats, stored.PerSeatServiceUnits)
	}
	if _, visible := data["required_seats"]; visible {
		t.Fatal("商品接口不应暴露席位字段")
	}

	updateBody := `{"title":"入口等级","service_type":"play","unit_price_cents":1000,"service_units":1,` +
		`"service_unit_name":"局","required_seats":4,"per_seat_service_units":9,"quota_total":-1,"status":"on_sale",`
	status, payload = w4CallJSON(t, app, "PUT", "/admin/products/"+jsonInt(int(id)), updateBody+`"required_level":"mentor"}`)
	if status != fiber.StatusOK {
		t.Fatalf("更新 SKU 应成功，status=%d payload=%+v", status, payload)
	}
	if got := db.EsportsSku.GetX(t.Context(), int(id)).RequiredLevel; got != esportssku.RequiredLevelMentor {
		t.Fatalf("更新入口未落库 required_level：%q", got)
	}
	if got := db.EsportsSku.GetX(t.Context(), int(id)); got.RequiredSeats != 1 || got.PerSeatServiceUnits != 0 {
		t.Fatalf("商品接口不应接受席位输入：%d/%d", got.RequiredSeats, got.PerSeatServiceUnits)
	}

	status, payload = w4CallJSON(t, app, "PUT", "/admin/products/"+jsonInt(int(id)), updateBody+`"required_level":"dragon"}`)
	if status != fiber.StatusBadRequest {
		t.Fatalf("非法技能等级应返回 400，status=%d payload=%+v", status, payload)
	}
	if message, _ := payload["message"].(string); !strings.Contains(message, "技能等级门槛") {
		t.Fatalf("非法技能等级应给出中文提示：%q", message)
	}
}

// 商品内容图片（content）：创建/更新入口写入、地址逐条校验与去重、空列表清空。
func TestW4SkuContentImagesWritePath(t *testing.T) {
	db := openProductDB(t, "esports_w4_sku_content")
	defer db.Close()
	ctx := t.Context()

	sku, err := saveSku(ctx, db, skuInput{
		Code: "W4-CONTENT-01", Title: "内容图片商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
		Content: []string{
			"/uploads/esports-product/a.png",
			" https://cdn.example.com/b.webp ",
			"/uploads/esports-product/a.png",
			"",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sku.Content) != 2 || sku.Content[0] != "/uploads/esports-product/a.png" || sku.Content[1] != "https://cdn.example.com/b.webp" {
		t.Fatalf("内容图片未按去重/去空白落库：%q", sku.Content)
	}

	// 非法地址逐条拒绝：非 http/https 的绝对地址、站内路径带查询串。
	for _, bad := range []string{"ftp://cdn.example.com/x.png", "/uploads/a.png?size=1"} {
		if _, err = saveSku(ctx, db, skuInput{
			Code: "W4-CONTENT-BAD", Title: "非法地址商品", ServiceType: "play", UnitPriceCents: 1000,
			ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
			Content: []string{bad},
		}); !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "内容图片") {
			t.Fatalf("非法内容图片地址应返回参数错误（%q）：%v", bad, err)
		}
	}

	// 更新整体替换；空列表清空（读回为 NULL → nil）。
	replaced, err := updateSku(ctx, db, sku.ID, skuUpdateInput{
		Title: "内容图片商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
		Content: []string{"https://cdn.example.com/c.jpg"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(replaced.Content) != 1 || replaced.Content[0] != "https://cdn.example.com/c.jpg" {
		t.Fatalf("更新未整体替换内容图片：%q", replaced.Content)
	}
	cleared, err := updateSku(ctx, db, sku.ID, skuUpdateInput{
		Title: "内容图片商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
		Content: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared.Content) != 0 {
		t.Fatalf("空列表应清空内容图片：%q", cleared.Content)
	}

	// 超过上限拒绝。
	flood := make([]string, 51)
	for i := range flood {
		flood[i] = fmt.Sprintf("/uploads/esports-product/%02d.png", i)
	}
	if _, err = updateSku(ctx, db, sku.ID, skuUpdateInput{
		Title: "内容图片商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
		Content: flood,
	}); !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "50") {
		t.Fatalf("超过 50 张应返回参数错误：%v", err)
	}
}
