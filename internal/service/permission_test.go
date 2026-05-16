package service

import (
	"testing"

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
