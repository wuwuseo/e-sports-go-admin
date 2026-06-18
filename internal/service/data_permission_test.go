package service

import (
	"context"
	"errors"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/role"
)

func TestContainsInt_Found(t *testing.T) {
	if !containsInt([]int{1, 2, 3}, 2) {
		t.Fatal("期望 true")
	}
}

func TestContainsInt_NotFound(t *testing.T) {
	if containsInt([]int{1, 2, 3}, 99) {
		t.Fatal("期望 false")
	}
}

func TestContainsInt_EmptySlice(t *testing.T) {
	if containsInt(nil, 1) {
		t.Fatal("期望 false")
	}
}

func TestEnsureAdminInScope_NilOp(t *testing.T) {
	if err := ensureAdminInScope(context.Background(), nil, nil, 999); err != nil {
		t.Fatalf("op==nil 应放行（且无需 client），got %v", err)
	}
}

func TestEnsureAdminInScope_SuperAdmin(t *testing.T) {
	op := &EffectiveDataScope{IsSuperAdmin: true}
	if err := ensureAdminInScope(context.Background(), nil, op, 999); err != nil {
		t.Fatalf("超管应放行（且无需 client），got %v", err)
	}
}

func TestEnsureDepartmentInScope_NilOp(t *testing.T) {
	if err := ensureDepartmentInScope(nil, 999); err != nil {
		t.Fatalf("op==nil 应放行，got %v", err)
	}
}

func TestEnsureDepartmentInScope_SuperAdmin(t *testing.T) {
	op := &EffectiveDataScope{IsSuperAdmin: true}
	if err := ensureDepartmentInScope(op, 999); err != nil {
		t.Fatalf("超管应放行，got %v", err)
	}
}

func TestEnsureDepartmentInScope_DataScopeAll(t *testing.T) {
	op := &EffectiveDataScope{Scope: DataScopeAll}
	if err := ensureDepartmentInScope(op, 0); err != nil {
		t.Fatalf("All 应放行 0，got %v", err)
	}
	if err := ensureDepartmentInScope(op, 5); err != nil {
		t.Fatalf("All 应放行 5，got %v", err)
	}
}

func TestEnsureDepartmentInScope_DataScopeSelf_Rejects(t *testing.T) {
	op := &EffectiveDataScope{Scope: DataScopeSelf}
	if err := ensureDepartmentInScope(op, 5); !errors.Is(err, ErrDepartmentOutOfScope) {
		t.Fatalf("Self 应拒绝，got %v", err)
	}
}

func TestEnsureDepartmentInScope_Department_Hit(t *testing.T) {
	op := &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1, 3, 5}}
	if err := ensureDepartmentInScope(op, 3); err != nil {
		t.Fatalf("命中部门应放行，got %v", err)
	}
}

func TestEnsureDepartmentInScope_Department_Miss(t *testing.T) {
	op := &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1, 3, 5}}
	if err := ensureDepartmentInScope(op, 9); !errors.Is(err, ErrDepartmentOutOfScope) {
		t.Fatalf("未命中部门应拒绝，got %v", err)
	}
}

func TestEnsureDepartmentInScope_DepartmentZero_Rejects(t *testing.T) {
	op := &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1, 3}}
	if err := ensureDepartmentInScope(op, 0); !errors.Is(err, ErrDepartmentOutOfScope) {
		t.Fatalf("非超管 dept=0 应拒绝，got %v", err)
	}
}

func TestEnsureDepartmentInScope_DefaultFailClosed(t *testing.T) {
	op := &EffectiveDataScope{Scope: "unknown_scope"}
	if err := ensureDepartmentInScope(op, 5); !errors.Is(err, ErrDepartmentOutOfScope) {
		t.Fatalf("未知 scope 应 fail-closed，got %v", err)
	}
}

