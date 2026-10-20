package esports

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/apps/esports/schematier"
	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorderevent"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/global"
)

// 本文件是 round4 商品/订单桶（fix_owner=product-order）的验收回归测试。

// poSeedConfig 写入 / 覆盖一条 esports 系统配置。
func poSeedConfig(t *testing.T, db *ent.Client, key, value string) {
	t.Helper()
	ctx := t.Context()
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(key)).Only(ctx)
	if ent.IsNotFound(err) {
		db.SystemConfig.Create().SetKey(key).SetName(key).SetValue(value).
			SetType("string").SetGroup("esports").SetStatus(1).SaveX(ctx)
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	db.SystemConfig.UpdateOne(item).SetValue(value).SaveX(ctx)
}

// poSeedBaseConfigs 备齐报单必需的两项配置，并关闭重复报单提醒以便隔离被测项。
func poSeedBaseConfigs(t *testing.T, db *ent.Client) {
	t.Helper()
	poSeedConfig(t, db, "app.esports.payment_deadline_minutes", "30")
	poSeedConfig(t, db, duplicateOrderWindowKey, "0")
}

// poSeedCategory 造一个启用中的商品分类（game 即「游戏分区」）。
func poSeedCategory(t *testing.T, db *ent.Client, code, game string) *ent.EsportsProductCategory {
	t.Helper()
	return db.EsportsProductCategory.Create().SetCode(code).SetName(code).
		SetGame(game).SetStatus("enabled").SaveX(t.Context())
}

// poSeedMemberLevel 造一个启用中的会员等级（discount_bps 为折后价基点：8000 即让利 20%）。
func poSeedMemberLevel(t *testing.T, db *ent.Client, code string, discountBps int) {
	t.Helper()
	db.EsportsMemberLevelRule.Create().SetCode(code).SetName(code).
		SetDiscountBps(discountBps).SetStatus("enabled").SaveX(t.Context())
}

// poAdminApp 构造带登录态的最小路由（与 app.go 同路径，去掉权限与归属包装以便聚焦业务口径）。
func poAdminApp(admin *ent.Admin, isSuper bool) *fiber.App {
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("uid", admin.UID)
		c.Locals("isSuperAdmin", isSuper)
		return c.Next()
	})
	app.Get("/admin/orders", adminOrders)
	app.Get("/admin/orders/:id", adminOrder)
	app.Post("/admin/orders", createOrder)
	app.Put("/admin/orders/:id/quote", repriceOrderHandler)
	app.Post("/admin/orders/:id/cancel", cancelOrderHandler)
	app.Post("/admin/orders/:id/close", closeOrderHandler)
	app.Get("/admin/orders/:id/close-form", orderCloseFormHandler)
	app.Get("/admin/skus", adminSkus)
	app.Get("/skus", publicSkus)
	return app
}

// poSendJSON 发起一次带幂等键的 JSON 请求，返回状态码与解析后的响应体。
func poSendJSON(t *testing.T, app *fiber.App, method, path, idemKey string, body any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("请求 %s %s 失败：%v", method, path, err)
	}
	defer resp.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	return resp.StatusCode, payload
}

// poMessage 取响应 message 字段。
func poMessage(payload map[string]any) string {
	msg, _ := payload["message"].(string)
	return msg
}

// ── 4.6-02 订单价明显偏离 SKU 标准价：提醒 + 原因必填（阈值与强制开关可配） ──

