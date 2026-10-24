package esports

import (
	"encoding/json"
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
	"go-admin/internal/global"
)

// csJSONResponse 是客服接口测试用的一次性响应信封。
type csJSONResponse struct {
	status int
	body   map[string]any
}

// csHandlerTestApp 构造带登录态的最小客服路由，直接挂 cs_handlers.go 的真实 handler，
// 与 PermissionMiddleware 之后的运行环境一致。
func csHandlerTestApp(admin *ent.Admin, isSuper bool) *fiber.App {
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("uid", admin.UID)
		c.Locals("isSuperAdmin", isSuper)
		return c.Next()
	})
	app.Post("/admin/customers/wecom-intake", wecomIntakeHandler)
	app.Put("/admin/customers/:id/profile", updateCustomerProfileHandler)
	return app
}

// csCallJSON 发送一次 JSON 请求并解析响应信封。
func csCallJSON(t *testing.T, app *fiber.App, method, path, body string) csJSONResponse {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("%s %s 请求失败：%v", method, path, err)
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
	return csJSONResponse{status: resp.StatusCode, body: out}
}

// csData 取出响应信封中的 data 对象。
func csData(t *testing.T, resp csJSONResponse) map[string]any {
	t.Helper()
	data, _ := resp.body["data"].(map[string]any)
	if data == nil {
		t.Fatalf("响应缺少 data：%+v", resp.body)
	}
	return data
}

// csTagStrings 把 data 里的 tags 字段（null / 数组）读成字符串切片。
func csTagStrings(t *testing.T, raw any) []string {
	t.Helper()
	if raw == nil {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("tags 字段不是数组：%#v", raw)
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		tag, ok := item.(string)
		if !ok {
			t.Fatalf("标签不是字符串：%#v", item)
		}
		out = append(out, tag)
	}
	return out
}

// csCustomerOf 取出 data.customer 对象（企微建档接口的响应结构）。
func csCustomerOf(t *testing.T, resp csJSONResponse) map[string]any {
	t.Helper()
	customer, _ := csData(t, resp)["customer"].(map[string]any)
	if customer == nil {
		t.Fatalf("响应缺少 data.customer：%+v", resp.body)
	}
	return customer
}

// ：企微自动建档命中他人客户时不得回传整条档案，
// 只回最小卡片并标记归属，让调用方知道客户已被他人负责。
func TestWecomIntakeCrossCsReturnsMinimalCard(t *testing.T) {
	db := openCSDB(t, "funding_cs_intake_cross")
	defer db.Close()
	ctx := t.Context()
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()

	owner := seedAdminAccount(t, db, "cs-owner")
	invader := seedAdminAccount(t, db, "cs-invader")

	ownerCustomer, created, err := wecomUpsertCustomer(ctx, db, owner.ID, wecomIntakeInput{
		CorpID: "corp-1", ExternalID: "ext-1", DisplayName: "客户甲", Phone: "13800000000",
	}, time.Now())
	if err != nil || !created {
		t.Fatalf("客服 A 首次建档应成功，got created=%v err=%v", created, err)
	}
	if _, err := db.EsportsCustomer.UpdateOneID(ownerCustomer.ID).
		SetTags([]string{"vip", "复购"}).SetConsumptionPreference("王者荣耀").Save(ctx); err != nil {
		t.Fatal(err)
	}

	resp := csCallJSON(t, csHandlerTestApp(invader, false), http.MethodPost,
		"/admin/customers/wecom-intake",
		`{"corp_id":"corp-1","external_id":"ext-1","display_name":"客户甲","phone":"13700000000"}`)
	if resp.status != http.StatusOK {
		t.Fatalf("客服 B 建档应返回 200，got %d（%+v）", resp.status, resp.body)
	}
	if created, _ := csData(t, resp)["created"].(bool); created {
		t.Fatalf("命中既有客户不应新建，got created=%v", csData(t, resp)["created"])
	}
	customer := csCustomerOf(t, resp)
	if id, _ := customer["id"].(float64); int(id) != ownerCustomer.ID {
		t.Fatalf("应命中客户 A 的档案 id=%d，got %+v", ownerCustomer.ID, customer)
	}
	if phone, _ := customer["phone"].(string); phone != "" {
		t.Fatalf("跨客服建档不得回传他人客户手机号，got %q（%+v）", phone, customer)
	}
	if tags := csTagStrings(t, customer["tags"]); len(tags) != 0 {
		t.Fatalf("跨客服建档不得回传他人客户标签，got %v", tags)
	}
	if preference, _ := customer["consumption_preference"].(string); preference != "" {
		t.Fatalf("跨客服建档不得回传他人客户消费偏好，got %q", preference)
	}
	if owned, _ := customer["owned_by_other"].(bool); !owned {
		t.Fatalf("应标记客户已归属他人，got %+v", customer)
	}
	if name, _ := customer["owner_admin_name"].(string); name != owner.Username {
		t.Fatalf("应返回归属客服名称 %q，got %q", owner.Username, name)
	}
	message, _ := customer["message"].(string)
	if !strings.Contains(message, owner.Username) || !strings.Contains(message, "联系管理员") {
		t.Fatalf("应给出「客户已被他人负责」的中文提示，got %q", message)
	}
	// 客户 A 的档案不得因 B 的建档被改写。
	reloaded, err := db.EsportsCustomer.Get(ctx, ownerCustomer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Tags) != 2 || reloaded.Phone != "13800000000" {
		t.Fatalf("他人建档不得改写原档案，got tags=%v phone=%q", reloaded.Tags, reloaded.Phone)
	}
}

