package service

import (
	"context"
	"fmt"
	"os"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/role"

	"github.com/wuwuseo/cmf/cache"
	"github.com/wuwuseo/cmf/config"

	_ "github.com/mattn/go-sqlite3"
)

func TestMain(m *testing.M) {
	// Fix RefTable on foreign keys (not set by ent codegen)
	migrate.UserRolesTable.ForeignKeys[0].RefTable = migrate.UsersTable
	migrate.UserRolesTable.ForeignKeys[1].RefTable = migrate.RolesTable
	migrate.AttachmentsTable.ForeignKeys[0].RefTable = migrate.AttachmentGroupsTable
	os.Exit(m.Run())
}

// newTestCache creates an in-memory cache for testing
func newTestCache(t *testing.T) *cache.Cache[[]byte] {
	t.Helper()
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
	return cache.NewCache(context.Background(), cfg)
}

// newTestPermissionService creates a PermissionService with test DB and cache
func newTestPermissionService(t *testing.T) (*PermissionService, *ent.Client, *cache.TypedCache[[]*PermissionResponse]) {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	rawCache := newTestCache(t)
	typedCache := cache.NewTypedCache[[]*PermissionResponse](rawCache)

	svc := &PermissionService{
		client: client,
		cache:  typedCache,
	}
	return svc, client, typedCache
}

// setupTestData creates a role, user, user_role, and casbin rule for testing
func setupTestData(t *testing.T, client *ent.Client, roleName, domain string) (userUID string) {
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

	// Create user with UID
	uid := fmt.Sprintf("uid-%s", roleName)
	u, err := client.User.Create().
		SetEmail(fmt.Sprintf("%s@test.com", roleName)).
		SetPassword("hashedpwd").
		SetStatus("active").
		SetUID(uid).
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

	// Create a menu permission casbin rule
	_, err = client.CasbinRule.Create().
		SetPtype("p").
		SetV0(roleName).
		SetV1(domain).
		SetV2("/dashboard").
		SetV3("menu").
		SetV4("menu").
		Save(ctx)
	if err != nil {
		t.Fatalf("failed to create casbin rule: %v", err)
	}

	return uid
}

func TestGetUserMenuPermissions_CacheHit(t *testing.T) {
	svc, client, typedCache := newTestPermissionService(t)
	ctx := context.Background()
	domain := "default"
	userID := setupTestData(t, client, "testrole1", domain)

	// First call - should query DB and populate cache
	perms1, err := svc.GetUserMenuPermissions(ctx, userID, domain)
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if len(perms1) != 1 {
		t.Fatalf("expected 1 permission, got %d", len(perms1))
	}

	// Verify cache is populated
	cacheKey := svc.cacheKey("user", "menu", userID, domain)
	cached, err := typedCache.Get(ctx, cacheKey)
	if err != nil {
		t.Fatalf("cache should be populated, got error: %v", err)
	}
	if len(cached) != 1 {
		t.Fatalf("cache should have 1 permission, got %d", len(cached))
	}

	// Second call - should return from cache (same result)
	perms2, err := svc.GetUserMenuPermissions(ctx, userID, domain)
	if err != nil {
		t.Fatalf("second call failed: %v", err)
	}
	if len(perms2) != len(perms1) {
		t.Fatalf("cache hit should return same data, got %d vs %d", len(perms2), len(perms1))
	}
}

