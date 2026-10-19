package esports

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsorderevent"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/global"
)

// P0-13：历史订单补录的定位是「让管理员补录任意客户的历史单」，此前 backfillOrder 硬校验
// 「客户归属 = 本人」且 handler 不透传超管标识，导致超管无法补录他人客户订单。
// 本文件锁定修复后的权限边界与留痕要求：
//  1. 非超管补录他人客户 → 拒绝；
//  2. 超管补录他人客户 → 成功，且订单事件 / 外盒记录操作人、被补录客户与跨归属标记；
//  3. 非超管补录本人客户 → 成功（原有能力不回退）；
//  4. 补录原因缺失 → 拒绝（高危写操作必须留痕）。

// backfillTestApp 构造带登录态的最小补录路由：uid / isSuperAdmin 由 Locals 注入，
// 与 PermissionMiddleware 之后的 handler 运行环境一致。
func backfillTestApp(admin *ent.Admin, isSuper bool) *fiber.App {
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("uid", admin.UID)
		c.Locals("isSuperAdmin", isSuper)
		return c.Next()
	})
	app.Post("/admin/orders/backfill", backfillOrderHandler)
	return app
}

// postBackfill 发送一次补录请求并解析响应信封（状态码 + 统一 {code,message,data} 结构）。
func postBackfill(t *testing.T, app *fiber.App, key, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/admin/orders/backfill", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("补录请求失败：%v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应失败：%v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("响应不是 JSON：%s", raw)
	}
	return resp.StatusCode, out
}

// backfillSeedAdmin 创建一名可登录的管理员账号（超管标识由 Locals 注入，账号本身只提供 uid）。
func backfillSeedAdmin(t *testing.T, db *ent.Client, uid string) *ent.Admin {
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

// findBackfillEvent 返回补录单的 order.backfilled 订单事件（不存在则失败）。
func findBackfillEvent(t *testing.T, db *ent.Client, orderID int) *ent.EsportsOrderEvent {
	t.Helper()
	event, err := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(orderID),
		esportsorderevent.EventTypeEQ("order.backfilled"),
	).Only(t.Context())
	if err != nil {
		t.Fatalf("缺少 order.backfilled 订单事件：%v", err)
	}
	return event
}

// findBackfillOutbox 返回补录单的 order.backfilled 外盒审计记录（不存在则失败）。
func findBackfillOutbox(t *testing.T, db *ent.Client, orderNo string) *ent.EsportsOutbox {
	t.Helper()
	item, err := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("order.backfilled"),
		esportsoutbox.AggregateRefEQ(orderNo),
	).Only(t.Context())
	if err != nil {
		t.Fatalf("缺少 order.backfilled 外盒审计：%v", err)
	}
	return item
}

