package service

import (
	"errors"
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

func TestPermissionService_GetUserMenuPermissions_SuperAdminReturnsEnabledMenus(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	adminRole, err := client.Role.Create().
		SetName("admin").
		SetDisplayName("超级管理员").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin role failed: %v", err)
	}

	adminUser, err := client.User.Create().
		SetEmail("admin@example.com").
		SetUsername("admin").
		SetPassword("password").
		SetPasswordSalt("salt").
		SetNickname("Admin").
		SetUID("uid-admin").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin user failed: %v", err)
	}

	_, err = client.UserRole.Create().
		SetUserID(adminUser.ID).
		SetRoleID(adminRole.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create user role failed: %v", err)
	}

	_, err = client.CasbinRule.Create().
		SetPtype("g").
		SetV0(adminUser.UID).
		SetV1("admin").
		SetV2("default").
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin grouping rule failed: %v", err)
	}

	_, err = client.SystemPermission.Create().
		SetDomain("default").
		SetType("menu").
		SetName("工作台").
		SetCode("dashboard").
		SetMenuPath("/dashboard/workplace").
		SetSortOrder(10).
		SetStatus(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("create default menu failed: %v", err)
	}

	_, err = client.SystemPermission.Create().
		SetDomain("shared-domain").
		SetType("menu").
		SetName("共享菜单").
		SetCode("shared-menu").
		SetMenuPath("/shared/menu").
		SetSortOrder(5).
		SetIsCommon(true).
		SetStatus(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("create common menu failed: %v", err)
	}

	_, err = client.SystemPermission.Create().
		SetDomain("default").
		SetType("menu").
		SetName("已禁用菜单").
		SetCode("disabled-menu").
		SetMenuPath("/disabled/menu").
		SetSortOrder(1).
		SetStatus(0).
		Save(ctx)
	if err != nil {
		t.Fatalf("create disabled menu failed: %v", err)
	}

	_, err = client.SystemPermission.Create().
		SetDomain("default").
		SetType("button").
		SetName("按钮").
		SetCode("dashboard:add").
		SetSortOrder(20).
		SetStatus(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("create button permission failed: %v", err)
	}

	svc := NewPermissionService(nil)
	perms, err := svc.GetUserMenuPermissions(ctx, adminUser.UID, "default")
	if err != nil {
		t.Fatalf("GetUserMenuPermissions failed: %v", err)
	}

	if len(perms) != 2 {
		t.Fatalf("expected 2 menu permissions, got %d", len(perms))
	}

	got := map[string]PermissionType{}
	for _, perm := range perms {
		got[perm.Object] = perm.Type
		if perm.Action != string(PermissionTypeMenu) {
			t.Fatalf("expected action %q, got %q", PermissionTypeMenu, perm.Action)
		}
	}

	if got["/dashboard/workplace"] != PermissionTypeMenu {
		t.Fatalf("expected /dashboard/workplace menu permission, got %#v", got)
	}
	if got["/shared/menu"] != PermissionTypeMenu {
		t.Fatalf("expected /shared/menu menu permission, got %#v", got)
	}
}

