package service

import (
	"context"
	"errors"
	"testing"
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
