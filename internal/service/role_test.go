package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/role"

	"github.com/wuwuseo/cmf/cache"
	"github.com/wuwuseo/cmf/config"

	_ "github.com/mattn/go-sqlite3"
)

// newTestRoleService creates a RoleService with test DB and cache
func newTestRoleService(t *testing.T) (*RoleService, *ent.Client, *cache.TypedCache[[]*PermissionResponse]) {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	cfg := &config.Config{}
	cfg.Cache.Default = "memory"
	cfg.Cache.Stores = map[string]struct {
		Driver     string `mapstructure:"driver"`
		DefaultTTL int    `mapstructure:"default_ttl"`
		Options    any    `mapstructure:"options"`
	}{
		"memory": {
			Driver:     "memory",
			DefaultTTL: 600,
		},
	}
	rawCache := cache.NewCache(context.Background(), cfg)
	typedCache := cache.NewTypedCache[[]*PermissionResponse](rawCache)

	svc := &RoleService{
		client: client,
		cache:  typedCache,
	}
	return svc, client, typedCache
}

// setupRoleTestData creates a role, user, user_role and populates user permission caches
func setupRoleTestData(t *testing.T, client *ent.Client, typedCache *cache.TypedCache[[]*PermissionResponse], roleName, domain string) (roleID int, userUID string) {
	t.Helper()
	ctx := context.Background()

	// Create role
	r, err := client.Role.Create().
		SetName(roleName).
		SetDisplayName(roleName + " Display").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create role: %v", err)
	}

	// Create user
	u, err := client.User.Create().
		SetEmail(fmt.Sprintf("%s@roletest.com", roleName)).
		SetPassword("hashedpwd").
		SetStatus("active").
		SetUID(fmt.Sprintf("uid-%s", roleName)).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	// Create user_role
	_, err = client.UserRole.Create().
		SetUserID(u.ID).
		SetRoleID(r.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create user_role: %v", err)
	}

	// Populate user permission caches using UID (matching production cache keys)
	menuKey := fmt.Sprintf("user:menu:%s:%s", u.UID, domain)
	buttonKey := fmt.Sprintf("user:button:%s:%s", u.UID, domain)
	apiKey := fmt.Sprintf("user:api:%s:%s", u.UID, domain)

	dummyPerms := []*PermissionResponse{{ID: 1, RoleName: roleName, Domain: domain, Object: "/test", Action: "menu"}}
	_ = typedCache.SetWithExpiration(ctx, menuKey, dummyPerms, svc_defaultCacheTTL)
	_ = typedCache.SetWithExpiration(ctx, buttonKey, dummyPerms, svc_defaultCacheTTL)
	_ = typedCache.SetWithExpiration(ctx, apiKey, dummyPerms, svc_defaultCacheTTL)

	return r.ID, u.UID
}

var svc_defaultCacheTTL = 5 * time.Minute

func TestUpdateRole_ClearsUserPermissionsCache(t *testing.T) {
	svc, client, typedCache := newTestRoleService(t)
	ctx := context.Background()
	domain := "default"
	roleID, userUID := setupRoleTestData(t, client, typedCache, "update_role", domain)

	// Verify caches are populated
	menuKey := fmt.Sprintf("user:menu:%s:%s", userUID, domain)
	_, err := typedCache.Get(ctx, menuKey)
	if err != nil {
		t.Fatalf("menu cache should be populated: %v", err)
	}

	// UpdateRole
	err = svc.UpdateRole(ctx, roleID, &UpdateRoleRequest{
		Description: "updated description",
	})
	if err != nil {
		t.Fatalf("UpdateRole failed: %v", err)
	}

	// Verify user permission caches are cleared
	_, err = typedCache.Get(ctx, menuKey)
	if err == nil {
		t.Fatal("menu cache should be invalidated after UpdateRole")
	}
	buttonKey := fmt.Sprintf("user:button:%s:%s", userUID, domain)
	_, err = typedCache.Get(ctx, buttonKey)
	if err == nil {
		t.Fatal("button cache should be invalidated after UpdateRole")
	}
	apiKey := fmt.Sprintf("user:api:%s:%s", userUID, domain)
	_, err = typedCache.Get(ctx, apiKey)
	if err == nil {
		t.Fatal("api cache should be invalidated after UpdateRole")
	}
}

