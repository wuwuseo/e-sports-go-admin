package esports

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsstoredvalueexception"
	"go-admin/internal/global"
)

// 本文件是 round4 wallet-member 桶 §4.6-06「异常队列查询 / 指派 / 处置」的验收回归测试。

// sveSeedException 直接入队一条异常记录（已实现部分），返回记录本身。
func sveSeedException(t *testing.T, db *ent.Client, in storedValueExceptionInput, now time.Time) *ent.EsportsStoredValueException {
	t.Helper()
	row, err := recordStoredValueException(t.Context(), db, in, now)
	if err != nil {
		t.Fatalf("入队异常失败：%v", err)
	}
	return row
}

// sveSeedAdmin 造一个**指定 ID** 的管理员。
// 本文件的断言要固定「指派给谁」（22 / 33 / 44），所以不能依赖自增 ID；
// 而 assignStoredValueException 会校验跟进人存在（stored_value_exception.go 的 Admin.Exist），
// 所以这些 ID 必须真实落库，否则会以「跟进人不存在」失败。
func sveSeedAdmin(t *testing.T, db *ent.Client, id int, uid string) *ent.Admin {
	t.Helper()
	row, err := db.Admin.Create().SetID(id).SetEmail(uid + "@example.com").SetPassword("test").
		SetUID(uid).SetUsername(uid).SetStatus("active").Save(t.Context())
	if err != nil {
		t.Fatalf("造管理员 %d 失败：%v", id, err)
	}
	return row
}

// sveAdminApp 构造带登录态的最小路由（同 app.go 路径，去掉权限包装以便聚焦业务口径）。
func sveAdminApp(admin *ent.Admin, isSuper bool) *fiber.App {
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("uid", admin.UID)
		c.Locals("isSuperAdmin", isSuper)
		return c.Next()
	})
	app.Get("/admin/stored-value-exceptions", adminStoredValueExceptions)
	app.Get("/admin/stored-value-exceptions/:id", adminStoredValueExceptionDetail)
	app.Post("/admin/stored-value-exceptions/:id/assign", assignStoredValueExceptionHandler)
	app.Post("/admin/stored-value-exceptions/:id/resolve", resolveStoredValueExceptionHandler)
	return app
}

