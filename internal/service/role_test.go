package service

import (
	"errors"
	"strings"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func setupTestClient(t *testing.T) *ent.Client {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
	return client
}

// ======================== Create 测试 ========================

func TestRoleService_Create_Success(t *testing.T) {
	_ = setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	resp, err := svc.Create(ctx, &CreateRoleRequest{
		Name:        "editor",
		DisplayName: "编辑者",
	})
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}
	if resp.ID == 0 {
		t.Errorf("期望角色ID不为0")
	}
	if resp.Name != "editor" {
		t.Errorf("期望角色名称为 editor, 实际为 %s", resp.Name)
	}
	if resp.DisplayName != "编辑者" {
		t.Errorf("期望显示名称为 编辑者, 实际为 %s", resp.DisplayName)
	}
	if resp.Status != string(role.StatusActive) {
		t.Errorf("期望状态为 active, 实际为 %s", resp.Status)
	}
	if resp.IsSystem {
		t.Errorf("期望 is_system 为 false")
	}
}

func TestRoleService_Create_EmptyName(t *testing.T) {
	_ = setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	_, err := svc.Create(ctx, &CreateRoleRequest{
		Name:        "",
		DisplayName: "测试角色",
	})
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrInvalidRoleName) {
		t.Errorf("期望错误为 ErrInvalidRoleName, 实际为 %v", err)
	}
}

func TestRoleService_Create_EmptyDisplayName(t *testing.T) {
	_ = setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	_, err := svc.Create(ctx, &CreateRoleRequest{
		Name:        "test_role",
		DisplayName: "",
	})
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !strings.Contains(err.Error(), "显示名称不能为空") {
		t.Errorf("期望错误包含'显示名称不能为空', 实际为 %v", err)
	}
}

func TestRoleService_Create_DuplicateName(t *testing.T) {
	_ = setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	_, err := svc.Create(ctx, &CreateRoleRequest{
		Name:        "editor",
		DisplayName: "编辑者",
	})
	if err != nil {
		t.Fatalf("首次创建角色失败: %v", err)
	}

	_, err = svc.Create(ctx, &CreateRoleRequest{
		Name:        "editor",
		DisplayName: "编辑者2",
	})
	if err == nil {
		t.Fatal("期望返回重复名称错误，但未返回")
	}
	if !errors.Is(err, ErrRoleNameExists) {
		t.Errorf("期望错误为 ErrRoleNameExists, 实际为 %v", err)
	}
}

func TestRoleService_Create_WithOptionalFields(t *testing.T) {
	_ = setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	resp, err := svc.Create(ctx, &CreateRoleRequest{
		Name:        "manager",
		DisplayName: "管理员",
		Description: "部门管理员角色",
		CreatedBy:   "admin_uid",
	})
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}
	if resp.Description != "部门管理员角色" {
		t.Errorf("期望描述为'部门管理员角色', 实际为 %s", resp.Description)
	}
	if resp.CreatedBy != "admin_uid" {
		t.Errorf("期望创建者为'admin_uid', 实际为 %s", resp.CreatedBy)
	}
}

// ======================== Get 测试 ========================

func TestRoleService_Get_Success(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	r, err := client.Role.Create().
		SetName("viewer").
		SetDisplayName("查看者").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试角色失败: %v", err)
	}

	resp, err := svc.Get(ctx, r.ID)
	if err != nil {
		t.Fatalf("获取角色失败: %v", err)
	}
	if resp.ID != r.ID {
		t.Errorf("期望ID为 %d, 实际为 %d", r.ID, resp.ID)
	}
	if resp.Name != "viewer" {
		t.Errorf("期望角色名称为 viewer, 实际为 %s", resp.Name)
	}
}

func TestRoleService_Get_NotFound(t *testing.T) {
	_ = setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	_, err := svc.Get(ctx, 99999)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrRoleNotFound) {
		t.Errorf("期望错误为 ErrRoleNotFound, 实际为 %v", err)
	}
}

// ======================== List 测试 ========================

func TestRoleService_List_NoFilters(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	for i, name := range []string{"role_a", "role_b", "role_c"} {
		_, err := client.Role.Create().
			SetName(name).
			SetDisplayName(name).
			SetStatus(role.StatusActive).
			Save(ctx)
		if err != nil {
			t.Fatalf("创建角色 %s 失败: %v", name, err)
		}
		_ = i
	}

	resp, err := svc.List(ctx, &ListRolesRequest{})
	if err != nil {
		t.Fatalf("获取角色列表失败: %v", err)
	}
	if resp.Total != 3 {
		t.Errorf("期望总数为 3, 实际为 %d", resp.Total)
	}
	if len(resp.Data) != 3 {
		t.Errorf("期望数据长度为 3, 实际为 %d", len(resp.Data))
	}
}