// 同一客服重复调用企微建档仍幂等：created=false，返回本人整条档案。
func TestWecomIntakeSameCsIdempotent(t *testing.T) {
	db := openCSDB(t, "funding_cs_intake_same")
	defer db.Close()
	ctx := t.Context()
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()

	admin := seedAdminAccount(t, db, "cs-owner-2")
	app := csHandlerTestApp(admin, false)
	body := `{"corp_id":"corp-2","external_id":"ext-2","display_name":"客户乙","phone":"13800000002"}`

	first := csCallJSON(t, app, http.MethodPost, "/admin/customers/wecom-intake", body)
	if first.status != http.StatusOK {
		t.Fatalf("首次建档应返回 200，got %d（%+v）", first.status, first.body)
	}
	firstData := csData(t, first)
	if created, _ := firstData["created"].(bool); !created {
		t.Fatalf("首次建档 created 应为 true，got %+v", firstData)
	}
	firstCustomer, _ := firstData["customer"].(map[string]any)
	firstID, _ := firstCustomer["id"].(float64)
	if firstID == 0 {
		t.Fatalf("首次建档应返回客户 ID：%+v", firstCustomer)
	}
	if _, err := db.EsportsCustomer.UpdateOneID(int(firstID)).SetTags([]string{"vip"}).Save(ctx); err != nil {
		t.Fatal(err)
	}

	second := csCallJSON(t, app, http.MethodPost, "/admin/customers/wecom-intake", body)
	if second.status != http.StatusOK {
		t.Fatalf("重复建档应返回 200，got %d（%+v）", second.status, second.body)
	}
	secondData := csData(t, second)
	if created, _ := secondData["created"].(bool); created {
		t.Fatalf("重复建档应幂等 created=false，got %+v", secondData)
	}
	customer := csCustomerOf(t, second)
	if id, _ := customer["id"].(float64); int(id) != int(firstID) {
		t.Fatalf("重复建档应命中同一条客户 id=%d，got %+v", int(firstID), customer)
	}
	if phone, _ := customer["phone"].(string); phone != "13800000002" {
		t.Fatalf("本人客户应原样返回档案，got phone=%q（%+v）", phone, customer)
	}
	if tags := csTagStrings(t, customer["tags"]); len(tags) != 1 || tags[0] != "vip" {
		t.Fatalf("本人客户标签应可见，got %v", tags)
	}
	owned, ok := customer["owned_by_other"].(bool)
	if !ok || owned {
		t.Fatalf("本人客户应明确标记 owned_by_other=false，got %+v", customer)
	}
}