// §4.6-06：异常队列的查询（默认只看待跟进）、指派、处置与终态让出 dedup_key 的完整生命周期。
func TestSveQueueListAssignResolveLifecycle(t *testing.T) {
	db := openStoredValueDB(t, "esports_sve_queue_lifecycle")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	card := sveSeedActiveCard(t, db, 11, 3000, "sve-queue-open-1")
	order := sveSeedCardOrder(t, db, "sve-queue-order-1", card.ID)
	// 跟进人必须真实存在（指派时校验）；999999 不造，用于断言「跟进人不存在」。
	for _, id := range []int{22, 33, 44} {
		sveSeedAdmin(t, db, id, "sve-admin-"+strconv.Itoa(id))
	}

	link := sveSeedException(t, db, storedValueExceptionInput{
		CardID: card.ID, OrderID: order.ID, CustomerID: order.CustomerID,
		Kind: esportsstoredvalueexception.KindCardLinkMismatch, Reason: "订单与卡的关联缺核销流水",
	}, now)
	flag := sveSeedException(t, db, storedValueExceptionInput{
		CardID: card.ID, CustomerID: card.ID,
		Kind: esportsstoredvalueexception.KindCardFlagMismatch, Reason: "卡标记与流水不符",
	}, now)

	// 默认（不传 status）只看待跟进，且按 kind 过滤。
	all, err := listStoredValueExceptions(ctx, db, 11, true, storedValueExceptionFilter{}, 20)
	if err != nil || len(all) != 2 {
		t.Fatalf("默认列表应返回 2 条待跟进：%d %v", len(all), err)
	}
	onlyFlag, err := listStoredValueExceptions(ctx, db, 11, true,
		storedValueExceptionFilter{Kind: string(esportsstoredvalueexception.KindCardFlagMismatch)}, 20)
	if err != nil || len(onlyFlag) != 1 || onlyFlag[0].ID != flag.ID {
		t.Fatalf("按类型过滤错误：%+v %v", onlyFlag, err)
	}
	if _, err = listStoredValueExceptions(ctx, db, 11, true,
		storedValueExceptionFilter{Kind: "not_a_kind"}, 20); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法类型应返回参数错误，err=%v", err)
	}
	if _, err = listStoredValueExceptions(ctx, db, 11, true,
		storedValueExceptionFilter{Status: "not_a_status"}, 20); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法状态应返回参数错误，err=%v", err)
	}

	// 指派：open → following，写入跟进人与时间；重复指派换人允许。
	assigned, err := assignStoredValueException(ctx, db, link.ID, 11, true, 22, now)
	if err != nil {
		t.Fatalf("指派失败：%v", err)
	}
	if assigned.Status != string(esportsstoredvalueexception.StatusFollowing) ||
		assigned.AssigneeAdminID == nil || *assigned.AssigneeAdminID != 22 || assigned.AssignedAt == nil {
		t.Fatalf("指派后状态不正确：%+v", assigned)
	}
	reassigned, err := assignStoredValueException(ctx, db, link.ID, 11, true, 33, now.Add(time.Minute))
	if err != nil || reassigned.AssigneeAdminID == nil || *reassigned.AssigneeAdminID != 33 {
		t.Fatalf("重复指派换人失败：%+v %v", reassigned, err)
	}
	if _, err = assignStoredValueException(ctx, db, link.ID, 11, true, 0, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("未指定跟进人应返回参数错误，err=%v", err)
	}
	if _, err = assignStoredValueException(ctx, db, link.ID, 11, true, 999999, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("跟进人不存在应返回参数错误，err=%v", err)
	}

	// 处置：resolve 必须写处置说明；乐观锁不匹配返回冲突。
	if _, err = resolveStoredValueException(ctx, db, link.ID, 22, true, "resolve", "", 0, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("处置说明为空应返回参数错误，err=%v", err)
	}
	if _, err = resolveStoredValueException(ctx, db, link.ID, 22, true, "resolve", "已补录核销流水", 99, now); !errors.Is(err, errConflict) {
		t.Fatalf("版本不匹配应返回冲突，err=%v", err)
	}
	resolved, err := resolveStoredValueException(ctx, db, link.ID, 22, true, "resolve", "已补录核销流水", 0, now)
	if err != nil {
		t.Fatalf("处置失败：%v", err)
	}
	if resolved.Status != string(esportsstoredvalueexception.StatusResolved) ||
		resolved.ResolvedByAdminID == nil || *resolved.ResolvedByAdminID != 22 ||
		resolved.ResolvedAt == nil || resolved.Resolution != "已补录核销流水" {
		t.Fatalf("处置后状态不正确：%+v", resolved)
	}
	if row := db.EsportsStoredValueException.GetX(ctx, link.ID); row.DedupKey != nil {
		t.Fatalf("落终态必须让出 dedup_key，got=%v", *row.DedupKey)
	}
	if _, err = resolveStoredValueException(ctx, db, link.ID, 22, true, "resolve", "重复处置", 0, now); !errors.Is(err, errConflict) {
		t.Fatalf("终态重复处置应返回冲突，err=%v", err)
	}
	if _, err = assignStoredValueException(ctx, db, link.ID, 11, true, 44, now); !errors.Is(err, errConflict) {
		t.Fatalf("终态不可再指派，err=%v", err)
	}

	// ignored 是另一种终态，允许不写处置说明。
	ignored, err := resolveStoredValueException(ctx, db, flag.ID, 11, true, "ignore", "", 0, now)
	if err != nil || ignored.Status != string(esportsstoredvalueexception.StatusIgnored) {
		t.Fatalf("忽略处置失败：%+v %v", ignored, err)
	}
	if _, err = resolveStoredValueException(ctx, db, flag.ID, 11, true, "bogus", "x", 0, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非法处置动作应返回参数错误，err=%v", err)
	}

	// 默认列表此时应为空；显式查历史可见。
	if rows, err := listStoredValueExceptions(ctx, db, 11, true, storedValueExceptionFilter{}, 20); err != nil || len(rows) != 0 {
		t.Fatalf("处置后默认列表应清空：%+v %v", rows, err)
	}
	history, err := listStoredValueExceptions(ctx, db, 11, true,
		storedValueExceptionFilter{Status: string(esportsstoredvalueexception.StatusResolved)}, 20)
	if err != nil || len(history) != 1 || history[0].ID != link.ID {
		t.Fatalf("按终态查历史失败：%+v %v", history, err)
	}

	// 终态让出 dedup_key 后，同一（卡 + 订单 + 类型）再次不一致应重新入队一条新记录。
	reopened := sveSeedException(t, db, storedValueExceptionInput{
		CardID: card.ID, OrderID: order.ID, CustomerID: order.CustomerID,
		Kind: esportsstoredvalueexception.KindCardLinkMismatch, Reason: "复发：仍缺核销流水",
	}, now.Add(2*time.Minute))
	if reopened.ID == link.ID || reopened.Occurrences != 1 ||
		reopened.ExceptionNo == db.EsportsStoredValueException.GetX(ctx, link.ID).ExceptionNo {
		t.Fatalf("终态后复发应重新入队新记录：%+v", reopened)
	}
}

