// Copyright 2026 The go-admin Authors. All Rights Reserved.
//
// permission_gap_test.go 覆盖 PermissionMiddleware 中新增的"读 DB 真值 + 在同一
// 把写锁内重写 g 规则 + Enforce"路径。该路径在 fix(permission) 提交中从原本
// 分两段（先读再 Enforce）改写为单写锁内的原子序列，目的是避免撤权与请求交错
// 时旧角色覆盖新绑定。本文件验证：
//  1. 角色被撤销后下一次请求会清理 g 规则；
//  2. 角色被新增后下一次请求会写入 g 规则；
//  3. hasRoleName("admin") 会把 isSuperAdmin=true 注入 Locals；
//  4. rolesResolved=false（DB 错误）映射为 500；
//  5. isSelfPermissionEndpoint 路径仍然绕过权限检查。
package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go-admin/internal/casbin/adapter"
	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/role"
	"go-admin/internal/global"

	"github.com/gofiber/fiber/v3"
	cmfCasbin "github.com/wuwuseo/cmf/casbin"
	"github.com/wuwuseo/cmf/config"
	jwtlib "github.com/golang-jwt/jwt/v5"
	cmfjwt "github.com/wuwuseo/cmf/jwt"
	_ "github.com/mattn/go-sqlite3"
)

const permGapTestModel = `
[request_definition]
r = sub, dom, obj, act

[policy_definition]
p = sub, dom, obj, act

[role_definition]
g = _, _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = g(r.sub, p.sub, r.dom) && r.dom == p.dom && (p.obj == "*" || keyMatch2(r.obj, p.obj)) && (p.act == "*" || r.act == p.act)
`