func TestPermissionService_GetUserButtonPermissions_SuperAdminReturnsEnabledButtons(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	adminRole, err := client.Role.Create().
		SetName("admin").
		SetDisplayName("超级管理员").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin role failed: %v", err)
	}

	adminUser, err := client.User.Create().
		SetEmail("admin@example.com").
		SetUsername("admin").
		SetPassword("password").
		SetPasswordSalt("salt").
		SetNickname("Admin").
		SetUID("uid-admin-btn").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin user failed: %v", err)
	}

	_, err = client.UserRole.Create().
		SetUserID(adminUser.ID).
		SetRoleID(adminRole.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create user role failed: %v", err)
	}

	_, err = client.CasbinRule.Create().
		SetPtype("g").
		SetV0(adminUser.UID).
		SetV1("admin").
		SetV2("default").
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin grouping rule failed: %v", err)
	}

	_, err = client.SystemPermission.Create().
		SetDomain("default").
		SetType("button").
		SetName("新增").
		SetCode("role:add").
		SetSortOrder(10).
		SetStatus(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("create default button failed: %v", err)
	}

	_, err = client.SystemPermission.Create().
		SetDomain("shared-domain").
		SetType("button").
		SetName("共享按钮").
		SetCode("shared:btn").
		SetSortOrder(5).
		SetIsCommon(true).
		SetStatus(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("create common button failed: %v", err)
	}

	_, err = client.SystemPermission.Create().
		SetDomain("default").
		SetType("button").
		SetName("已禁用按钮").
		SetCode("disabled:btn").
		SetSortOrder(1).
		SetStatus(0).
		Save(ctx)
	if err != nil {
		t.Fatalf("create disabled button failed: %v", err)
	}

	_, err = client.SystemPermission.Create().
		SetDomain("default").
		SetType("menu").
		SetName("菜单").
		SetCode("menu:1").
		SetMenuPath("/menu/1").
		SetSortOrder(20).
		SetStatus(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("create menu permission failed: %v", err)
	}

	svc := NewPermissionService(nil)
	perms, err := svc.GetUserButtonPermissions(ctx, adminUser.UID, "default")
	if err != nil {
		t.Fatalf("GetUserButtonPermissions failed: %v", err)
	}

	if len(perms) != 2 {
		t.Fatalf("expected 2 button permissions, got %d", len(perms))
	}

	got := map[string]PermissionType{}
	for _, perm := range perms {
		got[perm.Object] = perm.Type
		if perm.Action != string(PermissionTypeButton) {
			t.Fatalf("expected action %q, got %q", PermissionTypeButton, perm.Action)
		}
	}

	if got["role:add"] != PermissionTypeButton {
		t.Fatalf("expected role:add button permission, got %#v", got)
	}
	if got["shared:btn"] != PermissionTypeButton {
		t.Fatalf("expected shared:btn button permission, got %#v", got)
	}
}

func TestPermissionService_GetUserMenuPermissions_AdminAccountUsesAdminRoles(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	normalRole, err := client.Role.Create().
		SetName("normal_admin").
		SetDisplayName("普通管理员").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role failed: %v", err)
	}

	adminAccount, err := client.Admin.Create().
		SetEmail("test@example.com").
		SetUsername("test").
		SetPassword("password").
		SetPasswordSalt("salt").
		SetNickname("Test").
		SetUID("uid-admin-normal").
		SetStatus(admin.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin failed: %v", err)
	}

	_, err = client.AdminRole.Create().
		SetAdminID(adminAccount.ID).
		SetRoleID(normalRole.ID).
		SetIsPrimary(true).
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin role failed: %v", err)
	}

	_, err = client.CasbinRule.Create().
		SetPtype("p").
		SetV0("normal_admin").
		SetV1("default").
		SetV2("/dashboard/workplace").
		SetV3("MENU").
		SetV4("menu").
		SetV5("工作台").
		Save(ctx)
	if err != nil {
		t.Fatalf("create menu permission failed: %v", err)
	}

	svc := NewPermissionService(nil)
	perms, err := svc.GetUserMenuPermissions(ctx, adminAccount.UID, "default")
	if err != nil {
		t.Fatalf("GetUserMenuPermissions failed: %v", err)
	}

	if len(perms) != 1 {
		t.Fatalf("expected 1 menu permission, got %d", len(perms))
	}
	if perms[0].Object != "/dashboard/workplace" {
		t.Fatalf("expected /dashboard/workplace, got %q", perms[0].Object)
	}
}