func TestRoleService_List_WithKeyword(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	_, err := client.Role.Create().
		SetName("admin_role").
		SetDisplayName("超级管理员").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}
	_, err = client.Role.Create().
		SetName("editor").
		SetDisplayName("编辑者").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	resp, err := svc.List(ctx, &ListRolesRequest{Keyword: "管理"})
	if err != nil {
		t.Fatalf("获取角色列表失败: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("期望总数为 1, 实际为 %d", resp.Total)
	}
	if resp.Data[0].DisplayName != "超级管理员" {
		t.Errorf("期望角色为'超级管理员', 实际为 %s", resp.Data[0].DisplayName)
	}
}

func TestRoleService_List_WithStatus(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	_, err := client.Role.Create().
		SetName("active_role").
		SetDisplayName("已激活").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}
	_, err = client.Role.Create().
		SetName("inactive_role").
		SetDisplayName("已禁用").
		SetStatus(role.StatusInactive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	resp, err := svc.List(ctx, &ListRolesRequest{Status: string(role.StatusInactive)})
	if err != nil {
		t.Fatalf("获取角色列表失败: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("期望总数为 1, 实际为 %d", resp.Total)
	}
	if resp.Data[0].Name != "inactive_role" {
		t.Errorf("期望角色为 inactive_role, 实际为 %s", resp.Data[0].Name)
	}
}

func TestRoleService_List_Pagination(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	for i := 0; i < 5; i++ {
		_, err := client.Role.Create().
			SetName("role_" + string(rune('a'+i))).
			SetDisplayName("角色" + string(rune('A'+i))).
			SetStatus(role.StatusActive).
			Save(ctx)
		if err != nil {
			t.Fatalf("创建角色失败: %v", err)
		}
	}

	resp, err := svc.List(ctx, &ListRolesRequest{
		PaginationRequest: PaginationRequest{Page: 1, PageSize: 2},
	})
	if err != nil {
		t.Fatalf("获取角色列表失败: %v", err)
	}
	if resp.Total != 5 {
		t.Errorf("期望总数为 5, 实际为 %d", resp.Total)
	}
	if len(resp.Data) != 2 {
		t.Errorf("期望第一页返回 2 条, 实际返回 %d 条", len(resp.Data))
	}
	if resp.Page != 1 {
		t.Errorf("期望当前页为 1, 实际为 %d", resp.Page)
	}

	resp2, err := svc.List(ctx, &ListRolesRequest{
		PaginationRequest: PaginationRequest{Page: 2, PageSize: 2},
	})
	if err != nil {
		t.Fatalf("获取第二页角色列表失败: %v", err)
	}
	if len(resp2.Data) != 2 {
		t.Errorf("期望第二页返回 2 条, 实际返回 %d 条", len(resp2.Data))
	}

	resp3, err := svc.List(ctx, &ListRolesRequest{
		PaginationRequest: PaginationRequest{Page: 3, PageSize: 2},
	})
	if err != nil {
		t.Fatalf("获取第三页角色列表失败: %v", err)
	}
	if len(resp3.Data) != 1 {
		t.Errorf("期望第三页返回 1 条, 实际返回 %d 条", len(resp3.Data))
	}
}

// ======================== ListAll 测试 ========================

func TestRoleService_ListAll_AllRoles(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	_, err := client.Role.Create().
		SetName("admin").
		SetDisplayName("超级管理员").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建超级管理员角色失败: %v", err)
	}

	for _, name := range []string{"role_x", "role_y"} {
		_, err := client.Role.Create().
			SetName(name).
			SetDisplayName(name).
			SetStatus(role.StatusActive).
			Save(ctx)
		if err != nil {
			t.Fatalf("创建角色 %s 失败: %v", name, err)
		}
	}

	data, err := svc.ListAll(ctx, &ListAllRolesRequest{})
	if err != nil {
		t.Fatalf("获取全部角色失败: %v", err)
	}
	if len(data) != 2 {
		t.Errorf("期望返回 2 个角色, 实际返回 %d 个", len(data))
	}
}

func TestRoleService_ListAll_WithKeyword(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	_, err := client.Role.Create().
		SetName("dev").
		SetDisplayName("开发者").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}
	_, err = client.Role.Create().
		SetName("ops").
		SetDisplayName("运维").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	data, err := svc.ListAll(ctx, &ListAllRolesRequest{Keyword: "开发"})
	if err != nil {
		t.Fatalf("获取全部角色失败: %v", err)
	}
	if len(data) != 1 {
		t.Errorf("期望返回 1 个角色, 实际返回 %d 个", len(data))
	}
	if data[0].Name != "dev" {
		t.Errorf("期望角色为 dev, 实际为 %s", data[0].Name)
	}
}

