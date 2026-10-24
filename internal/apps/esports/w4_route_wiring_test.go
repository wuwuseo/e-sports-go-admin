package esports

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	_ "github.com/mattn/go-sqlite3"
	cmfapp "github.com/wuwuseo/cmf/app"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorderevent"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/global"
)

// esportsAppDeclaration 返回注册表里的 esports 功能集声明（注册在 init() 中，与启用开关无关）。
func esportsAppDeclaration(t *testing.T) cmfapp.App {
	t.Helper()
	for _, app := range cmfapp.Registered() {
		if app.Code == "esports" {
			return app
		}
	}
	t.Fatal("esports 功能集未注册")
	return cmfapp.App{}
}

// normalizeAdminRoutePath 把 Fiber 路由参数（:id）规范化为权限声明写法（{id}）。
func normalizeAdminRoutePath(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if strings.HasPrefix(segment, ":") {
			segments[i] = "{" + strings.TrimPrefix(segment, ":") + "}"
		}
	}
	return strings.Join(segments, "/")
}

// w4PriorityBookingProcessRoute 是处理会员优先预约意向的路由模式（与 app.go 声明一致）。
const w4PriorityBookingProcessRoute = "/admin/priority-bookings/:id/process"

// w4PriorityBookingProcessPath 返回处理意向的具体请求路径。
func w4PriorityBookingProcessPath(intentID int) string {
	return "/admin/priority-bookings/" + strconv.Itoa(intentID) + "/process"
}

// 路由契约（AGENTS.md ）：新接线的「处理会员优先预约」路由必须同时具备
// 方法 + 完整路径完全一致的 PermAPI 声明，且处理函数就是本次实现的 handler。
func TestPriorityBookingProcessRouteDeclaredWithPermission(t *testing.T) {
	app := esportsAppDeclaration(t)
	const wantPath = "/admin/priority-bookings/:id/process"
	const wantPermPath = "/api/v1/esports/admin/priority-bookings/{id}/process"

	var route *cmfapp.Route
	for i := range app.Routes {
		if app.Routes[i].Method == http.MethodPost && app.Routes[i].Path == wantPath {
			route = &app.Routes[i]
		}
	}
	if route == nil {
		t.Fatalf("缺少路由 POST %s", wantPath)
	}
	if route.Auth != cmfapp.AuthAdmin {
		t.Fatalf("处理预约意向应为管理端鉴权（AuthAdmin），got=%v", route.Auth)
	}
	if route.Handler == nil ||
		reflect.ValueOf(route.Handler).Pointer() != reflect.ValueOf(adminResolvePriorityBookingHandler).Pointer() {
		t.Fatal("路由未指向 adminResolvePriorityBookingHandler")
	}

	var perm *cmfapp.Permission
	for i := range app.Permissions {
		if app.Permissions[i].Type == cmfapp.PermAPI && app.Permissions[i].Path == wantPermPath {
			perm = &app.Permissions[i]
		}
	}
	if perm == nil {
		t.Fatalf("缺少 PermAPI 声明 %s", wantPermPath)
	}
	if perm.Method != http.MethodPost || perm.Code != "esports:members:priority_booking_process" ||
		perm.Parent != "esports:member:menu" {
		t.Fatalf("PermAPI 声明不正确：%+v", perm)
	}

	// 更一般的契约：/admin/priority-bookings 下的每条路由都要有同方法、同规范化路径的 PermAPI。
	apiPerms := map[string]bool{}
	for _, item := range app.Permissions {
		if item.Type != cmfapp.PermAPI {
			continue
		}
		apiPerms[item.Method+" "+item.Path] = true
	}
	checked := 0
	for _, item := range app.Routes {
		if item.Auth != cmfapp.AuthAdmin {
			continue
		}
		if item.Path != "/admin/priority-bookings" && !strings.HasPrefix(item.Path, "/admin/priority-bookings/") {
			continue
		}
		checked++
		key := item.Method + " /api/v1/esports" + normalizeAdminRoutePath(item.Path)
		if !apiPerms[key] {
			t.Fatalf("路由 %s 缺少方法 + 路径一致的 PermAPI 声明（期望 %q）", item.Path, key)
		}
	}
	if checked < 2 {
		t.Fatalf("优先预约路由数少于预期（应含 GET 列表 + POST 处理），got=%d", checked)
	}
}

// postJSONToHandler 构造带登录态的最小路由并调用一次 handler，返回状态码与解析后的响应体。
// uid / isSuperAdmin 由 Locals 注入，与 PermissionMiddleware 之后的 handler 运行环境一致；
// route 是 Fiber 路由模式（含 :id），path 是本次请求的具体路径。
func postJSONToHandler(t *testing.T, admin *ent.Admin, isSuper bool,
	handler fiber.Handler, route, path, body string) (int, map[string]any) {
	t.Helper()
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("uid", admin.UID)
		c.Locals("isSuperAdmin", isSuper)
		return c.Next()
	})
	app.Post(route, handler)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("请求 %s 失败：%v", path, err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	return resp.StatusCode, decoded
}

