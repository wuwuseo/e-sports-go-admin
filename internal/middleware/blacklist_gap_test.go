// Copyright 2026 The go-admin Authors. All Rights Reserved.
//
// blacklist_gap_test.go 覆盖 internal/middleware/blacklist.go 中
// accountIsActive 尚未被现有测试覆盖的关键分支：user 类型校验、
// 未知 accountType 报错、global.ORM 为 nil 时的 fail-closed 行为，
// 以及 SQL 异常被映射为 500 的兜底路径。这些路径直接影响"被停用的
// 账号仍能继续访问"和"底层 ORM 异常导致请求被错误放行"两类回归。
package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"
	"go-admin/internal/service"

	"github.com/gofiber/fiber/v3"
	jwtlib "github.com/golang-jwt/jwt/v5"
	_ "github.com/mattn/go-sqlite3"
	cmfjwt "github.com/wuwuseo/cmf/jwt"
)

func TestBlacklistCheck_RejectsTokensAfterPasswordReset(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:reset-access?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	t.Cleanup(func() { client.Close() })
	previous := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = previous })
	u := client.User.Create().SetUID("reset-access").SetUsername("resetaccess").SetEmail("reset-access@example.com").SetStatus(user.StatusActive).SaveX(t.Context())
	app := fiber.New()
	app.Use(cmfjwt.NewJWTMiddleware(blacklistTestSecret), BlacklistCheck(nil, "user"))
	app.Get("/protected", func(c fiber.Ctx) error { return c.JSON(fiber.Map{"code": global.ResponseSuccessCode}) })
	request := func(raw string, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		req.Header.Set("Authorization", "Bearer "+raw)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		if code, _ := readRespCode(t, resp); code != want {
			t.Fatalf("code=%d want=%d", code, want)
		}
	}
	legacy := makeBlacklistTestToken(t, u.UID)
	request(legacy, global.ResponseSuccessCode)
	if _, err := service.NewUserService().ResetManagedUserPassword(t.Context(), u.ID); err != nil {
		t.Fatal(err)
	}
	request(legacy, global.ResponseUnauthorized)
	for _, tc := range []struct {
		version any
		want    int
	}{
		{float64(0), global.ResponseUnauthorized},
		{float64(1), global.ResponseSuccessCode},
		{"1", global.ResponseUnauthorized},
		{float64(1.5), global.ResponseUnauthorized},
	} {
		token, err := cmfjwt.CreateToken(jwtlib.MapClaims{"id": u.UID, "exp": time.Now().Add(time.Hour).Unix(), "token_version": tc.version}, blacklistTestSecret)
		if err != nil {
			t.Fatal(err)
		}
		request(token, tc.want)
	}
}

