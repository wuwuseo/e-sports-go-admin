package service

import (
	"errors"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func setupRoleAssignmentTest(t *testing.T) *ent.Client {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
	return client
}

func TestRoleAssignmentService_GetUserRoles_NoRoles(t *testing.T) {
	client := setupRoleAssignmentTest(t)
	ctx := t.Context()

	u, err := client.User.Create().
		SetEmail("norole@test.com").
		SetUID("uid-norole").
		SetUsername("norole").
		SetPassword("password").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}

	svc := NewRoleAssignmentService(nil, NewDepartmentService())
	resp, err := svc.GetUserRoles(ctx, u.ID)
	if err != nil {
		t.Fatalf("获取用户角色失败: %v", err)
	}
	if len(resp.Roles) != 0 {
		t.Errorf("期望角色列表为空, 实际为 %d 个", len(resp.Roles))
	}
	if resp.PrimaryRoleID != 0 {
		t.Errorf("期望 PrimaryRoleID=0, 实际为 %d", resp.PrimaryRoleID)
	}
}

func TestRoleAssignmentService_GetUserRoles_WithRoles(t *testing.T) {
	client := setupRoleAssignmentTest(t)
	ctx := t.Context()

	u, err := client.User.Create().
		SetEmail("withrole@test.com").
		SetUID("uid-withrole").
		SetUsername("withrole").
		SetPassword("password").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}

	r1, err := client.Role.Create().
		SetName("editor").
		SetDisplayName("编辑者").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	r2, err := client.Role.Create().
		SetName("viewer").
		SetDisplayName("查看者").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	_, err = client.UserRole.Create().
		SetUserID(u.ID).
		SetRoleID(r1.ID).
		SetIsPrimary(true).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建用户角色关联失败: %v", err)
	}

	_, err = client.UserRole.Create().
		SetUserID(u.ID).
		SetRoleID(r2.ID).
		SetIsPrimary(false).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建用户角色关联失败: %v", err)
	}

	svc := NewRoleAssignmentService(nil, NewDepartmentService())
	resp, err := svc.GetUserRoles(ctx, u.ID)
	if err != nil {
		t.Fatalf("获取用户角色失败: %v", err)
	}
	if len(resp.Roles) != 2 {
		t.Errorf("期望角色数为 2, 实际为 %d", len(resp.Roles))
	}
	if resp.PrimaryRoleID != r1.ID {
		t.Errorf("期望主角色ID为 %d, 实际为 %d", r1.ID, resp.PrimaryRoleID)
	}
	if resp.PrimaryRole == nil {
		t.Fatal("期望 PrimaryRole 不为 nil")
	}
	if resp.PrimaryRole.Name != "editor" {
		t.Errorf("期望主角色名称为 editor, 实际为 %s", resp.PrimaryRole.Name)
	}
}

func TestRoleAssignmentService_GetUserRoles_UserNotFound(t *testing.T) {
	_ = setupRoleAssignmentTest(t)
	ctx := t.Context()

	svc := NewRoleAssignmentService(nil, NewDepartmentService())
	_, err := svc.GetUserRoles(ctx, 99999)
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("期望错误为 ErrUserNotFound, 实际为 %v", err)
	}
}

// TestRoleAssignmentService_AssignSelfRoles_Forbidden 验证不允许修改自己的角色
func TestRoleAssignmentService_AssignSelfRoles_Forbidden(t *testing.T) {
	client := setupRoleAssignmentTest(t)
	ctx := t.Context()

	a, err := client.Admin.Create().
		SetEmail("selfr@test.com").
		SetUID("u-selfr").
		SetUsername("selfradmin").
		SetPassword("password").
		SetPasswordSalt("salt").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试管理员失败: %v", err)
	}

	svc := NewRoleAssignmentService(nil, NewDepartmentService())
	op := &EffectiveDataScope{AdminID: a.ID, AdminUID: a.UID, IsSuperAdmin: true}
	err = svc.AssignAdminRoles(ctx, op, a.ID, &AssignRolesRequest{}, "u-selfr")
	if !errors.Is(err, ErrCannotModifySelfRoles) {
		t.Fatalf("期望 ErrCannotModifySelfRoles，got %v", err)
	}
}