func TestResolveAdminScope_AdminRoleHasAllDataScope(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	adminRole := client.Role.Create().
		SetName(SuperAdminRoleName).
		SetDisplayName("super admin").
		SetStatus(role.StatusActive).
		SaveX(ctx)
	operator := client.Admin.Create().
		SetEmail("seed-admin@test.com").
		SetUID("seed-admin-uid").
		SetUsername("seed-admin").
		SetPassword("pass").
		SetPasswordSalt("salt").
		SetStatus(admin.StatusActive).
		SaveX(ctx)
	client.AdminRole.Create().
		SetAdminID(operator.ID).
		SetRoleID(adminRole.ID).
		SetIsPrimary(true).
		SaveX(ctx)

	svc := NewDataPermissionService(NewDepartmentService())
	op, err := svc.ResolveAdminScope(ctx, operator.UID)
	if err != nil {
		t.Fatalf("ResolveAdminScope failed: %v", err)
	}
	if op.Scope != DataScopeAll {
		t.Fatalf("admin role should resolve to all data scope, got %s", op.Scope)
	}
	if !op.IsSuperAdmin {
		t.Fatal("admin role should mark operator as super admin for data permission checks")
	}
	if err := ensureDepartmentInScope(op, 0); err != nil {
		t.Fatalf("admin role should allow creating admin without department, got %v", err)
	}
}

// ======================== ensureRolesNotEscalating 测试 ========================
// 注意：本文件采用纯 helper 单元测试风格，部分测试需要 client，使用 setupSuperAdminTest helper
// setupSuperAdminTest 返回 (client, bs, cleanup)，测试中通过 t.Context() 获取 ctx

func TestEnsureRolesNotEscalating_NilOp(t *testing.T) {
	if err := ensureRolesNotEscalating(context.Background(), nil, nil, []int{1}); err != nil {
		t.Fatalf("nil op 应放行, got %v", err)
	}
}

func TestEnsureRolesNotEscalating_SuperAdmin(t *testing.T) {
	op := &EffectiveDataScope{IsSuperAdmin: true}
	if err := ensureRolesNotEscalating(context.Background(), nil, op, []int{1}); err != nil {
		t.Fatalf("超管应放行, got %v", err)
	}
}

func TestEnsureRolesNotEscalating_OpScopeAll(t *testing.T) {
	op := &EffectiveDataScope{Scope: DataScopeAll}
	if err := ensureRolesNotEscalating(context.Background(), nil, op, []int{1}); err != nil {
		t.Fatalf("op.Scope=All 应放行, got %v", err)
	}
}

func TestEnsureRolesNotEscalating_EmptyRoleIDs(t *testing.T) {
	op := &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1}}
	if err := ensureRolesNotEscalating(context.Background(), nil, op, nil); err != nil {
		t.Fatalf("空 roleIDs 应放行, got %v", err)
	}
}

func TestEnsureRolesNotEscalating_UnknownOpScope(t *testing.T) {
	op := &EffectiveDataScope{Scope: DataScope("invalid")}
	err := ensureRolesNotEscalating(context.Background(), nil, op, []int{1})
	if !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("期望 ErrRoleScopeEscalation, got %v", err)
	}
}

// 下方 6 个测试需要 client，使用 setupSuperAdminTest helper
func TestEnsureRolesNotEscalating_RoleAllRejected_OpDept(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	defer cleanup()
	ctx := t.Context()
	r := client.Role.Create().SetName("r_all").SetDisplayName("r_all").
		SetDataScope(role.DataScopeAll).SetStatus(role.StatusActive).SaveX(ctx)
	op := &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1}}
	if err := ensureRolesNotEscalating(ctx, client, op, []int{r.ID}); !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("期望 ErrRoleScopeEscalation, got %v", err)
	}
}

func TestEnsureRolesNotEscalating_RoleDept_OpDept(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	defer cleanup()
	ctx := t.Context()
	r := client.Role.Create().SetName("r_dept").SetDisplayName("r_dept").
		SetDataScope(role.DataScopeDepartment).SetStatus(role.StatusActive).SaveX(ctx)
	op := &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1}}
	if err := ensureRolesNotEscalating(ctx, client, op, []int{r.ID}); err != nil {
		t.Fatalf("同级应放行, got %v", err)
	}
}

func TestEnsureRolesNotEscalating_RoleSelf_OpCustom(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	defer cleanup()
	ctx := t.Context()
	r := client.Role.Create().SetName("r_self").SetDisplayName("r_self").
		SetDataScope(role.DataScopeSelf).SetStatus(role.StatusActive).SaveX(ctx)
	op := &EffectiveDataScope{Scope: DataScopeCustom, DepartmentIDs: []int{1, 2}}
	if err := ensureRolesNotEscalating(ctx, client, op, []int{r.ID}); err != nil {
		t.Fatalf("更低范围应放行, got %v", err)
	}
}