func TestPermissionService_GetUserRoleNames_AdminAccountUsesAdminRoles(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	normalRole, err := client.Role.Create().
		SetName("normal_admin").
		SetDisplayName("普通管理员").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role failed: %v", err)
	}

	adminAccount, err := client.Admin.Create().
		SetEmail("role-names@example.com").
		SetUsername("role_names").
		SetPassword("password").
		SetPasswordSalt("salt").
		SetNickname("Role Names").
		SetUID("uid-admin-role-names").
		SetStatus(admin.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin failed: %v", err)
	}

	_, err = client.AdminRole.Create().
		SetAdminID(adminAccount.ID).
		SetRoleID(normalRole.ID).
		SetIsPrimary(true).
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin role failed: %v", err)
	}

	svc := NewPermissionService(nil)
	roleNames, err := svc.GetUserRoleNames(ctx, adminAccount.UID)
	if err != nil {
		t.Fatalf("GetUserRoleNames failed: %v", err)
	}
	if len(roleNames) != 1 || roleNames[0] != "normal_admin" {
		t.Fatalf("expected normal_admin role, got %v", roleNames)
	}
}

func TestPermissionService_CheckPermission_AdminAccountUsesAdminRoles(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	normalRole, err := client.Role.Create().
		SetName("normal_admin").
		SetDisplayName("普通管理员").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role failed: %v", err)
	}

	adminAccount, err := client.Admin.Create().
		SetEmail("test@example.com").
		SetUsername("test").
		SetPassword("password").
		SetPasswordSalt("salt").
		SetNickname("Test").
		SetUID("uid-admin-normal-check").
		SetStatus(admin.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin failed: %v", err)
	}

	_, err = client.AdminRole.Create().
		SetAdminID(adminAccount.ID).
		SetRoleID(normalRole.ID).
		SetIsPrimary(true).
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin role failed: %v", err)
	}

	_, err = client.CasbinRule.Create().
		SetPtype("p").
		SetV0("normal_admin").
		SetV1("default").
		SetV2("/api/v1/admin/auth/roles").
		SetV3("GET").
		SetV4("api").
		Save(ctx)
	if err != nil {
		t.Fatalf("create api permission failed: %v", err)
	}

	svc := NewPermissionService(nil)
	ok, err := svc.CheckPermission(ctx, adminAccount.UID, "default", "/api/v1/admin/auth/roles", "GET")
	if err != nil {
		t.Fatalf("CheckPermission failed: %v", err)
	}
	if !ok {
		t.Fatal("expected admin account role permission to be allowed")
	}
}

func TestPermissionService_CheckUserHasRole_AdminAccountUsesAdminRoles(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	adminRole, err := client.Role.Create().
		SetName("admin").
		SetDisplayName("管理员").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role failed: %v", err)
	}

	adminAccount, err := client.Admin.Create().
		SetEmail("admin@example.com").
		SetUsername("admin").
		SetPassword("password").
		SetPasswordSalt("salt").
		SetNickname("Admin").
		SetUID("uid-admin-role-check").
		SetStatus(admin.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin failed: %v", err)
	}

	_, err = client.AdminRole.Create().
		SetAdminID(adminAccount.ID).
		SetRoleID(adminRole.ID).
		SetIsPrimary(true).
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin role failed: %v", err)
	}

	svc := NewPermissionService(nil)
	ok, err := svc.CheckUserHasRole(ctx, adminAccount.UID, "default", "admin")
	if err != nil {
		t.Fatalf("CheckUserHasRole failed: %v", err)
	}
	if !ok {
		t.Fatal("expected admin account DB role to satisfy role check")
	}
}

// ======================== CreatePermission 测试 ========================

