package service

import (
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"

	cmfCache "github.com/wuwuseo/cmf/cache"
	cmfConfig "github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/bootstrap"
	"github.com/wuwuseo/cmf/crypto"

	_ "github.com/mattn/go-sqlite3"
)

func setupFinalCoverage(t *testing.T) *ent.Client {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
	return client
}

// ======================== UserService Logout with cache ========================

func TestUserService_Logout_WithCache(t *testing.T) {
	client := setupFinalCoverage(t)
	ctx := t.Context()

	cfg := &cmfConfig.Config{}
	cfg.Cache.Default = "memory"
	cfg.Cache.Stores = map[string]struct {
		Driver     string `mapstructure:"driver"`
		DefaultTTL int    `mapstructure:"default_ttl"`
		Options    any    `mapstructure:"options"`
	}{
		"memory": {Driver: "memory", DefaultTTL: 3600},
	}
	cacheInst := cmfCache.NewCache(ctx, cfg)

	bs := bootstrap.NewBootstrap()
	appCfg := &cmfConfig.Config{}
	appCfg.App.LoginExpires = 86400
	bs.RegisterService("config", appCfg)
	prevBS := global.Bootstrap
	global.Bootstrap = bs
	t.Cleanup(func() { global.Bootstrap = prevBS })

	svc := NewUserServiceWithCache(cacheInst)
	err := svc.Logout(ctx, "uid-logout-cache")
	if err != nil {
		t.Fatalf("Logout with cache: %v", err)
	}

	_ = client
}

// ======================== UserService Logout with cache via bootstrap ========================

func TestUserService_Logout_Bootstrap(t *testing.T) {
	client := setupFinalCoverage(t)

	cfg := &cmfConfig.Config{}
	cfg.App.LoginExpires = 86400
	cfg.Cache.Default = "memory"
	cfg.Cache.Stores = map[string]struct {
		Driver     string `mapstructure:"driver"`
		DefaultTTL int    `mapstructure:"default_ttl"`
		Options    any    `mapstructure:"options"`
	}{
		"memory": {Driver: "memory", DefaultTTL: 3600},
	}

	bs := bootstrap.NewBootstrap()
	bs.RegisterService("config", cfg)
	prevBS := global.Bootstrap
	global.Bootstrap = bs
	t.Cleanup(func() { global.Bootstrap = prevBS })

	ctx := t.Context()
	cacheInst := cmfCache.NewCache(ctx, cfg)
	svc := NewUserServiceWithCache(cacheInst)

	err := svc.Logout(ctx, "uid-bootstrap-logout")
	if err != nil {
		t.Fatalf("Logout with bootstrap: %v", err)
	}

	_ = client
}

// ======================== Permission UpdatePermission edge cases ========================

func TestPermissionService_UpdatePermission_NewObjectOnly(t *testing.T) {
	client := setupFinalCoverage(t)
	ctx := t.Context()
	svc := NewPermissionService(nil)

	_, err := client.Role.Create().
		SetName("update-edge-role").
		SetDisplayName("update edge role").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role: %v", err)
	}

	svc.CreatePermission(ctx, "", &CreatePermissionRequest{RoleName: "update-edge-role", Domain: "default", Type: PermissionTypeAPI, Object: "/api/orig", Action: "GET"})

	// update only NewObject, keep NewAction same
	err = svc.UpdatePermission(ctx, "", &UpdatePermissionRequest{
		RoleName:  "update-edge-role",
		Domain:    "default",
		Type:      PermissionTypeAPI,
		Object:    "/api/orig",
		Action:    "GET",
		NewObject: "/api/newpath",
		NewAction: "GET",
	})
	if err != nil {
		t.Fatalf("UpdatePermission new object: %v", err)
	}
}

func TestPermissionService_UpdatePermission_OnlyDescription(t *testing.T) {
	client := setupFinalCoverage(t)
	ctx := t.Context()
	svc := NewPermissionService(nil)

	_, err := client.Role.Create().
		SetName("desc-role").
		SetDisplayName("desc role").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role: %v", err)
	}

	svc.CreatePermission(ctx, "", &CreatePermissionRequest{RoleName: "desc-role", Domain: "default", Type: PermissionTypeAPI, Object: "/api/desc", Action: "POST", Description: "old desc"})

	err = svc.UpdatePermission(ctx, "", &UpdatePermissionRequest{
		RoleName:    "desc-role",
		Domain:      "default",
		Type:        PermissionTypeAPI,
		Object:      "/api/desc",
		Action:      "POST",
		Description: "new description",
	})
	if err != nil {
		t.Fatalf("UpdatePermission desc: %v", err)
	}
}

