package service

import (
	"context"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func setupPermissionMenuSuperadminTest(t *testing.T) *ent.Client {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
	return client
}

func createSystemPermission(t *testing.T, ctx context.Context, client *ent.Client, name, domain, ptype string, status int8, isCommon bool) *ent.SystemPermission {
	p, err := client.SystemPermission.Create().
		SetName(name).
		SetCode(name + "-code").
		SetDomain(domain).
		SetType(ptype).
		SetStatus(status).
		SetIsCommon(isCommon).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试系统权限失败: %v", err)
	}
	return p
}

func TestGetEnabledSystemPermissionsFiltersRequestedType(t *testing.T) {
	client := setupPermissionMenuSuperadminTest(t)
	ctx := t.Context()
	createSystemPermission(t, ctx, client, "菜单权限", "admin", string(PermissionTypeMenu), 1, false)
	createSystemPermission(t, ctx, client, "按钮权限", "admin", string(PermissionTypeButton), 1, false)

	permissions, err := getEnabledSystemPermissions(ctx, "", PermissionTypeMenu)
	if err != nil {
		t.Fatalf("getEnabledSystemPermissions() error = %v", err)
	}
	if len(permissions) != 1 || permissions[0].Type != PermissionTypeMenu {
		t.Fatalf("permissions = %#v", permissions)
	}
}

// ======================== getEnabledSystemMenuPermissions 测试 ========================

func TestGetEnabledSystemMenuPermissions_Empty(t *testing.T) {
	_ = setupPermissionMenuSuperadminTest(t)
	ctx := t.Context()

	permissions, err := getEnabledSystemMenuPermissions(ctx, "")
	if err != nil {
		t.Fatalf("获取系统菜单权限失败: %v", err)
	}
	if len(permissions) != 0 {
		t.Errorf("期望权限列表为空, 实际长度为 %d", len(permissions))
	}
}

func TestGetEnabledSystemMenuPermissions_ReturnsOnlyEnabled(t *testing.T) {
	client := setupPermissionMenuSuperadminTest(t)
	ctx := t.Context()

	// 创建启用的菜单权限
	createSystemPermission(t, ctx, client, "菜单1", "admin", string(PermissionTypeMenu), 1, false)
	createSystemPermission(t, ctx, client, "菜单2", "admin", string(PermissionTypeMenu), 1, false)

	// 创建禁用的菜单权限
	createSystemPermission(t, ctx, client, "菜单3", "admin", string(PermissionTypeMenu), 0, false)

	permissions, err := getEnabledSystemMenuPermissions(ctx, "")
	if err != nil {
		t.Fatalf("获取系统菜单权限失败: %v", err)
	}
	if len(permissions) != 2 {
		t.Errorf("期望返回2个权限, 实际为 %d", len(permissions))
	}
}

func TestGetEnabledSystemMenuPermissions_ReturnsOnlyMenuType(t *testing.T) {
	client := setupPermissionMenuSuperadminTest(t)
	ctx := t.Context()

	// 创建菜单权限
	createSystemPermission(t, ctx, client, "菜单权限", "admin", string(PermissionTypeMenu), 1, false)

	// 创建按钮权限
	createSystemPermission(t, ctx, client, "按钮权限", "admin", string(PermissionTypeButton), 1, false)

	permissions, err := getEnabledSystemMenuPermissions(ctx, "")
	if err != nil {
		t.Fatalf("获取系统菜单权限失败: %v", err)
	}
	if len(permissions) != 1 {
		t.Errorf("期望返回1个权限, 实际为 %d", len(permissions))
	}
	if permissions[0].Type != PermissionTypeMenu {
		t.Errorf("期望类型为 %s, 实际为 %s", PermissionTypeMenu, permissions[0].Type)
	}
}

