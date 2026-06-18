package service

import (
	"context"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/user"

	_ "github.com/mattn/go-sqlite3"
)

// -------------------------------------------------------------------
// isUserSuperAdmin 测试
// -------------------------------------------------------------------

func TestIsUserSuperAdmin_AdminAccountWithAdminRole(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:superadmin_admin?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	ctx := context.Background()

	_, err := client.Role.Create().
		SetName("admin").
		SetDisplayName("超级管理员").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin role failed: %v", err)
	}

	adminAccount, err := client.Admin.Create().
		SetEmail("super@test.com").
		SetUsername("super").
		SetPassword("password").
		SetPasswordSalt("salt").
		SetUID("uid-super-admin").
		SetStatus(admin.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin failed: %v", err)
	}

	_, err = client.AdminRole.Create().
		SetAdminID(adminAccount.ID).
		SetRoleID(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin role link failed: %v", err)
	}

	isSuper, err := isUserSuperAdmin(ctx, client, adminAccount.UID)
	if err != nil {
		t.Fatalf("isUserSuperAdmin failed: %v", err)
	}
	if !isSuper {
		t.Fatal("expected admin account with admin role to be super admin")
	}
}

func TestIsUserSuperAdmin_UserAccountWithAdminRole(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:superadmin_user?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	ctx := context.Background()

	_, err := client.Role.Create().
		SetName("admin").
		SetDisplayName("超级管理员").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin role failed: %v", err)
	}

	userAccount, err := client.User.Create().
		SetEmail("user@test.com").
		SetUsername("user").
		SetPassword("password").
		SetPasswordSalt("salt").
		SetUID("uid-super-user").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create user failed: %v", err)
	}

	_, err = client.UserRole.Create().
		SetUserID(userAccount.ID).
		SetRoleID(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("create user role link failed: %v", err)
	}

	isSuper, err := isUserSuperAdmin(ctx, client, userAccount.UID)
	if err != nil {
		t.Fatalf("isUserSuperAdmin failed: %v", err)
	}
	if !isSuper {
		t.Fatal("expected user account with admin role to be super admin")
	}
}

func TestIsUserSuperAdmin_NoAdminRole(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:superadmin_no_role?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	ctx := context.Background()

	_, err := client.Role.Create().
		SetName("editor").
		SetDisplayName("编辑者").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role failed: %v", err)
	}

	adminAccount, err := client.Admin.Create().
		SetEmail("editor@test.com").
		SetUsername("editor").
		SetPassword("password").
		SetPasswordSalt("salt").
		SetUID("uid-editor").
		SetStatus(admin.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin failed: %v", err)
	}

	_, err = client.AdminRole.Create().
		SetAdminID(adminAccount.ID).
		SetRoleID(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin role link failed: %v", err)
	}

	isSuper, err := isUserSuperAdmin(ctx, client, adminAccount.UID)
	if err != nil {
		t.Fatalf("isUserSuperAdmin failed: %v", err)
	}
	if isSuper {
		t.Fatal("expected admin account without admin role to not be super admin")
	}
}

func TestIsUserSuperAdmin_EmptyUID(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:superadmin_empty?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	ctx := context.Background()

	isSuper, err := isUserSuperAdmin(ctx, client, "")
	if err != nil {
		t.Fatalf("isUserSuperAdmin failed: %v", err)
	}
	if isSuper {
		t.Fatal("expected empty UID to not be super admin")
	}
}

// -------------------------------------------------------------------
// permissionLookupKeys 测试
// -------------------------------------------------------------------

func TestPermissionLookupKeys_API(t *testing.T) {
	keys := permissionLookupKeys("api", "/api/v1/users", "GET")
	if len(keys) != 1 {
		t.Fatalf("expected 1 key for API with uppercase action, got %d: %v", len(keys), keys)
	}
	if keys[0] != "api\x00/api/v1/users\x00GET" {
		t.Errorf("expected key to be api<nil>/api/v1/users<nil>GET, got %q", keys[0])
	}
}

func TestPermissionLookupKeys_Menu(t *testing.T) {
	keys := permissionLookupKeys("menu", "/dashboard", "menu")
	if len(keys) != 3 {
		t.Fatalf("expected 3 keys for Menu, got %d: %v", len(keys), keys)
	}
}