// TestBlacklistCheck_RejectsInactiveUserAccount 验证 accountIsActive
// 在 accountType="user" 时也按 status=active 严格过滤。
func TestBlacklistCheck_RejectsInactiveUserAccount(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:blacklist-user-account?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	t.Cleanup(func() { _ = client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	client.User.Create().
		SetEmail("inactive-user@example.com").
		SetUID("inactive-user").
		SetUsername("inactive-user").
		SetPassword("hash").
		SetPasswordSalt("salt").
		SetNickname("Inactive").
		SetStatus(user.StatusInactive).
		SaveX(t.Context())

	app := fiber.New()
	app.Use(cmfjwt.NewJWTMiddleware(blacklistTestSecret))
	app.Use(BlacklistCheck(&mockBlacklistSvc{}, "user"))
	app.Get("/protected", func(c fiber.Ctx) error { return c.SendStatus(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+makeBlacklistTestToken(t, "inactive-user"))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	code, _ := readRespCode(t, resp)
	if code != global.ResponseUnauthorized {
		t.Fatalf("inactive user response code=%d, want %d", code, global.ResponseUnauthorized)
	}
}

// TestBlacklistCheck_AllowsActiveUserAccount 验证 status=active 的 user
// 可以正常通行，确保 user 分支不会误把所有 user 全部拒绝。
func TestBlacklistCheck_AllowsActiveUserAccount(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:blacklist-user-active?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	t.Cleanup(func() { _ = client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	client.User.Create().
		SetEmail("active-user@example.com").
		SetUID("active-user").
		SetUsername("active-user").
		SetPassword("hash").
		SetPasswordSalt("salt").
		SetNickname("Active").
		SetStatus(user.StatusActive).
		SaveX(t.Context())

	app := fiber.New()
	app.Use(cmfjwt.NewJWTMiddleware(blacklistTestSecret))
	app.Use(BlacklistCheck(&mockBlacklistSvc{}, "user"))
	app.Get("/protected", func(c fiber.Ctx) error { return c.SendString("ok-user") })
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+makeBlacklistTestToken(t, "active-user"))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("active user 应放行, 状态码=%d", resp.StatusCode)
	}
}

// TestBlacklistCheck_UnknownAccountTypeMapsTo500 验证传入未知的
// accountType 时，accountIsActive 返回错误，中间件把它降级为 500，
// 而不是错误地放行或返回 401。
func TestBlacklistCheck_UnknownAccountTypeMapsTo500(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:blacklist-unknown-type?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	t.Cleanup(func() { _ = client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	app := fiber.New()
	app.Use(cmfjwt.NewJWTMiddleware(blacklistTestSecret))
	app.Use(BlacklistCheck(&mockBlacklistSvc{}, "guest"))
	app.Get("/protected", func(c fiber.Ctx) error { return c.SendStatus(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+makeBlacklistTestToken(t, "whoever"))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	code, msg := readRespCode(t, resp)
	if code != global.ResponseInternalServerError {
		t.Fatalf("未知 accountType 应映射为 500, 实际 code=%d msg=%s", code, msg)
	}
	if msg != "账号状态校验异常" {
		t.Fatalf("错误消息不匹配: %s", msg)
	}
}

// TestBlacklistCheck_NilORMStrictDenies 验证 global.ORM 为 nil 时
// accountIsActive 直接返回错误，中间件应严格返回 500，不允许放行。
// 这是防御性兜底，避免 ORM 未初始化时把请求放过去。
func TestBlacklistCheck_NilORMStrictDenies(t *testing.T) {
	prevORM := global.ORM
	global.ORM = nil
	t.Cleanup(func() { global.ORM = prevORM })

	app := fiber.New()
	app.Use(cmfjwt.NewJWTMiddleware(blacklistTestSecret))
	app.Use(BlacklistCheck(&mockBlacklistSvc{}, "admin"))
	app.Get("/protected", func(c fiber.Ctx) error { return c.SendStatus(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+makeBlacklistTestToken(t, "uid-nil"))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	code, _ := readRespCode(t, resp)
	if code != global.ResponseInternalServerError {
		t.Fatalf("nil ORM 应映射为 500, 实际 code=%d", code)
	}
}

// TestBlacklistCheck_AccountQueryFailureMapsTo500 验证 accountIsActive
// 的 SQL 查询失败（通过关闭 client 强制）会映射为 500，而不是 401
// 拒绝（避免把"系统异常"误报为"账号停用"）。
func TestBlacklistCheck_AccountQueryFailureMapsTo500(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:blacklist-query-fail?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
	// 关闭 ORM 后任何 SQL 都会失败
	if err := client.Close(); err != nil {
		t.Fatalf("close client: %v", err)
	}

	app := fiber.New()
	app.Use(cmfjwt.NewJWTMiddleware(blacklistTestSecret))
	app.Use(BlacklistCheck(&mockBlacklistSvc{}, "admin"))
	app.Get("/protected", func(c fiber.Ctx) error { return c.SendStatus(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+makeBlacklistTestToken(t, "uid-closed"))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	code, msg := readRespCode(t, resp)
	if code != global.ResponseInternalServerError {
		t.Fatalf("account 查询失败应映射为 500, 实际 code=%d msg=%s", code, msg)
	}
	if msg != "账号状态校验异常" {
		t.Fatalf("错误消息不匹配: %s", msg)
	}
}

// TestAccountIsActive_NilORMReturnsError 直接验证 accountIsActive 函数
// 在 ORM=nil 时的行为（避免间接通过 HTTP 失败难定位）。
func TestAccountIsActive_NilORMReturnsError(t *testing.T) {
	prevORM := global.ORM
	global.ORM = nil
	t.Cleanup(func() { global.ORM = prevORM })

	_, err := accountIsActive(context.Background(), "uid-x", "admin", nil)
	if err == nil {
		t.Fatal("ORM 为 nil 时应返回非 nil 错误")
	}
}

// TestAccountIsActive_UnknownTypeReturnsError 直接验证未知 accountType
// 返回错误而不是默默通过。
func TestAccountIsActive_UnknownTypeReturnsError(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:blacklist-unknown-direct?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	t.Cleanup(func() { _ = client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	_, err := accountIsActive(context.Background(), "uid-x", "moderator", nil)
	if err == nil {
		t.Fatal("未知 accountType 应返回非 nil 错误")
	}
}
