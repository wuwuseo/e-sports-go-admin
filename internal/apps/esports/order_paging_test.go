package esports

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/global"
)

func seedAdminAccount(t *testing.T, db *ent.Client, uid string) *ent.Admin {
	t.Helper()
	admin, err := db.Admin.Create().
		SetEmail(uid + "@example.com").
		SetPassword("test-password").
		SetUID(uid).
		SetUsername(uid).
		SetStatus("active").
		Save(t.Context())
	if err != nil {
		t.Fatalf("创建管理员失败：%v", err)
	}
	return admin
}

// adminListTestApp 构造带登录态的最小路由：uid / isSuperAdmin 由 Locals 注入，
// 与 PermissionMiddleware 之后的 handler 运行环境一致。
func adminListTestApp(admin *ent.Admin, isSuper bool) *fiber.App {
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("uid", admin.UID)
		c.Locals("isSuperAdmin", isSuper)
		return c.Next()
	})
	app.Get("/admin/orders", adminOrders)
	app.Get("/admin/orders/:id", adminOrder)
	app.Get("/admin/customers", adminCustomers)
	return app
}

func getJSON(t *testing.T, app *fiber.App, path string) (int, *http.Response, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("请求 %s 失败：%v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应失败：%v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("响应不是 JSON：%s", body)
	}
	return resp.StatusCode, resp, payload
}

func seedPagingOrder(t *testing.T, db *ent.Client, orderNo string, customerID, adminID int) *ent.EsportsOrder {
	t.Helper()
	order, err := db.EsportsOrder.Create().
		SetOrderNo(orderNo).
		SetCustomerID(customerID).
		SetSkuID(0).
		SetQuantity(1).
		SetUnitPriceCents(1000).
		SetPayableCents(1000).
		SetPromisedServiceUnits(1).
		SetRequiredSeats(1).
		SetResponsibleAdminID(adminID).
		SetPaymentDeadline(time.Now().Add(time.Hour)).
		Save(t.Context())
	if err != nil {
		t.Fatalf("创建订单 %s 失败：%v", orderNo, err)
	}
	return order
}