func TestEnableRole_ClearsUserPermissionsCache(t *testing.T) {
	svc, client, typedCache := newTestRoleService(t)
	ctx := context.Background()
	domain := "default"

	// Create role in inactive state
	r, err := client.Role.Create().
		SetName("enable_role").
		SetDisplayName("Enable Role").
		SetStatus(role.StatusInactive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role failed: %v", err)
	}
	u, err := client.User.Create().
		SetEmail("enable@roletest.com").
		SetPassword("pwd").
		SetStatus("active").
		SetUID("uid-enable_role").
		Save(ctx)
	if err != nil {
		t.Fatalf("create user failed: %v", err)
	}
	_, err = client.UserRole.Create().SetUserID(u.ID).SetRoleID(r.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create user_role failed: %v", err)
	}

	// Populate caches using UID (matching production cache keys)
	menuKey := fmt.Sprintf("user:menu:%s:%s", u.UID, domain)
	buttonKey := fmt.Sprintf("user:button:%s:%s", u.UID, domain)
	apiKey := fmt.Sprintf("user:api:%s:%s", u.UID, domain)
	dummyPerms := []*PermissionResponse{{ID: 1}}
	_ = typedCache.SetWithExpiration(ctx, menuKey, dummyPerms, svc_defaultCacheTTL)
	_ = typedCache.SetWithExpiration(ctx, buttonKey, dummyPerms, svc_defaultCacheTTL)
	_ = typedCache.SetWithExpiration(ctx, apiKey, dummyPerms, svc_defaultCacheTTL)

	// EnableRole
	err = svc.EnableRole(ctx, r.ID)
	if err != nil {
		t.Fatalf("EnableRole failed: %v", err)
	}

	// Verify all user permission caches are cleared
	_, err = typedCache.Get(ctx, menuKey)
	if err == nil {
		t.Fatal("menu cache should be invalidated after EnableRole")
	}
	_, err = typedCache.Get(ctx, buttonKey)
	if err == nil {
		t.Fatal("button cache should be invalidated after EnableRole")
	}
	_, err = typedCache.Get(ctx, apiKey)
	if err == nil {
		t.Fatal("api cache should be invalidated after EnableRole")
	}
}

func TestDisableRole_ClearsUserPermissionsCache(t *testing.T) {
	svc, client, typedCache := newTestRoleService(t)
	ctx := context.Background()
	domain := "default"

	// Create role in active state
	r, err := client.Role.Create().
		SetName("disable_role").
		SetDisplayName("Disable Role").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role failed: %v", err)
	}
	u, err := client.User.Create().
		SetEmail("disable@roletest.com").
		SetPassword("pwd").
		SetStatus("active").
		SetUID("uid-disable_role").
		Save(ctx)
	if err != nil {
		t.Fatalf("create user failed: %v", err)
	}
	_, err = client.UserRole.Create().SetUserID(u.ID).SetRoleID(r.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create user_role failed: %v", err)
	}

	// Populate caches using UID (matching production cache keys)
	menuKey := fmt.Sprintf("user:menu:%s:%s", u.UID, domain)
	buttonKey := fmt.Sprintf("user:button:%s:%s", u.UID, domain)
	apiKey := fmt.Sprintf("user:api:%s:%s", u.UID, domain)
	dummyPerms := []*PermissionResponse{{ID: 1}}
	_ = typedCache.SetWithExpiration(ctx, menuKey, dummyPerms, svc_defaultCacheTTL)
	_ = typedCache.SetWithExpiration(ctx, buttonKey, dummyPerms, svc_defaultCacheTTL)
	_ = typedCache.SetWithExpiration(ctx, apiKey, dummyPerms, svc_defaultCacheTTL)

	// DisableRole
	err = svc.DisableRole(ctx, r.ID)
	if err != nil {
		t.Fatalf("DisableRole failed: %v", err)
	}

	// Verify all user permission caches are cleared
	_, err = typedCache.Get(ctx, menuKey)
	if err == nil {
		t.Fatal("menu cache should be invalidated after DisableRole")
	}
	_, err = typedCache.Get(ctx, buttonKey)
	if err == nil {
		t.Fatal("button cache should be invalidated after DisableRole")
	}
	_, err = typedCache.Get(ctx, apiKey)
	if err == nil {
		t.Fatal("api cache should be invalidated after DisableRole")
	}
}
