package service

import (
	"fmt"
	"testing"

	"go-admin/internal/ent/systempermission"
)

// -------------------------------------------------------------------
// cascadeSetChildrenStatus boundary gap tests
// covers refactor commit 24c96e9 shared recursive helper
// -------------------------------------------------------------------

func TestCascadeSetChildrenStatus_MixedStatusOnlyTouchesMatchingFromStatus(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{client: client}
	ctx := withPermissionSystemOperator(t.Context())

	root := createSystemPermissionForTest(t, client, "api", "root", "/root", "GET", 0, 0, 1)
	childA := createSystemPermissionForTest(t, client, "api", "child-a", "/root/a", "GET", root.ID, 0, 1)
	childB := createSystemPermissionForTest(t, client, "api", "child-b", "/root/b", "GET", root.ID, 0, 0)
	grandA := createSystemPermissionForTest(t, client, "api", "grand-a", "/root/a/g", "GET", childA.ID, 0, 1)

	if err := svc.cascadeSetChildrenStatus(ctx, client.SystemPermission, root.ID, 1, 0); err != nil {
		t.Fatalf("cascadeSetChildrenStatus error = %v", err)
	}

	checks := []struct {
		id       int
		name     string
		wantStat int8
	}{
		{childA.ID, "child-a", 0},
		{childB.ID, "child-b", 0},
		{grandA.ID, "grand-a", 0},
	}
	for _, c := range checks {
		p, err := client.SystemPermission.Get(ctx, c.id)
		if err != nil {
			t.Fatalf("get %s error = %v", c.name, err)
		}
		if p.Status != c.wantStat {
			t.Fatalf("%s status = %d, want %d", c.name, p.Status, c.wantStat)
		}
	}
}

func TestCascadeSetChildrenStatus_DeepTreeRecursesAllLevels(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{client: client}
	ctx := withPermissionSystemOperator(t.Context())

	l1 := createSystemPermissionForTest(t, client, "menu", "l1", "", "", 0, 0, 0)
	l2 := createSystemPermissionForTest(t, client, "menu", "l2", "", "", l1.ID, 0, 0)
	l3 := createSystemPermissionForTest(t, client, "menu", "l3", "", "", l2.ID, 0, 0)
	l4 := createSystemPermissionForTest(t, client, "menu", "l4", "", "", l3.ID, 0, 0)
	l5 := createSystemPermissionForTest(t, client, "api", "l5", "/deep", "GET", l4.ID, 0, 0)

	if err := svc.cascadeSetChildrenStatus(ctx, client.SystemPermission, l1.ID, 0, 1); err != nil {
		t.Fatalf("cascadeSetChildrenStatus error = %v", err)
	}

	for _, p := range []struct {
		id   int
		name string
	}{
		{l2.ID, "l2"}, {l3.ID, "l3"}, {l4.ID, "l4"}, {l5.ID, "l5"},
	} {
		got, err := client.SystemPermission.Get(ctx, p.id)
		if err != nil {
			t.Fatalf("get %s error = %v", p.name, err)
		}
		if got.Status != 1 {
			t.Fatalf("%s status = %d, want 1 (recursion did not reach deep level)", p.name, got.Status)
		}
	}
}

func TestCascadeSetChildrenStatus_LeafNoChildrenIsNoOp(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{client: client}
	ctx := withPermissionSystemOperator(t.Context())

	leaf := createSystemPermissionForTest(t, client, "api", "leaf", "/leaf", "GET", 0, 0, 1)

	if err := svc.cascadeSetChildrenStatus(ctx, client.SystemPermission, leaf.ID, 1, 0); err != nil {
		t.Fatalf("leaf node call should not error, got %v", err)
	}
	got, err := client.SystemPermission.Get(ctx, leaf.ID)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if got.Status != 1 {
		t.Fatalf("cascade helper must not modify parent itself, status = %d", got.Status)
	}
}