// 非超管补录他人客户订单被拒；超管补录同一客户的同一张单成功，且留痕记录操作人与跨归属。
func TestBackfillOrderSuperAdminCanBackfillOtherOwnersCustomer(t *testing.T) {
	db := openAfterSaleDB(t, "esports_backfill_super_permission")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	settled := now.AddDate(0, 0, -12).Format(time.RFC3339)
	// 客户归属客服 22，操作人是超管 99。
	customer := saveCustomerOrFail(t, db, 22)

	in := backfillOrderInput{
		CustomerID: customer.ID, Quantity: 2, UnitPriceCents: 3000,
		SettledAt: settled, Reason: "客户历史线下成交漏报，超管代为补录",
	}
	// 1) 非超管（同一操作人）补录他人客户：必须被拒且不落单。
	if _, err := backfillOrderWithRole(ctx, db, 99, false, in, "backfill-super-denied-0001", now); !errors.Is(err, errForbidden) {
		t.Fatalf("非超管补录他人客户未被拦截：%v", err)
	}
	if n := db.EsportsOrder.Query().CountX(ctx); n != 0 {
		t.Fatalf("被拒的补录仍写了订单：%d", n)
	}
	if n := db.EsportsIdempotency.Query().CountX(ctx); n != 0 {
		t.Fatalf("被拒的补录仍写了幂等记录：%d", n)
	}
	// 2) 超管补录他人客户：成功，且责任客服仍是客户归属客服（业绩不记到补录人头上）。
	order, err := backfillOrderWithRole(ctx, db, 99, true, in, "backfill-super-allowed-0001", now)
	if err != nil {
		t.Fatalf("超管补录他人客户失败：%v", err)
	}
	if order.CustomerID != customer.ID || order.EntryPoint != esportsorder.EntryPointAdminBackfill {
		t.Fatalf("超管补录单信息不正确：%+v", order)
	}
	if order.CreatedByAdminID == nil || *order.CreatedByAdminID != 99 {
		t.Fatalf("补录人应记为操作人：%+v", order.CreatedByAdminID)
	}
	if order.ResponsibleAdminID == nil || *order.ResponsibleAdminID != 22 {
		t.Fatalf("责任客服应回落到客户归属客服：%+v", order.ResponsibleAdminID)
	}
	if order.BackfillReason != in.Reason || order.BackfilledByAdminID == nil ||
		*order.BackfilledByAdminID != 99 || order.BackfilledAt == nil {
		t.Fatalf("补录留痕字段不正确：%+v", order)
	}
	// 3) 订单事件留痕：操作人、被补录客户、客户归属、跨归属标记。
	event := findBackfillEvent(t, db, order.ID)
	if event.ActorType != "admin" || event.ActorID != "99" {
		t.Fatalf("事件操作人记录不正确：%+v", event)
	}
	details := event.Details
	if numOf(details["operator_admin_id"]) != 99 || numOf(details["customer_id"]) != customer.ID ||
		numOf(details["customer_owner_admin_id"]) != 22 || numOf(details["responsible_admin_id"]) != 22 {
		t.Fatalf("事件未记录操作人 / 客户 / 归属：%+v", details)
	}
	if details["cross_owner"] != true || details["is_super_admin"] != true {
		t.Fatalf("事件未标记超管跨归属补录：%+v", details)
	}
	if details["reason"] != in.Reason {
		t.Fatalf("事件未记录补录原因：%+v", details)
	}
	// 4) 外盒审计同样保留追溯要素。
	payload := findBackfillOutbox(t, db, order.OrderNo).Payload
	if numOf(payload["operator_admin_id"]) != 99 || numOf(payload["customer_id"]) != customer.ID ||
		payload["cross_owner"] != true || payload["is_super_admin"] != true {
		t.Fatalf("外盒审计缺少跨归属补录要素：%+v", payload)
	}
}

// numOf 把 JSON 反序列化得到的数字统一按 int 比较（memory JSON 字段读回为 float64）。
func numOf(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return -1
	}
}

// 非超管补录本人客户仍然成功，且事件标记为非跨归属，避免修复把原有能力一起收紧。
func TestBackfillOrderNonSuperBackfillOwnCustomerStillWorks(t *testing.T) {
	db := openAfterSaleDB(t, "esports_backfill_non_super_own")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	settled := now.AddDate(0, 0, -9).Format(time.RFC3339)
	customer := saveCustomerOrFail(t, db, 22)

	order, err := backfillOrderWithRole(ctx, db, 22, false, backfillOrderInput{
		CustomerID: customer.ID, Quantity: 1, UnitPriceCents: 1500,
		SettledAt: settled, Reason: "客服为本人客户补录历史成交",
	}, "backfill-own-0001", now)
	if err != nil {
		t.Fatalf("非超管补录本人客户失败：%v", err)
	}
	if order.ResponsibleAdminID == nil || *order.ResponsibleAdminID != 22 {
		t.Fatalf("本人客户补录的责任客服不正确：%+v", order.ResponsibleAdminID)
	}
	details := findBackfillEvent(t, db, order.ID).Details
	if details["cross_owner"] != false || details["is_super_admin"] != false {
		t.Fatalf("同归属补录不应标记跨归属：%+v", details)
	}
	// 旧的非超管入口（backfillOrder）保持同一语义：本人客户可补录、他人客户被拒。
	if _, err := backfillOrder(ctx, db, 22, backfillOrderInput{
		CustomerID: customer.ID, Quantity: 1, UnitPriceCents: 1500,
		SettledAt: settled, Reason: "客服为本人客户补录历史成交",
	}, "backfill-own-0002", now); err != nil {
		t.Fatalf("backfillOrder 本人客户补录失败：%v", err)
	}
}