// 新路由的 handler 可达性：处理 pending 意向返回成功并落到 processed，待办随之消解；
// 非法状态 400、非归属客服 403。
func TestPriorityBookingProcessHandlerReachable(t *testing.T) {
	db := openMemberDB(t, "w4_priority_booking_process_route")
	defer db.Close()
	ctx := t.Context()
	previous := global.ORM
	global.ORM = db
	t.Cleanup(func() { global.ORM = previous })

	owner := seedAdminAccount(t, db, "w4-route-owner")
	other := seedAdminAccount(t, db, "w4-route-other")
	seedMemberLevel(t, db, memberLevelInput{Code: "w4-route-gold", Name: "金卡", GrowthThreshold: 0,
		DiscountBps: 10000, PriorityBooking: memberBoolPtr(true)})
	customer := saveCustomerOrFail(t, db, owner.ID)
	customer = db.EsportsCustomer.UpdateOneID(customer.ID).SetMemberLevelCode("w4-route-gold").SaveX(ctx)
	player := seedPlayer(t, db, 88201, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	now := time.Now()
	intent, err := createPriorityBooking(ctx, db, customer, priorityBookingInput{
		PlayerID: player.ID, RequestedStartAt: now.Add(48 * time.Hour).Format(time.RFC3339),
	}, now)
	if err != nil {
		t.Fatalf("提交预约意向失败：%v", err)
	}

	status, body := postJSONToHandler(t, owner, false, adminResolvePriorityBookingHandler,
		w4PriorityBookingProcessRoute, w4PriorityBookingProcessPath(intent.ID), `{"status":"processed"}`)
	if status != http.StatusOK {
		t.Fatalf("处理意向应返回 200，got=%d body=%+v", status, body)
	}
	if data, _ := body["data"].(map[string]any); data == nil || data["status"] != "processed" {
		t.Fatalf("响应应回传已处理意向：%+v", body)
	}
	if got := db.EsportsPriorityBookingIntent.GetX(ctx, intent.ID); got.Status != "processed" {
		t.Fatalf("意向应落 processed，got=%s", got.Status)
	}
	todos, err := csWorkbenchTodos(ctx, db, owner.ID, false, now)
	if err != nil {
		t.Fatalf("工作台待办失败：%v", err)
	}
	if got := csTodoCountByKey(todos, "member_priority_booking"); got != 0 {
		t.Fatalf("处理后待办应消解为 0，got=%d", got)
	}

	// 非法处理结果 → 400。
	second, err := createPriorityBooking(ctx, db, customer, priorityBookingInput{
		PlayerID: player.ID, RequestedStartAt: now.Add(72 * time.Hour).Format(time.RFC3339),
	}, now)
	if err != nil {
		t.Fatalf("再次提交失败：%v", err)
	}
	if status, _ := postJSONToHandler(t, owner, false, adminResolvePriorityBookingHandler,
		w4PriorityBookingProcessRoute, w4PriorityBookingProcessPath(second.ID), `{"status":"done"}`); status != http.StatusBadRequest {
		t.Fatalf("非法状态应返回 400，got=%d", status)
	}
	// 非归属客服 → 403。
	if status, _ := postJSONToHandler(t, other, false, adminResolvePriorityBookingHandler,
		w4PriorityBookingProcessRoute, w4PriorityBookingProcessPath(second.ID), `{"status":"cancelled"}`); status != http.StatusForbidden {
		t.Fatalf("非归属客服应返回 403，got=%d", status)
	}
}

// P1-12 建档去重的第二条路径：手工建档（saveCustomer）填了手机号后同租户唯一，
// 重复建档返回中文冲突提示且不新增记录；不填手机号仍可建多条。
func TestSaveCustomerPhoneDedupeOnCreate(t *testing.T) {
	db := openCSDB(t, "w4_save_customer_phone")
	defer db.Close()
	ctx := t.Context()

	first, err := saveCustomer(ctx, db, 11, customerInput{DisplayName: "手工客户甲", Phone: "13700000001"})
	if err != nil {
		t.Fatalf("首次建档失败：%v", err)
	}
	if first.Phone != "13700000001" {
		t.Fatalf("手机号未落库：%+v", first)
	}
	_, err = saveCustomer(ctx, db, 11, customerInput{DisplayName: "重复手机号", Phone: "13700000001"})
	if !errors.Is(err, errConflict) {
		t.Fatalf("同手机号重复建档应返回冲突，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "已存在客户档案") {
		t.Fatalf("应给出中文冲突提示，实际 %q", err.Error())
	}
	if count := db.EsportsCustomer.Query().CountX(ctx); count != 1 {
		t.Fatalf("冲突时不得新增档案，got=%d", count)
	}

	// 不填手机号不受唯一约束（未采集号码的档案可以有多条）。
	for i := 0; i < 2; i++ {
		if _, err := saveCustomer(ctx, db, 11, customerInput{DisplayName: "无号码客户"}); err != nil {
			t.Fatalf("无手机号建档不应冲突：%v", err)
		}
	}
	// 超长手机号按参数错误拒绝。
	if _, err := saveCustomer(ctx, db, 11, customerInput{
		DisplayName: "超长号码", Phone: strings.Repeat("1", 33),
	}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("超长手机号应返回参数错误，实际 %v", err)
	}
}

// P1-11 权益消费点：下单路径读取会员权益，并把「专属通道 / 会员优先」作为派单优先级标记
// 写进 order.created 订单事件（权益页的「已生效」因此可追溯）。
func TestPlaceOrderRecordsMemberDispatchPriority(t *testing.T) {
	db := openMemberDB(t, "w4_place_order_member_privilege")
	defer db.Close()
	ctx := t.Context()
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)

	level := seedMemberLevel(t, db, memberLevelInput{Code: "w4-order-priv", Name: "黑卡", GrowthThreshold: 0,
		DiscountBps: 9000, DedicatedChannel: memberBoolPtr(true)})
	customer := saveCustomerOrFail(t, db, 11)
	db.EsportsCustomer.UpdateOneID(customer.ID).SetMemberLevelCode(level.Code).SaveX(ctx)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "W4-PRIV-SKU", Title: "会员通道陪玩", ServiceType: "play", UnitPriceCents: 10000,
		ServiceUnits: 1, ServiceUnitName: "小时", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"w4-priv-order-1", now)
	if err != nil {
		t.Fatalf("下单失败：%v", err)
	}
	// 会员折扣仍按权益快照生效：10000 × (1 − 0.9) = 1000 分优惠。
	if order.PayableCents != 9000 {
		t.Fatalf("会员折扣未生效：payable=%d", order.PayableCents)
	}
	event, err := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(order.ID),
		esportsorderevent.EventTypeEQ("order.created"),
	).Only(ctx)
	if err != nil {
		t.Fatalf("订单事件缺失：%v", err)
	}
	marker, ok := event.Details["member_dispatch_priority"].(map[string]any)
	if !ok {
		t.Fatalf("订单事件应记录会员派单优先级标记：%+v", event.Details)
	}
	if marker["dedicated_channel"] != true {
		t.Fatalf("专属通道标记应为 true：%+v", marker)
	}
}