// ======================== Permission DeletePermission all paths ========================

func TestPermissionService_DeletePermission_CachePaths(t *testing.T) {
	client := setupFinalCoverage(t)
	ctx := t.Context()

	cfg := &cmfConfig.Config{}
	cfg.Cache.Default = "memory"
	cfg.Cache.Stores = map[string]struct {
		Driver     string `mapstructure:"driver"`
		DefaultTTL int    `mapstructure:"default_ttl"`
		Options    any    `mapstructure:"options"`
	}{
		"memory": {Driver: "memory", DefaultTTL: 3600},
	}
	cacheInst := cmfCache.NewCache(ctx, cfg)
	svc := NewPermissionService(cacheInst)

	r, _ := client.Role.Create().SetName("del-cache-role").SetDisplayName("del cache").SetStatus(role.StatusActive).Save(ctx)
	u, _ := client.User.Create().SetEmail("delcache@t.com").SetUID("uid-del-cache").SetUsername("delcache").SetPassword("p").SetPasswordSalt("s").SetStatus(user.StatusActive).Save(ctx)
	client.UserRole.Create().SetUserID(u.ID).SetRoleID(r.ID).Save(ctx)

	svc.CreatePermission(ctx, "", &CreatePermissionRequest{RoleName: "del-cache-role", Domain: "default", Type: PermissionTypeAPI, Object: "/api/delcache", Action: "GET"})

	err := svc.DeletePermission(ctx, "", "del-cache-role", "default", "/api/delcache", "GET")
	if err != nil {
		t.Fatalf("DeletePermission with cache: %v", err)
	}
}

// ======================== Bootstrap Login/RefreshToken/ChangePassword ========================

func TestIntegration_AuthEndToEnd(t *testing.T) {
	client := setupFinalCoverage(t)

	cfg := &cmfConfig.Config{}
	cfg.App.Name = "test-e2e"
	cfg.App.Secret = "e2e-test-secret"
	cfg.App.LoginExpires = 86400
	cfg.App.RefreshExpires = 604800

	bs := bootstrap.NewBootstrap()
	bs.RegisterService("config", cfg)
	prevBS := global.Bootstrap
	global.Bootstrap = bs
	t.Cleanup(func() { global.Bootstrap = prevBS })

	ctx := t.Context()
	hash, salt, _ := crypto.HashPassword("e2e-pass")
	client.User.Create().
		SetEmail("e2e-auth@test.com").
		SetUID("uid-e2e-auth").
		SetUsername("e2eauth").
		SetPassword(hash).
		SetPasswordSalt(salt).
		SetStatus(user.StatusActive).
		Save(ctx)

	svc := NewUserService()

	// Login
	resp, err := svc.Login(ctx, &LoginRequest{Email: "e2e-auth@test.com", Password: "e2e-pass"})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	// RefreshToken
	refreshed, err := svc.RefreshToken(ctx, resp.RefreshToken)
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if refreshed.AccessToken == "" {
		t.Fatal("expected new access token")
	}

	// ChangePassword
	err = svc.ChangePassword(ctx, "uid-e2e-auth", "e2e-pass", "e2e-new-pass")
	if err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	// Login new password
	_, err = svc.Login(ctx, &LoginRequest{Email: "e2e-auth@test.com", Password: "e2e-new-pass"})
	if err != nil {
		t.Fatalf("new pass login: %v", err)
	}
}

// ======================== DictData GetByType empty result ========================

func TestDictDataService_GetByType_NotFound(t *testing.T) {
	_ = setupFinalCoverage(t)
	ctx := t.Context()
	svc := NewDictDataService()

	data, err := svc.GetByType(ctx, "nonexistent_type")
	if err != nil {
		t.Fatalf("GetByType: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("want 0, got %d", len(data))
	}
}
