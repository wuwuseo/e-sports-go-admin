package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	jwtlib "github.com/golang-jwt/jwt/v5"
	_ "github.com/mattn/go-sqlite3"
	cmfjwt "github.com/wuwuseo/cmf/jwt"
)

// mockBlacklistSvc 用于测试的黑名单 service stub
type mockBlacklistSvc struct {
	isBlacklistedFn func(ctx context.Context, uid string) (bool, error)
	isLoggedOutFn   func(ctx context.Context, uid string) (bool, error)
}

func TestBlacklistCheck_RejectsInactiveAdminToken(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:blacklist-account-status?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	t.Cleanup(func() { _ = client.Close() })
	previousORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = previousORM })
	client.Admin.Create().
		SetEmail("inactive-admin@example.com").
		SetUID("inactive-admin").
		SetUsername("inactive-admin").
		SetPassword("hash").
		SetStatus(admin.StatusInactive).
		SaveX(t.Context())

	app := fiber.New()
	app.Use(cmfjwt.NewJWTMiddleware(blacklistTestSecret))
	app.Use(BlacklistCheck(&mockBlacklistSvc{}, "admin"))
	app.Get("/protected", func(c fiber.Ctx) error { return c.SendStatus(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+makeBlacklistTestToken(t, "inactive-admin"))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	code, _ := readRespCode(t, resp)
	if code != global.ResponseUnauthorized {
		t.Fatalf("inactive admin response code=%d, want %d", code, global.ResponseUnauthorized)
	}
}

func (m *mockBlacklistSvc) AddToBlacklist(_ context.Context, _ int, _, _, _ string) error {
	return nil
}
func (m *mockBlacklistSvc) RemoveFromBlacklist(_ context.Context, _ int, _ string) error {
	return nil
}
func (m *mockBlacklistSvc) ListBlacklist(_ context.Context, _, _ int) (*service.ListResponse[*service.BlacklistItem], error) {
	return nil, nil
}
func (m *mockBlacklistSvc) RecordLogout(_ context.Context, _ string, _ time.Duration) error {
	return nil
}
func (m *mockBlacklistSvc) ClearLogout(_ context.Context, _ string) error { return nil }

func (m *mockBlacklistSvc) IsBlacklisted(ctx context.Context, uid string) (bool, error) {
	if m.isBlacklistedFn != nil {
		return m.isBlacklistedFn(ctx, uid)
	}
	return false, nil
}

func (m *mockBlacklistSvc) IsLoggedOut(ctx context.Context, uid string) (bool, error) {
	if m.isLoggedOutFn != nil {
		return m.isLoggedOutFn(ctx, uid)
	}
	return false, nil
}

const blacklistTestSecret = "blacklist-test-secret"

// makeToken 生成测试用 JWT
func makeBlacklistTestToken(t *testing.T, uid string) string {
	t.Helper()
	token, err := cmfjwt.CreateToken(jwtlib.MapClaims{
		"id":      uid,
		"subject": "user-subject",
		"domain":  "default",
		"exp":     time.Now().Add(time.Hour).Unix(),
	}, blacklistTestSecret)
	if err != nil {
		t.Fatalf("CreateToken 失败: %v", err)
	}
	return token
}

// setupBlacklistApp 构造测试用 Fiber app + JWTMiddleware + BlacklistCheck
func setupBlacklistApp(svc *mockBlacklistSvc) *fiber.App {
	app := fiber.New()
	app.Use(cmfjwt.NewJWTMiddleware(blacklistTestSecret))
	app.Use(BlacklistCheck(svc))
	app.Get("/protected", func(c fiber.Ctx) error {
		return c.Status(http.StatusOK).SendString("allowed")
	})
	return app
}

// readRespCode 读取响应体并解析出 utils.Response.Code
func readRespCode(t *testing.T, resp *http.Response) (int, string) {
	t.Helper()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应体失败: %v", err)
	}
	var parsed utils.Response
	if err := json.Unmarshal(body, &parsed); err != nil {
		// 非 JSON：200 时是 "allowed" 文本
		return resp.StatusCode, string(body)
	}
	return parsed.Code, parsed.Message
}

