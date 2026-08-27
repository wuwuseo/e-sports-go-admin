package service

import (
	"errors"
	"strings"
	"testing"

	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/role"
)

// ======================== Enable/Disable 操作者持有角色拒绝测试 ========================

// TestRoleService_Enable_RejectsOperatorOwnRole 验证启用角色时，若操作者自身持有该角色则拒绝
func TestRoleService_Enable_RejectsOperatorOwnRole(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	// 创建一个非系统角色
	r, err := client.Role.Create().
		SetName("enable_self_role").
		SetDisplayName("EnableSelfRole").
		SetStatus(role.StatusInactive).
		SetDataScope(role.DataScopeAll).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	// 创建管理员并绑定该角色
	a, err := client.Admin.Create().
		SetEmail("enable-self@example.com").
		SetUsername("enable-self").
		SetPassword("hash").
		SetPasswordSalt("salt").
		SetUID("uid-enable-self").
		SetStatus(admin.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建管理员失败: %v", err)
	}
	if _, err = client.AdminRole.Create().SetAdminID(a.ID).SetRoleID(r.ID).SetIsPrimary(true).Save(ctx); err != nil {
		t.Fatalf("绑定管理员角色失败: %v", err)
	}

	// 操作者正是持有该角色的管理员
	op := &EffectiveDataScope{
		Scope:    DataScopeAll,
		AdminID:  a.ID,
		AdminUID: a.UID,
	}

	err = svc.Enable(ctx, op, r.ID)
	if !errors.Is(err, ErrCannotModifySelfRoles) {
		t.Fatalf("期望 ErrCannotModifySelfRoles，got %v", err)
	}
}

// TestRoleService_Disable_RejectsOperatorOwnRole 验证禁用角色时，若操作者自身持有该角色则拒绝
func TestRoleService_Disable_RejectsOperatorOwnRole(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	// 创建一个非系统角色
	r, err := client.Role.Create().
		SetName("disable_self_role").
		SetDisplayName("DisableSelfRole").
		SetStatus(role.StatusActive).
		SetDataScope(role.DataScopeAll).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	// 创建管理员并绑定该角色
	a, err := client.Admin.Create().
		SetEmail("disable-self@example.com").
		SetUsername("disable-self").
		SetPassword("hash").
		SetPasswordSalt("salt").
		SetUID("uid-disable-self").
		SetStatus(admin.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建管理员失败: %v", err)
	}
	if _, err = client.AdminRole.Create().SetAdminID(a.ID).SetRoleID(r.ID).SetIsPrimary(true).Save(ctx); err != nil {
		t.Fatalf("绑定管理员角色失败: %v", err)
	}

	// 操作者正是持有该角色的管理员
	op := &EffectiveDataScope{
		Scope:    DataScopeAll,
		AdminID:  a.ID,
		AdminUID: a.UID,
	}

	err = svc.Disable(ctx, op, r.ID)
	if !errors.Is(err, ErrCannotModifySelfRoles) {
		t.Fatalf("期望 ErrCannotModifySelfRoles，got %v", err)
	}
}

func TestRoleService_EnableSameStatusStillChecksScope(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	r := client.Role.Create().
		SetName("same_status_scope_guard").
		SetDisplayName("Same status scope guard").
		SetStatus(role.StatusActive).
		SetDataScope(role.DataScopeAll).
		SaveX(ctx)
	op := &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1}}
	if err := NewRoleService(nil).Enable(ctx, op, r.ID); !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("相同状态也必须执行范围校验，got %v", err)
	}
}

// ======================== Create 边界条件测试 ========================

// TestRoleService_Create_DataScopeDefaultsToSelf 验证创建角色时 DataScope 为空则默认为 "self"
func TestRoleService_Create_DataScopeDefaultsToSelf(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	resp, err := svc.Create(ctx, nil, &CreateRoleRequest{
		Name:        "default_scope_role",
		DisplayName: "默认范围角色",
		// DataScope 故意留空
	})
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}
	if resp.DataScope != string(DataScopeSelf) {
		t.Errorf("期望 DataScope 为 %q，实际为 %q", DataScopeSelf, resp.DataScope)
	}

	// 再次从数据库直读确认持久化正确
	r, err := client.Role.Get(ctx, resp.ID)
	if err != nil {
		t.Fatalf("查询角色失败: %v", err)
	}
	if string(r.DataScope) != string(DataScopeSelf) {
		t.Errorf("数据库中 DataScope 期望 %q，实际 %q", DataScopeSelf, r.DataScope)
	}
}

// TestRoleService_Create_NameExceeds100Chars 验证角色名称超过100字符时被拒绝
func TestRoleService_Create_NameExceeds100Chars(t *testing.T) {
	_ = setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	longName := strings.Repeat("a", 101)

	_, err := svc.Create(ctx, nil, &CreateRoleRequest{
		Name:        longName,
		DisplayName: "超长名称角色",
	})
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !strings.Contains(err.Error(), "角色名称不能超过100个字符") {
		t.Errorf("期望错误包含'角色名称不能超过100个字符'，实际为 %v", err)
	}
}