func TestRound4PO4602PriceDeviationRequiresReason(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_po_price_deviation")
	defer db.Close()
	ctx := t.Context()
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()

	poSeedBaseConfigs(t, db)
	poSeedConfig(t, db, "app.esports.price_deviation_threshold_bp", "1000")
	poSeedMemberLevel(t, db, "PO-LV-DEV", 8000)
	admin := seedAdminAccount(t, db, "po-dev-admin")
	customer := saveCustomerOrFail(t, db, admin.ID)
	db.EsportsCustomer.UpdateOneID(customer.ID).SetMemberLevelCode("PO-LV-DEV").SaveX(ctx)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "PO-DEV-1", Title: "偏离标准价商品", ServiceType: "play", UnitPriceCents: 10000,
		ServiceUnits: 1, ServiceUnitName: "小时", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	app := poAdminApp(admin, true)
	body := map[string]any{"customer_id": customer.ID, "sku_id": sku.ID, "quantity": 1}

	// 折后 8000 分（偏离 2000bp > 阈值 1000bp）且未填原因 → 拒绝并明确提示。
	status, payload := poSendJSON(t, app, http.MethodPost, "/admin/orders", "po-dev-001", body)
	if status != fiber.StatusConflict || !strings.Contains(poMessage(payload), "偏离") {
		t.Fatalf("超阈值偏离未要求填写原因：status=%d msg=%s", status, poMessage(payload))
	}
	if got := db.EsportsOrder.Query().CountX(ctx); got != 0 {
		t.Fatalf("被拒报单不应落单，当前订单数 %d", got)
	}

	// 填写偏离原因 → 放行，并把偏离度与原因写进订单事件（留痕）。
	withReason := map[string]any{
		"customer_id": customer.ID, "sku_id": sku.ID, "quantity": 1,
		"price_deviation_reason": "老客户议价补偿",
	}
	status, payload = poSendJSON(t, app, http.MethodPost, "/admin/orders", "po-dev-002", withReason)
	if status != fiber.StatusOK {
		t.Fatalf("填写偏离原因后应放行：status=%d msg=%s", status, poMessage(payload))
	}
	data, _ := payload["data"].(map[string]any)
	if data == nil {
		t.Fatalf("响应缺少订单数据：%+v", payload)
	}
	orderID := int(data["id"].(float64))
	event, err := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(orderID),
		esportsorderevent.EventTypeEQ("order.created"),
	).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(event.Details["price_deviation_bp"]) != "2000" {
		t.Fatalf("订单事件未记录偏离度：%+v", event.Details)
	}
	if fmt.Sprint(event.Details["price_deviation_reason"]) != "老客户议价补偿" {
		t.Fatalf("订单事件未记录偏离原因：%+v", event.Details)
	}

	// 关闭强制填写开关（只提醒不拦截）→ 不填原因也放行，仍留痕。
	poSeedConfig(t, db, "app.esports.price_deviation_reason_required", "false")
	status, payload = poSendJSON(t, app, http.MethodPost, "/admin/orders", "po-dev-003", body)
	if status != fiber.StatusOK {
		t.Fatalf("关闭强制开关后应放行：status=%d msg=%s", status, poMessage(payload))
	}
	remindData, _ := payload["data"].(map[string]any)
	remindOrderNo, _ := remindData["order_no"].(string)
	flagged, err := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("order.price_deviation_flagged"),
		esportsoutbox.AggregateRefEQ(remindOrderNo),
	).Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if flagged != 1 {
		t.Fatalf("提醒档应落一条偏离提醒留痕，实际 %d 条", flagged)
	}

	// 阈值置 0（关闭）→ 不再要求原因。
	poSeedConfig(t, db, "app.esports.price_deviation_threshold_bp", "0")
	status, payload = poSendJSON(t, app, http.MethodPost, "/admin/orders", "po-dev-004", body)
	if status != fiber.StatusOK {
		t.Fatalf("关闭偏离判定后应放行：status=%d msg=%s", status, poMessage(payload))
	}
}

// ── 4.6-01 相似单口径：同游戏分区 + 同预约服务时段 ──

