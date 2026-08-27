package service

import (
	"errors"
	"testing"
)

// P0-1 回归：部门写操作必须受操作者数据范围约束（修复 S1-1 越权缺陷）

func scopeSelf(adminID int) *EffectiveDataScope {
	return &EffectiveDataScope{Scope: DataScopeSelf, AdminID: adminID, AdminUID: "scope-test-admin"}
}

func scopeDeptAndChild(adminID int, deptIDs ...int) *EffectiveDataScope {
	return &EffectiveDataScope{Scope: DataScopeDepartmentAndChild, AdminID: adminID, AdminUID: "scope-test-admin", DepartmentIDs: deptIDs}
}

func scopeSuperAdmin() *EffectiveDataScope {
	return &EffectiveDataScope{Scope: DataScopeAll, AdminID: 1, AdminUID: "super", IsSuperAdmin: true}
}

func TestDepartmentScope_SelfScopeCantWriteAnyDepartment(t *testing.T) {
	_ = setupDepartmentTestClient(t)
	ctx := t.Context()
	svc := NewDepartmentService()
	dept := createDepartmentForTest(t, svc, "Scope-Target", 0, 0)

	op := scopeSelf(2)
	if err := svc.Update(ctx, op, dept.ID, &UpdateDepartmentRequest{Name: "hacked"}); !errors.Is(err, ErrDepartmentOutOfScope) {
		t.Fatalf("self 范围 Update 应拒绝，got %v", err)
	}
	if err := svc.Delete(ctx, op, dept.ID); !errors.Is(err, ErrDepartmentOutOfScope) {
		t.Fatalf("self 范围 Delete 应拒绝，got %v", err)
	}
	if _, err := svc.Get(ctx, op, dept.ID); !errors.Is(err, ErrDepartmentOutOfScope) {
		t.Fatalf("self 范围 Get 应拒绝，got %v", err)
	}
	if _, err := svc.Create(ctx, op, &CreateDepartmentRequest{Name: "New", ParentID: dept.ID}); !errors.Is(err, ErrDepartmentOutOfScope) {
		t.Fatalf("self 范围 Create(带父部门) 应拒绝，got %v", err)
	}
}

func TestDepartmentScope_DeptAndChildAllowsInsideAndRejectsOutside(t *testing.T) {
	_ = setupDepartmentTestClient(t)
	ctx := t.Context()
	svc := NewDepartmentService()
	inside := createDepartmentForTest(t, svc, "Inside-A", 0, 0)
	outside := createDepartmentForTest(t, svc, "Outside-B", 0, 0)

	op := scopeDeptAndChild(2, inside.ID)

	if err := svc.Update(ctx, op, inside.ID, &UpdateDepartmentRequest{Name: "Inside-A-Renamed"}); err != nil {
		t.Fatalf("范围内 Update 应成功，got %v", err)
	}
	if _, err := svc.Get(ctx, op, inside.ID); err != nil {
		t.Fatalf("范围内 Get 应成功，got %v", err)
	}
	if _, err := svc.Create(ctx, op, &CreateDepartmentRequest{Name: "Inside-Child", ParentID: inside.ID}); err != nil {
		t.Fatalf("范围内父部门下 Create 应成功，got %v", err)
	}
	if err := svc.Update(ctx, op, outside.ID, &UpdateDepartmentRequest{Name: "hacked"}); !errors.Is(err, ErrDepartmentOutOfScope) {
		t.Fatalf("范围外 Update 应拒绝，got %v", err)
	}
	if _, err := svc.Get(ctx, op, outside.ID); !errors.Is(err, ErrDepartmentOutOfScope) {
		t.Fatalf("范围外 Get 应拒绝，got %v", err)
	}
	if err := svc.Delete(ctx, op, outside.ID); !errors.Is(err, ErrDepartmentOutOfScope) {
		t.Fatalf("范围外 Delete 应拒绝，got %v", err)
	}
	if _, err := svc.Create(ctx, op, &CreateDepartmentRequest{Name: "Hijack", ParentID: outside.ID}); !errors.Is(err, ErrDepartmentOutOfScope) {
		t.Fatalf("范围外父部门下 Create 应拒绝，got %v", err)
	}
}

