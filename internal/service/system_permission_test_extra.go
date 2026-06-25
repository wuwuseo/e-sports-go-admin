package service

import (
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/systempermission"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func setupSystemPermissionTestClient(t *testing.T) *ent.Client {
	client := enttest.Open(t, "sqlite3", "file:sysp?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
	return client
}

func createSystemPermissionForTest(t *testing.T, client *ent.Client, typ, name, path, method string, parentID, sortOrder int, status int8) *ent.SystemPermission {
	t.Helper()
	b := client.SystemPermission.Create().
		SetType(typ).
		SetName(name).
		SetCode("code-" + name).
		SetParentID(parentID).
		SetSortOrder(sortOrder).
		SetStatus(status)
	if path != "" {
		b.SetPath(path)
	}
	if method != "" {
		b.SetMethod(method)
	}
	p, err := b.Save(t.Context())
	if err != nil {
		t.Fatalf("创建 system-permission 失败: %v", err)
	}
	return p
}

// -------------------------------------------------------------------
// 级联禁用/启用子权限
// -------------------------------------------------------------------

func TestCascadeDisableChildren_RecursivelyDisablesDescendants(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{}
	ctx := withPermissionSystemOperator(t.Context())

	root := createSystemPermissionForTest(t, client, "api", "root", "/root", "GET", 0, 0, 1)
	child1 := createSystemPermissionForTest(t, client, "api", "child-1", "/root/1", "GET", root.ID, 0, 1)
	child2 := createSystemPermissionForTest(t, client, "api", "child-2", "/root/2", "GET", root.ID, 0, 1)
	_ = createSystemPermissionForTest(t, client, "api", "grandchild", "/root/1/1", "GET", child1.ID, 0, 1)

	if err := svc.cascadeDisableChildren(ctx, root.ID); err != nil {
		t.Fatalf("级联禁用失败: %v", err)
	}

	for _, id := range []int{child1.ID, child2.ID} {
		p, err := client.SystemPermission.Get(ctx, id)
		if err != nil {
			t.Fatalf("获取子节点失败: %v", err)
		}
		if p.Status != 0 {
			t.Fatalf("节点 %s 应被禁用，status=%d", p.Name, p.Status)
		}
	}
}

func TestCascadeEnableChildren_RecursivelyEnablesDescendants(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{}
	ctx := withPermissionSystemOperator(t.Context())

	root := createSystemPermissionForTest(t, client, "api", "root", "/root", "GET", 0, 0, 0)
	child := createSystemPermissionForTest(t, client, "api", "child", "/root/c", "GET", root.ID, 0, 0)
	grand := createSystemPermissionForTest(t, client, "api", "grand", "/root/c/g", "GET", child.ID, 0, 0)

	if err := svc.cascadeEnableChildren(ctx, root.ID); err != nil {
		t.Fatalf("级联启用失败: %v", err)
	}

	for _, id := range []int{child.ID, grand.ID} {
		p, err := client.SystemPermission.Get(ctx, id)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if p.Status != 1 {
			t.Fatalf("%s 应被启用，status=%d", p.Name, p.Status)
		}
	}
}

// -------------------------------------------------------------------
// Update: 状态变更触发事务级级联
// -------------------------------------------------------------------

func TestSystemPermissionUpdate_DisableTriggersTransactionalCascade(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{}
	ctx := withPermissionSystemOperator(t.Context())

	root := createSystemPermissionForTest(t, client, "api", "root", "/root", "GET", 0, 0, 1)
	child := createSystemPermissionForTest(t, client, "api", "child", "/root/c", "GET", root.ID, 0, 1)
	grand := createSystemPermissionForTest(t, client, "api", "grand", "/root/c/g", "GET", child.ID, 0, 1)

	disable := int8(0)
	req := &UpdateSystemPermissionRequest{Status: &disable}
	if err := svc.Update(ctx, "", root.ID, req); err != nil {
		t.Fatalf("Update 失败: %v", err)
	}

	for _, id := range []int{root.ID, child.ID, grand.ID} {
		p, err := client.SystemPermission.Get(ctx, id)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if p.Status != 0 {
			t.Fatalf("%s 应被级联禁用，status=%d", p.Name, p.Status)
		}
	}
}

func TestSystemPermissionUpdate_EnableTriggersTransactionalCascade(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{}
	ctx := withPermissionSystemOperator(t.Context())

	root := createSystemPermissionForTest(t, client, "api", "root", "/root", "GET", 0, 0, 0)
	child := createSystemPermissionForTest(t, client, "api", "child", "/root/c", "GET", root.ID, 0, 0)
	grand := createSystemPermissionForTest(t, client, "api", "grand", "/root/c/g", "GET", child.ID, 0, 0)

	enable := int8(1)
	req := &UpdateSystemPermissionRequest{Status: &enable}
	if err := svc.Update(ctx, "", root.ID, req); err != nil {
		t.Fatalf("Update 失败: %v", err)
	}

	for _, id := range []int{root.ID, child.ID, grand.ID} {
		p, err := client.SystemPermission.Get(ctx, id)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if p.Status != 1 {
			t.Fatalf("%s 应被启用，status=%d", p.Name, p.Status)
		}
	}
}

// -------------------------------------------------------------------
// 父节点自环/环依赖：Update 应拒绝
// -------------------------------------------------------------------

func TestSystemPermissionUpdate_SelfParentRejected(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{}
	ctx := withPermissionSystemOperator(t.Context())

	root := createSystemPermissionForTest(t, client, "api", "root", "/root", "GET", 0, 0, 1)
	req := &UpdateSystemPermissionRequest{ParentID: &root.ID}
	err := svc.Update(ctx, "", root.ID, req)
	if err != ErrPermissionParentCycle {
		t.Fatalf("期望 ErrPermissionParentCycle，got %v", err)
	}
}

func TestSystemPermissionUpdate_DescendantParentRejected(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{}
	ctx := withPermissionSystemOperator(t.Context())

	root := createSystemPermissionForTest(t, client, "api", "root", "/root", "GET", 0, 0, 1)
	child := createSystemPermissionForTest(t, client, "api", "child", "/root/c", "GET", root.ID, 0, 1)
	grand := createSystemPermissionForTest(t, client, "api", "grand", "/root/c/g", "GET", child.ID, 0, 1)

	req := &UpdateSystemPermissionRequest{ParentID: &grand.ID}
	err := svc.Update(ctx, "", root.ID, req)
	if err != ErrPermissionParentCycle {
		t.Fatalf("期望 ErrPermissionParentCycle，got %v", err)
	}
}

// -------------------------------------------------------------------
// Create 重复代码检测
// -------------------------------------------------------------------

func TestSystemPermissionCreate_DuplicateCodeRejected(t *testing.T) {
	_ = setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{}
	ctx := withPermissionSystemOperator(t.Context())

	_, err := svc.Create(ctx, "", &CreateSystemPermissionRequest{
		Domain:   "default",
		Type:     "api",
		Name:     "first",
		Code:     "dup-code",
		Path:     "/a",
		Method:   "GET",
		ParentID: 0,
		Status:   1,
	})
	if err != nil {
		t.Fatalf("首次创建应成功，got %v", err)
	}
	_, err = svc.Create(ctx, "", &CreateSystemPermissionRequest{
		Domain:   "default",
		Type:     "api",
		Name:     "second",
		Code:     "dup-code",
		Path:     "/b",
		Method:   "GET",
		ParentID: 0,
		Status:   1,
	})
	if err == nil {
		t.Fatalf("重复 code 应被拒绝")
	}
}

// -------------------------------------------------------------------
// Delete: 子权限存在时拒绝
// -------------------------------------------------------------------

func TestSystemPermissionDelete_RootWithChildrenRejected(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{}
	ctx := withPermissionSystemOperator(t.Context())

	root := createSystemPermissionForTest(t, client, "api", "root", "/root", "GET", 0, 0, 1)
	_ = createSystemPermissionForTest(t, client, "api", "child", "/root/c", "GET", root.ID, 0, 1)

	err := svc.Delete(ctx, "", root.ID)
	if err == nil {
		t.Fatalf("有子权限的节点应无法删除")
	}
}

func TestSystemPermissionDelete_LeafDeletedSuccessfully(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{}
	ctx := withPermissionSystemOperator(t.Context())

	root := createSystemPermissionForTest(t, client, "api", "root", "/root", "GET", 0, 0, 1)
	child := createSystemPermissionForTest(t, client, "api", "child", "/root/c", "GET", root.ID, 0, 1)

	if err := svc.Delete(ctx, "", child.ID); err != nil {
		t.Fatalf("删除叶子节点失败: %v", err)
	}
	_, err := client.SystemPermission.Get(ctx, child.ID)
	if !ent.IsNotFound(err) {
		t.Fatalf("叶子节点应已删除，got err=%v", err)
	}
}

// -------------------------------------------------------------------
// InitSeedPermissions: 创建与幂等更新
// -------------------------------------------------------------------

func TestInitSeedPermissions_CreatesInitialSeeds(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{}
	ctx := withPermissionSystemOperator(t.Context())

	seeds := []SeedPermission{
		{ID: 1001, Domain: "default", Type: "api", Name: "seed-api", Code: "seed-api-1", Path: "/seed/api", Method: "GET", ParentID: 0, SortOrder: 1, Status: 1},
		{ID: 1002, Domain: "default", Type: "menu", Name: "seed-menu", Code: "seed-menu-1", Path: "/seed/menu", ParentID: 1001, SortOrder: 2, Status: 1},
	}
	if err := svc.InitSeedPermissions(ctx, seeds); err != nil {
		t.Fatalf("InitSeedPermissions 失败: %v", err)
	}

	p, err := client.SystemPermission.Query().Where(systempermission.CodeEQ("seed-api-1")).Only(ctx)
	if err != nil {
		t.Fatalf("未找到种子: %v", err)
	}
	child, err := client.SystemPermission.Query().Where(systempermission.CodeEQ("seed-menu-1")).Only(ctx)
	if err != nil {
		t.Fatalf("未找到种子菜单: %v", err)
	}
	if child.ParentID != p.ID {
		t.Fatalf("父子关系未正确解析，child.ParentID=%d parent.ID=%d", child.ParentID, p.ID)
	}
}

func TestInitSeedPermissions_IdempotentUpdate(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{}
	ctx := withPermissionSystemOperator(t.Context())

	seeds := []SeedPermission{
		{ID: 2001, Domain: "default", Type: "api", Name: "old", Code: "seed-2001", Path: "/old", Method: "GET", ParentID: 0, SortOrder: 1, Status: 1, Description: "first"},
	}
	if err := svc.InitSeedPermissions(ctx, seeds); err != nil {
		t.Fatalf("首次 init 失败: %v", err)
	}

	seeds2 := []SeedPermission{
		{ID: 2001, Domain: "default", Type: "api", Name: "new", Code: "seed-2001", Path: "/new", Method: "POST", ParentID: 0, SortOrder: 5, Status: 0, Description: "updated"},
	}
	if err := svc.InitSeedPermissions(ctx, seeds2); err != nil {
		t.Fatalf("第二次 init 失败: %v", err)
	}

	count, err := client.SystemPermission.Query().Where(systempermission.CodeEQ("seed-2001")).Count(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("应保持 1 条记录，got %d", count)
	}
	updated, err := client.SystemPermission.Query().Where(systempermission.CodeEQ("seed-2001")).Only(ctx)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if updated.Name != "new" || updated.Path != "/new" || updated.Method != "POST" || updated.Status != 0 || updated.Description != "updated" {
		t.Fatalf("字段未被更新: %+v", updated)
	}
}

// -------------------------------------------------------------------
// List: 关键字过滤
// -------------------------------------------------------------------

func TestSystemPermissionList_KeywordFilterAndPagination(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{}
	ctx := withPermissionSystemOperator(t.Context())

	for _, n := range []string{"alpha", "beta", "gamma"} {
		_ = createSystemPermissionForTest(t, client, "api", n, "/"+n, "GET", 0, 0, 1)
	}

	req := &ListSystemPermissionsRequest{}
	req.Keyword = "alpha"
	req.Page = 1
	req.PageSize = 10
	resp, err := svc.List(ctx, "", req)
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("期望匹配 1 条，got total=%d", resp.Total)
	}
	if len(resp.Data) != 1 || resp.Data[0].Name != "alpha" {
		t.Fatalf("关键字过滤不正确，got %+v", resp.Data)
	}
}

// -------------------------------------------------------------------
// 工具函数: attachmentAccessSubject
// -------------------------------------------------------------------

func TestAttachmentAccessSubject_UsesIDInPath(t *testing.T) {
	got := attachmentAccessSubject(42)
	want := "attachment:42"
	if got != want {
		t.Fatalf("期望 %q，got %q", want, got)
	}
}