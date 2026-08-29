package service

import (
	"errors"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/user"
)

// -------------------------------------------------------------------
// L3 回归：用户角色分配的权限级防提升。
// users 表无部门维度，无法做部门级范围校验，因此与 AssignAdminRoles
// 一样调用 ensureRolePermissionsNotEscalating：操作者不能授予包含
// 其自身不具备 Casbin 权限的角色。
// -------------------------------------------------------------------

func createUserScopeRole(t *testing.T, client *ent.Client, name string) *ent.Role {
	t.Helper()
	r, err := client.Role.Create().
		SetName(name).
		SetDisplayName(name).
		SetStatus(role.StatusActive).
		Save(t.Context())
	if err != nil {
		t.Fatalf("创建角色 %s 失败: %v", name, err)
	}
	return r
}

func createUserScopeCasbinRule(t *testing.T, client *ent.Client, roleName, obj, act string) {
	t.Helper()
	if _, err := client.CasbinRule.Create().SetPtype("p").SetV0(roleName).SetV1(defaultPermissionDomain).
		SetV2(obj).SetV3(act).Save(t.Context()); err != nil {
		t.Fatalf("创建 casbin 规则失败: %v", err)
	}
}

func setupUserScopeFixture(t *testing.T) (*ent.Client, *ent.Admin, *ent.User) {
	t.Helper()
	client := setupRoleAssignmentAdminScopeTest(t)
	ctx := t.Context()

	operator := client.Admin.Create().
		SetEmail("user-scope-op@example.com").
		SetUID("uid-user-scope-op").
		SetUsername("userscopeop").
		SetPassword("pass").
		SetPasswordSalt("salt").
		SetStatus(admin.StatusActive).
		SaveX(ctx)
	target := client.User.Create().
		SetEmail("user-scope-target@example.com").
		SetUID("uid-user-scope-target").
		SetUsername("userscopetarget").
		SetPassword("pass").
		SetStatus(user.StatusActive).
		SaveX(ctx)
	return client, operator, target
}

func TestRoleAssignmentService_AssignRoles_RejectsAPIPermissionEscalation(t *testing.T) {
	client, operator, target := setupUserScopeFixture(t)
	ctx := t.Context()

	// 操作者仅持有 GET /users 权限
	operatorRole := createUserScopeRole(t, client, "user_scope_operator_role")
	client.AdminRole.Create().SetAdminID(operator.ID).SetRoleID(operatorRole.ID).SaveX(ctx)
	createUserScopeCasbinRule(t, client, operatorRole.Name, "/api/v1/admin/auth/users", "GET")

	// 待授予角色持有 DELETE /users 权限，超出操作者
	delegatedRole := createUserScopeRole(t, client, "user_scope_delegated_role")
	createUserScopeCasbinRule(t, client, delegatedRole.Name, "/api/v1/admin/auth/users", "DELETE")

	op := &EffectiveDataScope{Scope: DataScopeAll, AdminID: operator.ID, AdminUID: operator.UID}
	err := NewRoleAssignmentService(nil, NewDepartmentService()).AssignRoles(ctx, target.ID, &AssignRolesRequest{
		RoleIDs:       []int{delegatedRole.ID},
		PrimaryRoleID: delegatedRole.ID,
	}, op, operator.UID, defaultPermissionDomain)
	if !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("授予超出操作者权限的角色应返回 ErrRoleScopeEscalation, got %v", err)
	}
	if count := client.UserRole.Query().CountX(ctx); count != 0 {
		t.Fatalf("拒绝后不应写入 user_role 记录, got %d", count)
	}
}

func TestRoleAssignmentService_AssignRoles_AllowsWithinOperatorPermissions(t *testing.T) {
	client, operator, target := setupUserScopeFixture(t)
	ctx := t.Context()

	operatorRole := createUserScopeRole(t, client, "user_scope_ok_operator_role")
	client.AdminRole.Create().SetAdminID(operator.ID).SetRoleID(operatorRole.ID).SaveX(ctx)
	createUserScopeCasbinRule(t, client, operatorRole.Name, "/api/v1/admin/auth/users", "GET")

	// 待授予角色权限是操作者权限的子集，应放行
	delegatedRole := createUserScopeRole(t, client, "user_scope_ok_delegated_role")
	createUserScopeCasbinRule(t, client, delegatedRole.Name, "/api/v1/admin/auth/users", "GET")

	op := &EffectiveDataScope{Scope: DataScopeAll, AdminID: operator.ID, AdminUID: operator.UID}
	err := NewRoleAssignmentService(nil, NewDepartmentService()).AssignRoles(ctx, target.ID, &AssignRolesRequest{
		RoleIDs:       []int{delegatedRole.ID},
		PrimaryRoleID: delegatedRole.ID,
	}, op, operator.UID, defaultPermissionDomain)
	if err != nil {
		t.Fatalf("授予操作者权限范围内的角色不应失败: %v", err)
	}
}

func TestRoleAssignmentService_AssignRoles_SuperAdminBypassesEscalationCheck(t *testing.T) {
	client, operator, target := setupUserScopeFixture(t)
	ctx := t.Context()

	// 操作者无任何 casbin 规则，但为超级管理员上下文，应放行
	delegatedRole := createUserScopeRole(t, client, "user_scope_super_delegated_role")
	createUserScopeCasbinRule(t, client, delegatedRole.Name, "/api/v1/admin/auth/users", "DELETE")

	op := &EffectiveDataScope{Scope: DataScopeAll, IsSuperAdmin: true, AdminID: operator.ID, AdminUID: operator.UID}
	err := NewRoleAssignmentService(nil, NewDepartmentService()).AssignRoles(ctx, target.ID, &AssignRolesRequest{
		RoleIDs:       []int{delegatedRole.ID},
		PrimaryRoleID: delegatedRole.ID,
	}, op, operator.UID, defaultPermissionDomain)
	if err != nil {
		t.Fatalf("超级管理员分配角色不应失败: %v", err)
	}
}