func TestCascadeSetChildrenStatus_SiblingBranchesIndependent(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{client: client}
	ctx := withPermissionSystemOperator(t.Context())

	rootA := createSystemPermissionForTest(t, client, "menu", "rootA", "", "", 0, 0, 1)
	rootB := createSystemPermissionForTest(t, client, "menu", "rootB", "", "", 0, 0, 1)
	childA := createSystemPermissionForTest(t, client, "api", "childA", "/a", "GET", rootA.ID, 0, 1)
	childB := createSystemPermissionForTest(t, client, "api", "childB", "/b", "GET", rootB.ID, 0, 1)

	if err := svc.cascadeSetChildrenStatus(ctx, client.SystemPermission, rootA.ID, 1, 0); err != nil {
		t.Fatalf("%v", err)
	}

	a, _ := client.SystemPermission.Get(ctx, childA.ID)
	b, _ := client.SystemPermission.Get(ctx, childB.ID)
	if a.Status != 0 {
		t.Fatalf("childA should be disabled, status=%d", a.Status)
	}
	if b.Status != 1 {
		t.Fatalf("childB belongs to another root, should be unaffected, status=%d", b.Status)
	}
}

func TestCascadeDisableChildrenTx_WorksWithTransactionClient(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{client: client}
	ctx := withPermissionSystemOperator(t.Context())

	root := createSystemPermissionForTest(t, client, "api", "root", "/r", "GET", 0, 0, 1)
	child := createSystemPermissionForTest(t, client, "api", "child", "/r/c", "GET", root.ID, 0, 1)

	tx, err := client.Tx(ctx)
	if err != nil {
		t.Fatalf("%v", err)
	}

	if err := svc.cascadeDisableChildrenTx(ctx, tx, root.ID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("%v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("%v", err)
	}

	c, err := client.SystemPermission.Get(ctx, child.ID)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if c.Status != 0 {
		t.Fatalf("transactional cascade disable did not apply, status=%d", c.Status)
	}
}

func TestCascadeEnableChildrenTx_WorksWithTransactionClient(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{client: client}
	ctx := withPermissionSystemOperator(t.Context())

	root := createSystemPermissionForTest(t, client, "api", "root", "/r", "GET", 0, 0, 0)
	child := createSystemPermissionForTest(t, client, "api", "child", "/r/c", "GET", root.ID, 0, 0)

	tx, err := client.Tx(ctx)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if err := svc.cascadeEnableChildrenTx(ctx, tx, root.ID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("%v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("%v", err)
	}

	c, err := client.SystemPermission.Get(ctx, child.ID)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if c.Status != 1 {
		t.Fatalf("transactional cascade enable did not apply, status=%d", c.Status)
	}
}

func TestSystemPermissionUpdate_NoStatusChangeDoesNotCascade(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{client: client}
	ctx := withPermissionSystemOperator(t.Context())

	root := createSystemPermissionForTest(t, client, "api", "root", "/r", "GET", 0, 0, 1)
	child := createSystemPermissionForTest(t, client, "api", "child", "/r/c", "GET", root.ID, 0, 0)

	newName := "renamed-root"
	if err := svc.Update(ctx, "", root.ID, &UpdateSystemPermissionRequest{Name: newName}); err != nil {
		t.Fatalf("%v", err)
	}

	r, _ := client.SystemPermission.Get(ctx, root.ID)
	c, _ := client.SystemPermission.Get(ctx, child.ID)
	if r.Name != newName {
		t.Fatalf("name not updated: %s", r.Name)
	}
	if c.Status != 0 {
		t.Fatalf("without status change cascade must not run, child status=%d", c.Status)
	}
}

func TestSystemPermissionUpdate_SameStatusNoCascadeSideEffects(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{client: client}
	ctx := withPermissionSystemOperator(t.Context())

	root := createSystemPermissionForTest(t, client, "api", "root", "/r", "GET", 0, 0, 1)
	enabledChild := createSystemPermissionForTest(t, client, "api", "e", "/r/e", "GET", root.ID, 0, 1)
	disabledChild := createSystemPermissionForTest(t, client, "api", "d", "/r/d", "GET", root.ID, 0, 0)

	same := int8(1)
	if err := svc.Update(ctx, "", root.ID, &UpdateSystemPermissionRequest{Status: &same}); err != nil {
		t.Fatalf("%v", err)
	}

	d, _ := client.SystemPermission.Get(ctx, disabledChild.ID)
	e, _ := client.SystemPermission.Get(ctx, enabledChild.ID)
	if d.Status != 0 {
		t.Fatalf("same status must not cascade-enable disabledChild, status=%d", d.Status)
	}
	if e.Status != 1 {
		t.Fatalf("enabledChild status unexpectedly modified, status=%d", e.Status)
	}
}

func TestCascadeSetChildrenStatus_ReverseTransitionIdempotent(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{client: client}
	ctx := withPermissionSystemOperator(t.Context())

	root := createSystemPermissionForTest(t, client, "menu", "root-rev", "", "", 0, 0, 1)
	ids := make([]int, 0, 4)
	cur := root
	for i := 0; i < 4; i++ {
		code := fmt.Sprintf("rev-n-%d-%d", root.ID, i)
		c := createSystemPermissionForTest(t, client, "api", code, "", "GET", cur.ID, 0, 1)
		ids = append(ids, c.ID)
		cur = c
	}

	if err := svc.cascadeSetChildrenStatus(ctx, client.SystemPermission, root.ID, 1, 0); err != nil {
		t.Fatalf("%v", err)
	}
	if err := svc.cascadeSetChildrenStatus(ctx, client.SystemPermission, root.ID, 0, 1); err != nil {
		t.Fatalf("%v", err)
	}

	for _, id := range ids {
		p, err := client.SystemPermission.Get(ctx, id)
		if err != nil {
			t.Fatalf("%v", err)
		}
		if p.Status != 1 {
			t.Fatalf("after reverse transition id=%d status=%d, want 1", id, p.Status)
		}
	}
}

func TestSystemPermission_ListStatusSentinelZeroMeansNoFilter(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{client: client}
	ctx := withPermissionSystemOperator(t.Context())

	on := createSystemPermissionForTest(t, client, "api", "on-list", "/list/on", "GET", 0, 0, 1)
	off := createSystemPermissionForTest(t, client, "api", "off-list", "/list/off", "GET", 0, 0, 0)

	resp, err := svc.List(ctx, "", &ListSystemPermissionsRequest{Status: 0})
	if err != nil {
		t.Fatalf("%v", err)
	}
	// ponytail: Status=0 是"不过滤"的哨兵值（与 Domain/Type/Keyword 的零值语义一致）。
	// 如果需要按 status=0 过滤，应该改为指针字段或使用枚举值。
	// 这里锁定当前行为：Status=0 返回 on 和 off 两者。
	foundOn := false
	foundOff := false
	for _, p := range resp.Data {
		if p.ID == on.ID {
			foundOn = true
		}
		if p.ID == off.ID {
			foundOff = true
		}
	}
	if !foundOn {
		t.Fatalf("Status=0 (no-filter) should include enabled item on-list, total=%d", resp.Total)
	}
	if !foundOff {
		t.Fatalf("Status=0 (no-filter) should include disabled item off-list, total=%d", resp.Total)
	}
}

func TestSystemPermission_ListStatusOneFiltersOnlyEnabled(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{client: client}
	ctx := withPermissionSystemOperator(t.Context())

	on := createSystemPermissionForTest(t, client, "api", "on-filt", "/filt/on", "GET", 0, 0, 1)
	_ = createSystemPermissionForTest(t, client, "api", "off-filt", "/filt/off", "GET", 0, 0, 0)

	resp, err := svc.List(ctx, "", &ListSystemPermissionsRequest{Status: 1})
	if err != nil {
		t.Fatalf("%v", err)
	}
	for _, p := range resp.Data {
		if p.Status != 1 {
			t.Fatalf("Status=1 filter returned item with status=%d (id=%d)", p.Status, p.ID)
		}
	}
	found := false
	for _, p := range resp.Data {
		if p.ID == on.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("enabled item not in Status=1 result, total=%d", resp.Total)
	}
}

// keep import used (avoid compile warning)
var _ = systempermission.ParentIDEQ