// 订单列表超过 100 条后仍能拿到全量计数；两种形态（数组 / 分页对象）与 X-Total-Count 均正确。
func TestAdminOrdersPaginationAndTotal(t *testing.T) {
	db := openAfterSaleDB(t, "esports_order_paging")
	defer db.Close()
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()

	admin := seedAdminAccount(t, db, "cs-paging")
	customer := db.EsportsCustomer.Create().SetDisplayName("分页客户").SetOwnerAdminID(admin.ID).SaveX(t.Context())
	other := db.EsportsCustomer.Create().SetDisplayName("其他客户").SetOwnerAdminID(admin.ID).SaveX(t.Context())

	const total = 105
	for i := 0; i < total; i++ {
		seedPagingOrder(t, db, "PAGE-"+strconv.Itoa(1000+i), customer.ID, admin.ID)
	}
	// 另一位客户的订单用于验证 customer_id 过滤不影响总数口径。
	otherOrder := seedPagingOrder(t, db, "PAGE-OTHER-0001", other.ID, admin.ID)

	app := adminListTestApp(admin, false)

	// 默认形态：数组、最多 100 条、X-Total-Count 为真实总数。
	status, resp, payload := getJSON(t, app, "/admin/orders")
	if status != http.StatusOK {
		t.Fatalf("默认列表状态码：%d", status)
	}
	items, ok := payload["data"].([]any)
	if !ok {
		t.Fatalf("默认形态应为 JSON 数组：%T", payload["data"])
	}
	if len(items) != adminListDefaultLimit {
		t.Fatalf("默认应返回 %d 条，实际 %d 条", adminListDefaultLimit, len(items))
	}
	if got := resp.Header.Get("X-Total-Count"); got != strconv.Itoa(total+1) {
		t.Fatalf("X-Total-Count=%q，期望 %d", got, total+1)
	}

	// limit / offset 形态。
	status, resp, payload = getJSON(t, app, "/admin/orders?limit=10&offset=100")
	if status != http.StatusOK {
		t.Fatalf("limit/offset 状态码：%d", status)
	}
	items = payload["data"].([]any)
	if len(items) != 6 {
		t.Fatalf("offset=100 应剩 6 条，实际 %d 条", len(items))
	}
	if got := resp.Header.Get("X-Total-Count"); got != strconv.Itoa(total+1) {
		t.Fatalf("limit/offset 的 X-Total-Count=%q，期望 %d", got, total+1)
	}

	// page / page_size 形态。
	status, resp, payload = getJSON(t, app, "/admin/orders?page=2&page_size=10")
	if status != http.StatusOK {
		t.Fatalf("page 状态码：%d", status)
	}
	pageData, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("带 page 时应返回分页对象：%T", payload["data"])
	}
	if pageItems, _ := pageData["items"].([]any); len(pageItems) != 10 {
		t.Fatalf("第 2 页应 10 条，实际 %d 条", len(pageItems))
	}
	if pageData["total"] != float64(total+1) || pageData["page"] != float64(2) || pageData["page_size"] != float64(10) {
		t.Fatalf("分页元数据错误：%+v", pageData)
	}
	if got := resp.Header.Get("X-Total-Count"); got != strconv.Itoa(total+1) {
		t.Fatalf("分页形态的 X-Total-Count=%q，期望 %d", got, total+1)
	}

	// customer_id 精确过滤：只返回该客户订单，总数按筛选后口径。
	status, resp, payload = getJSON(t, app, "/admin/orders?customer_id="+strconv.Itoa(customer.ID)+"&page_size=5")
	if status != http.StatusOK {
		t.Fatalf("customer_id 状态码：%d", status)
	}
	pageData = payload["data"].(map[string]any)
	if pageData["total"] != float64(total) {
		t.Fatalf("customer_id 过滤后总数=%v，期望 %d", pageData["total"], total)
	}
	for _, raw := range pageData["items"].([]any) {
		row := raw.(map[string]any)
		if int(row["customer_id"].(float64)) != customer.ID {
			t.Fatalf("customer_id 过滤混入其他客户订单：%+v", row)
		}
	}
	if got := resp.Header.Get("X-Total-Count"); got != strconv.Itoa(total) {
		t.Fatalf("customer_id 的 X-Total-Count=%q，期望 %d", got, total)
	}

	// limit 超上限截断到 500，而不是报错。
	status, _, payload = getJSON(t, app, "/admin/orders?limit=9999")
	if status != http.StatusOK {
		t.Fatalf("limit 超上限应截断而非报错：%d", status)
	}
	if len(payload["data"].([]any)) != total+1 {
		t.Fatalf("limit 截断后应返回全部 %d 条", total+1)
	}

	// 非法参数一律 400。
	for _, path := range []string{
		"/admin/orders?limit=abc",
		"/admin/orders?limit=0",
		"/admin/orders?offset=-1",
		"/admin/orders?page=0",
		"/admin/orders?page_size=0",
		"/admin/orders?customer_id=x",
		"/admin/orders?fulfillment_status=unknown",
		"/admin/orders?payment_status=unknown",
	} {
		status, _, payload = getJSON(t, app, path)
		if status != http.StatusBadRequest || payload["code"] != float64(http.StatusBadRequest) {
			t.Fatalf("%s 应返回 400，实际 status=%d body=%+v", path, status, payload)
		}
	}

	// 越权：协作前其他客服看不到该订单。
	outsider := seedAdminAccount(t, db, "cs-outsider")
	outsiderApp := adminListTestApp(outsider, false)
	status, _, _ = getJSON(t, outsiderApp, "/admin/orders/"+strconv.Itoa(otherOrder.ID))
	if status != http.StatusNotFound {
		t.Fatalf("非负责客服读取订单应为 404，实际 %d", status)
	}
}