func TestPermissionLookupKeys_Button(t *testing.T) {
	keys := permissionLookupKeys("button", "user:delete", "button")
	if len(keys) != 3 {
		t.Fatalf("expected 3 keys for Button, got %d: %v", len(keys), keys)
	}
}

func TestPermissionLookupKeys_LowercaseAction(t *testing.T) {
	keys := permissionLookupKeys("api", "/api/v1/test", "post")
	foundUpper := false
	for _, key := range keys {
		if key == "api\x00/api/v1/test\x00POST" {
			foundUpper = true
			break
		}
	}
	if !foundUpper {
		t.Fatalf("expected uppercase action key for POST, got %v", keys)
	}
}

// -------------------------------------------------------------------
// adminInOperatorScope 测试
// -------------------------------------------------------------------

func TestAdminInOperatorScope_NilOp(t *testing.T) {
	target := &ent.Admin{UID: "test"}
	if !adminInOperatorScope(nil, target) {
		t.Fatal("expected nil op to return true")
	}
}

func TestAdminInOperatorScope_SuperAdmin(t *testing.T) {
	op := &EffectiveDataScope{IsSuperAdmin: true}
	target := &ent.Admin{UID: "test"}
	if !adminInOperatorScope(op, target) {
		t.Fatal("expected super admin to return true")
	}
}

func TestAdminInOperatorScope_NilTarget(t *testing.T) {
	op := &EffectiveDataScope{Scope: DataScopeAll}
	if adminInOperatorScope(op, nil) {
		t.Fatal("expected nil target to return false")
	}
}

func TestAdminInOperatorScope_Self(t *testing.T) {
	op := &EffectiveDataScope{Scope: DataScopeSelf, AdminUID: "self-uid"}
	target := &ent.Admin{UID: "self-uid"}
	if !adminInOperatorScope(op, target) {
		t.Fatal("expected self to be in scope")
	}
}

func TestAdminInOperatorScope_SelfScope_Other(t *testing.T) {
	op := &EffectiveDataScope{Scope: DataScopeSelf, AdminUID: "self-uid"}
	target := &ent.Admin{UID: "other-uid"}
	if adminInOperatorScope(op, target) {
		t.Fatal("expected other to not be in self scope")
	}
}

func TestAdminInOperatorScope_Department_Hit(t *testing.T) {
	op := &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1, 2, 3}}
	target := &ent.Admin{UID: "test", DepartmentID: 2}
	if !adminInOperatorScope(op, target) {
		t.Fatal("expected target in department scope")
	}
}

func TestAdminInOperatorScope_Department_Miss(t *testing.T) {
	op := &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1, 2, 3}}
	target := &ent.Admin{UID: "test", DepartmentID: 99}
	if adminInOperatorScope(op, target) {
		t.Fatal("expected target not in department scope")
	}
}

func TestAdminInOperatorScope_Department_Zero(t *testing.T) {
	op := &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1, 2}}
	target := &ent.Admin{UID: "test", DepartmentID: 0}
	if adminInOperatorScope(op, target) {
		t.Fatal("expected target with department 0 to be rejected")
	}
}

// -------------------------------------------------------------------
// systemPermissionObject 测试
// -------------------------------------------------------------------

func TestSystemPermissionObject_MenuPath(t *testing.T) {
	p := &ent.SystemPermission{MenuPath: "/dashboard/workplace", Code: "dashboard"}
	result := systemPermissionObject(p)
	if result != "/dashboard/workplace" {
		t.Errorf("expected MenuPath to be used, got %q", result)
	}
}

func TestSystemPermissionObject_Code(t *testing.T) {
	p := &ent.SystemPermission{MenuPath: "", Code: "user:create"}
	result := systemPermissionObject(p)
	if result != "user:create" {
		t.Errorf("expected Code to be used, got %q", result)
	}
}

func TestSystemPermissionObject_Fallback(t *testing.T) {
	p := &ent.SystemPermission{MenuPath: "", Code: "", Path: "/api/test"}
	result := systemPermissionObject(p)
	if result != "" {
		t.Errorf("expected empty for empty MenuPath and Code, got %q", result)
	}
}

