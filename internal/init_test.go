package internal

import (
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	cmflog "github.com/wuwuseo/cmf/log"

	"go-admin/internal/ent"
	"go-admin/internal/ent/casbinrule"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/role"
	"go-admin/internal/global"
)

func setupInitTest(t *testing.T) *ent.Client {
	t.Helper()

	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	client := enttest.Open(t, "sqlite3", "file:"+dbName+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)

	prevApp := appInstance
	prevORM := global.ORM
	prevEnforcerManager := global.EnforcerManager
	appInstance = &App{Logger: cmflog.GetDefault()}
	global.ORM = client
	global.EnforcerManager = nil

	t.Cleanup(func() {
		client.Close()
		appInstance = prevApp
		global.ORM = prevORM
		global.EnforcerManager = prevEnforcerManager
	})

	return client
}

func createInitSystemPermission(t *testing.T, client *ent.Client, typ, name, code string, parentID int, object, action, description string) *ent.SystemPermission {
	t.Helper()

	create := client.SystemPermission.Create().
		SetDomain("default").
		SetType(typ).
		SetName(name).
		SetCode(code).
		SetParentID(parentID).
		SetSortOrder(1).
		SetStatus(1).
		SetDescription(description)

	switch typ {
	case "api":
		create.SetPath(object).SetMethod(action)
	case "menu":
		create.SetMenuPath(object)
	}

	perm, err := create.Save(t.Context())
	if err != nil {
		t.Fatalf("create system permission %s failed: %v", code, err)
	}
	return perm
}

func createInitCasbinRule(t *testing.T, client *ent.Client, roleName, domain, object, action, typ, description string) {
	t.Helper()

	create := client.CasbinRule.Create().
		SetPtype("p").
		SetV0(roleName).
		SetV1(domain).
		SetV2(object).
		SetV3(action).
		SetV4(typ)
	if description != "" {
		create.SetV5(description)
	}
	if _, err := create.Save(t.Context()); err != nil {
		t.Fatalf("create casbin rule %s %s failed: %v", object, action, err)
	}
}

func initCasbinRuleExists(t *testing.T, client *ent.Client, roleName, domain, object, action string) bool {
	t.Helper()

	exists, err := client.CasbinRule.Query().
		Where(
			casbinrule.PtypeEQ("p"),
			casbinrule.V0EQ(roleName),
			casbinrule.V1EQ(domain),
			casbinrule.V2EQ(object),
			casbinrule.V3EQ(action),
		).Exist(t.Context())
	if err != nil {
		t.Fatalf("check casbin rule %s %s failed: %v", object, action, err)
	}
	return exists
}

func TestInitNormalAdminRoleDoesNotSeedPluginPermissions(t *testing.T) {
	client := setupInitTest(t)
	ctx := t.Context()

	createInitSystemPermission(t, client, "api", "List roles", "role:list", 0, "/api/v1/admin/auth/roles", "GET", "List roles API")
	pluginMenu := createInitSystemPermission(t, client, "menu", "Plugin management", "plugin", 0, "/system/plugin", "menu", "Plugin management page")
	createInitSystemPermission(t, client, "api", "List plugins", "plugin:list", pluginMenu.ID, "/api/v1/admin/auth/plugins", "GET", "List plugins API")
	createInitSystemPermission(t, client, "button", "Install plugin", "plugin:install", pluginMenu.ID, "plugin:install", "button", "Install plugin button")

	if err := initNormalAdminRole(ctx, client); err != nil {
		t.Fatalf("init normal_admin role failed: %v", err)
	}

	if !initCasbinRuleExists(t, client, "normal_admin", "default", "/api/v1/admin/auth/roles", "GET") {
		t.Fatal("expected new normal_admin role to receive non-plugin baseline permissions")
	}
	for _, rule := range []struct {
		object string
		action string
	}{
		{object: "/system/plugin", action: "menu"},
		{object: "/api/v1/admin/auth/plugins", action: "GET"},
		{object: "plugin:install", action: "button"},
	} {
		if initCasbinRuleExists(t, client, "normal_admin", "default", rule.object, rule.action) {
			t.Fatalf("normal_admin should not receive plugin permission %s %s by default", rule.object, rule.action)
		}
	}
}

func TestInitNormalAdminRoleDoesNotBackfillExistingRolePermissions(t *testing.T) {
	client := setupInitTest(t)
	ctx := t.Context()

	_, err := client.Role.Create().
		SetName("normal_admin").
		SetDisplayName("Normal admin").
		SetIsSystem(true).
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create normal_admin role failed: %v", err)
	}

	createInitSystemPermission(t, client, "api", "List roles", "role:list", 0, "/api/v1/admin/auth/roles", "GET", "List roles API")
	pluginMenu := createInitSystemPermission(t, client, "menu", "Plugin management", "plugin", 0, "/system/plugin", "menu", "Plugin management page")
	pluginAPI := createInitSystemPermission(t, client, "api", "List plugins", "plugin:list", pluginMenu.ID, "/api/v1/admin/auth/plugins", "GET", "List plugins API")
	createInitSystemPermission(t, client, "button", "Install plugin", "plugin:install", pluginMenu.ID, "plugin:install", "button", "Install plugin button")

	createInitCasbinRule(t, client, "normal_admin", "default", pluginAPI.Path, pluginAPI.Method, "api", pluginAPI.Description)
	createInitCasbinRule(t, client, "normal_admin", "default", "plugin:install", "button", "button", "manual grant")

	if err := initNormalAdminRole(ctx, client); err != nil {
		t.Fatalf("init normal_admin role failed: %v", err)
	}

	if initCasbinRuleExists(t, client, "normal_admin", "default", "/api/v1/admin/auth/roles", "GET") {
		t.Fatal("existing normal_admin role should not be backfilled with permissions during init")
	}
	if initCasbinRuleExists(t, client, "normal_admin", "default", pluginAPI.Path, pluginAPI.Method) {
		t.Fatal("expected init-seeded plugin permission to be cleaned from existing normal_admin role")
	}
	if !initCasbinRuleExists(t, client, "normal_admin", "default", "plugin:install", "button") {
		t.Fatal("manual plugin grant should be preserved")
	}
}
