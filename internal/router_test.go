package internal

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appcaptcha "go-admin/internal/captcha"
	"go-admin/internal/casbin/adapter"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"
	appoauth "go-admin/internal/oauth"

	"github.com/gofiber/fiber/v3"
	"github.com/golang-jwt/jwt/v5"
	_ "github.com/mattn/go-sqlite3"
	cmfCasbin "github.com/wuwuseo/cmf/casbin"
	"github.com/wuwuseo/cmf/config"
	cmfJwt "github.com/wuwuseo/cmf/jwt"
)

const routerPermissionTestModel = `
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

func TestRegisterRouteRegistersPluginProxyRoutesWithoutProxySegment(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:router_plugin_proxy?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()
	global.ORM = client

	app := fiber.New()
	cfg := config.NewConfig()
	cfg.App.AdminSecret = "admin-test-secret"
	RegisterRoute(app, cfg)

	routes := app.GetRoutes(true)
	assertRouteRegistered(t, routes, "/api/v1/plugin/:plugin_id/*")
	assertRouteRegistered(t, routes, "/api/v1/plugin/:plugin_id/admin/auth")
	assertRouteRegistered(t, routes, "/api/v1/plugin/:plugin_id/admin/auth/*")
	assertRouteNotRegistered(t, routes, "/proxy")
}

func TestRegisterRouteRegistersGitHubOAuthRoutes(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:router_github_oauth?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()
	global.ORM = client

	app := fiber.New()
	cfg := config.NewConfig()
	cfg.App.Secret = "user-test-secret"
	cfg.App.AdminSecret = "admin-test-secret"
	RegisterRoute(app, cfg)

	routes := app.GetRoutes(true)
	for _, path := range []string{
		"/api/v1/public/oauth/providers",
		"/api/v1/public/oauth/user/:provider/authorize",
		"/api/v1/public/oauth/admin/:provider/authorize",
		"/api/v1/public/oauth/:provider/callback",
		"/api/v1/public/oauth/exchange",
		"/api/v1/user/auth/oauth/identities",
		"/api/v1/user/auth/oauth/:provider/authorize",
		"/api/v1/user/auth/oauth/:provider",
		"/api/v1/admin/auth/oauth/identities",
		"/api/v1/admin/auth/oauth/:provider/authorize",
		"/api/v1/admin/auth/oauth/:provider",
		"/api/v1/admin/auth/oauth/providers",
		"/api/v1/admin/auth/oauth/providers/:provider",
		"/api/v1/admin/auth/third-party-admins",
		"/api/v1/admin/auth/third-party-admins/:id",
	} {
		assertRouteRegistered(t, routes, path)
	}
	assertRouteNotRegistered(t, routes, "/google/")
}

func TestGitHubOAuthPublicEndpointsListAndRedirect(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:router_github_oauth_http?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()
	global.ORM = client

	key := bytes.Repeat([]byte{7}, 32)
	t.Setenv("OAUTH_CONFIG_MASTER_KEY", base64.StdEncoding.EncodeToString(key))
	cipher, err := appoauth.NewSecretCipher(key)
	if err != nil {
		t.Fatalf("NewSecretCipher: %v", err)
	}
	encryptedSecret, err := cipher.Encrypt("github-secret")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	_, err = client.OAuthProviderConfig.Create().
		SetProvider("github").
		SetDisplayName("GitHub").
		SetClientID("github-client").
		SetClientSecretCiphertext(encryptedSecret).
		SetCallbackURL("http://localhost:8080/api/v1/public/oauth/github/callback").
		SetUserRedirectURL("http://localhost:3000/oauth/callback").
		SetAdminRedirectURL("http://localhost:3001/oauth/callback").
		SetUserEnabled(true).
		SetAdminEnabled(true).
		Save(t.Context())
	if err != nil {
		t.Fatalf("create OAuth config: %v", err)
	}

	app := fiber.New()
	cfg := config.NewConfig()
	cfg.App.Secret = "user-test-secret"
	cfg.App.AdminSecret = "admin-test-secret"
	RegisterRoute(app, cfg)

	request, _ := http.NewRequest(http.MethodGet, "/api/v1/public/oauth/providers?subject_type=user", nil)
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("provider list request: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"provider":"github"`) || strings.Contains(string(body), `"provider":"google"`) {
		t.Fatalf("unexpected provider response status=%d body=%s", response.StatusCode, body)
	}

	request, _ = http.NewRequest(http.MethodGet, "/api/v1/public/oauth/user/github/authorize", nil)
	response, err = app.Test(request)
	if err != nil {
		t.Fatalf("authorize request: %v", err)
	}
	_ = response.Body.Close()
	location := response.Header.Get("Location")
	if response.StatusCode != http.StatusFound || !strings.HasPrefix(location, "https://github.com/login/oauth/authorize?") {
		t.Fatalf("unexpected authorize redirect status=%d location=%q", response.StatusCode, location)
	}
	if !strings.Contains(location, "code_challenge=") || !strings.Contains(location, "state=") {
		t.Fatalf("authorize redirect is missing PKCE/state: %q", location)
	}
}

