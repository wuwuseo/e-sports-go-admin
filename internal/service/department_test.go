package service

import (
	"errors"
	"fmt"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/department"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func setupDepartmentTestClient(t *testing.T) *ent.Client {
	client := enttest.Open(t, "sqlite3", "file:department?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
	return client
}

func createDepartmentForTest(t *testing.T, svc *DepartmentService, name string, parentID int, sort int) *DepartmentResponse {
	t.Helper()
	resp, err := svc.Create(t.Context(), &CreateDepartmentRequest{
		Name:     name,
		ParentID: parentID,
		Sort:     sort,
	})
	if err != nil {
		t.Fatalf("create department %s failed: %v", name, err)
	}
	return resp
}

func TestDepartmentService_CreateRootSetsPathAndDefaultStatus(t *testing.T) {
	_ = setupDepartmentTestClient(t)
	ctx := t.Context()
	svc := NewDepartmentService()

	resp, err := svc.Create(ctx, &CreateDepartmentRequest{Name: "Head Office"})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	expectedPath := fmt.Sprintf("/%d", resp.ID)
	if resp.Path != expectedPath {
		t.Fatalf("expected path %s, got %s", expectedPath, resp.Path)
	}
	if resp.Status != string(department.StatusActive) {
		t.Fatalf("expected status active, got %s", resp.Status)
	}
	if resp.ParentID != 0 {
		t.Fatalf("expected parent_id 0, got %d", resp.ParentID)
	}
}

func TestDepartmentService_CreateChildSetsParentPath(t *testing.T) {
	_ = setupDepartmentTestClient(t)
	ctx := t.Context()
	svc := NewDepartmentService()
	parent := createDepartmentForTest(t, svc, "Engineering", 0, 0)

	child, err := svc.Create(ctx, &CreateDepartmentRequest{
		Name:     "Platform",
		ParentID: parent.ID,
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	expectedPath := fmt.Sprintf("%s/%d", parent.Path, child.ID)
	if child.Path != expectedPath {
		t.Fatalf("expected path %s, got %s", expectedPath, child.Path)
	}
	if child.ParentID != parent.ID {
		t.Fatalf("expected parent_id %d, got %d", parent.ID, child.ParentID)
	}
}

func TestDepartmentService_CreateWithMissingParentReturnsNotFound(t *testing.T) {
	_ = setupDepartmentTestClient(t)
	ctx := t.Context()
	svc := NewDepartmentService()

	_, err := svc.Create(ctx, &CreateDepartmentRequest{
		Name:     "Ghost Team",
		ParentID: 99999,
	})
	if !errors.Is(err, ErrDepartmentNotFound) {
		t.Fatalf("expected ErrDepartmentNotFound, got %v", err)
	}
}

func TestDepartmentService_CreateWithNegativeParentReturnsInvalidParent(t *testing.T) {
	_ = setupDepartmentTestClient(t)
	ctx := t.Context()
	svc := NewDepartmentService()

	_, err := svc.Create(ctx, &CreateDepartmentRequest{
		Name:     "Invalid Parent",
		ParentID: -1,
	})
	if !errors.Is(err, ErrInvalidDepartmentParent) {
		t.Fatalf("expected ErrInvalidDepartmentParent, got %v", err)
	}
}

func TestDepartmentService_TreeBuildsHierarchyAndOrdersSiblings(t *testing.T) {
	_ = setupDepartmentTestClient(t)
	ctx := t.Context()
	svc := NewDepartmentService()
	rootB := createDepartmentForTest(t, svc, "Root B", 0, 2)
	rootA := createDepartmentForTest(t, svc, "Root A", 0, 1)
	childB := createDepartmentForTest(t, svc, "Child B", rootA.ID, 2)
	childA := createDepartmentForTest(t, svc, "Child A", rootA.ID, 1)
	inactiveRoot := createDepartmentForTest(t, svc, "Inactive Root", 0, 3)
	if err := svc.Update(ctx, inactiveRoot.ID, &UpdateDepartmentRequest{Status: string(department.StatusInactive)}); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}

	tree, err := svc.Tree(ctx, &ListDepartmentsRequest{Status: string(department.StatusActive)})
	if err != nil {
		t.Fatalf("Tree returned error: %v", err)
	}

	if len(tree) != 2 {
		t.Fatalf("expected 2 active roots, got %d", len(tree))
	}
	if tree[0].ID != rootA.ID || tree[1].ID != rootB.ID {
		t.Fatalf("expected roots ordered as Root A, Root B; got %q, %q", tree[0].Name, tree[1].Name)
	}
	if len(tree[0].Children) != 2 {
		t.Fatalf("expected Root A to have 2 children, got %d", len(tree[0].Children))
	}
	if tree[0].Children[0].ID != childA.ID || tree[0].Children[1].ID != childB.ID {
		t.Fatalf("expected children ordered as Child A, Child B; got %q, %q", tree[0].Children[0].Name, tree[0].Children[1].Name)
	}

	filtered, err := svc.Tree(ctx, &ListDepartmentsRequest{Keyword: "Child A"})
	if err != nil {
		t.Fatalf("Tree with keyword returned error: %v", err)
	}
	if len(filtered) != 1 || filtered[0].ID != childA.ID {
		t.Fatalf("expected missing filtered parent to make Child A a root, got %#v", filtered)
	}
}

func TestDepartmentService_TreeInScopeFiltersToOperatorDepartments(t *testing.T) {
	_ = setupDepartmentTestClient(t)
	ctx := t.Context()
	svc := NewDepartmentService()

	root := createDepartmentForTest(t, svc, "Root", 0, 1)
	allowed := createDepartmentForTest(t, svc, "Allowed", root.ID, 1)
	denied := createDepartmentForTest(t, svc, "Denied", root.ID, 2)

	tree, err := svc.TreeInScope(ctx, &EffectiveDataScope{
		Scope:         DataScopeCustom,
		DepartmentIDs: []int{allowed.ID},
	}, &ListDepartmentsRequest{Status: string(department.StatusActive)})
	if err != nil {
		t.Fatalf("TreeInScope returned error: %v", err)
	}
	if len(tree) != 1 || tree[0].ID != allowed.ID {
		t.Fatalf("expected only allowed department %d, got %#v", allowed.ID, tree)
	}
	if tree[0].ID == denied.ID {
		t.Fatal("out-of-scope department leaked into scoped tree")
	}

	allTree, err := svc.TreeInScope(ctx, &EffectiveDataScope{Scope: DataScopeAll}, &ListDepartmentsRequest{Status: string(department.StatusActive)})
	if err != nil {
		t.Fatalf("TreeInScope all returned error: %v", err)
	}
	if len(allTree) != 1 || allTree[0].ID != root.ID || len(allTree[0].Children) != 2 {
		t.Fatalf("expected all-scope tree to keep full hierarchy, got %#v", allTree)
	}
}

func TestDepartmentService_ChildIDsReturnsDescendantsWithAndWithoutSelf(t *testing.T) {
	_ = setupDepartmentTestClient(t)
	ctx := t.Context()
	svc := NewDepartmentService()
	root := createDepartmentForTest(t, svc, "Root", 0, 0)
	child := createDepartmentForTest(t, svc, "Child", root.ID, 0)
	grandchild := createDepartmentForTest(t, svc, "Grandchild", child.ID, 0)
	sibling := createDepartmentForTest(t, svc, "Sibling", 0, 0)

	withoutSelf, err := svc.ChildIDs(ctx, root.ID, false)
	if err != nil {
		t.Fatalf("ChildIDs returned error: %v", err)
	}
	assertIntSet(t, withoutSelf, []int{child.ID, grandchild.ID})

	withSelf, err := svc.ChildIDs(ctx, root.ID, true)
	if err != nil {
		t.Fatalf("ChildIDs returned error: %v", err)
	}
	assertIntSet(t, withSelf, []int{root.ID, child.ID, grandchild.ID})
	for _, id := range withSelf {
		if id == sibling.ID {
			t.Fatalf("ChildIDs included sibling department %d", sibling.ID)
		}
	}
}

func TestDepartmentService_UpdateRejectsSelfAndDescendantParent(t *testing.T) {
	_ = setupDepartmentTestClient(t)
	ctx := t.Context()
	svc := NewDepartmentService()
	root := createDepartmentForTest(t, svc, "Root", 0, 0)
	child := createDepartmentForTest(t, svc, "Child", root.ID, 0)
	grandchild := createDepartmentForTest(t, svc, "Grandchild", child.ID, 0)

	selfParentID := root.ID
	err := svc.Update(ctx, root.ID, &UpdateDepartmentRequest{ParentID: &selfParentID})
	if !errors.Is(err, ErrDepartmentCycle) {
		t.Fatalf("expected ErrDepartmentCycle for self parent, got %v", err)
	}

	descendantParentID := grandchild.ID
	err = svc.Update(ctx, root.ID, &UpdateDepartmentRequest{ParentID: &descendantParentID})
	if !errors.Is(err, ErrDepartmentCycle) {
		t.Fatalf("expected ErrDepartmentCycle for descendant parent, got %v", err)
	}
}

func TestDepartmentService_UpdateWithNegativeParentReturnsInvalidParent(t *testing.T) {
	_ = setupDepartmentTestClient(t)
	ctx := t.Context()
	svc := NewDepartmentService()
	root := createDepartmentForTest(t, svc, "Root", 0, 0)

	negativeParentID := -1
	err := svc.Update(ctx, root.ID, &UpdateDepartmentRequest{ParentID: &negativeParentID})
	if !errors.Is(err, ErrInvalidDepartmentParent) {
		t.Fatalf("expected ErrInvalidDepartmentParent, got %v", err)
	}
}

func TestDepartmentService_UpdatePreservesContactFieldsWhenOmitted(t *testing.T) {
	client := setupDepartmentTestClient(t)
	ctx := t.Context()
	svc := NewDepartmentService()

	dept, err := svc.Create(ctx, &CreateDepartmentRequest{
		Name:   "Operations",
		Sort:   1,
		Leader: "Ava",
		Phone:  "13800138000",
		Email:  "ava@example.com",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	sort := 9
	err = svc.Update(ctx, dept.ID, &UpdateDepartmentRequest{
		Sort:   &sort,
		Status: string(department.StatusInactive),
	})
	if err != nil {
		t.Fatalf("Update returned error: %v", err)
	}

	updated, err := client.Department.Get(ctx, dept.ID)
	if err != nil {
		t.Fatalf("get department failed: %v", err)
	}
	if updated.Sort != sort {
		t.Fatalf("expected sort %d, got %d", sort, updated.Sort)
	}
	if updated.Status != department.StatusInactive {
		t.Fatalf("expected status inactive, got %s", updated.Status)
	}
	if updated.Leader != "Ava" || updated.Phone != "13800138000" || updated.Email != "ava@example.com" {
		t.Fatalf("expected contact fields to be preserved, got leader=%q phone=%q email=%q", updated.Leader, updated.Phone, updated.Email)
	}
}

func TestDepartmentService_UpdateClearsContactFieldsWhenEmptyPointersProvided(t *testing.T) {
	client := setupDepartmentTestClient(t)
	ctx := t.Context()
	svc := NewDepartmentService()

	dept, err := svc.Create(ctx, &CreateDepartmentRequest{
		Name:   "Support",
		Leader: "Ben",
		Phone:  "13900139000",
		Email:  "ben@example.com",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	empty := ""
	err = svc.Update(ctx, dept.ID, &UpdateDepartmentRequest{
		Leader: &empty,
		Phone:  &empty,
		Email:  &empty,
	})
	if err != nil {
		t.Fatalf("Update returned error: %v", err)
	}

	updated, err := client.Department.Get(ctx, dept.ID)
	if err != nil {
		t.Fatalf("get department failed: %v", err)
	}
	if updated.Leader != "" || updated.Phone != "" || updated.Email != "" {
		t.Fatalf("expected contact fields to be cleared, got leader=%q phone=%q email=%q", updated.Leader, updated.Phone, updated.Email)
	}
}

func TestDepartmentService_UpdateParentRecalculatesDescendantPaths(t *testing.T) {
	client := setupDepartmentTestClient(t)
	ctx := t.Context()
	svc := NewDepartmentService()
	oldRoot := createDepartmentForTest(t, svc, "Old Root", 0, 0)
	newRoot := createDepartmentForTest(t, svc, "New Root", 0, 0)
	child := createDepartmentForTest(t, svc, "Child", oldRoot.ID, 0)
	grandchild := createDepartmentForTest(t, svc, "Grandchild", child.ID, 0)

	newParentID := newRoot.ID
	err := svc.Update(ctx, child.ID, &UpdateDepartmentRequest{
		ParentID: &newParentID,
	})
	if err != nil {
		t.Fatalf("Update returned error: %v", err)
	}

	updatedChild, err := client.Department.Get(ctx, child.ID)
	if err != nil {
		t.Fatalf("get child failed: %v", err)
	}
	updatedGrandchild, err := client.Department.Get(ctx, grandchild.ID)
	if err != nil {
		t.Fatalf("get grandchild failed: %v", err)
	}

	expectedChildPath := fmt.Sprintf("%s/%d", newRoot.Path, child.ID)
	expectedGrandchildPath := fmt.Sprintf("%s/%d", expectedChildPath, grandchild.ID)
	if updatedChild.Path != expectedChildPath {
		t.Fatalf("expected child path %s, got %s", expectedChildPath, updatedChild.Path)
	}
	if updatedGrandchild.Path != expectedGrandchildPath {
		t.Fatalf("expected grandchild path %s, got %s", expectedGrandchildPath, updatedGrandchild.Path)
	}
}

func TestDepartmentService_DeleteRejectsChildrenAndDeletesLeaf(t *testing.T) {
	client := setupDepartmentTestClient(t)
	ctx := t.Context()
	svc := NewDepartmentService()
	root := createDepartmentForTest(t, svc, "Root", 0, 0)
	child := createDepartmentForTest(t, svc, "Child", root.ID, 0)

	err := svc.Delete(ctx, root.ID)
	if !errors.Is(err, ErrDepartmentHasChildren) {
		t.Fatalf("expected ErrDepartmentHasChildren, got %v", err)
	}

	if err := svc.Delete(ctx, child.ID); err != nil {
		t.Fatalf("Delete leaf returned error: %v", err)
	}
	exists, err := client.Department.Query().Where(department.IDEQ(child.ID)).Exist(ctx)
	if err != nil {
		t.Fatalf("check child existence failed: %v", err)
	}
	if exists {
		t.Fatal("expected child department to be deleted")
	}
}

func TestDepartmentService_DeleteMissingDepartmentReturnsNotFound(t *testing.T) {
	_ = setupDepartmentTestClient(t)
	ctx := t.Context()
	svc := NewDepartmentService()

	err := svc.Delete(ctx, 99999)
	if !errors.Is(err, ErrDepartmentNotFound) {
		t.Fatalf("expected ErrDepartmentNotFound, got %v", err)
	}
}

func assertIntSet(t *testing.T, got []int, want []int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("expected IDs %v, got %v", want, got)
	}
	seen := make(map[int]bool, len(got))
	for _, id := range got {
		seen[id] = true
	}
	for _, id := range want {
		if !seen[id] {
			t.Fatalf("expected IDs %v, got %v", want, got)
		}
	}
}