func TestPermissionService_CreatePermission_Success(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	_, err := client.Role.Create().
		SetName("editor").
		SetDisplayName("编辑者").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	svc := NewPermissionService(nil)
	err = svc.CreatePermission(ctx, &CreatePermissionRequest{
		RoleName:    "editor",
		Domain:      "default",
		Type:        PermissionTypeAPI,
		Object:      "/api/v1/articles",
		Action:      "GET",
		Description: "查看文章列表",
	})
	if err != nil {
		t.Fatalf("CreatePermission 失败: %v", err)
	}

	exists, err := client.CasbinRule.Query().
		Where(
			casbinrule.PtypeEQ("p"),
			casbinrule.V0EQ("editor"),
			casbinrule.V1EQ("default"),
			casbinrule.V2EQ("/api/v1/articles"),
			casbinrule.V3EQ("GET"),
		).Exist(ctx)
	if err != nil {
		t.Fatalf("查询权限失败: %v", err)
	}
	if !exists {
		t.Error("期望权限在数据库中存在, 但未找到")
	}
}

func TestPermissionService_CreatePermission_InvalidType(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	_, err := client.Role.Create().
		SetName("editor").
		SetDisplayName("编辑者").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	svc := NewPermissionService(nil)
	err = svc.CreatePermission(ctx, &CreatePermissionRequest{
		RoleName:    "editor",
		Domain:      "default",
		Type:        PermissionType("invalid"),
		Object:      "/api/v1/articles",
		Action:      "GET",
		Description: "无效类型",
	})
	if !errors.Is(err, ErrInvalidPermissionType) {
		t.Errorf("期望错误为 ErrInvalidPermissionType, 实际为 %v", err)
	}
}

func TestPermissionService_CreatePermission_RoleNotFound(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	svc := NewPermissionService(nil)
	err := svc.CreatePermission(ctx, &CreatePermissionRequest{
		RoleName:    "nonexistent",
		Domain:      "default",
		Type:        PermissionTypeAPI,
		Object:      "/api/v1/articles",
		Action:      "GET",
		Description: "角色不存在",
	})
	if !errors.Is(err, ErrRoleNotFound) {
		t.Errorf("期望错误为 ErrRoleNotFound, 实际为 %v", err)
	}
}

func TestPermissionService_CreatePermission_Duplicate(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	_, err := client.Role.Create().
		SetName("editor").
		SetDisplayName("编辑者").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	svc := NewPermissionService(nil)
	req := &CreatePermissionRequest{
		RoleName:    "editor",
		Domain:      "default",
		Type:        PermissionTypeAPI,
		Object:      "/api/v1/articles",
		Action:      "GET",
		Description: "查看文章列表",
	}

	err = svc.CreatePermission(ctx, req)
	if err != nil {
		t.Fatalf("第一次 CreatePermission 失败: %v", err)
	}

	err = svc.CreatePermission(ctx, req)
	if !errors.Is(err, ErrPermissionAlreadyExists) {
		t.Errorf("期望错误为 ErrPermissionAlreadyExists, 实际为 %v", err)
	}
}

// ======================== GetPermission 测试 ========================

func TestPermissionService_GetPermission_Success(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	_, err := client.Role.Create().
		SetName("editor").
		SetDisplayName("编辑者").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	_, err = client.CasbinRule.Create().
		SetPtype("p").
		SetV0("editor").
		SetV1("default").
		SetV2("/api/v1/articles").
		SetV3("GET").
		SetV4("api").
		SetV5("查看文章列表").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建 CasbinRule 失败: %v", err)
	}

	svc := NewPermissionService(nil)
	perm, err := svc.GetPermission(ctx, "editor", "default", "/api/v1/articles", "GET")
	if err != nil {
		t.Fatalf("GetPermission 失败: %v", err)
	}

	if perm.RoleName != "editor" {
		t.Errorf("期望 RoleName 为 editor, 实际为 %s", perm.RoleName)
	}
	if perm.Domain != "default" {
		t.Errorf("期望 Domain 为 default, 实际为 %s", perm.Domain)
	}
	if perm.Object != "/api/v1/articles" {
		t.Errorf("期望 Object 为 /api/v1/articles, 实际为 %s", perm.Object)
	}
	if perm.Action != "GET" {
		t.Errorf("期望 Action 为 GET, 实际为 %s", perm.Action)
	}
	if perm.Type != PermissionTypeAPI {
		t.Errorf("期望 Type 为 api, 实际为 %s", perm.Type)
	}
	if perm.Description != "查看文章列表" {
		t.Errorf("期望 Description 为 查看文章列表, 实际为 %s", perm.Description)
	}
}