func TestGetEnabledSystemMenuPermissions_DomainFilter(t *testing.T) {
	client := setupPermissionMenuSuperadminTest(t)
	ctx := t.Context()

	// 创建不同域的菜单权限
	createSystemPermission(t, ctx, client, "admin菜单", "admin", string(PermissionTypeMenu), 1, false)
	createSystemPermission(t, ctx, client, "user菜单", "user", string(PermissionTypeMenu), 1, false)
	createSystemPermission(t, ctx, client, "common菜单", "other", string(PermissionTypeMenu), 1, true) // 通用权限

	permissions, err := getEnabledSystemMenuPermissions(ctx, "admin")
	if err != nil {
		t.Fatalf("获取系统菜单权限失败: %v", err)
	}
	if len(permissions) != 2 {
		t.Errorf("期望返回2个权限(admin菜单和common菜单), 实际为 %d", len(permissions))
	}
}

func TestGetEnabledSystemMenuPermissions_DBNotInitialized(t *testing.T) {
	// 保存原始ORM
	prevORM := global.ORM
	global.ORM = nil
	defer func() { global.ORM = prevORM }()

	ctx := context.Background()
	_, err := getEnabledSystemMenuPermissions(ctx, "")
	if err == nil {
		t.Error("期望返回错误，但未返回")
	}
}

// ======================== getEnabledSystemButtonPermissions 测试 ========================

func TestGetEnabledSystemButtonPermissions_Empty(t *testing.T) {
	_ = setupPermissionMenuSuperadminTest(t)
	ctx := t.Context()

	permissions, err := getEnabledSystemButtonPermissions(ctx, "")
	if err != nil {
		t.Fatalf("获取系统按钮权限失败: %v", err)
	}
	if len(permissions) != 0 {
		t.Errorf("期望权限列表为空, 实际长度为 %d", len(permissions))
	}
}

func TestGetEnabledSystemButtonPermissions_ReturnsOnlyEnabled(t *testing.T) {
	client := setupPermissionMenuSuperadminTest(t)
	ctx := t.Context()

	// 创建启用的按钮权限
	createSystemPermission(t, ctx, client, "按钮1", "admin", string(PermissionTypeButton), 1, false)
	createSystemPermission(t, ctx, client, "按钮2", "admin", string(PermissionTypeButton), 1, false)

	// 创建禁用的按钮权限
	createSystemPermission(t, ctx, client, "按钮3", "admin", string(PermissionTypeButton), 0, false)

	permissions, err := getEnabledSystemButtonPermissions(ctx, "")
	if err != nil {
		t.Fatalf("获取系统按钮权限失败: %v", err)
	}
	if len(permissions) != 2 {
		t.Errorf("期望返回2个权限, 实际为 %d", len(permissions))
	}
}

func TestGetEnabledSystemButtonPermissions_ReturnsOnlyButtonType(t *testing.T) {
	client := setupPermissionMenuSuperadminTest(t)
	ctx := t.Context()

	// 创建菜单权限
	createSystemPermission(t, ctx, client, "菜单权限", "admin", string(PermissionTypeMenu), 1, false)

	// 创建按钮权限
	createSystemPermission(t, ctx, client, "按钮权限", "admin", string(PermissionTypeButton), 1, false)

	permissions, err := getEnabledSystemButtonPermissions(ctx, "")
	if err != nil {
		t.Fatalf("获取系统按钮权限失败: %v", err)
	}
	if len(permissions) != 1 {
		t.Errorf("期望返回1个权限, 实际为 %d", len(permissions))
	}
	if permissions[0].Type != PermissionTypeButton {
		t.Errorf("期望类型为 %s, 实际为 %s", PermissionTypeButton, permissions[0].Type)
	}
}