func TestRound4PO4601DuplicateCaliberGameAndAppointmentWindow(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_po_dup_caliber")
	defer db.Close()
	ctx := t.Context()
	poSeedBaseConfigs(t, db)
	poSeedConfig(t, db, duplicateOrderWindowKey, "10")

	catA := poSeedCategory(t, db, "PO-CAT-A", "王者荣耀")
	catB := poSeedCategory(t, db, "PO-CAT-B", "英雄联盟")
	newSku := func(code string, categoryID int, duration int) *ent.EsportsSku {
		t.Helper()
		sku, err := saveSku(ctx, db, skuInput{
			Code: code, Title: code, ServiceType: "play", UnitPriceCents: 5000, CategoryID: &categoryID,
			ServiceUnits: 1, ServiceUnitName: "小时", RequiredSeats: 1, QuotaTotal: -1,
			ServiceDurationMinutes: duration, Status: "on_sale",
		})
		if err != nil {
			t.Fatal(err)
		}
		return sku
	}
	skuA1 := newSku("PO-DUP-A1", catA.ID, 60)
	skuA2 := newSku("PO-DUP-A2", catA.ID, 60)
	skuB1 := newSku("PO-DUP-B1", catB.ID, 60)
	customer := saveCustomerOrFail(t, db, 11)

	now := time.Now().UTC()
	base := now.Add(24 * time.Hour).Truncate(time.Hour)
	first, err := placeOrder(ctx, db, 11, orderInput{
		CustomerID: customer.ID, SkuID: skuA1.ID, Quantity: 1, RequestedStartAt: &base,
	}, "po-dup-001", now)
	if err != nil {
		t.Fatal(err)
	}
	now2 := now.Add(time.Second)

	// 同游戏分区、不同 SKU，但预约服务时段重叠 → 判为相似单并回带订单号。
	overlap := base.Add(30 * time.Minute)
	_, err = placeOrder(ctx, db, 11, orderInput{
		CustomerID: customer.ID, SkuID: skuA2.ID, Quantity: 1, RequestedStartAt: &overlap,
	}, "po-dup-002", now2)
	if !errors.Is(err, errDuplicateOrder) {
		t.Fatalf("同游戏分区 + 时段重叠应判为相似单：%v", err)
	}
	if !strings.Contains(err.Error(), first.OrderNo) {
		t.Fatalf("相似单错误未回带订单号 %s：%v", first.OrderNo, err)
	}

	// 同游戏分区、时段不重叠 → 放行。
	apart := base.Add(10 * time.Hour)
	if _, err = placeOrder(ctx, db, 11, orderInput{
		CustomerID: customer.ID, SkuID: skuA2.ID, Quantity: 1, RequestedStartAt: &apart,
	}, "po-dup-003", now2); err != nil {
		t.Fatalf("同游戏不同时段不应判为相似单：%v", err)
	}

	// 不同游戏分区、时段重叠 → 放行。
	otherOverlap := base.Add(30 * time.Minute)
	if _, err = placeOrder(ctx, db, 11, orderInput{
		CustomerID: customer.ID, SkuID: skuB1.ID, Quantity: 1, RequestedStartAt: &otherOverlap,
	}, "po-dup-004", now2); err != nil {
		t.Fatalf("不同游戏分区不应判为相似单：%v", err)
	}

	// 前端回传 confirm_duplicate=true → 放行并留痕。
	confirmed, err := placeOrder(ctx, db, 11, orderInput{
		CustomerID: customer.ID, SkuID: skuA2.ID, Quantity: 1,
		RequestedStartAt: &overlap, ConfirmDuplicate: true,
	}, "po-dup-005", now2)
	if err != nil {
		t.Fatalf("显式确认相似单后应放行：%v", err)
	}
	event, err := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(confirmed.ID),
		esportsorderevent.EventTypeEQ("order.created"),
	).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(event.Details["duplicate_confirmed"]) != "true" {
		t.Fatalf("相似单确认未留痕：%+v", event.Details)
	}
}

// ── 4.6-04 欠款提醒档：默认提醒、可配置升级为强制拦截 ──