func TestEnsureRolesNotEscalating_RoleCustom_OpDept_Rejected(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	defer cleanup()
	ctx := t.Context()
	r := client.Role.Create().SetName("r_custom").SetDisplayName("r_custom").
		SetDataScope(role.DataScopeCustom).SetDataScopeDepartmentIds([]int{1}).
		SetStatus(role.StatusActive).SaveX(ctx)
	op := &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1}}
	if err := ensureRolesNotEscalating(ctx, client, op, []int{r.ID}); !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("Custom rank 高于 Dept 应拒绝, got %v", err)
	}
}

func TestEnsureRolesNotEscalating_CustomSubset(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	defer cleanup()
	ctx := t.Context()
	r := client.Role.Create().SetName("r_cs").SetDisplayName("r_cs").
		SetDataScope(role.DataScopeCustom).SetDataScopeDepartmentIds([]int{1}).
		SetStatus(role.StatusActive).SaveX(ctx)
	op := &EffectiveDataScope{Scope: DataScopeCustom, DepartmentIDs: []int{1, 2, 3}}
	if err := ensureRolesNotEscalating(ctx, client, op, []int{r.ID}); err != nil {
		t.Fatalf("子集应放行, got %v", err)
	}
}

func TestEnsureRolesNotEscalating_CustomNotSubset(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	defer cleanup()
	ctx := t.Context()
	r := client.Role.Create().SetName("r_ns").SetDisplayName("r_ns").
		SetDataScope(role.DataScopeCustom).SetDataScopeDepartmentIds([]int{1, 99}).
		SetStatus(role.StatusActive).SaveX(ctx)
	op := &EffectiveDataScope{Scope: DataScopeCustom, DepartmentIDs: []int{1, 2}}
	if err := ensureRolesNotEscalating(ctx, client, op, []int{r.ID}); !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("超出子集应拒绝, got %v", err)
	}
}

// ======================== ensureDepartmentExists 测试 ========================

func TestEnsureDepartmentExists_ZeroAllowed(t *testing.T) {
	if err := ensureDepartmentExists(context.Background(), nil, 0); err != nil {
		t.Fatalf("departmentID=0 应放行, got %v", err)
	}
}

func TestEnsureDepartmentExists_ExistingID(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	defer cleanup()
	ctx := t.Context()
	d := client.Department.Create().SetName("d1").SaveX(ctx)
	if err := ensureDepartmentExists(ctx, client, d.ID); err != nil {
		t.Fatalf("存在的 dept 应放行, got %v", err)
	}
}

func TestEnsureDepartmentExists_MissingID(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	defer cleanup()
	ctx := t.Context()
	if err := ensureDepartmentExists(ctx, client, 999999); !errors.Is(err, ErrDepartmentNotFound) {
		t.Fatalf("期望 ErrDepartmentNotFound, got %v", err)
	}
}

// ======================== 第四轮加固：守卫函数测试 ========================

// fakeDeptSvc 用于守卫层注入，避开真实 DB 查询
type fakeDeptSvc struct{ childIDs map[int][]int }

func (f *fakeDeptSvc) ChildIDs(ctx context.Context, id int, includeSelf bool) ([]int, error) {
	if f.childIDs == nil {
		return nil, nil
	}
	return f.childIDs[id], nil
}

// 其余 DepartmentServiceInterface 方法返回零值即可
func (f *fakeDeptSvc) Create(ctx context.Context, req *CreateDepartmentRequest) (*DepartmentResponse, error) {
	return nil, nil
}
func (f *fakeDeptSvc) Update(ctx context.Context, id int, req *UpdateDepartmentRequest) error {
	return nil
}
func (f *fakeDeptSvc) Delete(ctx context.Context, id int) error { return nil }
func (f *fakeDeptSvc) Get(ctx context.Context, id int) (*DepartmentResponse, error) {
	return nil, nil
}
func (f *fakeDeptSvc) Tree(ctx context.Context, req *ListDepartmentsRequest) ([]*DepartmentTreeNode, error) {
	return nil, nil
}
func (f *fakeDeptSvc) TreeInScope(ctx context.Context, op *EffectiveDataScope, req *ListDepartmentsRequest) ([]*DepartmentTreeNode, error) {
	return f.Tree(ctx, req)
}

