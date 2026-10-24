package internal

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/config"

	_ "github.com/mattn/go-sqlite3"
)

// TestUserAuthSMSCodeRoutesArePublic ：
// 短信验证码通道挂在平台级 user-auth 分组下（/api/v1/user/auth/*），必须是 AuthPublic：
// 无令牌请求不得被该分组的 JWT 中间件拦成 401（Fiber 的 USE 中间件对同前缀的后注册路由同样生效，
// 因此这两条公开路由必须在鉴权分组**之前**注册）。同时锁定既有受保护路由仍要求鉴权。
func TestUserAuthSMSCodeRoutesArePublic(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:router_sms_code_public?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer client.Close()
	global.ORM = client

	app := fiber.New()
	cfg := config.NewConfig()
	cfg.App.Secret = "user-sms-secret"
	cfg.App.AdminSecret = "admin-sms-secret"
	RegisterRoute(app, cfg)

	routes := app.GetRoutes(true)
	assertRouteRegistered(t, routes, "/api/v1/user/auth/sms-code")
	assertRouteRegistered(t, routes, "/api/v1/user/auth/login-by-sms")

	for _, test := range []struct {
		path string
		body string
	}{
		{"/api/v1/user/auth/sms-code", `{"phone":"13800000001"}`},
		{"/api/v1/user/auth/login-by-sms", `{"phone":"13800000001","code":"246810"}`},
	} {
		request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
		request.Header.Set("Content-Type", "application/json")
		response, err := app.Test(request)
		if err != nil {
			t.Fatalf("POST %s: %v", test.path, err)
		}
		var envelope struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		decodeErr := json.NewDecoder(response.Body).Decode(&envelope)
		_ = response.Body.Close()
		if response.StatusCode == http.StatusUnauthorized {
			t.Fatalf("POST %s 是公开接口，不应被 JWT 中间件拦成 401", test.path)
		}
		if decodeErr != nil {
			t.Fatalf("POST %s 响应解析失败: %v", test.path, decodeErr)
		}
		if envelope.Code == global.ResponseSuccessCode {
			t.Fatalf("POST %s 短信渠道未配置时必须明确失败（不得伪成功），实际 %+v", test.path, envelope)
		}
		if !strings.Contains(envelope.Message, "短信") && !strings.Contains(envelope.Message, "验证码") {
			t.Fatalf("POST %s 失败提示应为中文且指向短信/验证码，实际 %q", test.path, envelope.Message)
		}
	}

	// 回归：同一前缀下的既有受保护路由必须仍然被 JWT 中间件拦下（不得落到短信处理器）。
	// 注：该分组用 cmf 默认 JWT 中间件，缺令牌时返回 400 "missing or malformed JWT"（既有行为），
	// 因此这里断言「4xx 且未进入业务处理器」，而不是写死 401。
	protected, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/user/auth/oauth/identities", nil))
	if err != nil {
		t.Fatalf("GET /api/v1/user/auth/oauth/identities: %v", err)
	}
	protectedBody, _ := io.ReadAll(protected.Body)
	_ = protected.Body.Close()
	if protected.StatusCode != http.StatusUnauthorized && protected.StatusCode != http.StatusBadRequest {
		t.Fatalf("既有受保护路由无令牌状态 = %d，期望 4xx 鉴权拒绝", protected.StatusCode)
	}
	if strings.Contains(string(protectedBody), "验证码") {
		t.Fatalf("既有受保护路由不应落到短信处理器，实际 body=%s", protectedBody)
	}
}