func TestGetEnabledSystemButtonPermissions_DomainFilter(t *testing.T) {
	client := setupPermissionMenuSuperadminTest(t)
	ctx := t.Context()

	// 创建不同域的按钮权限
	createSystemPermission(t, ctx, client, "admin按钮", "admin", string(PermissionTypeButton), 1, false)
	createSystemPermission(t, ctx, client, "user按钮", "user", string(PermissionTypeButton), 1, false)
	createSystemPermission(t, ctx, client, "common按钮", "other", string(PermissionTypeButton), 1, true) // 通用权限

	permissions, err := getEnabledSystemButtonPermissions(ctx, "admin")
	if err != nil {
		t.Fatalf("获取系统按钮权限失败: %v", err)
	}
	if len(permissions) != 2 {
		t.Errorf("期望返回2个权限(admin按钮和common按钮), 实际为 %d", len(permissions))
	}
}

func TestGetEnabledSystemButtonPermissions_DBNotInitialized(t *testing.T) {
	// 保存原始ORM
	prevORM := global.ORM
	global.ORM = nil
	defer func() { global.ORM = prevORM }()

	ctx := context.Background()
	_, err := getEnabledSystemButtonPermissions(ctx, "")
	if err == nil {
		t.Error("期望返回错误，但未返回")
	}
}

// ======================== systemPermissionObject 测试 ========================

func TestSystemPermissionObject_MenuPathPriority(t *testing.T) {
	client := setupPermissionMenuSuperadminTest(t)
	ctx := t.Context()

	p, err := client.SystemPermission.Create().
		SetName("测试名称").
		SetCode("DASHBOARD").
		SetType(string(PermissionTypeMenu)).
		SetMenuPath("/dashboard").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建权限失败: %v", err)
	}

	result := systemPermissionObject(p)
	if result != "/dashboard" {
		t.Errorf("期望返回 /dashboard, 实际为 %s", result)
	}
}

func TestSystemPermissionObject_CodePriority(t *testing.T) {
	client := setupPermissionMenuSuperadminTest(t)
	ctx := t.Context()

	p, err := client.SystemPermission.Create().
		SetName("测试名称").
		SetCode("DASHBOARD").
		SetType(string(PermissionTypeMenu)).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建权限失败: %v", err)
	}

	result := systemPermissionObject(p)
	if result != "DASHBOARD" {
		t.Errorf("期望返回 DASHBOARD, 实际为 %s", result)
	}
}

func TestSystemPermissionObject_NameFallback(t *testing.T) {
	client := setupPermissionMenuSuperadminTest(t)
	ctx := t.Context()

	p, err := client.SystemPermission.Create().
		SetName("测试名称").
		SetCode("").
		SetType(string(PermissionTypeMenu)).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建权限失败: %v", err)
	}

	result := systemPermissionObject(p)
	if result != "测试名称" {
		t.Errorf("期望返回 测试名称, 实际为 %s", result)
	}
}

// TestGetEnabledSystemPermissions_DeduplicatesByObject 验证相同 Object 的权限会被去重
func TestGetEnabledSystemPermissions_DeduplicatesByObject(t *testing.T) {
	client := setupPermissionMenuSuperadminTest(t)
	ctx := t.Context()

	// 创建两个具有相同 MenuPath 但不同 Code 的菜单权限
	_, err := client.SystemPermission.Create().
		SetName("菜单A").
		SetCode("menu-a-code").
		SetDomain("admin").
		SetType(string(PermissionTypeMenu)).
		SetStatus(1).
		SetMenuPath("/dashboard").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建权限A失败: %v", err)
	}
	_, err = client.SystemPermission.Create().
		SetName("菜单B").
		SetCode("menu-b-code").
		SetDomain("admin").
		SetType(string(PermissionTypeMenu)).
		SetStatus(1).
		SetMenuPath("/dashboard").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建权限B失败: %v", err)
	}

	permissions, err := getEnabledSystemPermissions(ctx, "admin", PermissionTypeMenu)
	if err != nil {
		t.Fatalf("getEnabledSystemPermissions() error = %v", err)
	}
	if len(permissions) != 1 {
		t.Fatalf("期望去重后返回1个权限, 实际为 %d", len(permissions))
	}
	if permissions[0].Object != "/dashboard" {
		t.Errorf("期望Object=/dashboard, 实际为 %s", permissions[0].Object)
	}
}
