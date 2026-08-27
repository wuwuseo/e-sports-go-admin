package service

import (
	"testing"

	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/casbinrule"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

// -------------------------------------------------------------------
// 缓存清理函数测试
// -------------------------------------------------------------------

func TestPermissionService_ClearUserPermissionsCache_WithCache(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := withPermissionSystemOperator(t.Context())
	cache := makeMemCache(t)
	svc := NewPermissionService(cache)

	// 验证缓存清理不会panic
	svc.clearUserPermissionsCache(ctx, "test-uid", "default")
	svc.clearRolePermissionsCache(ctx, "test-role", "default")
}

func TestPermissionService_ClearAllUsersPermissionsByRole_WithAdminAndUser(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:clear_all_users?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := withPermissionSystemOperator(t.Context())
	cache := makeMemCache(t)
	svc := NewPermissionService(cache)

	testRole, err := client.Role.Create().
		SetName("test-role").
		SetDisplayName("Test Role").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role failed: %v", err)
	}

	adminAccount, err := client.Admin.Create().
		SetEmail("admin@test.com").
		SetUsername("admin").
		SetPassword("password").
		SetPasswordSalt("salt").
		SetUID("uid-admin-clear").
		SetStatus(admin.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin failed: %v", err)
	}

	userAccount, err := client.User.Create().
		SetEmail("user@test.com").
		SetUsername("user").
		SetPassword("password").
		SetPasswordSalt("salt").
		SetUID("uid-user-clear").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create user failed: %v", err)
	}

	_, err = client.AdminRole.Create().
		SetAdminID(adminAccount.ID).
		SetRoleID(testRole.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin role failed: %v", err)
	}

	_, err = client.UserRole.Create().
		SetUserID(userAccount.ID).
		SetRoleID(testRole.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create user role failed: %v", err)
	}

	svc.clearAllUsersPermissionsByRole(ctx, testRole.Name, "default")
}

func TestPermissionService_ClearRoleAndUsersCache_WithCache(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:clear_role_users?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := withPermissionSystemOperator(t.Context())
	cache := makeMemCache(t)
	svc := NewPermissionService(cache)

	testRole, err := client.Role.Create().
		SetName("cache-test-role").
		SetDisplayName("Cache Test Role").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role failed: %v", err)
	}

	svc.clearRoleAndUsersCache(ctx, testRole.Name, "default")
}

// -------------------------------------------------------------------
// AssignPermissionsToRole 和 RemovePermissionsFromRole 批量操作测试
// -------------------------------------------------------------------

func TestPermissionService_AssignPermissionsToRole_BatchSuccess(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:assign_batch?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := withPermissionSystemOperator(t.Context())
	svc := NewPermissionService(nil)

	_, err := client.Role.Create().
		SetName("batch-role").
		SetDisplayName("Batch Role").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role failed: %v", err)
	}

	err = svc.AssignPermissionsToRole(ctx, "", "batch-role", "default", []PermissionRule{
		{Object: "/api/v1/users", Action: "GET"},
		{Object: "/api/v1/users", Action: "POST"},
		{Object: "/api/v1/users/{id}", Action: "PUT"},
	})
	if err != nil {
		t.Fatalf("AssignPermissionsToRole failed: %v", err)
	}

	count, err := client.CasbinRule.Query().
		Where(casbinrule.V0EQ("batch-role")).
		Count(ctx)
	if err != nil {
		t.Fatalf("count permissions failed: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected 3 permissions, got %d", count)
	}
}

func TestPermissionService_AssignPermissionsToRole_SkipsDuplicates(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:assign_dup?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := withPermissionSystemOperator(t.Context())
	svc := NewPermissionService(nil)

	_, err := client.Role.Create().
		SetName("dup-role").
		SetDisplayName("Duplicate Role").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role failed: %v", err)
	}

	_, err = client.CasbinRule.Create().
		SetPtype("p").
		SetV0("dup-role").
		SetV1("default").
		SetV2("/api/v1/test").
		SetV3("GET").
		SetV4("api").
		Save(ctx)
	if err != nil {
		t.Fatalf("create existing permission failed: %v", err)
	}

	err = svc.AssignPermissionsToRole(ctx, "", "dup-role", "default", []PermissionRule{
		{Object: "/api/v1/test", Action: "GET"},
		{Object: "/api/v1/test", Action: "POST"},
	})
	if err != nil {
		t.Fatalf("AssignPermissionsToRole failed: %v", err)
	}

	count, err := client.CasbinRule.Query().
		Where(casbinrule.V0EQ("dup-role")).
		Count(ctx)
	if err != nil {
		t.Fatalf("count permissions failed: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 permissions (1 existing + 1 new), got %d", count)
	}
}

func TestPermissionService_RemovePermissionsFromRole_BatchSuccess(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:remove_batch?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := withPermissionSystemOperator(t.Context())
	svc := NewPermissionService(nil)

	_, err := client.Role.Create().
		SetName("remove-role").
		SetDisplayName("Remove Role").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role failed: %v", err)
	}

	for _, action := range []string{"GET", "POST", "PUT", "DELETE"} {
		_, err = client.CasbinRule.Create().
			SetPtype("p").
			SetV0("remove-role").
			SetV1("default").
			SetV2("/api/v1/items").
			SetV3(action).
			SetV4("api").
			Save(ctx)
		if err != nil {
			t.Fatalf("create permission failed: %v", err)
		}
	}

	err = svc.RemovePermissionsFromRole(ctx, "", "remove-role", "default", []PermissionRule{
		{Object: "/api/v1/items", Action: "POST"},
		{Object: "/api/v1/items", Action: "DELETE"},
	})
	if err != nil {
		t.Fatalf("RemovePermissionsFromRole failed: %v", err)
	}

	count, err := client.CasbinRule.Query().
		Where(casbinrule.V0EQ("remove-role")).
		Count(ctx)
	if err != nil {
		t.Fatalf("count permissions failed: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 remaining permissions, got %d", count)
	}
}