func TestRound4PO4604CustomerDebtReminderTier(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_po_debt_remind")
	defer db.Close()
	ctx := t.Context()
	poSeedBaseConfigs(t, db)
	customer := saveCustomerOrFail(t, db, 11)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "PO-DEBT-1", Title: "欠款提醒商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "小时", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	db.EsportsOrder.Create().SetOrderNo("po-debt-overdue").SetCustomerID(customer.ID).SetSkuID(sku.ID).
		SetQuantity(1).SetUnitPriceCents(12000).SetPayableCents(12000).SetPromisedServiceUnits(1).
		SetRequiredSeats(1).SetPaymentDeadline(now.Add(-time.Minute)).SaveX(ctx)

	// 未配置硬拦截阈值（默认）：有欠款只提醒，不拦截报单。
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"po-debt-001", now)
	if err != nil {
		t.Fatalf("默认应提醒而不拦截欠款报单：%v", err)
	}
	reminded, err := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("order.debt_remind"),
		esportsoutbox.AggregateRefEQ(order.OrderNo),
	).Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reminded != 1 {
		t.Fatalf("欠款提醒应落一条事务外盒，实际 %d 条", reminded)
	}
	event, err := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(order.ID),
		esportsorderevent.EventTypeEQ("order.created"),
	).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(event.Details["debt_remind_cents"]) != "12000" {
		t.Fatalf("订单事件未记录欠款提醒金额：%+v", event.Details)
	}

	// 配置硬拦截阈值 10000：欠款 12000 超阈值 → 拒绝。
	poSeedConfig(t, db, customerDebtLimitKey, "10000")
	if _, err = placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"po-debt-002", now); !errors.Is(err, errCustomerDebt) {
		t.Fatalf("超阈值欠款应强制拦截：%v", err)
	}
}

// ── 4.2-02 报单支持人工调整 + 指定负责客服 ──

func TestRound4PO4202ManualAdjustAndResponsibleAdmin(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_po_manual_adjust")
	defer db.Close()
	ctx := t.Context()
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()

	poSeedBaseConfigs(t, db)
	admin := seedAdminAccount(t, db, "po-adjust-admin")
	responsible := seedAdminAccount(t, db, "po-adjust-owner")
	customer := saveCustomerOrFail(t, db, admin.ID)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "PO-ADJ-1", Title: "人工调整商品", ServiceType: "play", UnitPriceCents: 10000,
		MinUnitPriceCents: 5000, AdjustmentApprovalBp: 1000,
		ServiceUnits: 1, ServiceUnitName: "小时", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	app := poAdminApp(admin, true)
	base := map[string]any{
		"customer_id": customer.ID, "sku_id": sku.ID, "quantity": 1,
		"manual_adjust_cents": -500, "responsible_admin_id": responsible.ID,
	}

	// 人工调整未填原因 → 参数错误。
	status, payload := poSendJSON(t, app, http.MethodPost, "/admin/orders", "po-adj-001", base)
	if status != fiber.StatusBadRequest || !strings.Contains(poMessage(payload), "原因") {
		t.Fatalf("人工调整未填写原因应被拒：status=%d msg=%s", status, poMessage(payload))
	}

	// 原因过短 → 参数错误。
	short := map[string]any{}
	for k, v := range base {
		short[k] = v
	}
	short["manual_adjust_reason"] = "贵"
	if status, payload = poSendJSON(t, app, http.MethodPost, "/admin/orders", "po-adj-002", short); status != fiber.StatusBadRequest {
		t.Fatalf("原因过短应被拒：status=%d msg=%s", status, poMessage(payload))
	}

	// 免审阈值内（|−500| ≤ 1000 分）→ 放行，落单即按传入值写入。
	ok := map[string]any{}
	for k, v := range base {
		ok[k] = v
	}
	ok["manual_adjust_reason"] = "老客户议价补偿"
	status, payload = poSendJSON(t, app, http.MethodPost, "/admin/orders", "po-adj-003", ok)
	if status != fiber.StatusOK {
		t.Fatalf("免审额度内的人工调整应放行：status=%d msg=%s", status, poMessage(payload))
	}
	data, _ := payload["data"].(map[string]any)
	orderID := int(data["id"].(float64))
	order := db.EsportsOrder.GetX(ctx, orderID)
	if order.PayableCents != 9500 || order.ManualAdjustCents != -500 {
		t.Fatalf("人工调整未按下单值写入：payable=%d manual=%d", order.PayableCents, order.ManualAdjustCents)
	}
	if order.ResponsibleAdminID == nil || *order.ResponsibleAdminID != responsible.ID {
		t.Fatalf("指定负责客服未写入：%+v", order.ResponsibleAdminID)
	}
	event := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(orderID),
		esportsorderevent.EventTypeEQ("order.created"),
	).OnlyX(ctx)
	if fmt.Sprint(event.Details["manual_adjust_reason"]) != "老客户议价补偿" {
		t.Fatalf("人工调整原因未留痕：%+v", event.Details)
	}

	// 超免审阈值（|−1200| > 1000 分）且未带已审批调价单 → 明确拒绝，提示先走改价审批。
	over := map[string]any{
		"customer_id": customer.ID, "sku_id": sku.ID, "quantity": 1,
		"manual_adjust_cents": -1200, "manual_adjust_reason": "老客户议价补偿",
	}
	status, payload = poSendJSON(t, app, http.MethodPost, "/admin/orders", "po-adj-004", over)
	if status != fiber.StatusBadRequest || !strings.Contains(poMessage(payload), "审批") {
		t.Fatalf("超免审阈值应要求先审批：status=%d msg=%s", status, poMessage(payload))
	}

	// 低于 SKU 最低允许价 → 参数错误。
	low := map[string]any{
		"customer_id": customer.ID, "sku_id": sku.ID, "quantity": 1,
		"manual_adjust_cents": -6000, "manual_adjust_reason": "老客户议价补偿",
	}
	if status, payload = poSendJSON(t, app, http.MethodPost, "/admin/orders", "po-adj-005", low); status != fiber.StatusBadRequest {
		t.Fatalf("低于最低允许价应被拒：status=%d msg=%s", status, poMessage(payload))
	}

	// 指定不存在的负责客服 → 参数错误。
	bad := map[string]any{"customer_id": customer.ID, "sku_id": sku.ID, "quantity": 1, "responsible_admin_id": 999999}
	if status, payload = poSendJSON(t, app, http.MethodPost, "/admin/orders", "po-adj-006", bad); status != fiber.StatusBadRequest {
		t.Fatalf("不存在的负责客服应被拒：status=%d msg=%s", status, poMessage(payload))
	}
}