func TestRegisterRouteRegistersCaptchaManagementAndPublicRoutes(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:router_captcha_routes?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()
	global.ORM = client

	app := fiber.New()
	cfg := config.NewConfig()
	cfg.App.AdminSecret = "admin-test-secret"
	cfg.Captcha.Store = "memory"
	RegisterRoute(app, cfg)

	routes := app.GetRoutes(true)
	for _, path := range []string{
		"/api/v1/public/captcha/challenges",
		"/api/v1/public/captcha/verifications",
		"/api/v1/admin/auth/captcha/settings",
		"/api/v1/admin/auth/captcha/scenes/:scene",
		"/api/v1/admin/auth/captcha/providers/:provider",
		"/api/v1/admin/auth/captcha/providers/:provider/test",
	} {
		assertRouteRegistered(t, routes, path)
	}
}

func TestRegisterRouteRequiresCaptchaTokenForEnabledLoginScenes(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:router_captcha_login?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()
	global.ORM = client
	repository := appcaptcha.NewSystemConfigRepository(client)
	for _, scene := range []string{"user_login", "admin_login"} {
		value, _ := json.Marshal(appcaptcha.SceneSettings{
			Enabled:      true,
			Provider:     "image_math",
			ChallengeTTL: 120,
			ProofTTL:     90,
		})
		if err := repository.Put(t.Context(), "captcha.scene."+scene, string(value)); err != nil {
			t.Fatalf("seed scene %s: %v", scene, err)
		}
	}

	app := fiber.New()
	cfg := config.NewConfig()
	cfg.App.AdminSecret = "admin-test-secret"
	cfg.Captcha.Store = "memory"
	RegisterRoute(app, cfg)

	for _, path := range []string{"/api/v1/public/login", "/api/v1/admin/public/login"} {
		response, err := app.Test(httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		var envelope struct {
			Code int `json:"code"`
		}
		if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if envelope.Code != global.ResponseCaptchaRequired {
			t.Fatalf("POST %s code = %d", path, envelope.Code)
		}
	}
}

// TestPluginProxyWildcardRouteEnforcesPermission 安全回归（VULN-M-002）：
// 通配代理路由 /api/v1/plugin/:plugin_id/* 必须经过 PermissionMiddleware，
// 无插件权限的已认证管理员请求应得到 403，而不是穿透到 ProxyPlugin。
func TestPluginProxyWildcardRouteEnforcesPermission(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:router_plugin_proxy_perm?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()
	global.ORM = client

	// 创建已激活管理员记录，保证 BlacklistCheck/角色解析走正常路径（结果为无角色）
	const testUID = "router-plugin-admin"
	_, err := client.Admin.Create().
		SetEmail("router-plugin-admin@test.com").
		SetUsername("routerpluginadmin").
		SetPassword("test-password").
		SetUID(testUID).
		SetStatus(admin.StatusActive).
		Save(t.Context())
	if err != nil {
		t.Fatalf("create admin record: %v", err)
	}

	// 初始化 Casbin（空策略库），Enforcer 正常工作但没有任何授权规则
	prevEnforcerManager := global.EnforcerManager
	cfg := &config.Config{}
	cfg.Casbin.DomainsDefault = "default"
	cfg.Casbin.Domains = []struct {
		Name      string `mapstructure:"name"`
		AutoLoad  bool   `mapstructure:"auto_load"`
		ModelPath string `mapstructure:"model_path"`
		ModelText string `mapstructure:"model_text"`
	}{
		{Name: "default", AutoLoad: true, ModelText: routerPermissionTestModel},
	}
	adp, err := adapter.NewAdapterWithClient(client)
	if err != nil {
		t.Fatalf("create casbin adapter: %v", err)
	}
	global.EnforcerManager = cmfCasbin.InitEnforcerManager(adp, cfg)
	t.Cleanup(func() { global.EnforcerManager = prevEnforcerManager })

	app := fiber.New()
	routerCfg := config.NewConfig()
	routerCfg.App.AdminSecret = "admin-test-secret"
	RegisterRoute(app, routerCfg)

	// 使用与 token_issuer 相同的 claims 结构签发有效管理员 JWT
	token, err := cmfJwt.CreateToken(jwt.MapClaims{
		"id": testUID, "subject": "routerpluginadmin", "issuer": "admin-api",
		"domain": "default", "token_type": "access",
		"expires":   jwt.NewNumericDate(time.Now().Add(time.Hour)),
		"issued_at": jwt.NewNumericDate(time.Now()),
	}, routerCfg.App.AdminSecret)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/plugin/demo/some/path", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("wildcard proxy request: %v", err)
	}
	defer response.Body.Close()
	var envelope struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode wildcard proxy response: %v", err)
	}
	if envelope.Code != http.StatusForbidden || !strings.Contains(envelope.Message, "没有权限") {
		t.Fatalf("无权限管理员应被权限中间件拒绝，实际 response=%+v", envelope)
	}
}

func assertRouteRegistered(t *testing.T, routes []fiber.Route, wantPath string) {
	t.Helper()
	for _, route := range routes {
		if route.Path == wantPath {
			return
		}
	}
	t.Fatalf("route %s is not registered", wantPath)
}

func assertRouteNotRegistered(t *testing.T, routes []fiber.Route, forbidden string) {
	t.Helper()
	for _, route := range routes {
		if strings.Contains(route.Path, forbidden) {
			t.Fatalf("route %s contains forbidden segment %s", route.Path, forbidden)
		}
	}
}