// -------------------------------------------------------------------
// SaveRolePermissions 事务和去重测试
// -------------------------------------------------------------------

func TestPermissionService_SaveRolePermissions_Deduplication(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:save_dedup?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := withPermissionSystemOperator(t.Context())
	svc := NewPermissionService(nil)

	testRole, err := client.Role.Create().
		SetName("dedup-role").
		SetDisplayName("Dedup Role").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role failed: %v", err)
	}

	err = svc.SaveRolePermissions(ctx, "", testRole.ID, &SaveRolePermissionsRequest{
		Domain: "default",
		Permissions: []PermissionRule{
			{Object: "/api/v1/docs", Action: "GET"},
			{Object: "/api/v1/docs", Action: "GET"},
			{Object: "/api/v1/docs", Action: "POST"},
			{Object: "/api/v1/docs", Action: "GET"},
		},
	})
	if err != nil {
		t.Fatalf("SaveRolePermissions failed: %v", err)
	}

	count, err := client.CasbinRule.Query().
		Where(casbinrule.V0EQ("dedup-role")).
		Count(ctx)
	if err != nil {
		t.Fatalf("count permissions failed: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 unique permissions after deduplication, got %d", count)
	}
}

// -------------------------------------------------------------------
// 系统权限级联状态更新深度测试
// -------------------------------------------------------------------

func TestSystemPermissionService_CascadeDisable_DeepNesting(t *testing.T) {
	setupSystemPermissionTest(t)
	svc := NewSystemPermissionService()
	ctx := withPermissionSystemOperator(t.Context())

	level1, err := svc.Create(ctx, "", &CreateSystemPermissionRequest{
		Domain: "default", Type: SystemPermissionTypeMenu, Name: "Level1", Code: "cascade:l1", Status: 1,
	})
	if err != nil {
		t.Fatalf("create level1 failed: %v", err)
	}

	level2, err := svc.Create(ctx, "", &CreateSystemPermissionRequest{
		Domain: "default", Type: SystemPermissionTypeMenu, Name: "Level2", Code: "cascade:l2", ParentID: level1.ID, Status: 1,
	})
	if err != nil {
		t.Fatalf("create level2 failed: %v", err)
	}

	level3, err := svc.Create(ctx, "", &CreateSystemPermissionRequest{
		Domain: "default", Type: SystemPermissionTypeMenu, Name: "Level3", Code: "cascade:l3", ParentID: level2.ID, Status: 1,
	})
	if err != nil {
		t.Fatalf("create level3 failed: %v", err)
	}

	level4, err := svc.Create(ctx, "", &CreateSystemPermissionRequest{
		Domain: "default", Type: SystemPermissionTypeMenu, Name: "Level4", Code: "cascade:l4", ParentID: level3.ID, Status: 1,
	})
	if err != nil {
		t.Fatalf("create level4 failed: %v", err)
	}

	disabledStatus := int8(0)
	err = svc.Update(ctx, "", level1.ID, &UpdateSystemPermissionRequest{Status: &disabledStatus})
	if err != nil {
		t.Fatalf("disable level1 failed: %v", err)
	}

	for _, id := range []int{level1.ID, level2.ID, level3.ID, level4.ID} {
		perm, err := svc.GetByID(ctx, id)
		if err != nil {
			t.Fatalf("get permission %d failed: %v", id, err)
		}
		if perm.Status != 0 {
			t.Errorf("permission %d expected status 0, got %d", id, perm.Status)
		}
	}
}

func TestSystemPermissionService_CascadeEnable_DeepNesting(t *testing.T) {
	setupSystemPermissionTest(t)
	svc := NewSystemPermissionService()
	ctx := withPermissionSystemOperator(t.Context())

	level1, err := svc.Create(ctx, "", &CreateSystemPermissionRequest{
		Domain: "default", Type: SystemPermissionTypeMenu, Name: "EnableL1", Code: "enable:l1", Status: 0,
	})
	if err != nil {
		t.Fatalf("create level1 failed: %v", err)
	}

	level2, err := svc.Create(ctx, "", &CreateSystemPermissionRequest{
		Domain: "default", Type: SystemPermissionTypeMenu, Name: "EnableL2", Code: "enable:l2", ParentID: level1.ID, Status: 0,
	})
	if err != nil {
		t.Fatalf("create level2 failed: %v", err)
	}

	level3, err := svc.Create(ctx, "", &CreateSystemPermissionRequest{
		Domain: "default", Type: SystemPermissionTypeMenu, Name: "EnableL3", Code: "enable:l3", ParentID: level2.ID, Status: 0,
	})
	if err != nil {
		t.Fatalf("create level3 failed: %v", err)
	}

	enabledStatus := int8(1)
	err = svc.Update(ctx, "", level1.ID, &UpdateSystemPermissionRequest{Status: &enabledStatus})
	if err != nil {
		t.Fatalf("enable level1 failed: %v", err)
	}

	for _, id := range []int{level1.ID, level2.ID, level3.ID} {
		perm, err := svc.GetByID(ctx, id)
		if err != nil {
			t.Fatalf("get permission %d failed: %v", id, err)
		}
		if perm.Status != 1 {
			t.Errorf("permission %d expected status 1, got %d", id, perm.Status)
		}
	}
}
