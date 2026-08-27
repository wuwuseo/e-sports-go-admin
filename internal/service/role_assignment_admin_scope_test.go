package service

import (
	"errors"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/adminrole"
	"go-admin/internal/ent/department"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/role"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func setupRoleAssignmentAdminScopeTest(t *testing.T) *ent.Client {
	client := enttest.Open(t, "sqlite3", "file:ra-scope?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	prevEnforcer := global.EnforcerManager
	global.EnforcerManager = nil
	t.Cleanup(func() { global.EnforcerManager = prevEnforcer })
	return client
}

func createAdminForRoleAssignmentScopeTest(t *testing.T, client *ent.Client, email, uid, username string, deptID int) *ent.Admin {
	t.Helper()
	a, err := client.Admin.Create().
		SetEmail(email).
		SetUID(uid).
		SetUsername(username).
		SetPassword("pass").
		SetPasswordSalt("salt").
		SetDepartmentID(deptID).
		SetStatus(admin.StatusActive).
		Save(t.Context())
	if err != nil {
		t.Fatalf("创建测试管理员失败: %v", err)
	}
	return a
}

func createDepartmentForRoleAssignmentScopeTest(t *testing.T, client *ent.Client, name string, parentID int) int {
	t.Helper()
	d, err := client.Department.Create().
		SetName(name).
		SetParentID(parentID).
		SetPath("/tmp").
		SetSort(0).
		SetStatus(department.StatusActive).
		Save(t.Context())
	if err != nil {
		t.Fatalf("创建测试部门失败: %v", err)
	}
	return d.ID
}

func TestRoleAssignmentService_AssignAdminRoles_Success(t *testing.T) {
	client := setupRoleAssignmentAdminScopeTest(t)
	ctx := t.Context()

	deptID := createDepartmentForRoleAssignmentScopeTest(t, client, "Eng", 0)
	target := createAdminForRoleAssignmentScopeTest(t, client, "target@example.com", "uid-target", "target", deptID)
	operator := createAdminForRoleAssignmentScopeTest(t, client, "operator@example.com", "uid-op", "operator", deptID)

	r, err := client.Role.Create().
		SetName("editor").
		SetDisplayName("编辑").
		SetDataScope(role.DataScopeSelf).
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	svc := NewRoleAssignmentService(nil, NewDepartmentService())
	op := &EffectiveDataScope{Scope: DataScopeAll, AdminID: operator.ID, AdminUID: operator.UID}
	req := &AssignRolesRequest{RoleIDs: []int{r.ID}, PrimaryRoleID: r.ID}
	if err := svc.AssignAdminRoles(ctx, op, target.ID, req, "uid-op"); err != nil {
		t.Fatalf("AssignAdminRoles 失败: %v", err)
	}

	ars, err := client.AdminRole.Query().Where(adminrole.AdminIDEQ(target.ID)).All(ctx)
	if err != nil {
		t.Fatalf("查询 admin-role 失败: %v", err)
	}
	if len(ars) != 1 {
		t.Fatalf("期望 1 条 admin-role 记录, 实际 %d", len(ars))
	}
	if ars[0].RoleID != r.ID || !ars[0].IsPrimary {
		t.Errorf("admin-role 记录不正确: %+v", ars[0])
	}
}

func TestRoleAssignmentService_AssignAdminRoles_RejectsAPIPermissionEscalation(t *testing.T) {
	client := setupRoleAssignmentAdminScopeTest(t)
	ctx := t.Context()
	deptID := createDepartmentForRoleAssignmentScopeTest(t, client, "API-Scope", 0)
	target := createAdminForRoleAssignmentScopeTest(t, client, "api-target@example.com", "uid-api-target", "api-target", deptID)
	operator := createAdminForRoleAssignmentScopeTest(t, client, "api-operator@example.com", "uid-api-operator", "api-operator", deptID)

	operatorRole := client.Role.Create().
		SetName("api_operator_role").
		SetDisplayName("API operator").
		SetDataScope(role.DataScopeAll).
		SetStatus(role.StatusActive).
		SaveX(ctx)
	delegatedRole := client.Role.Create().
		SetName("api_delegated_role").
		SetDisplayName("API delegated").
		SetDataScope(role.DataScopeSelf).
		SetStatus(role.StatusActive).
		SaveX(ctx)
	client.AdminRole.Create().SetAdminID(operator.ID).SetRoleID(operatorRole.ID).SaveX(ctx)
	client.CasbinRule.Create().SetPtype("p").SetV0(operatorRole.Name).SetV1(defaultPermissionDomain).
		SetV2("/api/v1/admin/auth/users").SetV3("GET").SaveX(ctx)
	client.CasbinRule.Create().SetPtype("p").SetV0(delegatedRole.Name).SetV1(defaultPermissionDomain).
		SetV2("/api/v1/admin/auth/users").SetV3("DELETE").SaveX(ctx)

	op := &EffectiveDataScope{Scope: DataScopeAll, AdminID: operator.ID, AdminUID: operator.UID}
	err := NewRoleAssignmentService(nil, NewDepartmentService()).AssignAdminRoles(ctx, op, target.ID, &AssignRolesRequest{
		RoleIDs:       []int{delegatedRole.ID},
		PrimaryRoleID: delegatedRole.ID,
	}, operator.UID)
	if !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("API 权限超出操作者时应拒绝角色委派，got %v", err)
	}
	if count := client.AdminRole.Query().Where(adminrole.AdminIDEQ(target.ID)).CountX(ctx); count != 0 {
		t.Fatalf("拒绝后不应写入目标角色，got %d", count)
	}
}

func TestRoleAssignmentService_AssignAdminRoles_TargetOutOfScope(t *testing.T) {
	client := setupRoleAssignmentAdminScopeTest(t)
	ctx := t.Context()

	deptA := createDepartmentForRoleAssignmentScopeTest(t, client, "DeptA", 0)
	deptB := createDepartmentForRoleAssignmentScopeTest(t, client, "DeptB", 0)
	target := createAdminForRoleAssignmentScopeTest(t, client, "target@example.com", "uid-target", "target", deptB)

	r, err := client.Role.Create().
		SetName("editor").
		SetDisplayName("编辑").
		SetDataScope(role.DataScopeSelf).
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	svc := NewRoleAssignmentService(nil, NewDepartmentService())
	op := &EffectiveDataScope{Scope: DataScopeDepartment, AdminID: 1, AdminUID: "uid-op", DepartmentIDs: []int{deptA}}
	req := &AssignRolesRequest{RoleIDs: []int{r.ID}, PrimaryRoleID: r.ID}
	err = svc.AssignAdminRoles(ctx, op, target.ID, req, "uid-op")
	if !errors.Is(err, ErrAdminOutOfScope) {
		t.Errorf("目标管理员超出范围应返回 ErrAdminOutOfScope, 实际 %v", err)
	}
}