// 报价试算同样读取会员权益，并把派单优先级标记透出给报单页（P1-11 消费点）。
func TestSkuOrderQuoteExposesMemberDispatchPriority(t *testing.T) {
	db := openMemberDB(t, "w4_quote_member_privilege")
	defer db.Close()
	ctx := t.Context()

	level := seedMemberLevel(t, db, memberLevelInput{Code: "w4-quote-priv", Name: "黑卡", GrowthThreshold: 0,
		DiscountBps: 9000, DedicatedChannel: memberBoolPtr(true)})
	customer := saveCustomerOrFail(t, db, 11)
	db.EsportsCustomer.UpdateOneID(customer.ID).SetMemberLevelCode(level.Code).SaveX(ctx)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "W4-QUOTE-SKU", Title: "报价用陪玩", ServiceType: "play", UnitPriceCents: 10000,
		ServiceUnits: 1, ServiceUnitName: "小时", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	quote, err := skuOrderQuote(ctx, db, customer.ID, sku.ID, 1)
	if err != nil {
		t.Fatalf("报价试算失败：%v", err)
	}
	if quote.MemberDiscountBps != 9000 || quote.PayableCents != 9000 {
		t.Fatalf("会员折扣口径不正确：%+v", quote)
	}
	if !quote.MemberPrivilegeActive || !quote.DedicatedChannel || !quote.DispatchPriority {
		t.Fatalf("报价应透出会员权益与派单优先级标记：%+v", quote)
	}

	// 未定级客户：权益未生效、无派单优先级标记。
	plain := saveCustomerOrFail(t, db, 11)
	plainQuote, err := skuOrderQuote(ctx, db, plain.ID, sku.ID, 1)
	if err != nil {
		t.Fatalf("未定级客户报价失败：%v", err)
	}
	if plainQuote.MemberPrivilegeActive || plainQuote.DispatchPriority || plainQuote.MemberDiscountBps != 0 {
		t.Fatalf("未定级客户不应有会员权益：%+v", plainQuote)
	}
}