// ：PUT profile 未提供 tags 时不改动既有标签；显式空数组才清空。
func TestCustomerProfileTagsIncremental(t *testing.T) {
	db := openCSDB(t, "funding_cs_tags_keep")
	defer db.Close()
	ctx := t.Context()
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()

	admin := seedAdminAccount(t, db, "cs-profile-keep")
	customer, err := db.EsportsCustomer.Create().SetDisplayName("客户丙").SetOwnerAdminID(admin.ID).
		SetPhone("13800000003").SetTags([]string{"vip", "复购"}).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	app := csHandlerTestApp(admin, false)
	path := "/admin/customers/" + strconv.Itoa(customer.ID) + "/profile"

	// 请求体不含 tags 字段：既有标签必须保持（修复前被整体清空）。
	kept := csCallJSON(t, app, http.MethodPut, path, `{"phone":"13900000003"}`)
	if kept.status != http.StatusOK {
		t.Fatalf("不含 tags 的档案更新应返回 200，got %d（%+v）", kept.status, kept.body)
	}
	keptCustomer := csData(t, kept)
	if phone, _ := keptCustomer["phone"].(string); phone != "13900000003" {
		t.Fatalf("手机号应正常更新，got %q", phone)
	}
	if tags := csTagStrings(t, keptCustomer["tags"]); len(tags) != 2 || tags[0] != "vip" || tags[1] != "复购" {
		t.Fatalf("未提供 tags 时不得改写既有标签，got %v", tags)
	}
	reloaded, err := db.EsportsCustomer.Get(ctx, customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Tags) != 2 || reloaded.Tags[0] != "vip" || reloaded.Tags[1] != "复购" {
		t.Fatalf("未提供 tags 时库中标签不得被清空，got %v", reloaded.Tags)
	}

	// tags 显式为 null：同样按「未提供」处理，不清空标签。
	nulled := csCallJSON(t, app, http.MethodPut, path, `{"tags":null}`)
	if nulled.status != http.StatusOK {
		t.Fatalf("tags 为 null 应被接受，got %d（%+v）", nulled.status, nulled.body)
	}
	if tags := csTagStrings(t, csData(t, nulled)["tags"]); len(tags) != 2 {
		t.Fatalf("tags 为 null 时不得改写既有标签，got %v", tags)
	}

	// 显式空数组：清空标签。
	cleared := csCallJSON(t, app, http.MethodPut, path, `{"tags":[]}`)
	if cleared.status != http.StatusOK {
		t.Fatalf("显式空数组应被接受，got %d（%+v）", cleared.status, cleared.body)
	}
	if tags := csTagStrings(t, csData(t, cleared)["tags"]); len(tags) != 0 {
		t.Fatalf("显式空数组应清空标签，got %v", tags)
	}
	reloaded, err = db.EsportsCustomer.Get(ctx, customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Tags) != 0 {
		t.Fatalf("显式空数组应清空库中标签，got %v", reloaded.Tags)
	}
}

// 回归：显式提供 tags 时仍去空去重，超过 20 个或单个超过 30 字仍被拒。
func TestCustomerProfileTagsProvidedValidated(t *testing.T) {
	db := openCSDB(t, "funding_cs_tags_validate")
	defer db.Close()
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()
	ctx := t.Context()

	admin := seedAdminAccount(t, db, "cs-profile-validate")
	customer, err := db.EsportsCustomer.Create().SetDisplayName("客户丁").SetOwnerAdminID(admin.ID).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	app := csHandlerTestApp(admin, false)
	path := "/admin/customers/" + strconv.Itoa(customer.ID) + "/profile"

	ok := csCallJSON(t, app, http.MethodPut, path, `{"tags":["vip"," vip ","","复购"]}`)
	if ok.status != http.StatusOK {
		t.Fatalf("正常标签应被接受，got %d（%+v）", ok.status, ok.body)
	}
	if tags := csTagStrings(t, csData(t, ok)["tags"]); len(tags) != 2 || tags[0] != "vip" || tags[1] != "复购" {
		t.Fatalf("标签应去空去重，got %v", tags)
	}

	tooMany := make([]string, 0, csMaxTags+1)
	for i := 0; i <= csMaxTags; i++ {
		tooMany = append(tooMany, "tag-"+strconv.Itoa(i))
	}
	body, err := json.Marshal(map[string]any{"tags": tooMany})
	if err != nil {
		t.Fatal(err)
	}
	rejected := csCallJSON(t, app, http.MethodPut, path, string(body))
	if rejected.status != http.StatusBadRequest {
		t.Fatalf("超过 %d 个标签应返回 400，got %d（%+v）", csMaxTags, rejected.status, rejected.body)
	}

	body, err = json.Marshal(map[string]any{"tags": []string{strings.Repeat("长", csMaxTagLen+1)}})
	if err != nil {
		t.Fatal(err)
	}
	rejected = csCallJSON(t, app, http.MethodPut, path, string(body))
	if rejected.status != http.StatusBadRequest {
		t.Fatalf("单个标签超过 %d 字应返回 400，got %d（%+v）", csMaxTagLen, rejected.status, rejected.body)
	}
}