func TestRoleService_ListAll_WithStatus(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	_, err := client.Role.Create().
		SetName("active_all").
		SetDisplayName("活跃").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}
	_, err = client.Role.Create().
		SetName("inactive_all").
		SetDisplayName("禁用").
		SetStatus(role.StatusInactive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	data, err := svc.ListAll(ctx, &ListAllRolesRequest{Status: string(role.StatusInactive)})
	if err != nil {
		t.Fatalf("获取全部角色失败: %v", err)
	}
	if len(data) != 1 {
		t.Errorf("期望返回 1 个角色, 实际返回 %d 个", len(data))
	}
	if data[0].Name != "inactive_all" {
		t.Errorf("期望角色为 inactive_all, 实际为 %s", data[0].Name)
	}
}

// ======================== Update 测试 ========================

func TestRoleService_Update_Success(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	r, err := client.Role.Create().
		SetName("tester").
		SetDisplayName("测试员").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试角色失败: %v", err)
	}

	err = svc.Update(ctx, r.ID, &UpdateRoleRequest{
		DisplayName: "测试工程师",
		Description: "负责测试工作",
		UpdatedBy:   "updater_uid",
	})
	if err != nil {
		t.Fatalf("更新角色失败: %v", err)
	}

	updated, err := svc.Get(ctx, r.ID)
	if err != nil {
		t.Fatalf("获取更新后角色失败: %v", err)
	}
	if updated.DisplayName != "测试工程师" {
		t.Errorf("期望显示名称为'测试工程师', 实际为 %s", updated.DisplayName)
	}
	if updated.Description != "负责测试工作" {
		t.Errorf("期望描述为'负责测试工作', 实际为 %s", updated.Description)
	}
	if updated.UpdatedBy != "updater_uid" {
		t.Errorf("期望更新者为'updater_uid', 实际为 %s", updated.UpdatedBy)
	}
}

func TestRoleService_Update_NotFound(t *testing.T) {
	_ = setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	err := svc.Update(ctx, 99999, &UpdateRoleRequest{
		DisplayName: "不存在",
	})
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrRoleNotFound) {
		t.Errorf("期望错误为 ErrRoleNotFound, 实际为 %v", err)
	}
}

func TestRoleService_Update_SystemRole_SameDisplayName(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	r, err := client.Role.Create().
		SetName("sys_role").
		SetDisplayName("系统角色").
		SetIsSystem(true).
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建系统角色失败: %v", err)
	}

	err = svc.Update(ctx, r.ID, &UpdateRoleRequest{
		DisplayName: "系统角色",
		Description: "更新描述",
	})
	if err != nil {
		t.Errorf("系统角色使用相同显示名称更新不应报错, 实际为 %v", err)
	}
}

func TestRoleService_Update_SystemRole_DifferentDisplayName(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	r, err := client.Role.Create().
		SetName("sys_role2").
		SetDisplayName("系统角色2").
		SetIsSystem(true).
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建系统角色失败: %v", err)
	}

	err = svc.Update(ctx, r.ID, &UpdateRoleRequest{
		DisplayName: "修改后的名称",
	})
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !strings.Contains(err.Error(), "系统内置角色不允许修改显示名称") {
		t.Errorf("期望错误包含'系统内置角色不允许修改显示名称', 实际为 %v", err)
	}
}

// ======================== Delete 测试 ========================

func TestRoleService_Delete_Success(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	r, err := client.Role.Create().
		SetName("temp_role").
		SetDisplayName("临时角色").
		SetIsSystem(false).
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试角色失败: %v", err)
	}

	err = svc.Delete(ctx, r.ID)
	if err != nil {
		t.Fatalf("删除角色失败: %v", err)
	}

	_, err = svc.Get(ctx, r.ID)
	if err == nil {
		t.Fatal("期望角色已被删除，但仍可获取")
	}
	if !errors.Is(err, ErrRoleNotFound) {
		t.Errorf("期望错误为 ErrRoleNotFound, 实际为 %v", err)
	}
}

func TestRoleService_Delete_SystemRole(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	r, err := client.Role.Create().
		SetName("builtin").
		SetDisplayName("内置角色").
		SetIsSystem(true).
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建系统角色失败: %v", err)
	}

	err = svc.Delete(ctx, r.ID)
	if err == nil {
		t.Fatal("期望返回系统角色保护错误，但未返回")
	}
	if !errors.Is(err, ErrSystemRoleProtected) {
		t.Errorf("期望错误为 ErrSystemRoleProtected, 实际为 %v", err)
	}
}