func TestResolveRoleEffectiveDepartments_AllAndSelf(t *testing.T) {
	ctx := context.Background()
	fd := &fakeDeptSvc{}
	eff, isAll, err := resolveRoleEffectiveDepartments(ctx, fd, &ent.Role{DataScope: "all"}, 5)
	if err != nil || !isAll || eff != nil {
		t.Fatalf("All 期望 isAll=true，got eff=%v isAll=%v err=%v", eff, isAll, err)
	}
	eff, isAll, err = resolveRoleEffectiveDepartments(ctx, fd, &ent.Role{DataScope: "self"}, 5)
	if err != nil || isAll || eff != nil {
		t.Fatalf("Self 期望 nil/false，got eff=%v isAll=%v err=%v", eff, isAll, err)
	}
}

func TestResolveRoleEffectiveDepartments_DepartmentAndChild(t *testing.T) {
	ctx := context.Background()
	fd := &fakeDeptSvc{childIDs: map[int][]int{5: {5, 6, 7}}}
	eff, _, _ := resolveRoleEffectiveDepartments(ctx, fd, &ent.Role{DataScope: "department"}, 5)
	if len(eff) != 1 || eff[0] != 5 {
		t.Fatalf("Department 期望 [5], got %v", eff)
	}
	eff, _, _ = resolveRoleEffectiveDepartments(ctx, fd, &ent.Role{DataScope: "department_and_child"}, 5)
	if len(eff) != 3 {
		t.Fatalf("DeptAndChild 期望 [5,6,7], got %v", eff)
	}
	eff, _, _ = resolveRoleEffectiveDepartments(ctx, fd, &ent.Role{DataScope: "department"}, 0)
	if eff != nil {
		t.Fatalf("targetDept=0 期望 nil, got %v", eff)
	}
}

func TestResolveRoleEffectiveDepartments_Custom(t *testing.T) {
	ctx := context.Background()
	fd := &fakeDeptSvc{}
	eff, _, _ := resolveRoleEffectiveDepartments(ctx, fd, &ent.Role{DataScope: "custom", DataScopeDepartmentIds: []int{3, 4}}, 99)
	if len(eff) != 2 || eff[0] != 3 || eff[1] != 4 {
		t.Fatalf("Custom 期望 [3,4], got %v", eff)
	}
}

func TestResolveRoleEffectiveDepartments_UnknownRejected(t *testing.T) {
	ctx := context.Background()
	_, _, err := resolveRoleEffectiveDepartments(ctx, &fakeDeptSvc{}, &ent.Role{DataScope: "weird"}, 5)
	if !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("未知 scope 期望 ErrRoleScopeEscalation, got %v", err)
	}
}

func TestEnsureRolesNotEscalatingForTarget_SkipPaths(t *testing.T) {
	ctx := context.Background()
	if err := ensureRolesNotEscalatingForTarget(ctx, nil, nil, nil, []int{1}, 5); err != nil {
		t.Fatalf("nil op 放行, got %v", err)
	}
	op := &EffectiveDataScope{IsSuperAdmin: true}
	if err := ensureRolesNotEscalatingForTarget(ctx, nil, nil, op, []int{1}, 5); err != nil {
		t.Fatalf("super 放行, got %v", err)
	}
	op = &EffectiveDataScope{Scope: DataScopeAll}
	if err := ensureRolesNotEscalatingForTarget(ctx, nil, nil, op, []int{1}, 5); err != nil {
		t.Fatalf("op All 放行, got %v", err)
	}
	op = &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1}}
	if err := ensureRolesNotEscalatingForTarget(ctx, nil, nil, op, nil, 5); err != nil {
		t.Fatalf("空 roleIDs 放行, got %v", err)
	}
}