func TestRoleAssignmentService_GetUserRoles_ExcludesDisabledRoles(t *testing.T) {
	client := setupRoleAssignmentTest(t)
	ctx := t.Context()

	u, err := client.User.Create().
		SetEmail("disabledrole@test.com").
		SetUID("uid-disabledrole").
		SetUsername("disabledrole").
		SetPassword("password").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}

	activeRole, err := client.Role.Create().
		SetName("active-role").
		SetDisplayName("启用的角色").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	inactiveRole, err := client.Role.Create().
		SetName("inactive-role").
		SetDisplayName("禁用的角色").
		SetStatus(role.StatusInactive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	_, err = client.UserRole.Create().
		SetUserID(u.ID).
		SetRoleID(activeRole.ID).
		SetIsPrimary(true).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建用户角色关联失败: %v", err)
	}

	_, err = client.UserRole.Create().
		SetUserID(u.ID).
		SetRoleID(inactiveRole.ID).
		SetIsPrimary(false).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建用户角色关联失败: %v", err)
	}

	svc := NewRoleAssignmentService(nil, NewDepartmentService())
	resp, err := svc.GetUserRoles(ctx, u.ID)
	if err != nil {
		t.Fatalf("获取用户角色失败: %v", err)
	}
	if len(resp.Roles) != 1 {
		t.Errorf("期望角色数为 1（排除禁用角色）, 实际为 %d", len(resp.Roles))
	}
	if resp.Roles[0].Name != "active-role" {
		t.Errorf("期望角色名称为 active-role, 实际为 %s", resp.Roles[0].Name)
	}
}

func TestRoleAssignmentService_AssignRoles_PrimaryRoleInvalid(t *testing.T) {
	_ = setupRoleAssignmentTest(t)
	ctx := t.Context()

	svc := NewRoleAssignmentService(nil, NewDepartmentService())
	err := svc.AssignRoles(ctx, 1, &AssignRolesRequest{
		RoleIDs:       []int{1, 2},
		PrimaryRoleID: 99,
	}, "operator-uid")

	if !errors.Is(err, ErrPrimaryRoleInvalid) {
		t.Errorf("期望错误为 ErrPrimaryRoleInvalid, 实际为 %v", err)
	}
}

func TestRoleAssignmentService_AssignRoles_InvalidRoleIDs(t *testing.T) {
	client := setupRoleAssignmentTest(t)
	ctx := t.Context()

	r1, err := client.Role.Create().
		SetName("valid-role").
		SetDisplayName("有效角色").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	svc := NewRoleAssignmentService(nil, NewDepartmentService())
	err = svc.AssignRoles(ctx, 1, &AssignRolesRequest{
		RoleIDs:       []int{r1.ID, 99999},
		PrimaryRoleID: r1.ID,
	}, "operator-uid")

	if !errors.Is(err, ErrInvalidRoleIDs) {
		t.Errorf("期望错误为 ErrInvalidRoleIDs, 实际为 %v", err)
	}
}

func TestRoleAssignmentService_AssignRoles_RejectsSuperAdminRole(t *testing.T) {
	client := setupRoleAssignmentTest(t)
	ctx := t.Context()

	u, err := client.User.Create().
		SetEmail("normal-user@test.com").
		SetUID("uid-normal-user").
		SetUsername("normaluser").
		SetPassword("password").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}

	adminRole, err := client.Role.Create().
		SetName("admin").
		SetDisplayName("超级管理员").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建超级管理员角色失败: %v", err)
	}

	svc := NewRoleAssignmentService(nil, NewDepartmentService())
	err = svc.AssignRoles(ctx, u.ID, &AssignRolesRequest{
		RoleIDs:       []int{adminRole.ID},
		PrimaryRoleID: adminRole.ID,
	}, "operator-uid")

	if !errors.Is(err, ErrSuperAdminRoleAssignmentForbidden) {
		t.Errorf("期望错误为 ErrSuperAdminRoleAssignmentForbidden, 实际为 %v", err)
	}
}

func TestRoleAssignmentService_AssignAdminRoles_RejectsSuperAdminRole(t *testing.T) {
	client := setupRoleAssignmentTest(t)
	ctx := t.Context()

	a, err := client.Admin.Create().
		SetEmail("normal-admin@test.com").
		SetUID("uid-normal-admin").
		SetUsername("normaladmin").
		SetPassword("password").
		SetPasswordSalt("salt").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试管理员失败: %v", err)
	}

	adminRole, err := client.Role.Create().
		SetName("admin").
		SetDisplayName("超级管理员").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建超级管理员角色失败: %v", err)
	}

	svc := NewRoleAssignmentService(nil, NewDepartmentService())
	err = svc.AssignAdminRoles(ctx, nil, a.ID, &AssignRolesRequest{
		RoleIDs:       []int{adminRole.ID},
		PrimaryRoleID: adminRole.ID,
	}, "operator-uid")

	if !errors.Is(err, ErrSuperAdminRoleAssignmentForbidden) {
		t.Errorf("期望错误为 ErrSuperAdminRoleAssignmentForbidden, 实际为 %v", err)
	}
}

func TestRoleAssignmentService_AssignRoles_UserNotFound(t *testing.T) {
	client := setupRoleAssignmentTest(t)
	ctx := t.Context()

	r1, err := client.Role.Create().
		SetName("role-for-missing").
		SetDisplayName("给不存在用户的角色").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	svc := NewRoleAssignmentService(nil, NewDepartmentService())
	err = svc.AssignRoles(ctx, 99999, &AssignRolesRequest{
		RoleIDs:       []int{r1.ID},
		PrimaryRoleID: r1.ID,
	}, "operator-uid")

	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("期望错误为 ErrUserNotFound, 实际为 %v", err)
	}
}