func TestRoleService_Delete_NotFound(t *testing.T) {
	_ = setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	err := svc.Delete(ctx, 99999)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrRoleNotFound) {
		t.Errorf("期望错误为 ErrRoleNotFound, 实际为 %v", err)
	}
}

func TestRoleService_Delete_RoleInUse(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	r, err := client.Role.Create().
		SetName("in_use_role").
		SetDisplayName("使用中角色").
		SetIsSystem(false).
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	u, err := client.User.Create().
		SetEmail("testuser@example.com").
		SetPassword("password123").
		SetUsername("testuser").
		SetUID("uid-testuser").
		SetNickname("测试用户").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	_, err = client.UserRole.Create().
		SetUserID(u.ID).
		SetRoleID(r.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建用户角色关联失败: %v", err)
	}

	err = svc.Delete(ctx, r.ID)
	if err == nil {
		t.Fatal("期望返回角色使用中错误，但未返回")
	}
	if !errors.Is(err, ErrRoleInUse) {
		t.Errorf("期望错误为 ErrRoleInUse, 实际为 %v", err)
	}
}

// ======================== Enable 测试 ========================

func TestRoleService_Enable_Success(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	r, err := client.Role.Create().
		SetName("disabled_role").
		SetDisplayName("已禁用角色").
		SetStatus(role.StatusInactive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建禁用角色失败: %v", err)
	}

	err = svc.Enable(ctx, r.ID)
	if err != nil {
		t.Fatalf("启用角色失败: %v", err)
	}

	updated, err := svc.Get(ctx, r.ID)
	if err != nil {
		t.Fatalf("获取更新后角色失败: %v", err)
	}
	if updated.Status != string(role.StatusActive) {
		t.Errorf("期望状态为 active, 实际为 %s", updated.Status)
	}
}

func TestRoleService_Enable_AlreadyActive(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	r, err := client.Role.Create().
		SetName("active_role_enable").
		SetDisplayName("已激活角色").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建已激活角色失败: %v", err)
	}

	err = svc.Enable(ctx, r.ID)
	if err != nil {
		t.Errorf("已激活角色启用操作不应报错, 实际为 %v", err)
	}
}

func TestRoleService_Enable_SystemRole(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	r, err := client.Role.Create().
		SetName("sys_enable").
		SetDisplayName("系统角色启用").
		SetIsSystem(true).
		SetStatus(role.StatusInactive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建系统角色失败: %v", err)
	}

	err = svc.Enable(ctx, r.ID)
	if err == nil {
		t.Fatal("期望返回系统角色保护错误，但未返回")
	}
	if !strings.Contains(err.Error(), "系统内置角色不允许修改状态") {
		t.Errorf("期望错误包含'系统内置角色不允许修改状态', 实际为 %v", err)
	}
}

// ======================== Disable 测试 ========================

func TestRoleService_Disable_Success(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	r, err := client.Role.Create().
		SetName("to_disable").
		SetDisplayName("待禁用角色").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	err = svc.Disable(ctx, r.ID)
	if err != nil {
		t.Fatalf("禁用角色失败: %v", err)
	}

	updated, err := svc.Get(ctx, r.ID)
	if err != nil {
		t.Fatalf("获取更新后角色失败: %v", err)
	}
	if updated.Status != string(role.StatusInactive) {
		t.Errorf("期望状态为 inactive, 实际为 %s", updated.Status)
	}
}

func TestRoleService_Disable_AlreadyInactive(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	r, err := client.Role.Create().
		SetName("already_disabled").
		SetDisplayName("已禁用角色").
		SetStatus(role.StatusInactive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建已禁用角色失败: %v", err)
	}

	err = svc.Disable(ctx, r.ID)
	if err != nil {
		t.Errorf("已禁用角色禁用操作不应报错, 实际为 %v", err)
	}
}

func TestRoleService_Disable_SystemRole(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	r, err := client.Role.Create().
		SetName("sys_disable").
		SetDisplayName("系统角色禁用").
		SetIsSystem(true).
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建系统角色失败: %v", err)
	}

	err = svc.Disable(ctx, r.ID)
	if err == nil {
		t.Fatal("期望返回系统角色保护错误，但未返回")
	}
	if !strings.Contains(err.Error(), "系统内置角色不允许修改状态") {
		t.Errorf("期望错误包含'系统内置角色不允许修改状态', 实际为 %v", err)
	}
}

func TestRoleService_Disable_NotFound(t *testing.T) {
	_ = setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	err := svc.Disable(ctx, 99999)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrRoleNotFound) {
		t.Errorf("期望错误为 ErrRoleNotFound, 实际为 %v", err)
	}
}