func TestPermissionService_GetPermission_NotFound(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	svc := NewPermissionService(nil)
	_, err := svc.GetPermission(ctx, "nonexistent", "default", "/api/v1/foo", "GET")
	if !errors.Is(err, ErrPermissionNotFound) {
		t.Errorf("期望错误为 ErrPermissionNotFound, 实际为 %v", err)
	}
}

// ======================== DeletePermission 测试 ========================

func TestPermissionService_DeletePermission_Success(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	_, err := client.Role.Create().
		SetName("editor").
		SetDisplayName("编辑者").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	_, err = client.CasbinRule.Create().
		SetPtype("p").
		SetV0("editor").
		SetV1("default").
		SetV2("/api/v1/articles").
		SetV3("GET").
		SetV4("api").
		SetV5("查看文章列表").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建 CasbinRule 失败: %v", err)
	}

	svc := NewPermissionService(nil)
	err = svc.DeletePermission(ctx, "editor", "default", "/api/v1/articles", "GET")
	if err != nil {
		t.Fatalf("DeletePermission 失败: %v", err)
	}

	exists, err := client.CasbinRule.Query().
		Where(
			casbinrule.PtypeEQ("p"),
			casbinrule.V0EQ("editor"),
			casbinrule.V1EQ("default"),
			casbinrule.V2EQ("/api/v1/articles"),
			casbinrule.V3EQ("GET"),
		).Exist(ctx)
	if err != nil {
		t.Fatalf("查询权限失败: %v", err)
	}
	if exists {
		t.Error("删除后权限应不存在于数据库中")
	}
}

func TestPermissionService_DeletePermission_NotFound(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	svc := NewPermissionService(nil)
	err := svc.DeletePermission(ctx, "nonexistent", "default", "/api/v1/foo", "GET")
	if !errors.Is(err, ErrPermissionNotFound) {
		t.Errorf("期望错误为 ErrPermissionNotFound, 实际为 %v", err)
	}
}

// ======================== UpdatePermission 测试 ========================

func TestPermissionService_UpdatePermission_Success(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	_, err := client.Role.Create().
		SetName("editor").
		SetDisplayName("编辑者").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	_, err = client.CasbinRule.Create().
		SetPtype("p").
		SetV0("editor").
		SetV1("default").
		SetV2("/api/v1/articles").
		SetV3("GET").
		SetV4("api").
		SetV5("查看文章列表").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建 CasbinRule 失败: %v", err)
	}

	svc := NewPermissionService(nil)
	err = svc.UpdatePermission(ctx, &UpdatePermissionRequest{
		RoleName:    "editor",
		Domain:      "default",
		Type:        PermissionTypeAPI,
		Object:      "/api/v1/articles",
		Action:      "GET",
		NewObject:   "/api/v1/posts",
		NewAction:   "POST",
		Description: "更新后的描述",
	})
	if err != nil {
		t.Fatalf("UpdatePermission 失败: %v", err)
	}

	perm, err := svc.GetPermission(ctx, "editor", "default", "/api/v1/posts", "POST")
	if err != nil {
		t.Fatalf("获取更新后的权限失败: %v", err)
	}

	if perm.Object != "/api/v1/posts" {
		t.Errorf("期望更新后的 Object 为 /api/v1/posts, 实际为 %s", perm.Object)
	}
	if perm.Action != "POST" {
		t.Errorf("期望更新后的 Action 为 POST, 实际为 %s", perm.Action)
	}
	if perm.Description != "更新后的描述" {
		t.Errorf("期望更新后的 Description 为 更新后的描述, 实际为 %s", perm.Description)
	}
}

