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

func TestBuildPermissionLookup_BuildsKeyMap(t *testing.T) {
	perms := []*PermissionResponse{
		{Type: PermissionTypeAPI, Object: "/api/test", Action: "GET"},
		{Type: PermissionTypeMenu, Object: "/menu/home", Action: "MENU"},
	}
	lookup := buildPermissionLookup(perms)

	apiKey := permissionLookupKey("api", "/api/test", "GET")
	menuKey := permissionLookupKey("menu", "/menu/home", "MENU")

	if _, ok := lookup[apiKey]; !ok {
		t.Errorf("期望 API 权限键存在")
	}
	if _, ok := lookup[menuKey]; !ok {
		t.Errorf("期望 Menu 权限键存在")
	}
	if len(lookup) != 2 {
		t.Errorf("期望 lookup 长度为 2，实际为 %d", len(lookup))
	}
}

func TestBuildPermissionLookup_EmptyInputReturnsEmptyMap(t *testing.T) {
	lookup := buildPermissionLookup(nil)
	if len(lookup) != 0 {
		t.Errorf("期望空输入返回空 map，实际长度为 %d", len(lookup))
	}

	lookup2 := buildPermissionLookup([]*PermissionResponse{})
	if len(lookup2) != 0 {
		t.Errorf("期望空切片返回空 map，实际长度为 %d", len(lookup2))
	}
}

func TestMatchSystemPermission_API(t *testing.T) {
	lookup := map[string]struct{}{
		permissionLookupKey("api", "/api/users", "GET"): {},
	}

	p := &ent.SystemPermission{
		Type:   "api",
		Path:   "/api/users",
		Method: "GET",
	}

	if !matchSystemPermission(p, lookup) {
		t.Error("期望匹配 API 权限")
	}

	pNoMatch := &ent.SystemPermission{
		Type:   "api",
		Path:   "/api/other",
		Method: "GET",
	}
	if matchSystemPermission(pNoMatch, lookup) {
		t.Error("期望不匹配非目标 API 权限")
	}
}

func TestMatchSystemPermission_Menu(t *testing.T) {
	lookup := map[string]struct{}{
		permissionLookupKey("menu", "/system/users", "MENU"): {},
	}

	p := &ent.SystemPermission{
		Type:     "menu",
		MenuPath: "/system/users",
	}

	if !matchSystemPermission(p, lookup) {
		t.Error("期望匹配 Menu 权限")
	}

	pNoMatch := &ent.SystemPermission{
		Type:     "menu",
		MenuPath: "/system/roles",
	}
	if matchSystemPermission(pNoMatch, lookup) {
		t.Error("期望不匹配非目标 Menu 权限")
	}
}

func TestMatchSystemPermission_Button(t *testing.T) {
	lookup := map[string]struct{}{
		permissionLookupKey("button", "user:add", "button"): {},
	}

	p := &ent.SystemPermission{
		Type: "button",
		Code: "user:add",
	}

	if !matchSystemPermission(p, lookup) {
		t.Error("期望匹配 Button 权限")
	}

	pNoMatch := &ent.SystemPermission{
		Type: "button",
		Code: "user:edit",
	}
	if matchSystemPermission(pNoMatch, lookup) {
		t.Error("期望不匹配非目标 Button 权限")
	}
}

func TestMatchSystemPermission_UnknownType(t *testing.T) {
	lookup := map[string]struct{}{}
	p := &ent.SystemPermission{
		Type: "unknown",
	}
	if matchSystemPermission(p, lookup) {
		t.Error("期望未知类型返回 false")
	}
}

func TestPermissionLookupKey_Format(t *testing.T) {
	key := permissionLookupKey("api", "/path", "GET")
	expected := "api\x00/path\x00GET"
	if key != expected {
		t.Errorf("期望 key=%q，实际=%q", expected, key)
	}
}

func TestBuildSystemPermissionTree_BasicTree(t *testing.T) {
	permissions := []*ent.SystemPermission{
		{ID: 1, Name: "Root", ParentID: 0},
		{ID: 2, Name: "Child", ParentID: 1},
		{ID: 3, Name: "Grandchild", ParentID: 2},
	}
	included := map[int]struct{}{1: {}, 2: {}, 3: {}}

	tree := buildSystemPermissionTree(permissions, included)

	if len(tree) != 1 {
		t.Fatalf("期望根节点数量为 1，实际为 %d", len(tree))
	}
	if tree[0].Name != "Root" {
		t.Errorf("期望根节点名称为 Root，实际为 %s", tree[0].Name)
	}
	if len(tree[0].Children) != 1 {
		t.Fatalf("期望子节点数量为 1，实际为 %d", len(tree[0].Children))
	}
	if tree[0].Children[0].Name != "Child" {
		t.Errorf("期望子节点名称为 Child，实际为 %s", tree[0].Children[0].Name)
	}
}

func TestBuildSystemPermissionTree_FiltersIncluded(t *testing.T) {
	permissions := []*ent.SystemPermission{
		{ID: 1, Name: "Root", ParentID: 0},
		{ID: 2, Name: "Child1", ParentID: 1},
		{ID: 3, Name: "Child2", ParentID: 1},
	}
	included := map[int]struct{}{1: {}, 2: {}}

	tree := buildSystemPermissionTree(permissions, included)

	if len(tree) != 1 {
		t.Fatalf("期望根节点数量为 1，实际为 %d", len(tree))
	}
	if len(tree[0].Children) != 1 {
		t.Fatalf("期望子节点数量为 1（排除 Child2），实际为 %d", len(tree[0].Children))
	}
	if tree[0].Children[0].Name != "Child1" {
		t.Errorf("期望子节点为 Child1，实际为 %s", tree[0].Children[0].Name)
	}
}

