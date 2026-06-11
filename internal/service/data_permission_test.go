package service

import (
	"context"
	"errors"
	"testing"

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
