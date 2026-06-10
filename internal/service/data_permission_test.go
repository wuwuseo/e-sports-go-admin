package service

import (
	"context"
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