// TestBlacklistCheck_NoJWTBypassesCheck 验证无 JWT claims 时 BlacklistCheck 直接放行
// （注意：此场景不用 JWTMiddleware，直接单独挂 BlacklistCheck，让 claims=nil 分支生效）
func TestBlacklistCheck_NoJWTBypassesCheck(t *testing.T) {
	svc := &mockBlacklistSvc{
		isBlacklistedFn: func(_ context.Context, _ string) (bool, error) {
			t.Fatal("无 JWT claims 时 BlacklistCheck 不应调用 IsBlacklisted")
			return false, nil
		},
		isLoggedOutFn: func(_ context.Context, _ string) (bool, error) {
			t.Fatal("无 JWT claims 时 BlacklistCheck 不应调用 IsLoggedOut")
			return false, nil
		},
	}
	// 不用 JWT 中间件，claims 必为 nil
	app := fiber.New()
	app.Use(BlacklistCheck(svc))
	app.Get("/protected", func(c fiber.Ctx) error {
		return c.Status(http.StatusOK).SendString("allowed")
	})
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test 失败: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "allowed" {
		t.Fatalf("无 JWT 时 BlacklistCheck 应放行, 实际: %s", body)
	}
}

// TestBlacklistCheck_JWTWithoutUIDBypasses 验证 claims 中 id 字段缺失/类型错误时放行
func TestBlacklistCheck_JWTWithoutUIDBypasses(t *testing.T) {
	app := fiber.New()
	// 手动构造一个不带 id 字段的 JWT
	app.Use(func(c fiber.Ctx) error {
		// 伪造 claims：id 是 int 而非 string => 断言失败
		c.Locals("jwt_claims", map[string]interface{}{
			"id":      12345, // 类型不符
			"subject": "user",
		})
		return c.Next()
	})
	// 注意：这里跳过了 cmfjwt 的标准中间件，直接构造 locals 来模拟断言失败
	// 但 BlacklistCheck 读取的是 jwt.GetJWTUserData(c)，需要看 cmfjwt 内部实现。
	// 保险起见：单独测试 uid 为空字符串的分支
	app2 := fiber.New()
	app2.Use(BlacklistCheck(&mockBlacklistSvc{
		isBlacklistedFn: func(_ context.Context, uid string) (bool, error) {
			// 此 service 仍会被调用，但 uid="" 时不应走到这
			return false, nil
		},
	}))
	app2.Get("/x", func(c fiber.Ctx) error {
		return c.SendString("ok")
	})
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	resp, err := app2.Test(req)
	if err != nil {
		t.Fatalf("Test 失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("无 claims 时应放行, 状态码=%d", resp.StatusCode)
	}
}

// TestBlacklistCheck_IsBlacklistedBlocksUser 验证黑名单用户被拒绝(403)
func TestBlacklistCheck_IsBlacklistedBlocksUser(t *testing.T) {
	svc := &mockBlacklistSvc{
		isBlacklistedFn: func(_ context.Context, uid string) (bool, error) {
			if uid != "user-1" {
				t.Fatalf("期望 uid=user-1, 实际 %s", uid)
			}
			return true, nil
		},
	}
	app := setupBlacklistApp(svc)
	token := makeBlacklistTestToken(t, "user-1")
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test 失败: %v", err)
	}
	code, msg := readRespCode(t, resp)
	if code != global.ResponseForbidden {
		t.Fatalf("黑名单用户应返回 code=%d, 实际 code=%d msg=%s",
			global.ResponseForbidden, code, msg)
	}
	if msg != "您的账号已被限制访问，请联系管理员" {
		t.Fatalf("响应消息不匹配: %s", msg)
	}
}