func TestPermissionService_UpdatePermission_NotFound(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	_, err := client.Role.Create().
		SetName("editor").
		SetDisplayName("编辑者").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	svc := NewPermissionService(nil)
	err = svc.UpdatePermission(ctx, &UpdatePermissionRequest{
		RoleName:    "editor",
		Domain:      "default",
		Type:        PermissionTypeAPI,
		Object:      "/api/v1/notfound",
		Action:      "GET",
		NewObject:   "/api/v1/new",
		NewAction:   "POST",
		Description: "不存在的权限",
	})
	if !errors.Is(err, ErrPermissionNotFound) {
		t.Errorf("期望错误为 ErrPermissionNotFound, 实际为 %v", err)
	}
}

// ======================== GetRolePermissions 测试 ========================

func TestPermissionService_GetRolePermissions_Success(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	_, err := client.Role.Create().
		SetName("editor").
		SetDisplayName("编辑者").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	_, err = client.CasbinRule.Create().
		SetPtype("p").
		SetV0("editor").
		SetV1("default").
		SetV2("/api/v1/articles").
		SetV3("GET").
		SetV4("api").
		SetV5("查看文章").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建 CasbinRule 失败: %v", err)
	}

	_, err = client.CasbinRule.Create().
		SetPtype("p").
		SetV0("editor").
		SetV1("default").
		SetV2("/api/v1/articles").
		SetV3("POST").
		SetV4("api").
		SetV5("创建文章").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建 CasbinRule 失败: %v", err)
	}

	svc := NewPermissionService(nil)
	perms, err := svc.GetRolePermissions(ctx, "editor", "default")
	if err != nil {
		t.Fatalf("GetRolePermissions 失败: %v", err)
	}

	if len(perms) != 2 {
		t.Fatalf("期望获取2个权限, 实际获取 %d 个", len(perms))
	}

	objects := map[string]bool{}
	for _, p := range perms {
		if p.RoleName != "editor" {
			t.Errorf("期望 RoleName 为 editor, 实际为 %s", p.RoleName)
		}
		if p.Domain != "default" {
			t.Errorf("期望 Domain 为 default, 实际为 %s", p.Domain)
		}
		objects[p.Object] = true
	}
	if !objects["/api/v1/articles"] {
		t.Error("期望权限集合包含 /api/v1/articles")
	}
}

func TestPermissionService_GetRolePermissions_RoleNotFound(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	svc := NewPermissionService(nil)
	_, err := svc.GetRolePermissions(ctx, "nonexistent", "default")
	if !errors.Is(err, ErrRoleNotFound) {
		t.Errorf("期望错误为 ErrRoleNotFound, 实际为 %v", err)
	}
}

// ======================== GetUserAPIPermissions 测试 ========================

func TestPermissionService_GetUserAPIPermissions_WithPermissions(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	editorRole, err := client.Role.Create().
		SetName("editor").
		SetDisplayName("编辑者").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	editorUser, err := client.User.Create().
		SetEmail("editor@example.com").
		SetUsername("editor").
		SetPassword("password").
		SetPasswordSalt("salt").
		SetNickname("Editor").
		SetUID("uid-editor").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	_, err = client.UserRole.Create().
		SetUserID(editorUser.ID).
		SetRoleID(editorRole.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建用户角色关联失败: %v", err)
	}

	_, err = client.CasbinRule.Create().
		SetPtype("p").
		SetV0("editor").
		SetV1("default").
		SetV2("/api/v1/articles").
		SetV3("GET").
		SetV4("api").
		SetV5("查看文章列表").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建 CasbinRule 失败: %v", err)
	}

	_, err = client.CasbinRule.Create().
		SetPtype("p").
		SetV0("editor").
		SetV1("default").
		SetV2("/dashboard/workplace").
		SetV3("MENU").
		SetV4("menu").
		SetV5("工作台").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建菜单 CasbinRule 失败: %v", err)
	}

	svc := NewPermissionService(nil)
	perms, err := svc.GetUserAPIPermissions(ctx, editorUser.UID, "default")
	if err != nil {
		t.Fatalf("GetUserAPIPermissions 失败: %v", err)
	}

	if len(perms) != 1 {
		t.Fatalf("期望获取1个API权限, 实际获取 %d 个", len(perms))
	}

	if perms[0].Object != "/api/v1/articles" {
		t.Errorf("期望 Object 为 /api/v1/articles, 实际为 %s", perms[0].Object)
	}
	if perms[0].Action != "GET" {
		t.Errorf("期望 Action 为 GET, 实际为 %s", perms[0].Action)
	}
	if perms[0].Type != PermissionTypeAPI {
		t.Errorf("期望 Type 为 api, 实际为 %s", perms[0].Type)
	}
}

