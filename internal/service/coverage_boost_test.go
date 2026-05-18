package service

import (
	"context"
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func setupCoverageTest(t *testing.T) *ent.Client {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
	return client
}

// ======================== ListPermissions 测试 ========================

func TestPermissionService_ListPermissions_Empty(t *testing.T) {
	_ = setupCoverageTest(t)
	ctx := t.Context()
	svc := NewPermissionService(nil)

	resp, err := svc.ListPermissions(ctx, &ListPermissionsRequest{
		PaginationRequest: PaginationRequest{Page: 1, PageSize: 10},
	})
	if err != nil {
		t.Fatalf("获取权限列表失败: %v", err)
	}
	if resp.Total != 0 {
		t.Errorf("期望0个权限, 实际为 %d", resp.Total)
	}
}

func TestPermissionService_ListPermissions_WithData(t *testing.T) {
	client := setupCoverageTest(t)
	ctx := t.Context()
	svc := NewPermissionService(nil)

	_, err := client.Role.Create().
		SetName("editor").
		SetDisplayName("编辑者").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	for i := range 3 {
		_ = svc.CreatePermission(ctx, &CreatePermissionRequest{
			RoleName: "editor",
			Domain:   "default",
			Type:     PermissionTypeAPI,
			Object:   "/api/list" + string(rune('1'+i)),
			Action:   "GET",
		})
	}

	resp, err := svc.ListPermissions(ctx, &ListPermissionsRequest{
		PaginationRequest: PaginationRequest{Page: 1, PageSize: 10},
	})
	if err != nil {
		t.Fatalf("获取权限列表失败: %v", err)
	}
	if resp.Total != 3 {
		t.Errorf("期望3个权限, 实际为 %d", resp.Total)
	}
}

func TestPermissionService_ListPermissions_FilterByRoleName(t *testing.T) {
	client := setupCoverageTest(t)
	ctx := t.Context()
	svc := NewPermissionService(nil)

	_, err := client.Role.Create().
		SetName("role-a").
		SetDisplayName("角色A").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}
	_, err = client.Role.Create().
		SetName("role-b").
		SetDisplayName("角色B").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	svc.CreatePermission(ctx, &CreatePermissionRequest{RoleName: "role-a", Domain: "default", Type: PermissionTypeAPI, Object: "/api/a1", Action: "GET"})
	svc.CreatePermission(ctx, &CreatePermissionRequest{RoleName: "role-a", Domain: "default", Type: PermissionTypeAPI, Object: "/api/a2", Action: "GET"})
	svc.CreatePermission(ctx, &CreatePermissionRequest{RoleName: "role-b", Domain: "default", Type: PermissionTypeAPI, Object: "/api/b1", Action: "GET"})

	resp, err := svc.ListPermissions(ctx, &ListPermissionsRequest{
		PaginationRequest: PaginationRequest{Page: 1, PageSize: 10},
		RoleName:          "role-a",
	})
	if err != nil {
		t.Fatalf("按角色筛选失败: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("期望2个权限, 实际为 %d", resp.Total)
	}
}

func TestPermissionService_ListPermissions_FilterByDomain(t *testing.T) {
	client := setupCoverageTest(t)
	ctx := t.Context()
	svc := NewPermissionService(nil)

	_, err := client.Role.Create().
		SetName("editor").
		SetDisplayName("编辑者").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	svc.CreatePermission(ctx, &CreatePermissionRequest{RoleName: "editor", Domain: "default", Type: PermissionTypeAPI, Object: "/api/d1", Action: "GET"})
	svc.CreatePermission(ctx, &CreatePermissionRequest{RoleName: "editor", Domain: "other", Type: PermissionTypeAPI, Object: "/api/d2", Action: "GET"})

	resp, err := svc.ListPermissions(ctx, &ListPermissionsRequest{
		PaginationRequest: PaginationRequest{Page: 1, PageSize: 10},
		Domain:            "default",
	})
	if err != nil {
		t.Fatalf("按域筛选失败: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("期望1个权限, 实际为 %d", resp.Total)
	}
}

func TestPermissionService_ListPermissions_Pagination(t *testing.T) {
	client := setupCoverageTest(t)
	ctx := t.Context()
	svc := NewPermissionService(nil)

	_, err := client.Role.Create().
		SetName("editor").
		SetDisplayName("编辑者").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	for i := range 5 {
		svc.CreatePermission(ctx, &CreatePermissionRequest{
			RoleName: "editor",
			Domain:   "default",
			Type:     PermissionTypeAPI,
			Object:   "/api/page" + string(rune('1'+i)),
			Action:   "GET",
		})
	}

	resp, err := svc.ListPermissions(ctx, &ListPermissionsRequest{
		PaginationRequest: PaginationRequest{Page: 2, PageSize: 2},
	})
	if err != nil {
		t.Fatalf("分页查询失败: %v", err)
	}
	if resp.Total != 5 {
		t.Errorf("期望5个权限, 实际为 %d", resp.Total)
	}
}

// ======================== ListAllPermissions 测试 ========================

func TestPermissionService_ListAllPermissions_Empty(t *testing.T) {
	_ = setupCoverageTest(t)
	ctx := t.Context()
	svc := NewPermissionService(nil)

	perms, err := svc.ListAllPermissions(ctx, "", "", "", "")
	if err != nil {
		t.Fatalf("获取所有权限失败: %v", err)
	}
	if len(perms) != 0 {
		t.Errorf("期望0个权限, 实际为 %d", len(perms))
	}
}

func TestPermissionService_ListAllPermissions_WithData(t *testing.T) {
	client := setupCoverageTest(t)
	ctx := t.Context()
	svc := NewPermissionService(nil)

	_, err := client.Role.Create().
		SetName("editor").
		SetDisplayName("编辑者").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	svc.CreatePermission(ctx, &CreatePermissionRequest{RoleName: "editor", Domain: "default", Type: PermissionTypeAPI, Object: "/api/all1", Action: "GET"})
	svc.CreatePermission(ctx, &CreatePermissionRequest{RoleName: "editor", Domain: "default", Type: PermissionTypeAPI, Object: "/api/all2", Action: "POST"})

	perms, err := svc.ListAllPermissions(ctx, "", "", "", "")
	if err != nil {
		t.Fatalf("获取所有权限失败: %v", err)
	}
	if len(perms) != 2 {
		t.Errorf("期望2个权限, 实际为 %d", len(perms))
	}
}

func TestPermissionService_ListAllPermissions_WithFilters(t *testing.T) {
	client := setupCoverageTest(t)
	ctx := t.Context()
	svc := NewPermissionService(nil)

	_, err := client.Role.Create().
		SetName("editor").
		SetDisplayName("编辑者").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	svc.CreatePermission(ctx, &CreatePermissionRequest{RoleName: "editor", Domain: "default", Type: PermissionTypeAPI, Object: "/api/filter1", Action: "GET"})
	svc.CreatePermission(ctx, &CreatePermissionRequest{RoleName: "editor", Domain: "default", Type: PermissionTypeMenu, Object: "/menu/filter", Action: "menu"})

	perms, err := svc.ListAllPermissions(ctx, "", PermissionTypeAPI, "default", "editor")
	if err != nil {
		t.Fatalf("筛选所有权限失败: %v", err)
	}
	if len(perms) != 1 {
		t.Errorf("期望1个API权限, 实际为 %d", len(perms))
	}
}

// ======================== RemovePermissionsFromRole 测试 ========================

func TestPermissionService_RemovePermissionsFromRole_Success(t *testing.T) {
	client := setupCoverageTest(t)
	ctx := t.Context()
	svc := NewPermissionService(nil)

	_, err := client.Role.Create().
		SetName("editor").
		SetDisplayName("编辑者").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	svc.CreatePermission(ctx, &CreatePermissionRequest{RoleName: "editor", Domain: "default", Type: PermissionTypeAPI, Object: "/api/remove1", Action: "GET"})
	svc.CreatePermission(ctx, &CreatePermissionRequest{RoleName: "editor", Domain: "default", Type: PermissionTypeAPI, Object: "/api/remove2", Action: "GET"})

	err = svc.RemovePermissionsFromRole(ctx, "editor", "default", []PermissionRule{
		{Object: "/api/remove1", Action: "GET"},
	})
	if err != nil {
		t.Fatalf("移除权限失败: %v", err)
	}

	perms, _ := svc.GetRolePermissions(ctx, "editor", "default")
	if len(perms) != 1 {
		t.Errorf("期望1个剩余权限, 实际为 %d", len(perms))
	}
}

func TestPermissionService_RemovePermissionsFromRole_RoleNotFound(t *testing.T) {
	_ = setupCoverageTest(t)
	ctx := t.Context()
	svc := NewPermissionService(nil)

	err := svc.RemovePermissionsFromRole(ctx, "nonexistent", "default", []PermissionRule{
		{Object: "/api/test", Action: "GET"},
	})
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
}

// ======================== GetRolePermissionsByRoleID 测试 ========================

func TestPermissionService_GetRolePermissionsByRoleID_Success(t *testing.T) {
	client := setupCoverageTest(t)
	ctx := t.Context()
	svc := NewPermissionService(nil)

	r, err := client.Role.Create().
		SetName("by-id-role").
		SetDisplayName("按ID角色").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	svc.CreatePermission(ctx, &CreatePermissionRequest{RoleName: "by-id-role", Domain: "default", Type: PermissionTypeAPI, Object: "/api/byid", Action: "GET"})

	perms, err := svc.GetRolePermissionsByRoleID(ctx, r.ID, "default")
	if err != nil {
		t.Fatalf("按ID获取角色权限失败: %v", err)
	}
	if len(perms) != 1 {
		t.Errorf("期望1个权限, 实际为 %d", len(perms))
	}
}

func TestPermissionService_GetRolePermissionsByRoleID_NotFound(t *testing.T) {
	_ = setupCoverageTest(t)
	ctx := t.Context()
	svc := NewPermissionService(nil)

	_, err := svc.GetRolePermissionsByRoleID(ctx, 99999, "default")
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
}

// ======================== SaveRolePermissions 测试 ========================

func TestPermissionService_SaveRolePermissions_Success(t *testing.T) {
	client := setupCoverageTest(t)
	ctx := t.Context()
	svc := NewPermissionService(nil)

	r, err := client.Role.Create().
		SetName("save-role").
		SetDisplayName("保存角色").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	// 先分配一些权限
	svc.CreatePermission(ctx, &CreatePermissionRequest{RoleName: "save-role", Domain: "default", Type: PermissionTypeAPI, Object: "/api/old1", Action: "GET"})

	// 覆盖式保存新权限
	err = svc.SaveRolePermissions(ctx, r.ID, &SaveRolePermissionsRequest{
		Domain: "default",
		Permissions: []PermissionRule{
			{Object: "/api/new1", Action: "GET"},
			{Object: "/api/new2", Action: "POST"},
		},
	})
	if err != nil {
		t.Fatalf("保存角色权限失败: %v", err)
	}

	perms, _ := svc.GetRolePermissions(ctx, "save-role", "default")
	if len(perms) != 2 {
		t.Errorf("期望2个新权限, 实际为 %d", len(perms))
	}
}

func TestPermissionService_SaveRolePermissions_RoleNotFound(t *testing.T) {
	_ = setupCoverageTest(t)
	ctx := t.Context()
	svc := NewPermissionService(nil)

	err := svc.SaveRolePermissions(ctx, 99999, &SaveRolePermissionsRequest{
		Domain: "default",
		Permissions: []PermissionRule{
			{Object: "/api/test", Action: "GET"},
		},
	})
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
}

// ======================== defaultCacheTTL / clearUserPermissionsCache 测试 ========================

func TestPermissionService_DefaultCacheTTL(t *testing.T) {
	svc := NewPermissionService(nil)
	ttl := svc.defaultCacheTTL()
	if ttl != 5*time.Minute {
		t.Errorf("期望5分钟, 实际为 %v", ttl)
	}
}

func TestPermissionService_ClearUserPermissionsCache_NoCache(t *testing.T) {
	svc := NewPermissionService(nil)
	svc.clearUserPermissionsCache(context.Background(), "test-uid", "default")
	// 不会panic即通过
}

func TestPermissionService_ClearRoleAndUsersCache(t *testing.T) {
	client := setupCoverageTest(t)
	ctx := t.Context()
	svc := NewPermissionService(nil)

	r, err := client.Role.Create().
		SetName("clear-cache-role").
		SetDisplayName("清除缓存角色").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	r2, err := client.Role.Create().
		SetName("clear-cache-role2").
		SetDisplayName("清除缓存角色2").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	u, err := client.User.Create().
		SetEmail("clearcache@test.com").
		SetUID("uid-clearcache").
		SetUsername("clearcacheuser").
		SetPassword("pass").
		SetPasswordSalt("salt").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	client.UserRole.Create().
		SetUserID(u.ID).
		SetRoleID(r.ID).
		Save(ctx)

	client.UserRole.Create().
		SetUserID(u.ID).
		SetRoleID(r2.ID).
		Save(ctx)

	svc.clearAllUsersPermissionsByRole(ctx, "clear-cache-role", "default")
	svc.clearAllUsersPermissionsByRole(ctx, "clear-cache-role2", "default")
	svc.clearAllUsersPermissionsByRole(ctx, "nonexistent", "default")

	svc.clearRoleAndUsersCache(ctx, "clear-cache-role", "default")
}

// ======================== GetGroups 测试 ========================

func TestConfigService_GetGroups_Empty(t *testing.T) {
	_ = setupCoverageTest(t)
	ctx := t.Context()
	svc := NewConfigService(nil)

	groups, err := svc.GetGroups(ctx)
	if err != nil {
		t.Fatalf("获取配置分组失败: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("期望0个分组, 实际为 %d", len(groups))
	}
}

func TestConfigService_GetGroups_WithData(t *testing.T) {
	client := setupCoverageTest(t)
	ctx := t.Context()
	svc := NewConfigService(nil)

	_, err := client.DictData.Create().
		SetLabel("测试分组").
		SetValue("test-group").
		SetDictType("sys_config_group").
		SetSort(1).
		SetStatus(1).
		SetIsDefault(false).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建字典数据失败: %v", err)
	}

	groups, err := svc.GetGroups(ctx)
	if err != nil {
		t.Fatalf("获取配置分组失败: %v", err)
	}
	if len(groups) != 1 {
		t.Errorf("期望1个分组, 实际为 %d", len(groups))
	}
	if groups[0].Label != "测试分组" {
		t.Errorf("期望Label=测试分组, 实际=%s", groups[0].Label)
	}
	if groups[0].Value != "test-group" {
		t.Errorf("期望Value=test-group, 实际=%s", groups[0].Value)
	}
}

// ======================== InitSystemPermissions 测试 ========================

func TestInitSystemPermissions_Success(t *testing.T) {
	_ = setupCoverageTest(t)
	ctx := t.Context()
	err := InitSystemPermissions(ctx)
	if err != nil {
		t.Fatalf("初始化系统权限失败: %v", err)
	}

	svc := NewSystemPermissionService()
	perms, err := svc.List(ctx, &ListSystemPermissionsRequest{})
	if err != nil {
		t.Fatalf("获取系统权限列表失败: %v", err)
	}
	if perms.Total < 10 {
		t.Errorf("期望至少10个权限初始化, 实际为 %d", perms.Total)
	}
}

func TestInitSystemPermissions_Idempotent(t *testing.T) {
	_ = setupCoverageTest(t)
	ctx := t.Context()
	err := InitSystemPermissions(ctx)
	if err != nil {
		t.Fatalf("第一次初始化失败: %v", err)
	}

	err = InitSystemPermissions(ctx)
	if err != nil {
		t.Fatalf("第二次初始化失败（应该幂等）: %v", err)
	}
}

// ======================== OperationLog 测试 ========================

func TestOperationLogService_Log_Basic(t *testing.T) {
	client := setupCoverageTest(t)
	ctx := t.Context()
	svc := NewOperationLogService()

	err := svc.Log(ctx, &LogEntry{
		Username:    "testuser",
		Module:      "user",
		Action:      "创建",
		Method:      "POST",
		Path:        "/api/v1/users",
		IP:          "127.0.0.1",
		StatusCode:  200,
		DurationMs:  50,
		Description: "测试日志条目",
	})
	if err != nil {
		t.Fatalf("记录操作日志失败: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	_ = client
}

func TestOperationLogService_Log_WithAllFields(t *testing.T) {
	client := setupCoverageTest(t)
	ctx := t.Context()
	svc := NewOperationLogService()

	err := svc.Log(ctx, &LogEntry{
		Username:    "fulluser",
		Module:      "role",
		Action:      "更新",
		Method:      "PUT",
		Path:        "/api/v1/roles/1",
		IP:          "192.168.1.1",
		StatusCode:  200,
		DurationMs:  123,
		Description: "更新角色描述",
		UserAgent:   "Mozilla/5.0",
		RequestBody: `{"name":"test"}`,
	})
	if err != nil {
		t.Fatalf("记录完整操作日志失败: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	_ = client
}

// RecordLogout / IsLoggedOut 测试已在 blacklist_test.go 中覆盖

// ======================== pageSafe 测试 ========================

func TestPageSafe_Defaults(t *testing.T) {
	page, pageSize := pageSafe(0, 0)
	if page != 1 {
		t.Errorf("期望page=1, 实际=%d", page)
	}
	if pageSize != 10 {
		t.Errorf("期望pageSize=10, 实际=%d", pageSize)
	}
}

func TestPageSafe_ValidInput(t *testing.T) {
	page, pageSize := pageSafe(3, 50)
	if page != 3 {
		t.Errorf("期望page=3, 实际=%d", page)
	}
	if pageSize != 50 {
		t.Errorf("期望pageSize=50, 实际=%d", pageSize)
	}
}

func TestPageSafe_MaxSize(t *testing.T) {
	page, pageSize := pageSafe(1, 200)
	if page != 1 {
		t.Errorf("期望page=1, 实际=%d", page)
	}
	if pageSize != 100 {
		t.Errorf("期望pageSize=100, 实际=%d", pageSize)
	}
}

// ======================== NewUserServiceWithCache 测试 ========================

func TestNewUserServiceWithCache_Nil(t *testing.T) {
	_ = setupCoverageTest(t)
	svc := NewUserServiceWithCache(nil)
	if svc.client == nil {
		t.Fatal("期望client不为nil")
	}
}

// ======================== UserService_Logout 测试 ========================

func TestUserService_Logout_NilCache(t *testing.T) {
	_ = setupCoverageTest(t)
	ctx := t.Context()
	svc := NewUserService()
	err := svc.Logout(ctx, "uid-logout")
	if err != nil {
		t.Fatalf("无缓存时退出应返回nil: %v", err)
	}
}

// ======================== Role clearUserPermissionsByRoleID 测试 ========================

func TestRoleService_ClearUserPermissionsByRoleID_NoCache(t *testing.T) {
	client := setupCoverageTest(t)
	_ = t.Context()

	r, err := client.Role.Create().
		SetName("clear-role").
		SetDisplayName("清除角色").
		SetStatus(role.StatusActive).
		Save(t.Context())
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	u, err := client.User.Create().
		SetEmail("clearuser@test.com").
		SetUID("uid-clear").
		SetUsername("clearuser").
		SetPassword("pass").
		SetPasswordSalt("salt").
		SetStatus(user.StatusActive).
		Save(t.Context())
	if err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	client.UserRole.Create().
		SetUserID(u.ID).
		SetRoleID(r.ID).
		Save(t.Context())

	svc := NewRoleService(nil)
	svc.clearUserPermissionsByRoleID(t.Context(), r.ID, "default")
	svc.clearUserPermissionsByRoleID(t.Context(), 99999, "default")
}

// ======================== ConfigService cache paths 测试 ========================

func TestConfigService_ClearConfigCache_NoCache(t *testing.T) {
	svc := NewConfigService(nil)
	svc.clearConfigCache(t.Context(), "test-key")
}

func TestConfigService_ConfigCacheKey(t *testing.T) {
	svc := NewConfigService(nil)
	key := svc.configCacheKey("key", "test")
	if key != "config:key:test" {
		t.Errorf("期望config:key:test, 实际=%s", key)
	}
}

// ======================== CronJob Enable/Disable 单元测试 ========================

func TestCronJobService_Enable_NotFound(t *testing.T) {
	_ = setupCoverageTest(t)
	ctx := t.Context()

	_, manager := setupCronJobTest(t)
	svc := NewCronJobService(manager)

	err := svc.Enable(ctx, 99999)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
}

func TestCronJobService_Disable_NotFound(t *testing.T) {
	_, manager := setupCronJobTest(t)
	ctx := t.Context()

	svc := NewCronJobService(manager)

	err := svc.Disable(ctx, 99999)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
}

func TestCronJobService_RunNow_NotFound(t *testing.T) {
	_, manager := setupCronJobTest(t)
	ctx := t.Context()

	svc := NewCronJobService(manager)

	err := svc.RunNow(ctx, 99999)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
}

func TestCronJobService_GetLogs_NotFound(t *testing.T) {
	_, manager := setupCronJobTest(t)
	ctx := t.Context()

	svc := NewCronJobService(manager)

	_, err := svc.GetLogs(ctx, 99999)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
}

func TestCronJobService_GetLogContent_NotFound(t *testing.T) {
	_, manager := setupCronJobTest(t)
	ctx := t.Context()

	svc := NewCronJobService(manager)

	_, err := svc.GetLogContent(ctx, 99999, "test.log")
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
}

func TestCronJobService_TailLog_NotFound(t *testing.T) {
	_, manager := setupCronJobTest(t)
	ctx := t.Context()

	svc := NewCronJobService(manager)

	_, err := svc.TailLog(ctx, 99999, 10)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
}

// ======================== IsValidPermissionType 测试 ========================

func TestIsValidPermissionType_Valid(t *testing.T) {
	if !isValidPermissionType(PermissionTypeAPI) {
		t.Error("期望api类型有效")
	}
	if !isValidPermissionType(PermissionTypeMenu) {
		t.Error("期望menu类型有效")
	}
	if !isValidPermissionType(PermissionTypeButton) {
		t.Error("期望button类型有效")
	}
}

func TestIsValidPermissionType_Invalid(t *testing.T) {
	if isValidPermissionType("other") {
		t.Error("期望other类型无效")
	}
	if isValidPermissionType("") {
		t.Error("期望空类型无效")
	}
}

// ======================== DeterminePermissionType 测试 ========================

func TestDeterminePermissionType(t *testing.T) {
	if determinePermissionType("menu") != PermissionTypeMenu {
		t.Error("期望menu类型")
	}
	if determinePermissionType("button") != PermissionTypeButton {
		t.Error("期望button类型")
	}
	if determinePermissionType("api") != PermissionTypeAPI {
		t.Error("期望api类型")
	}
	if determinePermissionType("GET") != PermissionTypeAPI {
		t.Error("期望GET方法映射为api类型")
	}
	if determinePermissionType("POST") != PermissionTypeAPI {
		t.Error("期望POST方法映射为api类型")
	}
	if determinePermissionType("PUT") != PermissionTypeAPI {
		t.Error("期望PUT方法映射为api类型")
	}
	if determinePermissionType("DELETE") != PermissionTypeAPI {
		t.Error("期望DELETE方法映射为api类型")
	}
	if determinePermissionType("PATCH") != PermissionTypeAPI {
		t.Error("期望PATCH方法映射为api类型")
	}
}
