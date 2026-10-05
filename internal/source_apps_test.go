package internal

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	jwtlib "github.com/golang-jwt/jwt/v5"
	cmfCasbin "github.com/wuwuseo/cmf/casbin"
	"github.com/wuwuseo/cmf/config"
	cmfJWT "github.com/wuwuseo/cmf/jwt"

	"go-admin/internal/apps"
	"go-admin/internal/casbin/adapter"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/casbinrule"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/systempermission"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"
)

func TestSourceAppProvisionPrecedesNormalAdminGrant(t *testing.T) {
	client := setupInitTest(t)
	cfg := &config.Config{}
	cfg.Casbin.DomainsDefault = "default"
	if err := apps.Provision(t.Context(), cfg, client); err != nil {
		t.Fatal(err)
	}
	if err := initNormalAdminRole(t.Context(), client, "default"); err != nil {
		t.Fatal(err)
	}
	api := client.SystemPermission.Query().Where(systempermission.CodeEQ("cms:contents:list")).OnlyX(t.Context())
	count := client.CasbinRule.Query().Where(
		casbinrule.PtypeEQ("p"), casbinrule.V0EQ("normal_admin"),
		casbinrule.V2EQ(api.Path), casbinrule.V3EQ(api.Method),
	).CountX(t.Context())
	if count != 1 {
		t.Fatalf("new normal_admin has %d CMS API grants", count)
	}
	client.CasbinRule.Delete().Where(casbinrule.PtypeEQ("p"), casbinrule.V0EQ("normal_admin"), casbinrule.V2EQ(api.Path)).ExecX(t.Context())
	if err := initNormalAdminRole(t.Context(), client, "default"); err != nil {
		t.Fatal(err)
	}
	count = client.CasbinRule.Query().Where(casbinrule.PtypeEQ("p"), casbinrule.V0EQ("normal_admin"), casbinrule.V2EQ(api.Path)).CountX(t.Context())
	if count != 0 {
		t.Fatal("existing normal_admin grant was restored")
	}
}

func TestSourceAppRouteAuthentication(t *testing.T) {
	client := setupInitTest(t)
	cfg := &config.Config{}
	cfg.App.Secret = "source-user-secret"
	cfg.App.AdminSecret = "source-admin-secret"
	cfg.Casbin.DomainsDefault = "default"
	cfg.Casbin.Domains = []struct {
		Name      string `mapstructure:"name"`
		AutoLoad  bool   `mapstructure:"auto_load"`
		ModelPath string `mapstructure:"model_path"`
		ModelText string `mapstructure:"model_text"`
	}{{Name: "default", AutoLoad: true, ModelText: routerPermissionTestModel}}
	adp, err := adapter.NewAdapterWithClient(client)
	if err != nil {
		t.Fatal(err)
	}
	global.EnforcerManager = cmfCasbin.InitEnforcerManager(adp, cfg)
	adminRole := client.Role.Create().SetName("admin").SetDisplayName("Admin").SetStatus(role.StatusActive).SaveX(t.Context())
	super := client.Admin.Create().SetUID("source-super").SetUsername("source-super").SetEmail("source-super@example.com").SetPassword("hash").SetStatus(admin.StatusActive).SaveX(t.Context())
	client.AdminRole.Create().SetAdminID(super.ID).SetRoleID(adminRole.ID).SaveX(t.Context())
	client.Admin.Create().SetUID("source-limited").SetUsername("source-limited").SetEmail("source-limited@example.com").SetPassword("hash").SetStatus(admin.StatusActive).SaveX(t.Context())
	client.User.Create().SetUID("source-user").SetEmail("source-user@example.com").SetStatus(user.StatusActive).SaveX(t.Context())
	enforcer, err := global.EnforcerManager.GetEnforcer("default")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enforcer.AddPolicy("admin", "default", "*", "*"); err != nil {
		t.Fatal(err)
	}
	if _, err := enforcer.AddRoleForUserInDomain(super.UID, "admin", "default"); err != nil {
		t.Fatal(err)
	}
	f := fiber.New()
	if err := apps.Mount(f.Group("/api/v1"), cfg, nil); err != nil {
		t.Fatal(err)
	}
	check := func(path, secret, uid string, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if secret != "" {
			claims := jwtlib.MapClaims{"id": uid, "domain": "default", "token_type": "access", "token_version": 0,
				"expires": jwtlib.NewNumericDate(time.Now().Add(time.Hour))}
			token, err := cmfJWT.CreateToken(claims, secret)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := f.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		actual := resp.StatusCode
		if actual == http.StatusOK && want != http.StatusOK {
			var body struct {
				Code int `json:"code"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err == nil && body.Code != 0 {
				actual = body.Code
			}
		}
		if actual != want {
			t.Fatalf("GET %s uid=%s status/code=%d, want %d", path, uid, actual, want)
		}
	}
	check("/api/v1/cms/articles", "", "", http.StatusOK)
	check("/api/v1/cms/admin/contents", "", "", http.StatusUnauthorized)
	check("/api/v1/cms/admin/contents", cfg.App.AdminSecret, "source-limited", http.StatusForbidden)
	check("/api/v1/cms/admin/contents", cfg.App.AdminSecret, super.UID, http.StatusOK)
}