// 补录原因缺失 / 过短被拒，并给出中文提示（高危写操作必须留痕原因）。
func TestBackfillOrderRequiresReason(t *testing.T) {
	db := openAfterSaleDB(t, "esports_backfill_reason_required")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)
	settled := now.AddDate(0, 0, -2).Format(time.RFC3339)
	customer := saveCustomerOrFail(t, db, 22)

	cases := []struct {
		name   string
		reason string
	}{
		{"原因为空", ""},
		{"原因仅空白", "    "},
		{"原因过短", "漏报"},
		{"原因超长", strings.Repeat("长", 256)},
	}
	for _, tc := range cases {
		_, err := backfillOrderWithRole(ctx, db, 22, true, backfillOrderInput{
			CustomerID: customer.ID, Quantity: 1, UnitPriceCents: 1500,
			SettledAt: settled, Reason: tc.reason,
		}, "backfill-reason-0001", now)
		if !errors.Is(err, errInvalidInput) {
			t.Fatalf("%s：未拦截，err=%v", tc.name, err)
		}
		if !strings.Contains(err.Error(), "补录原因必填") {
			t.Fatalf("%s：缺少中文提示，err=%v", tc.name, err)
		}
	}
	if n := db.EsportsOrder.Query().CountX(ctx); n != 0 {
		t.Fatalf("原因不合法的补录仍落单：%d", n)
	}
}

// handler 层验证 isSuperAdmin 透传：同一操作人，非超管 403、超管 200，且超管请求确实留痕。
func TestBackfillOrderHandlerPassesSuperAdminFlag(t *testing.T) {
	db := openAfterSaleDB(t, "esports_backfill_handler_super")
	defer db.Close()
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 22)
	operator := backfillSeedAdmin(t, db, "uid-backfill-super")
	settled := time.Now().UTC().AddDate(0, 0, -4).Format(time.RFC3339)
	body := `{"customer_id":` + strconv.Itoa(customer.ID) +
		`,"quantity":1,"unit_price_cents":1200,"settled_at":"` + settled +
		`","reason":"客户线下历史成交漏报补录"}`

	// 非超管：他人客户补录 403。
	status, payload := postBackfill(t, backfillTestApp(operator, false), "handler-backfill-0001", body)
	if status != fiber.StatusForbidden {
		t.Fatalf("非超管补录他人客户应 403，实得 %d %+v", status, payload)
	}
	if n := db.EsportsOrder.Query().CountX(ctx); n != 0 {
		t.Fatalf("被拒请求仍写了订单：%d", n)
	}
	// 超管：同一请求 200，并留存操作人信息。
	status, payload = postBackfill(t, backfillTestApp(operator, true), "handler-backfill-0002", body)
	if status != fiber.StatusOK {
		t.Fatalf("超管补录他人客户应 200，实得 %d %+v", status, payload)
	}
	order, err := db.EsportsOrder.Query().Where(
		esportsorder.CustomerIDEQ(customer.ID),
	).Only(ctx)
	if err != nil {
		t.Fatalf("超管补录未落单：%v", err)
	}
	details := findBackfillEvent(t, db, order.ID).Details
	if numOf(details["operator_admin_id"]) != operator.ID || details["cross_owner"] != true {
		t.Fatalf("handler 未透传超管标识或未留痕：%+v", details)
	}
	// 原因缺失：handler 返回 400 与中文提示。
	status, payload = postBackfill(t, backfillTestApp(operator, true), "handler-backfill-0003",
		`{"customer_id":`+strconv.Itoa(customer.ID)+`,"quantity":1,"unit_price_cents":1200,"settled_at":"`+settled+`"}`)
	if status != fiber.StatusBadRequest {
		t.Fatalf("缺少补录原因应 400，实得 %d %+v", status, payload)
	}
	if message, _ := payload["message"].(string); !strings.Contains(message, "补录原因必填") {
		t.Fatalf("缺少补录原因未给出中文提示：%+v", payload)
	}
}