func TestPermissionService_GetUserAPIPermissions_NoPermissions(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	editorRole, err := client.Role.Create().
		SetName("editor").
		SetDisplayName("编辑者").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	editorUser, err := client.User.Create().
		SetEmail("editor@example.com").
		SetUsername("editor").
		SetPassword("password").
		SetPasswordSalt("salt").
		SetNickname("Editor").
		SetUID("uid-editor-noperm").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	_, err = client.UserRole.Create().
		SetUserID(editorUser.ID).
		SetRoleID(editorRole.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建用户角色关联失败: %v", err)
	}

	svc := NewPermissionService(nil)
	perms, err := svc.GetUserAPIPermissions(ctx, editorUser.UID, "default")
	if err != nil {
		t.Fatalf("GetUserAPIPermissions 失败: %v", err)
	}

	if len(perms) != 0 {
		t.Errorf("期望获取0个API权限, 实际获取 %d 个", len(perms))
	}
}

// ======================== CheckPermission 测试 ========================

func TestPermissionService_CheckPermission_HasPermission(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	editorRole, err := client.Role.Create().
		SetName("editor").
		SetDisplayName("编辑者").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	editorUser, err := client.User.Create().
		SetEmail("editor@example.com").
		SetUsername("editor").
		SetPassword("password").
		SetPasswordSalt("salt").
		SetNickname("Editor").
		SetUID("uid-editor-check").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	_, err = client.UserRole.Create().
		SetUserID(editorUser.ID).
		SetRoleID(editorRole.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建用户角色关联失败: %v", err)
	}

	_, err = client.CasbinRule.Create().
		SetPtype("p").
		SetV0("editor").
		SetV1("default").
		SetV2("/api/v1/articles").
		SetV3("GET").
		SetV4("api").
		SetV5("查看文章").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建 CasbinRule 失败: %v", err)
	}

	svc := NewPermissionService(nil)
	hasPermission, err := svc.CheckPermission(ctx, editorUser.UID, "default", "/api/v1/articles", "GET")
	if err != nil {
		t.Fatalf("CheckPermission 失败: %v", err)
	}

	if !hasPermission {
		t.Error("期望用户拥有权限, 但返回 false")
	}
}

func TestPermissionService_CheckPermission_NoPermission(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	editorRole, err := client.Role.Create().
		SetName("editor").
		SetDisplayName("编辑者").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	editorUser, err := client.User.Create().
		SetEmail("editor@example.com").
		SetUsername("editor").
		SetPassword("password").
		SetPasswordSalt("salt").
		SetNickname("Editor").
		SetUID("uid-editor-noperm").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	_, err = client.UserRole.Create().
		SetUserID(editorUser.ID).
		SetRoleID(editorRole.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建用户角色关联失败: %v", err)
	}

	svc := NewPermissionService(nil)
	hasPermission, err := svc.CheckPermission(ctx, editorUser.UID, "default", "/api/v1/articles", "DELETE")
	if err != nil {
		t.Fatalf("CheckPermission 失败: %v", err)
	}

	if hasPermission {
		t.Error("期望用户没有权限, 但返回 true")
	}
}