// 订单号模糊搜索：只命中 order_no 子串。
func TestAdminOrdersKeywordSearch(t *testing.T) {
	db := openAfterSaleDB(t, "esports_order_keyword")
	defer db.Close()
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()

	admin := seedAdminAccount(t, db, "cs-keyword")
	customer := db.EsportsCustomer.Create().SetDisplayName("搜索客户").SetOwnerAdminID(admin.ID).SaveX(t.Context())
	seedPagingOrder(t, db, "KW-ALPHA-0001", customer.ID, admin.ID)
	seedPagingOrder(t, db, "KW-BETA-0002", customer.ID, admin.ID)
	seedPagingOrder(t, db, "ZZ-0003", customer.ID, admin.ID)

	app := adminListTestApp(admin, false)
	status, resp, payload := getJSON(t, app, "/admin/orders?keyword=ALPHA")
	if status != http.StatusOK {
		t.Fatalf("keyword 状态码：%d", status)
	}
	items := payload["data"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["order_no"] != "KW-ALPHA-0001" {
		t.Fatalf("keyword 命中结果错误：%+v", items)
	}
	if got := resp.Header.Get("X-Total-Count"); got != "1" {
		t.Fatalf("keyword 的 X-Total-Count=%q，期望 1", got)
	}
}

// 客户列表：display_name / phone 模糊搜索与状态过滤，默认仍返回数组。
func TestAdminCustomersSearchAndTotal(t *testing.T) {
	db := openAfterSaleDB(t, "esports_customer_search")
	defer db.Close()
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()

	admin := seedAdminAccount(t, db, "cs-customer-search")
	db.EsportsCustomer.Create().SetDisplayName("阿尔法").SetPhone("13800000001").
		SetFollowStatus(esportscustomer.FollowStatusFollowing).SetOwnerAdminID(admin.ID).SaveX(t.Context())
	db.EsportsCustomer.Create().SetDisplayName("贝塔").SetPhone("13900000002").
		SetFollowStatus(esportscustomer.FollowStatusNew).SetOwnerAdminID(admin.ID).SaveX(t.Context())
	for i := 0; i < 3; i++ {
		db.EsportsCustomer.Create().SetDisplayName("批量" + strconv.Itoa(i)).SetOwnerAdminID(admin.ID).SaveX(t.Context())
	}

	app := adminListTestApp(admin, false)

	status, resp, payload := getJSON(t, app, "/admin/customers")
	if status != http.StatusOK {
		t.Fatalf("客户默认列表状态码：%d", status)
	}
	if _, ok := payload["data"].([]any); !ok {
		t.Fatalf("客户默认形态应为数组：%T", payload["data"])
	}
	if got := resp.Header.Get("X-Total-Count"); got != "5" {
		t.Fatalf("客户 X-Total-Count=%q，期望 5", got)
	}

	// 昵称模糊。
	_, _, payload = getJSON(t, app, "/admin/customers?keyword=阿尔法")
	items := payload["data"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["display_name"] != "阿尔法" {
		t.Fatalf("昵称搜索错误：%+v", items)
	}

	// 手机号模糊。
	_, _, payload = getJSON(t, app, "/admin/customers?keyword=1390000")
	items = payload["data"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["phone"] != "13900000002" {
		t.Fatalf("手机号搜索错误：%+v", items)
	}

	// 跟进状态过滤 + 分页对象。
	_, _, payload = getJSON(t, app, "/admin/customers?follow_status=following&page=1&page_size=10")
	pageData := payload["data"].(map[string]any)
	if pageData["total"] != float64(1) || len(pageData["items"].([]any)) != 1 {
		t.Fatalf("跟进状态过滤错误：%+v", pageData)
	}

	// 非法状态 400。
	status, _, _ = getJSON(t, app, "/admin/customers?risk_status=unknown")
	if status != http.StatusBadRequest {
		t.Fatalf("非法 risk_status 应 400，实际 %d", status)
	}
}
