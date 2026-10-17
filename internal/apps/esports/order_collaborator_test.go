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

	"go-admin/internal/ent"
	"go-admin/internal/global"
)

func sendJSON(t *testing.T, app *fiber.App, method, path, body string) (int, map[string]any) {
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
		t.Fatalf("请求 %s %s 失败：%v", method, path, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应失败：%v", err)
	}
	var out map[string]any
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &out); err != nil {
			t.Fatalf("响应不是 JSON：%s", payload)
		}
	}
	return resp.StatusCode, out
}

func collaboratorTestApp(admin *ent.Admin, isSuper bool) *fiber.App {
	app := adminListTestApp(admin, isSuper)
	app.Get("/admin/orders/:id/collaborators", listOrderCollaboratorsHandler)
	app.Post("/admin/orders/:id/collaborators", addOrderCollaboratorHandler)
	app.Delete("/admin/orders/:id/collaborators/:adminId", removeOrderCollaboratorHandler)
	// 写操作仍按负责人边界：用于验证协作客服不能借道改单 / 结单类路由。
	app.Post("/admin/orders/:id/close-stub", ownedOrderHandler(func(c fiber.Ctx) error {
		return reply(c, fiber.Map{"ok": true})
	}))
	return app
}

// 授权协作：授予后可在列表与详情看到订单，移除后立即失去访问权；写路由保持严格。
func TestOrderCollaboratorAccess(t *testing.T) {
	db := openAfterSaleDB(t, "esports_order_collaborator")
	defer db.Close()
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()

	ctx := t.Context()
	owner := seedAdminAccount(t, db, "cs-collab-owner")
	helper := seedAdminAccount(t, db, "cs-collab-helper")
	order := seedBalancePaidOrder(t, db, "COLLAB")
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(owner.ID).ExecX(ctx)
	orderPath := "/admin/orders/" + strconv.Itoa(order.ID)
	collaboratorsPath := orderPath + "/collaborators"
	now := time.Now()

	// 非负责人不能授予协作。
	if _, err := addOrderCollaborator(ctx, db, helper.ID, false, order.ID, owner.ID, helper.ID, now); !errors.Is(err, errForbidden) {
		t.Fatalf("非负责人授予协作应被拒绝：%v", err)
	}
	// 负责人本人无需加入协作。
	if _, err := addOrderCollaborator(ctx, db, owner.ID, false, order.ID, owner.ID, owner.ID, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("把负责人加为协作应返回参数错误：%v", err)
	}

	app := collaboratorTestApp(owner, false)
	status, _ := sendJSON(t, app, http.MethodPost, collaboratorsPath, `{"admin_id":`+strconv.Itoa(helper.ID)+`}`)
	if status != http.StatusOK {
		t.Fatalf("负责人授予协作失败：%d", status)
	}
	// 重复授予幂等。
	status, payload := sendJSON(t, app, http.MethodPost, collaboratorsPath, `{"admin_id":`+strconv.Itoa(helper.ID)+`}`)
	if status != http.StatusOK {
		t.Fatalf("重复授予应幂等成功：%d %+v", status, payload)
	}
	status, _ = sendJSON(t, app, http.MethodPost, collaboratorsPath, `{"admin_id":`+strconv.Itoa(owner.ID)+`}`)
	if status != http.StatusBadRequest {
		t.Fatalf("负责人加自己应 400，实际 %d", status)
	}

	// 协作客服：读取放行。
	helperApp := collaboratorTestApp(helper, false)
	status, _, payload = func() (int, *http.Response, map[string]any) {
		resp, err := helperApp.Test(httptest.NewRequest(http.MethodGet, orderPath, nil), fiber.TestConfig{Timeout: 10 * time.Second})
		if err != nil {
			t.Fatalf("协作者读取订单失败：%v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(body, &out)
		return resp.StatusCode, resp, out
	}()
	if status != http.StatusOK {
		t.Fatalf("协作客服应能查看订单详情：%d %+v", status, payload)
	}
	if data, _ := payload["data"].(map[string]any); data == nil || int(data["id"].(float64)) != order.ID {
		t.Fatalf("订单详情返回错误：%+v", payload)
	}

	// 列表包含协作订单。
	req, _ := helperApp.Test(httptest.NewRequest(http.MethodGet, "/admin/orders", nil), fiber.TestConfig{Timeout: 10 * time.Second})
	body, _ := io.ReadAll(req.Body)
	req.Body.Close()
	var listPayload map[string]any
	_ = json.Unmarshal(body, &listPayload)
	items, _ := listPayload["data"].([]any)
	found := false
	for _, raw := range items {
		if int(raw.(map[string]any)["id"].(float64)) == order.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("协作订单未出现在协作客服的列表中：%+v", listPayload)
	}

	// 协作客服可查看协作者名单。
	status, _, payload = getJSON(t, helperApp, collaboratorsPath)
	if status != http.StatusOK {
		t.Fatalf("协作客服查看协作者名单失败：%d %+v", status, payload)
	}
	if rows, _ := payload["data"].([]any); len(rows) != 1 {
		t.Fatalf("协作者名单应为 1 条：%+v", payload)
	}

	// 写路由仍严格：协作客服不能结单 / 改单。
	status, _ = sendJSON(t, helperApp, http.MethodPost, orderPath+"/close-stub", `{}`)
	if status != http.StatusForbidden {
		t.Fatalf("协作客服不应通过写路由，实际 %d", status)
	}
	status, _ = sendJSON(t, app, http.MethodPost, orderPath+"/close-stub", `{}`)
	if status != http.StatusOK {
		t.Fatalf("负责人应通过写路由，实际 %d", status)
	}

	// 协作客服不能撤销协作（非负责人）。
	status, _ = sendJSON(t, helperApp, http.MethodDelete, collaboratorsPath+"/"+strconv.Itoa(helper.ID), "")
	if status != http.StatusForbidden {
		t.Fatalf("协作客服不应能撤销协作，实际 %d", status)
	}

	// 负责人撤销后，协作客服立即失去读取权限。
	status, _ = sendJSON(t, app, http.MethodDelete, collaboratorsPath+"/"+strconv.Itoa(helper.ID), "")
	if status != http.StatusOK {
		t.Fatalf("负责人撤销协作失败：%d", status)
	}
	status, _, _ = getJSON(t, helperApp, orderPath)
	if status != http.StatusNotFound {
		t.Fatalf("撤销后订单详情应为 404，实际 %d", status)
	}
	status, _, payload = getJSON(t, helperApp, "/admin/orders")
	if rows, _ := payload["data"].([]any); len(rows) != 0 {
		t.Fatalf("撤销后列表不应再出现该订单：%+v", payload)
	}
	status, _, _ = getJSON(t, helperApp, collaboratorsPath)
	if status != http.StatusForbidden && status != http.StatusNotFound {
		t.Fatalf("撤销后协作者名单应拒绝访问，实际 %d", status)
	}

	// 重复撤销：记录不存在。
	if err := removeOrderCollaborator(ctx, db, owner.ID, false, order.ID, helper.ID); !ent.IsNotFound(err) {
		t.Fatalf("重复撤销应返回记录不存在：%v", err)
	}
}

// 宽松 / 严格两种边界的单元口径：协作只放宽读取，不放宽写入。
func TestLoadOwnedOrderDistinguishesReadAndWrite(t *testing.T) {
	db := openAfterSaleDB(t, "esports_order_access_split")
	defer db.Close()
	ctx := t.Context()
	order := seedBalancePaidOrder(t, db, "ACCESS-SPLIT")
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(11).ExecX(ctx)
	if _, err := db.EsportsOrderCollaborator.Create().
		SetOrderID(order.ID).SetAdminID(22).SetGrantedByAdminID(11).Save(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOwnedOrder(ctx, db, 22, false, order.ID); err != nil {
		t.Fatalf("协作客服应通过读取边界：%v", err)
	}
	if _, err := loadStrictOwnedOrder(ctx, db, 22, false, order.ID); !errors.Is(err, errForbidden) {
		t.Fatalf("协作客服不应通过写入边界：%v", err)
	}
	if _, err := loadOwnedOrder(ctx, db, 33, false, order.ID); !errors.Is(err, errForbidden) {
		t.Fatalf("无关客服应被拒绝：%v", err)
	}
}