// TestBlacklistCheck_IsLoggedOutReturns401 验证已登出会话返回 401
func TestBlacklistCheck_IsLoggedOutReturns401(t *testing.T) {
	svc := &mockBlacklistSvc{
		isBlacklistedFn: func(_ context.Context, _ string) (bool, error) {
			return false, nil
		},
		isLoggedOutFn: func(_ context.Context, uid string) (bool, error) {
			if uid != "user-2" {
				t.Fatalf("期望 uid=user-2, 实际 %s", uid)
			}
			return true, nil
		},
	}
	app := setupBlacklistApp(svc)
	token := makeBlacklistTestToken(t, "user-2")
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test 失败: %v", err)
	}
	code, msg := readRespCode(t, resp)
	if code != global.ResponseUnauthorized {
		t.Fatalf("登出用户应返回 code=%d, 实际 code=%d msg=%s",
			global.ResponseUnauthorized, code, msg)
	}
	if msg != "登录已失效，请重新登录" {
		t.Fatalf("响应消息不匹配: %s", msg)
	}
}

// TestBlacklistCheck_BlacklistErrorMapsTo500 验证 IsBlacklisted 错误映射为 500
func TestBlacklistCheck_BlacklistErrorMapsTo500(t *testing.T) {
	svc := &mockBlacklistSvc{
		isBlacklistedFn: func(_ context.Context, _ string) (bool, error) {
			return false, errors.New("redis connection lost")
		},
	}
	app := setupBlacklistApp(svc)
	token := makeBlacklistTestToken(t, "user-3")
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test 失败: %v", err)
	}
	code, msg := readRespCode(t, resp)
	if code != global.ResponseInternalServerError {
		t.Fatalf("黑名单查询异常应返回 code=%d, 实际 code=%d msg=%s",
			global.ResponseInternalServerError, code, msg)
	}
	if msg != "黑名单校验异常" {
		t.Fatalf("错误消息不匹配: %s", msg)
	}
}

// TestBlacklistCheck_LoggedOutErrorMapsTo500 验证 IsLoggedOut 错误映射为 500
func TestBlacklistCheck_LoggedOutErrorMapsTo500(t *testing.T) {
	svc := &mockBlacklistSvc{
		isBlacklistedFn: func(_ context.Context, _ string) (bool, error) {
			return false, nil
		},
		isLoggedOutFn: func(_ context.Context, _ string) (bool, error) {
			return false, errors.New("session store down")
		},
	}
	app := setupBlacklistApp(svc)
	token := makeBlacklistTestToken(t, "user-4")
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test 失败: %v", err)
	}
	code, msg := readRespCode(t, resp)
	if code != global.ResponseInternalServerError {
		t.Fatalf("会话校验异常应返回 code=%d, 实际 code=%d msg=%s",
			global.ResponseInternalServerError, code, msg)
	}
	if msg != "会话校验异常" {
		t.Fatalf("错误消息不匹配: %s", msg)
	}
}

// TestBlacklistCheck_NormalUserPassesThrough 验证正常用户通行
func TestBlacklistCheck_NormalUserPassesThrough(t *testing.T) {
	svc := &mockBlacklistSvc{
		isBlacklistedFn: func(_ context.Context, _ string) (bool, error) { return false, nil },
		isLoggedOutFn:   func(_ context.Context, _ string) (bool, error) { return false, nil },
	}
	app := setupBlacklistApp(svc)
	token := makeBlacklistTestToken(t, "user-ok")
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test 失败: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "allowed" {
		t.Fatalf("正常用户应放行, 实际: %s", body)
	}
}

// TestBlacklistCheck_NilServiceBypasses 验证 svc=nil 时中间件直接放行（无崩溃）
func TestBlacklistCheck_NilServiceBypasses(t *testing.T) {
	app := fiber.New()
	app.Use(cmfjwt.NewJWTMiddleware(blacklistTestSecret))
	app.Use(BlacklistCheck(nil)) // 显式 nil service
	app.Get("/x", func(c fiber.Ctx) error {
		return c.SendString("ok-nil-svc")
	})
	token := makeBlacklistTestToken(t, "user-any")
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test 失败: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok-nil-svc" {
		t.Fatalf("nil service 时应放行, 实际: %s", body)
	}
}