// ======================== CheckUserHasRole 测试 ========================

func TestPermissionService_CheckUserHasRole_HasRole(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	adminRole, err := client.Role.Create().
		SetName("admin").
		SetDisplayName("管理员").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	adminAccount, err := client.Admin.Create().
		SetEmail("admin@example.com").
		SetUsername("admin").
		SetPassword("password").
		SetPasswordSalt("salt").
		SetNickname("Admin").
		SetUID("uid-test-user").
		SetStatus(admin.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建管理员失败: %v", err)
	}

	_, err = client.AdminRole.Create().
		SetAdminID(adminAccount.ID).
		SetRoleID(adminRole.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建管理员角色关联失败: %v", err)
	}

	svc := NewPermissionService(nil)
	hasRole, err := svc.CheckUserHasRole(ctx, "uid-test-user", "default", "admin")
	if err != nil {
		t.Fatalf("CheckUserHasRole 失败: %v", err)
	}

	if !hasRole {
		t.Error("期望用户拥有admin角色, 但返回false")
	}
}

func TestPermissionService_CheckUserHasRole_NoRole(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	_, err := client.Role.Create().
		SetName("admin").
		SetDisplayName("管理员").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	svc := NewPermissionService(nil)
	hasRole, err := svc.CheckUserHasRole(ctx, "uid-test-norole", "default", "admin")
	if err != nil {
		t.Fatalf("CheckUserHasRole 失败: %v", err)
	}

	if hasRole {
		t.Error("期望用户没有admin角色, 但返回true")
	}
}

func TestPermissionService_CheckUserHasRole_RoleNotFound(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	svc := NewPermissionService(nil)
	hasRole, err := svc.CheckUserHasRole(ctx, "uid-test-user", "default", "nonexistent-role")
	if err != nil {
		// 如果返回错误也没关系
	}
	// 无论如何，用户都不应该有该角色
	if hasRole {
		t.Error("期望返回false, 因为角色不存在或用户没有该角色")
	}
}

// ======================== AssignPermissionsToRole 测试 ========================

func TestPermissionService_AssignPermissionsToRole_Success(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	_, err := client.Role.Create().
		SetName("editor").
		SetDisplayName("编辑者").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	svc := NewPermissionService(nil)
	perms := []PermissionRule{
		{Object: "/api/v1/articles", Action: "GET"},
		{Object: "/api/v1/articles", Action: "POST"},
	}

	err = svc.AssignPermissionsToRole(ctx, "editor", "default", perms)
	if err != nil {
		t.Fatalf("AssignPermissionsToRole 失败: %v", err)
	}

	gotPerms, err := svc.GetRolePermissions(ctx, "editor", "default")
	if err != nil {
		t.Fatalf("获取角色权限失败: %v", err)
	}

	if len(gotPerms) != 2 {
		t.Errorf("期望2个权限, 实际为 %d", len(gotPerms))
	}
}

func TestPermissionService_AssignPermissionsToRole_RoleNotFound(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	svc := NewPermissionService(nil)
	perms := []PermissionRule{
		{Object: "/api/v1/articles", Action: "GET"},
	}

	err := svc.AssignPermissionsToRole(ctx, "nonexistent", "default", perms)
	if !errors.Is(err, ErrRoleNotFound) {
		t.Errorf("期望错误为 ErrRoleNotFound, 实际为 %v", err)
	}
}

func TestPermissionService_AssignPermissionsToRole_EmptyList(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	_, err := client.Role.Create().
		SetName("editor").
		SetDisplayName("编辑者").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	svc := NewPermissionService(nil)
	err = svc.AssignPermissionsToRole(ctx, "editor", "default", []PermissionRule{})
	if err != nil {
		t.Fatalf("空列表不应该返回错误, 实际返回: %v", err)
	}
}