func TestAssignPermissionsToRole_ClearsCache(t *testing.T) {
	svc, client, typedCache := newTestPermissionService(t)
	ctx := context.Background()
	domain := "default"
	userID := setupTestData(t, client, "testrole2", domain)

	// Populate cache
	_, err := svc.GetUserMenuPermissions(ctx, userID, domain)
	if err != nil {
		t.Fatalf("GetUserMenuPermissions failed: %v", err)
	}

	// Verify cache is populated
	cacheKey := svc.cacheKey("user", "menu", userID, domain)
	_, err = typedCache.Get(ctx, cacheKey)
	if err != nil {
		t.Fatalf("cache should be populated: %v", err)
	}

	// Assign new permission - should clear cache
	err = svc.AssignPermissionsToRole(ctx, "testrole2", domain, []PermissionRule{
		{Object: "/settings", Action: "menu"},
	})
	if err != nil {
		t.Fatalf("AssignPermissionsToRole failed: %v", err)
	}

	// Verify cache is cleared
	_, err = typedCache.Get(ctx, cacheKey)
	if err == nil {
		t.Fatal("cache should be invalidated after AssignPermissionsToRole")
	}

	// Re-query should return updated data
	perms, err := svc.GetUserMenuPermissions(ctx, userID, domain)
	if err != nil {
		t.Fatalf("re-query failed: %v", err)
	}
	if len(perms) != 2 {
		t.Fatalf("expected 2 permissions after assign, got %d", len(perms))
	}
}

func TestRemovePermissionsFromRole_ClearsCache(t *testing.T) {
	svc, client, typedCache := newTestPermissionService(t)
	ctx := context.Background()
	domain := "default"
	userID := setupTestData(t, client, "testrole3", domain)

	// Populate cache
	_, err := svc.GetUserMenuPermissions(ctx, userID, domain)
	if err != nil {
		t.Fatalf("GetUserMenuPermissions failed: %v", err)
	}

	cacheKey := svc.cacheKey("user", "menu", userID, domain)
	_, err = typedCache.Get(ctx, cacheKey)
	if err != nil {
		t.Fatalf("cache should be populated: %v", err)
	}

	// Remove permission - should clear cache
	err = svc.RemovePermissionsFromRole(ctx, "testrole3", domain, []PermissionRule{
		{Object: "/dashboard", Action: "menu"},
	})
	if err != nil {
		t.Fatalf("RemovePermissionsFromRole failed: %v", err)
	}

	// Verify cache cleared
	_, err = typedCache.Get(ctx, cacheKey)
	if err == nil {
		t.Fatal("cache should be invalidated after RemovePermissionsFromRole")
	}

	// Re-query should return empty
	perms, err := svc.GetUserMenuPermissions(ctx, userID, domain)
	if err != nil {
		t.Fatalf("re-query failed: %v", err)
	}
	if len(perms) != 0 {
		t.Fatalf("expected 0 permissions after remove, got %d", len(perms))
	}
}

func TestCreatePermission_ClearsCache(t *testing.T) {
	svc, client, typedCache := newTestPermissionService(t)
	ctx := context.Background()
	domain := "default"
	userID := setupTestData(t, client, "testrole4", domain)

	// Populate cache
	_, err := svc.GetUserMenuPermissions(ctx, userID, domain)
	if err != nil {
		t.Fatalf("populate cache failed: %v", err)
	}

	cacheKey := svc.cacheKey("user", "menu", userID, domain)
	_, err = typedCache.Get(ctx, cacheKey)
	if err != nil {
		t.Fatalf("cache should be populated: %v", err)
	}

	// Create permission
	err = svc.CreatePermission(ctx, &CreatePermissionRequest{
		RoleName: "testrole4",
		Domain:   domain,
		Type:     PermissionTypeMenu,
		Object:   "/users",
		Action:   "menu",
	})
	if err != nil {
		t.Fatalf("CreatePermission failed: %v", err)
	}

	// Verify cache cleared
	_, err = typedCache.Get(ctx, cacheKey)
	if err == nil {
		t.Fatal("cache should be invalidated after CreatePermission")
	}
}