func TestBuildSystemPermissionTree_MultipleRoots(t *testing.T) {
	permissions := []*ent.SystemPermission{
		{ID: 1, Name: "Root1", ParentID: 0},
		{ID: 2, Name: "Root2", ParentID: 0},
		{ID: 3, Name: "Child", ParentID: 1},
	}
	included := map[int]struct{}{1: {}, 2: {}, 3: {}}

	tree := buildSystemPermissionTree(permissions, included)

	if len(tree) != 2 {
		t.Fatalf("期望根节点数量为 2，实际为 %d", len(tree))
	}
}

func TestCheckUserHasRole_HasRole(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:check_role_test?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	ctx := t.Context()

	u := client.User.Create().SetUID("user1").SetUsername("user1").SetEmail("user1@test.com").SaveX(ctx)
	r := client.Role.Create().SetName("test-role").SetDisplayName("Test Role").SetStatus(role.StatusActive).SaveX(ctx)
	client.UserRole.Create().SetUser(u).SetRole(r).SaveX(ctx)

	svc := &PermissionService{client: client}
	has, err := svc.CheckUserHasRole(ctx, "user1", "default", "test-role")
	if err != nil {
		t.Fatalf("CheckUserHasRole failed: %v", err)
	}
	if !has {
		t.Error("期望用户拥有角色")
	}
}

func TestCheckUserHasRole_NoRole(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:check_role_no_test?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	ctx := t.Context()

	client.User.Create().SetUID("user1").SetUsername("user1").SetEmail("user1@test.com").SaveX(ctx)

	svc := &PermissionService{client: client}
	has, err := svc.CheckUserHasRole(ctx, "user1", "default", "test-role")
	if err != nil {
		t.Fatalf("CheckUserHasRole failed: %v", err)
	}
	if has {
		t.Error("期望用户不拥有角色")
	}
}

func TestCheckUserHasRole_UserNotFound(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:check_role_not_found?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	ctx := t.Context()

	svc := &PermissionService{client: client}
	has, err := svc.CheckUserHasRole(ctx, "unknown-user", "default", "test-role")
	if err != nil {
		t.Fatalf("CheckUserHasRole failed: %v", err)
	}
	if has {
		t.Error("期望不存在的用户返回 false")
	}
}

func TestGetUserRoleNames_AdminUser(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:get_role_admin?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	ctx := t.Context()

	a := client.Admin.Create().SetUID("admin1").SetUsername("admin1").SetPassword("pwd").SetEmail("admin1@test.com").SaveX(ctx)
	r := client.Role.Create().SetName("admin").SetDisplayName("Admin").SetStatus(role.StatusActive).SaveX(ctx)
	client.AdminRole.Create().SetAdmin(a).SetRole(r).SaveX(ctx)

	svc := &PermissionService{client: client}
	roles, err := svc.getUserRoleNames(ctx, "admin1")
	if err != nil {
		t.Fatalf("getUserRoleNames failed: %v", err)
	}
	if len(roles) != 1 || roles[0] != "admin" {
		t.Errorf("期望角色列表为 [admin]，实际为 %v", roles)
	}
}

func TestGetUserRoleNames_RegularUser(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:get_role_user?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	ctx := t.Context()

	u := client.User.Create().SetUID("user1").SetUsername("user1").SetEmail("user1@test.com").SaveX(ctx)
	r := client.Role.Create().SetName("user-role").SetDisplayName("User Role").SetStatus(role.StatusActive).SaveX(ctx)
	client.UserRole.Create().SetUser(u).SetRole(r).SaveX(ctx)

	svc := &PermissionService{client: client}
	roles, err := svc.getUserRoleNames(ctx, "user1")
	if err != nil {
		t.Fatalf("getUserRoleNames failed: %v", err)
	}
	if len(roles) != 1 || roles[0] != "user-role" {
		t.Errorf("期望角色列表为 [user-role]，实际为 %v", roles)
	}
}

func TestGetUserRoleNames_NoRoles(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:get_role_no?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	ctx := t.Context()

	client.User.Create().SetUID("user1").SetUsername("user1").SetEmail("user1@test.com").SaveX(ctx)

	svc := &PermissionService{client: client}
	roles, err := svc.getUserRoleNames(ctx, "user1")
	if err != nil {
		t.Fatalf("getUserRoleNames failed: %v", err)
	}
	if len(roles) != 0 {
		t.Errorf("期望角色列表为空，实际为 %v", roles)
	}
}

func TestGetUserPermissionsByType_Basic(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:get_perm_by_type?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	ctx := t.Context()

	u := client.User.Create().SetUID("user1").SetUsername("user1").SetEmail("user1@test.com").SaveX(ctx)
	r := client.Role.Create().SetName("role1").SetDisplayName("Role1").SetStatus(role.StatusActive).SaveX(ctx)
	client.UserRole.Create().SetUser(u).SetRole(r).SaveX(ctx)

	client.CasbinRule.Create().
		SetPtype("p").
		SetV0("role1").
		SetV1("default").
		SetV2("/api/test").
		SetV3("GET").
		SetV4("api").
		SaveX(ctx)

	svc := &PermissionService{client: client}
	perms, err := svc.getUserPermissionsByType(ctx, "user1", "default", "api")
	if err != nil {
		t.Fatalf("getUserPermissionsByType failed: %v", err)
	}
	if len(perms) != 1 {
		t.Errorf("期望权限数量为 1，实际为 %d", len(perms))
	}
	if perms[0].Object != "/api/test" {
		t.Errorf("期望权限对象为 /api/test，实际为 %s", perms[0].Object)
	}
}


