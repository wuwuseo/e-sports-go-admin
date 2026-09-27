package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	_ "github.com/mattn/go-sqlite3"
)

func setupSlidePublicTestApp(t *testing.T) (*fiber.App, *service.SlideService, func()) {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:"+t.Name()+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)

	prevORM := global.ORM
	global.ORM = client
	svc := service.NewSlideService()

	app := fiber.New()
	app.Get("/api/v1/public/slides", func(c fiber.Ctx) error {
		c.Locals("utils.Response", utils.NewResponse())
		c.Locals("service.SlideService", svc)
		return ListPublicSlides(c)
	})

	cleanup := func() {
		global.ORM = prevORM
		client.Close()
	}
	return app, svc, cleanup
}

func mustCreateSlideForTest(t *testing.T, svc *service.SlideService, title, group string, sort, status int) *service.SlideResponse {
	t.Helper()
	item, err := svc.Create(context.Background(), &service.CreateSlideRequest{
		Title: title, Image: "https://cdn.example.com/x.png",
		GroupCode: group, Sort: &sort, Status: &status, Remark: "内部备注",
	})
	if err != nil {
		t.Fatalf("准备幻灯片数据失败: %v", err)
	}
	return item
}

func TestListPublicSlides_AnonymousAccessibleAndFiltered(t *testing.T) {
	app, svc, cleanup := setupSlidePublicTestApp(t)
	defer cleanup()

	homeOn := mustCreateSlideForTest(t, svc, "第一位", "home", 1, 1)
	homeOn2 := mustCreateSlideForTest(t, svc, "第二位", "home", 2, 1)
	_ = mustCreateSlideForTest(t, svc, "已禁用", "home", 0, 0)
	_ = mustCreateSlideForTest(t, svc, "别组", "about", 1, 1)

	// group=home:仅启用中的 home,按 sort 升序
	req := httptest.NewRequest(http.MethodGet, "/api/v1/public/slides?group=home", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", resp.StatusCode, string(body))
	}

	var decoded struct {
		Code int `json:"code"`
		Data []struct {
			ID        int    `json:"id"`
			Title     string `json:"title"`
			Image     string `json:"image"`
			Link      string `json:"link"`
			GroupCode string `json:"group_code"`
			Remark    string `json:"remark"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode response failed: %v, body = %s", err, string(body))
	}
	if decoded.Code != global.ResponseSuccessCode {
		t.Fatalf("response code = %d, want %d, body = %s", decoded.Code, global.ResponseSuccessCode, string(body))
	}
	if len(decoded.Data) != 2 {
		t.Fatalf("期望 2 条, 实际 %d, body = %s", len(decoded.Data), string(body))
	}
	if decoded.Data[0].ID != homeOn.ID || decoded.Data[1].ID != homeOn2.ID {
		t.Fatalf("期望按 sort 升序 %d,%d, 实际 %d,%d", homeOn.ID, homeOn2.ID, decoded.Data[0].ID, decoded.Data[1].ID)
	}
	for _, item := range decoded.Data {
		if item.Remark != "" {
			t.Fatalf("公开响应不得包含 remark 字段, body = %s", string(body))
		}
	}
	if !strings.Contains(string(body), `"group_code":"home"`) {
		t.Fatalf("响应应包含 group_code 字段, body = %s", string(body))
	}
	var publicBody struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &publicBody); err != nil {
		t.Fatalf("decode public fields failed: %v", err)
	}
	for _, item := range publicBody.Data {
		if len(item) != 5 {
			t.Fatalf("公开响应应恰好包含五个字段, 实际 %#v", item)
		}
		for _, key := range []string{"id", "title", "image", "link", "group_code"} {
			if _, ok := item[key]; !ok {
				t.Fatalf("公开响应缺少 %s 字段, 实际 %#v", key, item)
			}
		}
	}
}

func TestUpdateSlide_EmptyRequiredFieldReturnsParameterError(t *testing.T) {
	app, svc, cleanup := setupSlidePublicTestApp(t)
	defer cleanup()
	created := mustCreateSlideForTest(t, svc, "原标题", "home", 0, 1)
	app.Put("/api/v1/admin/auth/slides/:id", func(c fiber.Ctx) error {
		c.Locals("utils.Response", utils.NewResponse())
		c.Locals("service.SlideService", svc)
		return UpdateSlide(c)
	})

	for _, field := range []string{"title", "image", "group_code"} {
		t.Run(field, func(t *testing.T) {
			body := fmt.Sprintf(`{"%s":""}`, field)
			req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/admin/auth/slides/%d", created.ID), strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := app.Test(req)
			if err != nil {
				t.Fatalf("app.Test returned error: %v", err)
			}
			defer resp.Body.Close()
			var result struct {
				Code int `json:"code"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				t.Fatalf("decode response failed: %v", err)
			}
			if result.Code != global.ResponseErrorCode {
				t.Fatalf("空 %s 应返回参数错误码 %d, 实际 %d", field, global.ResponseErrorCode, result.Code)
			}
		})
	}

	got, err := svc.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("查询幻灯片失败: %v", err)
	}
	if got.Title != "原标题" || got.Image == "" || got.GroupCode != "home" {
		t.Fatalf("无效更新不应更改幻灯片, 实际 %#v", got)
	}
}

func TestListPublicSlides_NoGroupReturnsAllEnabled(t *testing.T) {
	app, svc, cleanup := setupSlidePublicTestApp(t)
	defer cleanup()

	_ = mustCreateSlideForTest(t, svc, "甲", "home", 1, 1)
	_ = mustCreateSlideForTest(t, svc, "乙", "about", 1, 1)
	_ = mustCreateSlideForTest(t, svc, "丙", "about", 1, 0)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/public/slides", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body failed: %v", err)
	}
	var decoded struct {
		Code int `json:"code"`
		Data []struct {
			GroupCode string `json:"group_code"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if decoded.Code != global.ResponseSuccessCode || len(decoded.Data) != 2 {
		t.Fatalf("不传 group 期望全部启用 2 条, code=%d len=%d body=%s", decoded.Code, len(decoded.Data), string(body))
	}
}

func TestListPublicSlides_EmptyGroupReturnsEmptyArray(t *testing.T) {
	app, _, cleanup := setupSlidePublicTestApp(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/public/slides?group=nope", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body failed: %v", err)
	}
	if !strings.Contains(string(body), `"data":[]`) {
		t.Fatalf("空分组期望返回空数组, body = %s", string(body))
	}
}
