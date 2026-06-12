package service

import (
	"errors"
	"testing"

	"go-admin/internal/casbin/adapter"
	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/casbinrule"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"

	cmfCasbin "github.com/wuwuseo/cmf/casbin"
	"github.com/wuwuseo/cmf/config"

	_ "github.com/mattn/go-sqlite3"
)

const permissionTestModel = `
[request_definition]
r = sub, dom, obj, act

[policy_definition]
p = sub, dom, obj, act

[role_definition]
g = _, _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = g(r.sub, p.sub, r.dom) && r.dom == p.dom && (p.obj == "*" || keyMatch2(r.obj, p.obj)) && (p.act == "*" || r.act == p.act)
`

func setupPermissionTestEnforcer(t *testing.T, client *ent.Client) {
	t.Helper()

	prevEnforcerManager := global.EnforcerManager
	cfg := &config.Config{}
	cfg.Casbin.DomainsDefault = "default"
	cfg.Casbin.Domains = []struct {
		Name      string `mapstructure:"name"`
		AutoLoad  bool   `mapstructure:"auto_load"`
		ModelPath string `mapstructure:"model_path"`
		ModelText string `mapstructure:"model_text"`
	}{
		{
			Name:      "default",
			AutoLoad:  true,
			ModelText: permissionTestModel,
		},
	}

	adp, err := adapter.NewAdapterWithClient(client)
	if err != nil {
		t.Fatalf("create casbin adapter failed: %v", err)
	}

	global.EnforcerManager = cmfCasbin.InitEnforcerManager(adp, cfg)
	t.Cleanup(func() {
		global.EnforcerManager = prevEnforcerManager
	})
}

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

func TestPermissionService_GetUserMenuPermissionTree_ReturnsTree(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:permission_menu_tree?mode=memory&cache=shared&_fk=1",
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
		SetEmail("menu-tree@example.com").
		SetUsername("menu-tree").
		SetPassword("password").
		SetPasswordSalt("salt").
		SetNickname("MenuTree").
		SetUID("uid-menu-tree").
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

	parent, err := client.SystemPermission.Create().
		SetDomain("default").
		SetType("menu").
		SetName("系统管理").
		SetCode("system").
		SetMenuPath("/system").
		SetSortOrder(1).
		SetStatus(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建父菜单失败: %v", err)
	}

	child, err := client.SystemPermission.Create().
		SetDomain("default").
		SetType("menu").
		SetName("用户管理").
		SetCode("system:user").
		SetMenuPath("/system/user").
		SetParentID(parent.ID).
		SetSortOrder(1).
		SetStatus(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建子菜单失败: %v", err)
	}

	_, err = client.SystemPermission.Create().
		SetDomain("default").
		SetType("menu").
		SetName("角色管理").
		SetCode("system:role").
		SetMenuPath("/system/role").
		SetParentID(parent.ID).
		SetSortOrder(2).
		SetStatus(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建未授权菜单失败: %v", err)
	}

	_, err = client.CasbinRule.Create().
		SetPtype("p").
		SetV0("editor").
		SetV1("default").
		SetV2(child.MenuPath).
		SetV3(string(PermissionTypeMenu)).
		SetV4(string(PermissionTypeMenu)).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建菜单权限失败: %v", err)
	}

	svc := NewPermissionService(nil)
	tree, err := svc.GetUserMenuPermissionTree(ctx, editorUser.UID, "default")
	if err != nil {
		t.Fatalf("GetUserMenuPermissionTree 失败: %v", err)
	}

	if len(tree) != 1 {
		t.Fatalf("期望返回1个根节点, 实际返回 %d 个", len(tree))
	}
	if tree[0].ID != parent.ID || tree[0].Type != SystemPermissionTypeMenu {
		t.Fatalf("期望根节点为父菜单, 实际为 %#v", tree[0])
	}
	if len(tree[0].Children) != 1 {
		t.Fatalf("期望父菜单包含1个子节点, 实际包含 %d 个", len(tree[0].Children))
	}
	if tree[0].Children[0].ID != child.ID {
		t.Fatalf("期望子节点为授权菜单, 实际为 %#v", tree[0].Children[0])
	}
}