func TestEnsureRolesNotEscalatingForTarget_Coverage(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	defer cleanup()
	ctx := t.Context()
	fd := &fakeDeptSvc{childIDs: map[int][]int{5: {5, 6, 7}}}

	rAll := client.Role.Create().SetName("rt_all").SetDisplayName("rt_all").SetDataScope(role.DataScopeAll).SetStatus(role.StatusActive).SaveX(ctx)
	rSelf := client.Role.Create().SetName("rt_self").SetDisplayName("rt_self").SetDataScope(role.DataScopeSelf).SetStatus(role.StatusActive).SaveX(ctx)
	rDept := client.Role.Create().SetName("rt_dept").SetDisplayName("rt_dept").SetDataScope(role.DataScopeDepartment).SetStatus(role.StatusActive).SaveX(ctx)
	rDc := client.Role.Create().SetName("rt_dc").SetDisplayName("rt_dc").SetDataScope(role.DataScopeDepartmentAndChild).SetStatus(role.StatusActive).SaveX(ctx)
	rCustomOK := client.Role.Create().SetName("rt_cok").SetDisplayName("rt_cok").SetDataScope(role.DataScopeCustom).SetDataScopeDepartmentIds([]int{5, 6}).SetStatus(role.StatusActive).SaveX(ctx)
	rCustomBad := client.Role.Create().SetName("rt_cbad").SetDisplayName("rt_cbad").SetDataScope(role.DataScopeCustom).SetDataScopeDepartmentIds([]int{5, 99}).SetStatus(role.StatusActive).SaveX(ctx)

	op := &EffectiveDataScope{Scope: DataScopeCustom, DepartmentIDs: []int{5, 6, 7}}

	if err := ensureRolesNotEscalatingForTarget(ctx, client, fd, op, []int{rAll.ID}, 5); !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("RoleAll 期望 Esc, got %v", err)
	}
	if err := ensureRolesNotEscalatingForTarget(ctx, client, fd, op, []int{rSelf.ID}, 5); err != nil {
		t.Fatalf("RoleSelf 放行, got %v", err)
	}
	if err := ensureRolesNotEscalatingForTarget(ctx, client, fd, op, []int{rDept.ID}, 5); err != nil {
		t.Fatalf("RoleDept 命中放行, got %v", err)
	}
	if err := ensureRolesNotEscalatingForTarget(ctx, client, fd, op, []int{rDept.ID}, 99); !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("RoleDept 不命中期望 Esc, got %v", err)
	}
	if err := ensureRolesNotEscalatingForTarget(ctx, client, fd, op, []int{rDc.ID}, 5); err != nil {
		t.Fatalf("RoleDC 子集放行, got %v", err)
	}
	op2 := &EffectiveDataScope{Scope: DataScopeCustom, DepartmentIDs: []int{5}}
	if err := ensureRolesNotEscalatingForTarget(ctx, client, fd, op2, []int{rDc.ID}, 5); !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("RoleDC 超集期望 Esc, got %v", err)
	}
	if err := ensureRolesNotEscalatingForTarget(ctx, client, fd, op, []int{rCustomOK.ID}, 5); err != nil {
		t.Fatalf("RoleCustom 子集放行, got %v", err)
	}
	if err := ensureRolesNotEscalatingForTarget(ctx, client, fd, op, []int{rCustomBad.ID}, 5); !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("RoleCustom 非子集期望 Esc, got %v", err)
	}
}

func TestEnsureRoleScopeWritable(t *testing.T) {
	if err := ensureRoleScopeWritable(nil, DataScopeAll, nil); err != nil {
		t.Fatalf("nil 放行, got %v", err)
	}
	if err := ensureRoleScopeWritable(&EffectiveDataScope{IsSuperAdmin: true}, DataScopeAll, nil); err != nil {
		t.Fatalf("super 放行, got %v", err)
	}
	if err := ensureRoleScopeWritable(&EffectiveDataScope{Scope: DataScopeAll}, DataScopeAll, nil); err != nil {
		t.Fatalf("opAll 放行, got %v", err)
	}
	op := &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1}}
	if err := ensureRoleScopeWritable(op, DataScopeAll, nil); !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("Dept→All 期望 Esc, got %v", err)
	}
	if err := ensureRoleScopeWritable(op, DataScopeSelf, nil); err != nil {
		t.Fatalf("Dept→Self 放行, got %v", err)
	}
	op = &EffectiveDataScope{Scope: DataScopeCustom, DepartmentIDs: []int{1, 2}}
	if err := ensureRoleScopeWritable(op, DataScopeCustom, []int{1}); err != nil {
		t.Fatalf("Custom 子集放行, got %v", err)
	}
	if err := ensureRoleScopeWritable(op, DataScopeCustom, []int{1, 99}); !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("Custom 非子集期望 Esc, got %v", err)
	}
	if err := ensureRoleScopeWritable(op, DataScope("weird"), nil); !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("未知 newScope 期望 Esc, got %v", err)
	}
}