// ── 4.2-06 折扣上限 + 资金可用余额复核 ──

func TestRound4PO4206DiscountCapAndFundsGuard(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_po_discount_funds")
	defer db.Close()
	ctx := t.Context()
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()

	poSeedBaseConfigs(t, db)
	poSeedConfig(t, db, "app.esports.order_discount_limit_bps", "1000")
	poSeedMemberLevel(t, db, "PO-LV-CAP", 8000)
	admin := seedAdminAccount(t, db, "po-cap-admin")
	customer := saveCustomerOrFail(t, db, admin.ID)
	db.EsportsCustomer.UpdateOneID(customer.ID).SetMemberLevelCode("PO-LV-CAP").SaveX(ctx)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "PO-CAP-1", Title: "折扣上限商品", ServiceType: "play", UnitPriceCents: 10000,
		ServiceUnits: 1, ServiceUnitName: "小时", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	app := poAdminApp(admin, true)
	body := map[string]any{"customer_id": customer.ID, "sku_id": sku.ID, "quantity": 1}

	// 会员等级折扣 8000bp 超单笔折扣上限 1000bp → 参数错误。
	status, payload := poSendJSON(t, app, http.MethodPost, "/admin/orders", "po-cap-001", body)
	if status != fiber.StatusBadRequest || !strings.Contains(poMessage(payload), "折扣") {
		t.Fatalf("超折扣上限未拦截：status=%d msg=%s", status, poMessage(payload))
	}
	if got := db.EsportsOrder.Query().CountX(ctx); got != 0 {
		t.Fatalf("被拒报单不应落单，当前订单数 %d", got)
	}

	// 上限放宽 → 放行。
	poSeedConfig(t, db, "app.esports.order_discount_limit_bps", "10000")
	if status, payload = poSendJSON(t, app, http.MethodPost, "/admin/orders", "po-cap-002", body); status != fiber.StatusOK {
		t.Fatalf("折扣在上限内应放行：status=%d msg=%s", status, poMessage(payload))
	}

	// 站内余额核销但余额为 0 → 明确拒绝（而不是等到收款环节才失败）。
	balance := map[string]any{
		"customer_id": customer.ID, "sku_id": sku.ID, "quantity": 1, "payment_channel": "customer_balance",
	}
	status, payload = poSendJSON(t, app, http.MethodPost, "/admin/orders", "po-cap-003", balance)
	if status != fiber.StatusBadRequest || !strings.Contains(poMessage(payload), "余额") {
		t.Fatalf("余额不足应下单即拒：status=%d msg=%s", status, poMessage(payload))
	}

	// 已下线的储值卡收款方式 → 下单即明确拒绝（不再有卡内资金可复核）。
	card := map[string]any{
		"customer_id": customer.ID, "sku_id": sku.ID, "quantity": 1, "payment_channel": "stored_value_card",
	}
	status, payload = poSendJSON(t, app, http.MethodPost, "/admin/orders", "po-cap-004", card)
	if status != fiber.StatusBadRequest || !strings.Contains(poMessage(payload), "不支持的下单收款方式") {
		t.Fatalf("已下线的储值卡收款方式应下单即拒：status=%d msg=%s", status, poMessage(payload))
	}

	// 未指定站内收款方式（走外部收款）→ 不做资金复核，正常放行。
	if status, payload = poSendJSON(t, app, http.MethodPost, "/admin/orders", "po-cap-005", body); status != fiber.StatusOK {
		t.Fatalf("外部收款不应做站内资金复核：status=%d msg=%s", status, poMessage(payload))
	}
}