func TestPermissionService_GetUserPermissionTree_ReturnsMixedTreeWithTypes(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:permission_mixed_tree?mode=memory&cache=shared&_fk=1",
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
		SetEmail("mixed-tree@example.com").
		SetUsername("mixed-tree").
		SetPassword("password").
		SetPasswordSalt("salt").
		SetNickname("MixedTree").
		SetUID("uid-mixed-tree").
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

	menu, err := client.SystemPermission.Create().
		SetDomain("default").
		SetType("menu").
		SetName("用户管理").
		SetCode("system:user").
		SetMenuPath("/system/user").
		SetSortOrder(1).
		SetStatus(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建菜单失败: %v", err)
	}

	button, err := client.SystemPermission.Create().
		SetDomain("default").
		SetType("button").
		SetName("新增用户").
		SetCode("user:create").
		SetParentID(menu.ID).
		SetSortOrder(1).
		SetStatus(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建按钮失败: %v", err)
	}

	api, err := client.SystemPermission.Create().
		SetDomain("default").
		SetType("api").
		SetName("新增用户API").
		SetCode("user:create-api").
		SetPath("/api/v1/admin/auth/users").
		SetMethod("POST").
		SetParentID(menu.ID).
		SetSortOrder(2).
		SetStatus(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建API失败: %v", err)
	}

	for _, rule := range []struct {
		object string
		action string
		typ    PermissionType
	}{
		{menu.MenuPath, string(PermissionTypeMenu), PermissionTypeMenu},
		{button.Code, string(PermissionTypeButton), PermissionTypeButton},
		{api.Path, api.Method, PermissionTypeAPI},
	} {
		_, err = client.CasbinRule.Create().
			SetPtype("p").
			SetV0("editor").
			SetV1("default").
			SetV2(rule.object).
			SetV3(rule.action).
			SetV4(string(rule.typ)).
			Save(ctx)
		if err != nil {
			t.Fatalf("创建权限失败: %v", err)
		}
	}

	svc := NewPermissionService(nil)
	tree, err := svc.GetUserPermissionTree(ctx, editorUser.UID, "default")
	if err != nil {
		t.Fatalf("GetUserPermissionTree 失败: %v", err)
	}

	if len(tree) != 1 {
		t.Fatalf("期望返回1个根节点, 实际返回 %d 个", len(tree))
	}
	if tree[0].Type != SystemPermissionTypeMenu {
		t.Fatalf("期望根节点类型为 menu, 实际为 %s", tree[0].Type)
	}
	if len(tree[0].Children) != 2 {
		t.Fatalf("期望菜单包含2个子节点, 实际包含 %d 个", len(tree[0].Children))
	}

	childTypes := map[SystemPermissionType]bool{}
	for _, child := range tree[0].Children {
		childTypes[child.Type] = true
	}
	if !childTypes[SystemPermissionTypeButton] || !childTypes[SystemPermissionTypeAPI] {
		t.Fatalf("期望子节点包含 button 和 api 类型, 实际为 %#v", childTypes)
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

	setupPermissionTestEnforcer(t, client)

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

func TestPermissionService_CheckPermission_UsesEnforcerKeyMatchPolicy(t *testing.T) {
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
		SetUID("uid-enforcer-wildcard").
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

	setupPermissionTestEnforcer(t, client)

	enforcer, err := global.EnforcerManager.GetEnforcer("default")
	if err != nil {
		t.Fatalf("get enforcer failed: %v", err)
	}

	policyAdded, err := enforcer.AddPolicy("admin", "default", "/api/v1/admin/auth/roles/:id", "DELETE")
	if err != nil {
		t.Fatalf("add route-template policy failed: %v", err)
	}
	if !policyAdded {
		t.Fatal("route-template policy should be added")
	}

	groupAdded, err := enforcer.AddRoleForUserInDomain("uid-enforcer-wildcard", "admin", "default")
	if err != nil {
		t.Fatalf("add role grouping failed: %v", err)
	}
	if !groupAdded {
		t.Fatal("grouping rule should be added")
	}

	svc := NewPermissionService(nil)

	ok, err := svc.CheckPermission(ctx, "uid-enforcer-wildcard", "default", "/api/v1/admin/auth/roles/123", "DELETE")
	if err != nil {
		t.Fatalf("CheckPermission failed: %v", err)
	}
	if !ok {
		t.Fatal("expected keyMatch2 route-template policy to allow matching object")
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
	err = svc.CreatePermission(ctx, "", &CreatePermissionRequest{
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
	err = svc.CreatePermission(ctx, "", &CreatePermissionRequest{
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
	err := svc.CreatePermission(ctx, "", &CreatePermissionRequest{
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

	err = svc.CreatePermission(ctx, "", req)
	if err != nil {
		t.Fatalf("第一次 CreatePermission 失败: %v", err)
	}

	err = svc.CreatePermission(ctx, "", req)
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
	err = svc.DeletePermission(ctx, "", "editor", "default", "/api/v1/articles", "GET")
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
	err := svc.DeletePermission(ctx, "", "nonexistent", "default", "/api/v1/foo", "GET")
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
	err = svc.UpdatePermission(ctx, "", &UpdatePermissionRequest{
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
	err = svc.UpdatePermission(ctx, "", &UpdatePermissionRequest{
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

	setupPermissionTestEnforcer(t, client)

	svc := NewPermissionService(nil)
	hasPermission, err := svc.CheckPermission(ctx, editorUser.UID, "default", "/api/v1/articles", "GET")
	if err != nil {
		t.Fatalf("CheckPermission 失败: %v", err)
	}

	if !hasPermission {
		t.Error("期望用户拥有权限, 但返回 false")
	}
}

func TestPermissionService_AssignPermissionsToRole_ReloadsCasbinPolicy(t *testing.T) {
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
		SetDisplayName("Editor").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role failed: %v", err)
	}

	editorUser, err := client.User.Create().
		SetEmail("editor-casbin@example.com").
		SetUsername("editor-casbin").
		SetPassword("password").
		SetPasswordSalt("salt").
		SetUID("uid-editor-casbin-reload").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create user failed: %v", err)
	}

	_, err = client.UserRole.Create().
		SetUserID(editorUser.ID).
		SetRoleID(editorRole.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create user role failed: %v", err)
	}

	setupPermissionTestEnforcer(t, client)

	svc := NewPermissionService(nil)
	err = svc.AssignPermissionsToRole(ctx, "", "editor", "default", []PermissionRule{
		{Object: "/api/v1/admin/auth/plugins", Action: "GET"},
	})
	if err != nil {
		t.Fatalf("AssignPermissionsToRole failed: %v", err)
	}

	ok, err := svc.CheckPermission(ctx, editorUser.UID, "default", "/api/v1/admin/auth/plugins", "GET")
	if err != nil {
		t.Fatalf("CheckPermission failed: %v", err)
	}
	if !ok {
		t.Fatal("expected assigned permission to be visible to Casbin immediately")
	}
}

func TestPermissionService_SaveRolePermissions_ReloadsCasbinPolicy(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()

	editorRole, err := client.Role.Create().
		SetName("editor-save").
		SetDisplayName("Editor Save").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role failed: %v", err)
	}

	editorUser, err := client.User.Create().
		SetEmail("editor-save-casbin@example.com").
		SetUsername("editor-save-casbin").
		SetPassword("password").
		SetPasswordSalt("salt").
		SetUID("uid-editor-save-casbin-reload").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create user failed: %v", err)
	}

	_, err = client.UserRole.Create().
		SetUserID(editorUser.ID).
		SetRoleID(editorRole.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create user role failed: %v", err)
	}

	setupPermissionTestEnforcer(t, client)

	svc := NewPermissionService(nil)
	err = svc.SaveRolePermissions(ctx, "", editorRole.ID, &SaveRolePermissionsRequest{
		Domain: "default",
		Permissions: []PermissionRule{
			{Object: "/api/v1/admin/auth/messages", Action: "GET"},
		},
	})
	if err != nil {
		t.Fatalf("SaveRolePermissions failed: %v", err)
	}

	ok, err := svc.CheckPermission(ctx, editorUser.UID, "default", "/api/v1/admin/auth/messages", "GET")
	if err != nil {
		t.Fatalf("CheckPermission failed: %v", err)
	}
	if !ok {
		t.Fatal("expected saved permission to be visible to Casbin immediately")
	}
}

func TestPermissionService_SaveRolePermissions_ClearsAdminUserPermissionCache(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	prevEnforcerManager := global.EnforcerManager
	global.EnforcerManager = nil
	t.Cleanup(func() { global.EnforcerManager = prevEnforcerManager })

	ctx := t.Context()
	cache := makeMemCache(t)
	svc := NewPermissionService(cache)

	editorRole, err := client.Role.Create().
		SetName("admin-cache-role").
		SetDisplayName("Admin Cache Role").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role failed: %v", err)
	}

	adminAccount, err := client.Admin.Create().
		SetUsername("admin-cache-user").
		SetEmail("admin-cache@example.com").
		SetPassword("password").
		SetPasswordSalt("salt").
		SetUID("uid-admin-cache-role").
		SetStatus(admin.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin failed: %v", err)
	}

	_, err = client.AdminRole.Create().
		SetAdminID(adminAccount.ID).
		SetRoleID(editorRole.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("create admin role failed: %v", err)
	}

	_, err = client.CasbinRule.Create().
		SetPtype("p").
		SetV0(editorRole.Name).
		SetV1("default").
		SetV2("old-menu").
		SetV3("menu").
		SetV4(string(PermissionTypeMenu)).
		Save(ctx)
	if err != nil {
		t.Fatalf("create old menu rule failed: %v", err)
	}

	first, err := svc.GetUserMenuPermissions(ctx, adminAccount.UID, "default")
	if err != nil {
		t.Fatalf("first GetUserMenuPermissions failed: %v", err)
	}
	if len(first) != 1 || first[0].Object != "old-menu" {
		t.Fatalf("expected old-menu before save, got %+v", first)
	}

	err = svc.SaveRolePermissions(ctx, "", editorRole.ID, &SaveRolePermissionsRequest{
		Domain: "default",
		Permissions: []PermissionRule{
			{Object: "new-menu", Action: "menu"},
		},
	})
	if err != nil {
		t.Fatalf("SaveRolePermissions failed: %v", err)
	}

	second, err := svc.GetUserMenuPermissions(ctx, adminAccount.UID, "default")
	if err != nil {
		t.Fatalf("second GetUserMenuPermissions failed: %v", err)
	}
	if len(second) != 1 || second[0].Object != "new-menu" {
		t.Fatalf("expected admin permission cache to refresh to new-menu, got %+v", second)
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

	setupPermissionTestEnforcer(t, client)

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

	err = svc.AssignPermissionsToRole(ctx, "", "editor", "default", perms)
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

	err := svc.AssignPermissionsToRole(ctx, "", "nonexistent", "default", perms)
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
	err = svc.AssignPermissionsToRole(ctx, "", "editor", "default", []PermissionRule{})
	if err != nil {
		t.Fatalf("空列表不应该返回错误, 实际返回: %v", err)
	}
}