func TestUpdatePermission_ClearsCache(t *testing.T) {
	svc, client, typedCache := newTestPermissionService(t)
	ctx := context.Background()
	domain := "default"
	userID := setupTestData(t, client, "testrole5", domain)

	// Populate cache
	_, err := svc.GetUserMenuPermissions(ctx, userID, domain)
	if err != nil {
		t.Fatalf("populate cache failed: %v", err)
	}

	cacheKey := svc.cacheKey("user", "menu", userID, domain)
	_, err = typedCache.Get(ctx, cacheKey)
	if err != nil {
		t.Fatalf("cache should be populated: %v", err)
	}

	// Update permission
	err = svc.UpdatePermission(ctx, &UpdatePermissionRequest{
		RoleName:  "testrole5",
		Domain:    domain,
		Type:      PermissionTypeMenu,
		Object:    "/dashboard",
		Action:    "menu",
		NewObject: "/dashboard-v2",
	})
	if err != nil {
		t.Fatalf("UpdatePermission failed: %v", err)
	}

	// Verify cache cleared
	_, err = typedCache.Get(ctx, cacheKey)
	if err == nil {
		t.Fatal("cache should be invalidated after UpdatePermission")
	}
}

func TestDeletePermission_ClearsCache(t *testing.T) {
	svc, client, typedCache := newTestPermissionService(t)
	ctx := context.Background()
	domain := "default"
	userID := setupTestData(t, client, "testrole6", domain)

	// Populate cache
	_, err := svc.GetUserMenuPermissions(ctx, userID, domain)
	if err != nil {
		t.Fatalf("populate cache failed: %v", err)
	}

	cacheKey := svc.cacheKey("user", "menu", userID, domain)
	_, err = typedCache.Get(ctx, cacheKey)
	if err != nil {
		t.Fatalf("cache should be populated: %v", err)
	}

	// Delete permission
	err = svc.DeletePermission(ctx, "testrole6", domain, "/dashboard", "menu")
	if err != nil {
		t.Fatalf("DeletePermission failed: %v", err)
	}

	// Verify cache cleared
	_, err = typedCache.Get(ctx, cacheKey)
	if err == nil {
		t.Fatal("cache should be invalidated after DeletePermission")
	}
}

func TestGetRolePermissions_CacheAndInvalidation(t *testing.T) {
	svc, client, typedCache := newTestPermissionService(t)
	ctx := context.Background()
	domain := "default"
	_ = setupTestData(t, client, "testrole7", domain)

	// First call - populates cache
	perms1, err := svc.GetRolePermissions(ctx, "testrole7", domain)
	if err != nil {
		t.Fatalf("first GetRolePermissions failed: %v", err)
	}
	if len(perms1) != 1 {
		t.Fatalf("expected 1 role permission, got %d", len(perms1))
	}

	// Verify cache populated
	roleCacheKey := svc.cacheKey("role", "perms", "testrole7", domain)
	cached, err := typedCache.Get(ctx, roleCacheKey)
	if err != nil {
		t.Fatalf("role cache should be populated: %v", err)
	}
	if len(cached) != 1 {
		t.Fatalf("role cache should have 1 permission, got %d", len(cached))
	}

	// Second call - cache hit
	perms2, err := svc.GetRolePermissions(ctx, "testrole7", domain)
	if err != nil {
		t.Fatalf("second GetRolePermissions failed: %v", err)
	}
	if len(perms2) != len(perms1) {
		t.Fatalf("cache hit should return same count")
	}

	// Assign new permission - should clear role cache
	err = svc.AssignPermissionsToRole(ctx, "testrole7", domain, []PermissionRule{
		{Object: "/reports", Action: "menu"},
	})
	if err != nil {
		t.Fatalf("AssignPermissionsToRole failed: %v", err)
	}

	// Verify role cache cleared
	_, err = typedCache.Get(ctx, roleCacheKey)
	if err == nil {
		t.Fatal("role cache should be invalidated after AssignPermissionsToRole")
	}

	// Re-query should get fresh data
	perms3, err := svc.GetRolePermissions(ctx, "testrole7", domain)
	if err != nil {
		t.Fatalf("re-query GetRolePermissions failed: %v", err)
	}
	if len(perms3) != 2 {
		t.Fatalf("expected 2 role permissions after assign, got %d", len(perms3))
	}
}