func TestDepartmentScope_UpdateCannotMoveIntoOutsideParent(t *testing.T) {
	_ = setupDepartmentTestClient(t)
	ctx := t.Context()
	svc := NewDepartmentService()
	inside := createDepartmentForTest(t, svc, "Inside-A", 0, 0)
	outside := createDepartmentForTest(t, svc, "Outside-B", 0, 0)

	op := scopeDeptAndChild(2, inside.ID)
	outsideID := outside.ID
	if err := svc.Update(ctx, op, inside.ID, &UpdateDepartmentRequest{ParentID: &outsideID}); !errors.Is(err, ErrDepartmentOutOfScope) {
		t.Fatalf("把范围内部门移到范围外父级应拒绝，got %v", err)
	}
}

func TestDepartmentScope_UpdateCannotMoveDepartmentToRoot(t *testing.T) {
	_ = setupDepartmentTestClient(t)
	ctx := t.Context()
	svc := NewDepartmentService()
	root := createDepartmentForTest(t, svc, "Scoped-Root", 0, 0)
	child := createDepartmentForTest(t, svc, "Scoped-Child", root.ID, 0)
	op := scopeDeptAndChild(2, root.ID, child.ID)
	rootID := 0
	if err := svc.Update(ctx, op, child.ID, &UpdateDepartmentRequest{ParentID: &rootID}); !errors.Is(err, ErrDepartmentOutOfScope) {
		t.Fatalf("非全局操作者把部门移动为根节点应拒绝，got %v", err)
	}
}

func TestDepartmentScope_UpdateRequiresWholeSubtreeInScope(t *testing.T) {
	client := setupDepartmentTestClient(t)
	ctx := t.Context()
	svc := NewDepartmentService()
	source := createDepartmentForTest(t, svc, "Move-Source", 0, 0)
	child := createDepartmentForTest(t, svc, "Move-Excluded-Child", source.ID, 0)
	destination := createDepartmentForTest(t, svc, "Move-Destination", 0, 0)
	op := &EffectiveDataScope{Scope: DataScopeCustom, DepartmentIDs: []int{source.ID, destination.ID}}
	destinationID := destination.ID

	if err := svc.Update(ctx, op, source.ID, &UpdateDepartmentRequest{ParentID: &destinationID}); !errors.Is(err, ErrDepartmentOutOfScope) {
		t.Fatalf("子树含范围外部门时移动应拒绝，got %v", err)
	}
	storedChild := client.Department.GetX(ctx, child.ID)
	if storedChild.Path != child.Path {
		t.Fatalf("拒绝后子部门 path 不应变化，got %s want %s", storedChild.Path, child.Path)
	}
}

func TestDepartmentScope_NonSuperAdminCannotCreateRoot(t *testing.T) {
	_ = setupDepartmentTestClient(t)
	ctx := t.Context()
	svc := NewDepartmentService()

	if _, err := svc.Create(ctx, scopeSelf(2), &CreateDepartmentRequest{Name: "Root-Hijack"}); !errors.Is(err, ErrDepartmentOutOfScope) {
		t.Fatalf("非超管创建顶级部门应拒绝，got %v", err)
	}
	if _, err := svc.Create(ctx, scopeSuperAdmin(), &CreateDepartmentRequest{Name: "Root-OK"}); err != nil {
		t.Fatalf("超管创建顶级部门应成功，got %v", err)
	}
}

func TestDepartmentScope_SuperAdminAndNilOpBypass(t *testing.T) {
	_ = setupDepartmentTestClient(t)
	ctx := t.Context()
	svc := NewDepartmentService()
	dept := createDepartmentForTest(t, svc, "Any", 0, 0)

	if err := svc.Update(ctx, scopeSuperAdmin(), dept.ID, &UpdateDepartmentRequest{Name: "By-Super"}); err != nil {
		t.Fatalf("超管 Update 应放行，got %v", err)
	}
	if err := svc.Update(ctx, nil, dept.ID, &UpdateDepartmentRequest{Name: "By-Internal"}); err != nil {
		t.Fatalf("内部调用(nil op) Update 应放行，got %v", err)
	}
}