// ── 15.5-02 报单时快照 SKU 规格组席位到订单 seat_roles ──

func TestRound4PO15502SeatRolesSnapshotOnPlaceOrder(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_po_seat_roles")
	defer db.Close()
	ctx := t.Context()
	poSeedBaseConfigs(t, db)
	group := db.EsportsSpecGroup.Create().SetCode("PO-GRP-1").SetName("三排规格组").
		SetPerSeatServiceUnits(2).
		SetSeats([]schematier.SeatSpec{
			{SeatIndex: 0, Role: "坦克", Substitutable: true, Spec: "星耀以上", PromisedUnits: 2},
			{SeatIndex: 1, Role: "输出", PromisedUnits: 2},
		}).SaveX(ctx)
	customer := saveCustomerOrFail(t, db, 11)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "PO-SEAT-1", Title: "规格组商品", ServiceType: "play", UnitPriceCents: 5000,
		ServiceUnits: 4, ServiceUnitName: "小时", RequiredSeats: 2, PerSeatServiceUnits: 2,
		QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	db.EsportsSku.UpdateOneID(sku.ID).SetSpecGroupID(group.ID).SaveX(ctx)

	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"po-seat-001", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	roles, err := orderSeatRoles(ctx, db, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 2 || roles[0].Role != "坦克" || roles[1].Role != "输出" || roles[1].PromisedUnits != 2 {
		t.Fatalf("报单未快照规格组席位：%+v", roles)
	}
}

// ── 15.6-01 报单页带走「结单要求 + 会员折扣」：/admin/skus 支持按客户与份数试算 ──

func TestRound4PO15601AdminSkuQuotePreview(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_po_sku_quote")
	defer db.Close()
	ctx := t.Context()
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()

	poSeedMemberLevel(t, db, "PO-LV-QUOTE", 8000)
	admin := seedAdminAccount(t, db, "po-quote-admin")
	customer := saveCustomerOrFail(t, db, admin.ID)
	db.EsportsCustomer.UpdateOneID(customer.ID).SetMemberLevelCode("PO-LV-QUOTE").SaveX(ctx)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "PO-QUOTE-1", Title: "试算商品", ServiceType: "play", UnitPriceCents: 10000,
		ServiceUnits: 3, ServiceUnitName: "局", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	db.EsportsSku.UpdateOneID(sku.ID).SetCloseRuleCode("PO-CLOSE-RULE").SaveX(ctx)

	app := poAdminApp(admin, true)
	status, payload := poSendJSON(t, app, http.MethodGet,
		fmt.Sprintf("/admin/skus?quote_customer_id=%d&quote_quantity=1", customer.ID), "", nil)
	if status != fiber.StatusOK {
		t.Fatalf("试算请求失败：status=%d msg=%s", status, poMessage(payload))
	}
	data, _ := payload["data"].(map[string]any)
	if data == nil {
		t.Fatalf("试算响应缺少数据：%+v", payload)
	}
	quotes, _ := data["quotes"].(map[string]any)
	quote, _ := quotes[strconv.Itoa(sku.ID)].(map[string]any)
	if quote == nil {
		t.Fatalf("试算未返回该 SKU 的报价预览：%+v", data)
	}
	if fmt.Sprint(quote["member_discount_cents"]) != "2000" || fmt.Sprint(quote["payable_cents"]) != "8000" {
		t.Fatalf("会员折扣试算不正确：%+v", quote)
	}
	if fmt.Sprint(quote["close_rule_code"]) != "PO-CLOSE-RULE" {
		t.Fatalf("试算未带出结单要求：%+v", quote)
	}
	if fmt.Sprint(quote["promised_service_units"]) != "3" {
		t.Fatalf("试算未带出承诺服务量：%+v", quote)
	}

	// 公开 /skus 载荷至少带出结单要求与时长，供移动客服端报单页展示。
	_, publicPayload := poSendJSON(t, app, http.MethodGet, "/skus", "", nil)
	publicItems, _ := publicPayload["data"].([]any)
	if len(publicItems) != 1 {
		t.Fatalf("公开商品列表条数异常：%+v", publicPayload)
	}
	publicItem, _ := publicItems[0].(map[string]any)
	if fmt.Sprint(publicItem["close_rule_code"]) != "PO-CLOSE-RULE" {
		t.Fatalf("公开商品列表未带出结单要求：%+v", publicItem)
	}
}

// ── 3.5-05 财务角色可查看全部订单含金额明细 ──

func TestRound4PO3505FinanceRoleOrderScope(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_po_finance_scope")
	defer db.Close()
	ctx := t.Context()
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()

	const roleName = "po-finance-viewer"
	poSeedConfig(t, db, financeReportRoleNamesKey, roleName)
	role := db.Role.Create().SetName(roleName).SetDisplayName("财务只读").SetStatus("active").SaveX(ctx)
	finance := seedAdminAccount(t, db, "po-finance-user")
	db.AdminRole.Create().SetAdminID(finance.ID).SetRoleID(role.ID).SaveX(ctx)
	other := seedAdminAccount(t, db, "po-finance-other")

	order := db.EsportsOrder.Create().SetOrderNo("po-finance-order").SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(30000).SetPayableCents(30000).SetPromisedServiceUnits(1).
		SetRequiredSeats(1).SetPaymentDeadline(time.Now().UTC().Add(time.Hour)).
		SetResponsibleAdminID(other.ID).SaveX(ctx)

	app := poAdminApp(finance, false)
	status, payload := poSendJSON(t, app, http.MethodGet, "/admin/orders", "", nil)
	if status != fiber.StatusOK {
		t.Fatalf("财务角色读取订单列表失败：status=%d msg=%s", status, poMessage(payload))
	}
	items, _ := payload["data"].([]any)
	if len(items) != 1 {
		t.Fatalf("财务角色应看到全部订单，实际 %d 条：%+v", len(items), payload)
	}
	row, _ := items[0].(map[string]any)
	if fmt.Sprint(row["payable_cents"]) != "30000" {
		t.Fatalf("财务角色订单明细应含金额：%+v", row)
	}

	// 详情接口同样放行。
	status, detail := poSendJSON(t, app, http.MethodGet, fmt.Sprintf("/admin/orders/%d", order.ID), "", nil)
	if status != fiber.StatusOK {
		t.Fatalf("财务角色读取订单详情失败：status=%d msg=%s", status, poMessage(detail))
	}

	// 非财务角色仍收敛到本人负责 / 协作，看不到他人订单。
	plain := seedAdminAccount(t, db, "po-plain-user")
	plainApp := poAdminApp(plain, false)
	status, payload = poSendJSON(t, plainApp, http.MethodGet, "/admin/orders", "", nil)
	if status != fiber.StatusOK {
		t.Fatalf("普通客服读取订单列表失败：status=%d", status)
	}
	items, _ = payload["data"].([]any)
	if len(items) != 0 {
		t.Fatalf("普通客服不应看到他人负责的订单：%+v", items)
	}
}