func TestGetUserButtonPermissions_CacheHitAndInvalidation(t *testing.T) {
	svc, client, typedCache := newTestPermissionService(t)
	ctx := context.Background()
	domain := "default"
	roleName := "testrole_btn"

	r, err := client.Role.Create().
		SetName(roleName).
		SetDisplayName("Button Role").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role failed: %v", err)
	}
	u, err := client.User.Create().
		SetEmail("btn@test.com").
		SetPassword("pwd").
		SetStatus("active").
		SetUID("uid-btn").
		Save(ctx)
	if err != nil {
		t.Fatalf("create user failed: %v", err)
	}
	_, err = client.UserRole.Create().SetUserID(u.ID).SetRoleID(r.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create user_role failed: %v", err)
	}
	_, err = client.CasbinRule.Create().
		SetPtype("p").SetV0(roleName).SetV1(domain).
		SetV2("btn:delete").SetV3("button").SetV4("button").
		Save(ctx)
	if err != nil {
		t.Fatalf("create casbin rule failed: %v", err)
	}

	// First call
	perms, err := svc.GetUserButtonPermissions(ctx, u.UID, domain)
	if err != nil {
		t.Fatalf("GetUserButtonPermissions failed: %v", err)
	}
	if len(perms) != 1 {
		t.Fatalf("expected 1 button permission, got %d", len(perms))
	}

	// Verify cache
	cacheKey := svc.cacheKey("user", "button", u.UID, domain)
	_, err = typedCache.Get(ctx, cacheKey)
	if err != nil {
		t.Fatal("button cache should be populated")
	}

	// Assign new permission clears button cache too
	err = svc.AssignPermissionsToRole(ctx, roleName, domain, []PermissionRule{
		{Object: "btn:edit", Action: "button"},
	})
	if err != nil {
		t.Fatalf("assign failed: %v", err)
	}
	_, err = typedCache.Get(ctx, cacheKey)
	if err == nil {
		t.Fatal("button cache should be invalidated")
	}
}

func TestGetUserAPIPermissions_CacheHitAndInvalidation(t *testing.T) {
	svc, client, typedCache := newTestPermissionService(t)
	ctx := context.Background()
	domain := "default"
	roleName := "testrole_api"

	r, err := client.Role.Create().
		SetName(roleName).
		SetDisplayName("API Role").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role failed: %v", err)
	}
	u, err := client.User.Create().
		SetEmail("api@test.com").
		SetPassword("pwd").
		SetStatus("active").
		SetUID("uid-api").
		Save(ctx)
	if err != nil {
		t.Fatalf("create user failed: %v", err)
	}
	_, err = client.UserRole.Create().SetUserID(u.ID).SetRoleID(r.ID).Save(ctx)
	if err != nil {
		t.Fatalf("create user_role failed: %v", err)
	}
	_, err = client.CasbinRule.Create().
		SetPtype("p").SetV0(roleName).SetV1(domain).
		SetV2("/api/users").SetV3("GET").SetV4("api").
		Save(ctx)
	if err != nil {
		t.Fatalf("create casbin rule failed: %v", err)
	}

	// First call
	perms, err := svc.GetUserAPIPermissions(ctx, u.UID, domain)
	if err != nil {
		t.Fatalf("GetUserAPIPermissions failed: %v", err)
	}
	if len(perms) != 1 {
		t.Fatalf("expected 1 api permission, got %d", len(perms))
	}

	// Verify cache populated
	cacheKey := svc.cacheKey("user", "api", u.UID, domain)
	_, err = typedCache.Get(ctx, cacheKey)
	if err != nil {
		t.Fatal("api cache should be populated")
	}

	// Create permission clears all user caches
	err = svc.CreatePermission(ctx, &CreatePermissionRequest{
		RoleName: roleName,
		Domain:   domain,
		Type:     PermissionTypeAPI,
		Object:   "/api/roles",
		Action:   "POST",
	})
	if err != nil {
		t.Fatalf("CreatePermission failed: %v", err)
	}

	_, err = typedCache.Get(ctx, cacheKey)
	if err == nil {
		t.Fatal("api cache should be invalidated after CreatePermission")
	}
}