// §4.6-06：非超管只能看到「指派给自己 / 未指派 / 自己客户卡」的异常。
func TestSveQueueVisibilityScopedForNonSuper(t *testing.T) {
	db := openStoredValueDB(t, "esports_sve_queue_scope")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	card := sveSeedActiveCard(t, db, 11, 3000, "sve-scope-open-1")
	// 33 = 该异常的跟进人；22 = 无关客服（用于断言可见性收敛）。
	sveSeedAdmin(t, db, 33, "sve-scope-admin-33")
	sveSeedAdmin(t, db, 22, "sve-scope-admin-22")

	mine := sveSeedException(t, db, storedValueExceptionInput{
		CardID: card.ID, Kind: esportsstoredvalueexception.KindCardFlagMismatch, Reason: "指派给他人的卡",
	}, now)
	if _, err := assignStoredValueException(ctx, db, mine.ID, 11, true, 33, now); err != nil {
		t.Fatalf("指派失败：%v", err)
	}
	// 客服 22 既不是跟进人、也不负责该卡客户 → 看不到，也不能处置。
	if rows, err := listStoredValueExceptions(ctx, db, 22, false, storedValueExceptionFilter{}, 20); err != nil || len(rows) != 0 {
		t.Fatalf("越权可见他人卡异常：%+v %v", rows, err)
	}
	if _, err := getStoredValueException(ctx, db, 22, false, mine.ID); !errors.Is(err, errForbidden) {
		t.Fatalf("越权读取应返回无权，err=%v", err)
	}
	if _, err := resolveStoredValueException(ctx, db, mine.ID, 22, false, "resolve", "越权处置", 0, now); !errors.Is(err, errForbidden) {
		t.Fatalf("越权处置应返回无权，err=%v", err)
	}
	// 卡归属客服 11 与跟进人 33 都能看到。
	if _, err := getStoredValueException(ctx, db, 11, false, mine.ID); err != nil {
		t.Fatalf("卡归属客服应可见：%v", err)
	}
	if rows, err := listStoredValueExceptions(ctx, db, 33, false,
		storedValueExceptionFilter{Mine: true}, 20); err != nil || len(rows) != 1 {
		t.Fatalf("跟进人按「我的」过滤应命中：%+v %v", rows, err)
	}
}

// §4.6-06：异常队列的 HTTP 入口（列表 / 详情 / 指派 / 处置）与业务错误映射。
func TestSveQueueHandlersHTTP(t *testing.T) {
	db := openStoredValueDB(t, "esports_sve_queue_http")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	card := sveSeedActiveCard(t, db, 11, 3000, "sve-http-open-1")
	row := sveSeedException(t, db, storedValueExceptionInput{
		CardID: card.ID, Kind: esportsstoredvalueexception.KindCardFlagMismatch, Reason: "卡标记与流水不符",
	}, now)
	admin := db.Admin.Create().SetEmail("sve-http@example.com").SetPassword("test").
		SetUID("sve-http-admin").SetUsername("sve-http-admin").SetStatus("active").SaveX(ctx)
	// HTTP 指派目标必须真实存在（22）。
	sveSeedAdmin(t, db, 22, "sve-http-admin-22")

	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()
	app := sveAdminApp(admin, true)

	sveDo := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		var reader *bytes.Reader
		if body == nil {
			reader = bytes.NewReader(nil)
		} else {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			reader = bytes.NewReader(raw)
		}
		req := httptest.NewRequest(method, path, reader)
		req.Header.Set("Content-Type", "application/json")
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

	status, payload := sveDo("GET", "/admin/stored-value-exceptions", nil)
	if status != fiber.StatusOK {
		t.Fatalf("列表应 200，实际 %d %+v", status, payload)
	}
	if items, ok := payload["data"].([]any); !ok || len(items) != 1 {
		t.Fatalf("列表数据不正确：%+v", payload["data"])
	}
	path := "/admin/stored-value-exceptions/" + strconv.Itoa(row.ID)
	if status, _ := sveDo("GET", path, nil); status != fiber.StatusOK {
		t.Fatalf("详情应 200，实际 %d", status)
	}
	if status, _ := sveDo("GET", "/admin/stored-value-exceptions/999999", nil); status != fiber.StatusNotFound {
		t.Fatalf("不存在的异常应 404，实际 %d", status)
	}

	status, payload = sveDo("POST", path+"/assign",
		map[string]any{"assignee_admin_id": 22})
	if status != fiber.StatusOK {
		t.Fatalf("指派应 200，实际 %d %+v", status, payload)
	}
	status, _ = sveDo("POST", path+"/resolve",
		map[string]any{"action": "bogus", "resolution": "x"})
	if status != fiber.StatusBadRequest {
		t.Fatalf("非法处置动作应 400，实际 %d", status)
	}
	status, payload = sveDo("POST", path+"/resolve",
		map[string]any{"action": "ignore", "resolution": "重复触发，无需处理"})
	if status != fiber.StatusOK {
		t.Fatalf("忽略处置应 200，实际 %d %+v", status, payload)
	}
	status, _ = sveDo("POST", path+"/resolve",
		map[string]any{"action": "ignore", "resolution": "再来一次"})
	if status != fiber.StatusConflict {
		t.Fatalf("终态重复处置应 409，实际 %d", status)
	}
}