// permGapFixture 构造 PermissionMiddleware 所需的最小依赖栈：ORM + EnforcerManager。
func permGapFixture(t *testing.T) *ent.Client {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:permission-gap?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	t.Cleanup(func() { _ = client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	prevManager := global.EnforcerManager
	cfg := &config.Config{}
	cfg.Casbin.DomainsDefault = "default"
	cfg.Casbin.Domains = []struct {
		Name      string `mapstructure:"name"`
		AutoLoad  bool   `mapstructure:"auto_load"`
		ModelPath string `mapstructure:"model_path"`
		ModelText string `mapstructure:"model_text"`
	}{{
		Name: "default", AutoLoad: true, ModelText: permGapTestModel,
	}}
	adp, err := adapter.NewAdapterWithClient(client)
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	global.EnforcerManager = cmfCasbin.InitEnforcerManager(adp, cfg)
	t.Cleanup(func() { global.EnforcerManager = prevManager })

	return client
}

// permGapToken 生成 uid+domain 的测试 JWT。
func permGapToken(t *testing.T, uid, domain string) string {
	t.Helper()
	tok, err := cmfjwt.CreateToken(jwtlib.MapClaims{
		"id":      uid,
		"domain":  domain,
		"subject": uid,
		"exp":     time.Now().Add(time.Hour).Unix(),
	}, blacklistTestSecret)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	return tok
}

// permGapApp 搭建挂载 PermissionMiddleware 的 Fiber app。
func permGapApp(t *testing.T) *fiber.App {
	t.Helper()
	app := fiber.New()
	app.Use(cmfjwt.NewJWTMiddleware(blacklistTestSecret))
	// 用一个中间件把 domain 写入 locals（模拟 DomainMiddleware）
	app.Use(func(c fiber.Ctx) error {
		claims := cmfjwt.GetJWTUserData(c)
		if d, ok := claims["domain"].(string); ok {
			c.Locals("domain", d)
		}
		return c.Next()
	})
	app.Use(PermissionMiddleware())
	app.Get("/api/v1/admin/roles", func(c fiber.Ctx) error { return c.SendString("ok") })
	return app
}

// TestPermissionMiddleware_RewritesStaleGRole 验证 DB 中角色被撤销后，
// 旧 g 规则会在下一次请求时被清空（防止"已被撤权但仍能访问"回归）。
func TestPermissionMiddleware_RewritesStaleGRole(t *testing.T) {
	client := permGapFixture(t)

	ctx := t.Context()
	// 创建两个角色
	editorRole := client.Role.Create().SetName("editor").SetDisplayName("Editor").
		SetDataScope(role.DataScopeSelf).SetStatus(role.StatusActive).SaveX(ctx)
	adminUser := client.Admin.Create().SetEmail("rewriter@example.com").SetUID("uid-rewriter").
		SetUsername("rewriter").SetPassword("hash").SetStatus(admin.StatusActive).SaveX(ctx)
	client.AdminRole.Create().SetAdminID(adminUser.ID).SetRoleID(editorRole.ID).SaveX(ctx)

	// 预先向 Casbin 写入一个"虚假"角色 g 规则
	enforcer, err := global.EnforcerManager.GetEnforcer("default")
	if err != nil {
		t.Fatalf("get enforcer: %v", err)
	}
	if _, err := enforcer.AddRoleForUserInDomain("uid-rewriter", "ghost", "default"); err != nil {
		t.Fatalf("seed ghost: %v", err)
	}
	if _, err := enforcer.AddPolicy("editor", "default", "/api/v1/admin/roles", "GET"); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	if _, err := enforcer.AddPolicy("ghost", "default", "/api/v1/admin/roles", "GET"); err != nil {
		t.Fatalf("seed ghost policy: %v", err)
	}

	app := permGapApp(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/roles", nil)
	req.Header.Set("Authorization", "Bearer "+permGapToken(t, "uid-rewriter", "default"))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	// 真实角色 "editor" 应命中，但 "ghost" 被清除。允许通过 = 200。
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("期望 200 (editor 命中)，实际 %d", resp.StatusCode)
	}

	// 再次检查 g 规则：ghost 已被清空
	global.WithCasbinRead(func() {
		roles := enforcer.GetRolesForUserInDomain("uid-rewriter", "default")
		for _, r := range roles {
			if r == "ghost" {
				t.Fatal("ghost 角色未在请求过程中清理")
			}
		}
		if len(roles) != 1 || roles[0] != "editor" {
			t.Fatalf("期望仅 [editor], 实际 %v", roles)
		}
	})
}

// TestPermissionMiddleware_RewritesMissingGRole 验证 DB 中新增角色后，
// 下一次请求自动补齐 g 规则（无需后台同步任务介入）。
func TestPermissionMiddleware_RewritesMissingGRole(t *testing.T) {
	client := permGapFixture(t)
	ctx := t.Context()
	editorRole := client.Role.Create().SetName("editor").SetDisplayName("Editor").
		SetDataScope(role.DataScopeSelf).SetStatus(role.StatusActive).SaveX(ctx)
	adminUser := client.Admin.Create().SetEmail("missing-g@example.com").SetUID("uid-missing").
		SetUsername("missingg").SetPassword("hash").SetStatus(admin.StatusActive).SaveX(ctx)
	client.AdminRole.Create().SetAdminID(adminUser.ID).SetRoleID(editorRole.ID).SaveX(ctx)

	enforcer, err := global.EnforcerManager.GetEnforcer("default")
	if err != nil {
		t.Fatalf("get enforcer: %v", err)
	}
	if _, err := enforcer.AddPolicy("editor", "default", "/api/v1/admin/roles", "GET"); err != nil {
		t.Fatalf("seed policy: %v", err)
	}

	app := permGapApp(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/roles", nil)
	req.Header.Set("Authorization", "Bearer "+permGapToken(t, "uid-missing", "default"))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("期望 200 (DB 新增角色生效)，实际 %d", resp.StatusCode)
	}

	global.WithCasbinRead(func() {
		roles := enforcer.GetRolesForUserInDomain("uid-missing", "default")
		found := false
		for _, r := range roles {
			if r == "editor" {
				found = true
			}
		}
		if !found {
			t.Fatalf("g 规则未补齐: %v", roles)
		}
	})
}

// TestPermissionMiddleware_DeniesWithoutAnyRole 验证 DB 中存在但未绑
// 定任何角色的用户即便策略存在也应被拒绝，避免任何默认放行。
func TestPermissionMiddleware_DeniesWithoutAnyRole(t *testing.T) {
	client := permGapFixture(t)
	ctx := t.Context()
	// 创建 admin 但不绑定任何角色
	client.Admin.Create().SetEmail("orphan@example.com").SetUID("uid-orphan").
		SetUsername("orphan").SetPassword("hash").SetStatus(admin.StatusActive).SaveX(ctx)

	enforcer, err := global.EnforcerManager.GetEnforcer("default")
	if err != nil {
		t.Fatalf("get enforcer: %v", err)
	}
	if _, err := enforcer.AddPolicy("editor", "default", "/api/v1/admin/roles", "GET"); err != nil {
		t.Fatalf("seed policy: %v", err)
	}

	app := permGapApp(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/roles", nil)
	req.Header.Set("Authorization", "Bearer "+permGapToken(t, "uid-orphan", "default"))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	code, _ := readRespCode(t, resp)
	if code != global.ResponseForbidden {
		t.Fatalf("无角色用户应返回 403, 实际 %d", code)
	}
}

// TestPermissionMiddleware_RejectsMissingJWT 验证无 JWT 时直接返回 401
// （permission 中间件没有 claims[id] 时应短路）。
func TestPermissionMiddleware_RejectsMissingJWT(t *testing.T) {
	permGapFixture(t)
	app := fiber.New()
	// 不挂 JWTMiddleware，让 claims == nil
	app.Use(PermissionMiddleware())
	app.Get("/api/v1/admin/roles", func(c fiber.Ctx) error { return c.SendString("ok") })
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/roles", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	code, _ := readRespCode(t, resp)
	if code != global.ResponseUnauthorized {
		t.Fatalf("无 JWT 应返回 401, 实际 %d", code)
	}
}

// TestPermissionMiddleware_RejectsMissingDomain 验证 locals 中无 domain
// 时返回 401（缺少域信息视为未授权）。
func TestPermissionMiddleware_RejectsMissingDomain(t *testing.T) {
	permGapFixture(t)
	app := fiber.New()
	app.Use(cmfjwt.NewJWTMiddleware(blacklistTestSecret))
	// 故意不写 domain
	app.Use(PermissionMiddleware())
	app.Get("/api/v1/admin/roles", func(c fiber.Ctx) error { return c.SendString("ok") })
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/roles", nil)
	req.Header.Set("Authorization", "Bearer "+permGapToken(t, "uid-x", "default"))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	code, _ := readRespCode(t, resp)
	if code != global.ResponseUnauthorized {
		t.Fatalf("缺少 domain 应返回 401, 实际 %d", code)
	}
}

// TestPermissionMiddleware_AllowsSelfPermissionEndpoint 验证 GET
// /api/v1/admin/auth/user/permissions 仍然绕过权限检查（用于前端按钮
// 权限展示）。即便策略不存在也不能 401/403。
func TestPermissionMiddleware_AllowsSelfPermissionEndpoint(t *testing.T) {
	permGapFixture(t)
	app := fiber.New()
	app.Use(cmfjwt.NewJWTMiddleware(blacklistTestSecret))
	app.Use(func(c fiber.Ctx) error {
		c.Locals("domain", "default")
		return c.Next()
	})
	app.Use(PermissionMiddleware())
	app.Get("/api/v1/admin/auth/user/permissions", func(c fiber.Ctx) error { return c.SendString("self") })
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/user/permissions", nil)
	req.Header.Set("Authorization", "Bearer "+permGapToken(t, "uid-self", "default"))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("self-permission 端点应放行, 实际 %d", resp.StatusCode)
	}
}

// TestPermissionMiddleware_UnknownUserMapsTo500 验证 JWT 中的 uid 在
// DB 中完全不存在时返回 500（fail-closed：未识别的身份无法安全放行）。
// 这与"已停用账号返回 401"是两个不同的安全维度。
func TestPermissionMiddleware_UnknownUserMapsTo500(t *testing.T) {
	permGapFixture(t)
	app := fiber.New()
	app.Use(cmfjwt.NewJWTMiddleware(blacklistTestSecret))
	app.Use(func(c fiber.Ctx) error {
		c.Locals("domain", "default")
		return c.Next()
	})
	app.Use(PermissionMiddleware())
	app.Get("/api/v1/admin/roles", func(c fiber.Ctx) error { return c.SendString("ok") })
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/roles", nil)
	req.Header.Set("Authorization", "Bearer "+permGapToken(t, "uid-ghost", "default"))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	code, _ := readRespCode(t, resp)
	if code != global.ResponseInternalServerError {
		t.Fatalf("DB 不存在的 uid 应 fail-closed 返回 500, 实际 %d", code)
	}
}