// -------------------------------------------------------------------
// ensureRoleManageableForAssignedAdmins 测试
// -------------------------------------------------------------------

func TestEnsureRoleManageableForAssignedAdmins_NilOp(t *testing.T) {
	err := ensureRoleManageableForAssignedAdmins(context.Background(), nil, nil, nil, nil, DataScopeAll, nil, false)
	if err != nil {
		t.Fatalf("expected nil op to pass, got %v", err)
	}
}

func TestEnsureRoleManageableForAssignedAdmins_SuperAdmin(t *testing.T) {
	op := &EffectiveDataScope{IsSuperAdmin: true}
	err := ensureRoleManageableForAssignedAdmins(context.Background(), nil, nil, op, nil, DataScopeAll, nil, false)
	if err != nil {
		t.Fatalf("expected super admin to pass, got %v", err)
	}
}

func TestEnsureRoleManageableForAssignedAdmins_OpScopeAll(t *testing.T) {
	op := &EffectiveDataScope{Scope: DataScopeAll}
	err := ensureRoleManageableForAssignedAdmins(context.Background(), nil, nil, op, nil, DataScopeAll, nil, false)
	if err != nil {
		t.Fatalf("expected op scope all to pass, got %v", err)
	}
}

func TestEnsureRoleManageableForAssignedAdmins_NoHolders(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:role_manage_no_holders?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	ctx := context.Background()

	roleEntity, err := client.Role.Create().
		SetName("test-role").
		SetDisplayName("Test Role").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role failed: %v", err)
	}

	op := &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1}}
	err = ensureRoleManageableForAssignedAdmins(ctx, client, &fakeDeptSvc{}, op, roleEntity, DataScopeDepartment, []int{1}, false)
	if err != nil {
		t.Fatalf("expected no holders to pass, got %v", err)
	}
}

func TestEnsureRoleManageableForAssignedAdmins_HolderInScope(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:role_manage_holder_scope?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	ctx := context.Background()

	roleEntity, err := client.Role.Create().
		SetName("test-role").
		SetDisplayName("Test Role").
		SetDataScope(role.DataScopeDepartment).
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role failed: %v", err)
	}

	adminAcc, err := client.Admin.Create().
		SetEmail("holder@test.com").
		SetUsername("holder").
		SetPassword("password").
		SetPasswordSalt("salt").
		SetUID("uid-holder").
		SetDepartmentID(1).
		SetStatus(admin.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin failed: %v", err)
	}

	_, err = client.AdminRole.Create().
		SetAdminID(adminAcc.ID).
		SetRoleID(roleEntity.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin role failed: %v", err)
	}

	op := &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1}}
	err = ensureRoleManageableForAssignedAdmins(ctx, client, &fakeDeptSvc{}, op, roleEntity, DataScopeDepartment, []int{1}, false)
	if err != nil {
		t.Fatalf("expected holder in scope to pass, got %v", err)
	}
}

func TestEnsureRoleManageableForAssignedAdmins_HolderOutOfScope(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:role_manage_holder_out?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	ctx := context.Background()

	roleEntity, err := client.Role.Create().
		SetName("test-role").
		SetDisplayName("Test Role").
		SetDataScope(role.DataScopeDepartment).
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role failed: %v", err)
	}

	adminAcc, err := client.Admin.Create().
		SetEmail("holder@test.com").
		SetUsername("holder").
		SetPassword("password").
		SetPasswordSalt("salt").
		SetUID("uid-holder").
		SetDepartmentID(99).
		SetStatus(admin.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin failed: %v", err)
	}

	_, err = client.AdminRole.Create().
		SetAdminID(adminAcc.ID).
		SetRoleID(roleEntity.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin role failed: %v", err)
	}

	op := &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1}}
	err = ensureRoleManageableForAssignedAdmins(ctx, client, &fakeDeptSvc{}, op, roleEntity, DataScopeDepartment, []int{1}, false)
	if err == nil {
		t.Fatal("expected holder out of scope to fail")
	}
}


