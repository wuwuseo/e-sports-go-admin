package service

import (
	"encoding/json"
	"testing"

	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/role"
	"go-admin/internal/global"

	cmfCache "github.com/wuwuseo/cmf/cache"
	cmfConfig "github.com/wuwuseo/cmf/config"
)

// newDataScopeCache 构造内存缓存实例，用于数据范围缓存测试
func newDataScopeCache(t *testing.T) *cmfCache.Cache[[]byte] {
	t.Helper()
	cfg := &cmfConfig.Config{}
	cfg.Cache.Default = "memory"
	cfg.Cache.Stores = map[string]struct {
		Driver     string `mapstructure:"driver"`
		DefaultTTL int    `mapstructure:"default_ttl"`
		Options    any    `mapstructure:"options"`
	}{
		"memory": {Driver: "memory", DefaultTTL: 3600},
	}
	return cmfCache.NewCache(t.Context(), cfg)
}

// createDataScopeFixture 创建管理员+角色绑定，返回 (svc, 管理员UID)
func createDataScopeFixture(t *testing.T, dataScope string, departmentID int, cacheInst *cmfCache.Cache[[]byte]) (*DataPermissionService, string) {
	t.Helper()
	client, _, cleanup := setupSuperAdminTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	r := client.Role.Create().
		SetName("scope-role").
		SetDisplayName("scope role").
		SetStatus(role.StatusActive).
		SetDataScope(role.DataScope(dataScope)).
		SaveX(ctx)
	a := client.Admin.Create().
		SetEmail("scope-admin@test.com").
		SetUID("scope-admin-uid").
		SetUsername("scopeadmin").
		SetPassword("pass").
		SetPasswordSalt("salt").
		SetStatus(admin.StatusActive).
		SetDepartmentID(departmentID).
		SaveX(ctx)
	client.AdminRole.Create().
		SetAdminID(a.ID).
		SetRoleID(r.ID).
		SetIsPrimary(true).
		SaveX(ctx)

	deptSvc := NewDepartmentService()
	if cacheInst != nil {
		return NewDataPermissionService(deptSvc, cacheInst), a.UID
	}
	return NewDataPermissionService(deptSvc), a.UID
}

// P1-1 回归：缓存命中返回浅拷贝，调用方写入不污染缓存
func TestResolveAdminScope_CacheHitReturnsCopy(t *testing.T) {
	cacheInst := newDataScopeCache(t)
	svc, uid := createDataScopeFixture(t, string(DataScopeSelf), 0, cacheInst)
	ctx := t.Context()

	first, err := svc.ResolveAdminScope(ctx, uid)
	if err != nil {
		t.Fatalf("首次解析失败: %v", err)
	}
	if first.Scope != DataScopeSelf {
		t.Fatalf("期望 Self 范围, got %s", first.Scope)
	}
	// 调用方写 Domain（NewOperatorContext 的行为）不得污染缓存
	first.Domain = "polluted"
	second, err := svc.ResolveAdminScope(ctx, uid)
	if err != nil {
		t.Fatalf("缓存命中解析失败: %v", err)
	}
	if second.Domain == "polluted" {
		t.Fatal("缓存被调用方写入的 Domain 污染，浅拷贝缺失")
	}
	if first == second {
		t.Fatal("两次调用必须返回独立指针")
	}
}

// P1-1 回归：Department 范围禁用角色后缓存失效，范围立即收窄
func TestResolveAdminScope_InvalidateAfterRoleDisable(t *testing.T) {
	cacheInst := newDataScopeCache(t)
	svc, uid := createDataScopeFixture(t, string(DataScopeDepartment), 7, cacheInst)
	ctx := t.Context()

	before, err := svc.ResolveAdminScope(ctx, uid)
	if err != nil {
		t.Fatalf("首次解析失败: %v", err)
	}
	if before.Scope != DataScopeDepartment || len(before.DepartmentIDs) != 1 || before.DepartmentIDs[0] != 7 {
		t.Fatalf("期望 Department(7), got scope=%s ids=%v", before.Scope, before.DepartmentIDs)
	}

	// 禁用角色（角色启停是 casbin 同步点，clearUserPermissionsByRoleID 会顺带清 datascope 键；
	// 这里直接验证 InvalidateAdminScope 语义）
	client := global.ORM
	r := client.Role.Query().Where(role.NameEQ("scope-role")).OnlyX(ctx)
	client.Role.UpdateOne(r).SetStatus(role.StatusInactive).SaveX(ctx)
	svc.InvalidateAdminScope(ctx, uid)

	after, err := svc.ResolveAdminScope(ctx, uid)
	if err != nil {
		t.Fatalf("失效后重查失败: %v", err)
	}
	if after.Scope != DataScopeSelf {
		t.Fatalf("角色禁用后应回退 Self, got %s", after.Scope)
	}
}

// P1-1 回归：nil cache（未注入）时每次实时查库
func TestResolveAdminScope_NilCacheAlwaysQueries(t *testing.T) {
	svc, uid := createDataScopeFixture(t, string(DataScopeDepartment), 3, nil)
	ctx := t.Context()

	first, err := svc.ResolveAdminScope(ctx, uid)
	if err != nil {
		t.Fatalf("首次解析失败: %v", err)
	}
	client := global.ORM
	r := client.Role.Query().Where(role.NameEQ("scope-role")).OnlyX(ctx)
	client.Role.UpdateOne(r).SetStatus(role.StatusInactive).SaveX(ctx)

	second, err := svc.ResolveAdminScope(ctx, uid)
	if err != nil {
		t.Fatalf("二次解析失败: %v", err)
	}
	if second.Scope == first.Scope {
		t.Fatalf("无缓存时应实时反映角色禁用, got %s", second.Scope)
	}
}

// P1-3 回归：Department 模式下自己不在可见部门时，列表仍包含自己（与详情一致）
func TestListAdmins_DepartmentScopeIncludesSelf(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	// 操作者：Department 模式角色，但自己挂在部门 9（角色 Custom 范围只有部门 1），
	// 模拟"自己部门不在自己可见范围内"
	r := client.Role.Create().
		SetName("dept-scope-role").
		SetDisplayName("dept scope role").
		SetStatus(role.StatusActive).
		SetDataScope(role.DataScopeCustom).
		SetDataScopeDepartmentIds([]int{1}).
		SaveX(ctx)
	self := client.Admin.Create().
		SetEmail("self@test.com").
		SetUID("self-uid").
		SetUsername("selfuser").
		SetPassword("pass").
		SetPasswordSalt("salt").
		SetStatus(admin.StatusActive).
		SetDepartmentID(9).
		SaveX(ctx)
	client.AdminRole.Create().
		SetAdminID(self.ID).
		SetRoleID(r.ID).
		SetIsPrimary(true).
		SaveX(ctx)
	// 部门 1 下的另一个管理员（应在列表内）
	peer := client.Admin.Create().
		SetEmail("peer@test.com").
		SetUID("peer-uid").
		SetUsername("peeruser").
		SetPassword("pass").
		SetPasswordSalt("salt").
		SetStatus(admin.StatusActive).
		SetDepartmentID(1).
		SaveX(ctx)
	_ = peer
	// 部门 2 下的无关管理员（不应在列表内）
	client.Admin.Create().
		SetEmail("outsider@test.com").
		SetUID("outsider-uid").
		SetUsername("outsideruser").
		SetPassword("pass").
		SetPasswordSalt("salt").
		SetStatus(admin.StatusActive).
		SetDepartmentID(2).
		SaveX(ctx)

	svc := NewSuperAdminService(NewDataPermissionService(NewDepartmentService()), NewDepartmentService())
	resp, err := svc.ListAdmins(ctx, &AdminListRequest{Page: 1, PageSize: 50, CurrentUID: self.UID})
	if err != nil {
		t.Fatalf("ListAdmins 失败: %v", err)
	}

	got := map[string]bool{}
	for _, item := range resp.Data {
		got[item.UID] = true
	}
	if !got[self.UID] {
		t.Fatal("Department/Custom 模式下列表必须包含自己（与详情可见性一致）")
	}
	if !got[peer.UID] {
		t.Fatal("可见部门 1 内的 peer 应在列表中")
	}
	if got["outsider-uid"] {
		t.Fatal("范围外部门的管理员不应出现在列表中")
	}
}

// P1-1 补充回归：部门树新增节点后整体换键失效，DeptAndChild 范围立即扩大
func TestResolveAdminScope_DeptTreeChangeInvalidatesCache(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	deptSvc := NewDepartmentService()
	svc := NewDataPermissionService(deptSvc, newDataScopeCache(t))

	// 建父部门 A（op=nil 放行顶级部门创建）
	deptA, err := deptSvc.Create(ctx, nil, &CreateDepartmentRequest{Name: "dept-a"})
	if err != nil {
		t.Fatalf("创建父部门失败: %v", err)
	}

	r := client.Role.Create().
		SetName("dac-role").
		SetDisplayName("dac role").
		SetStatus(role.StatusActive).
		SetDataScope(role.DataScopeDepartmentAndChild).
		SaveX(ctx)
	a := client.Admin.Create().
		SetEmail("dac-admin@test.com").
		SetUID("dac-admin-uid").
		SetUsername("dacadmin").
		SetPassword("pass").
		SetPasswordSalt("salt").
		SetStatus(admin.StatusActive).
		SetDepartmentID(deptA.ID).
		SaveX(ctx)
	client.AdminRole.Create().
		SetAdminID(a.ID).
		SetRoleID(r.ID).
		SetIsPrimary(true).
		SaveX(ctx)

	first, err := svc.ResolveAdminScope(ctx, a.UID)
	if err != nil {
		t.Fatalf("首次解析失败: %v", err)
	}
	if len(first.DepartmentIDs) != 1 || first.DepartmentIDs[0] != deptA.ID {
		t.Fatalf("期望仅部门 %d, got %v", deptA.ID, first.DepartmentIDs)
	}

	// 新增子部门 B：Create 挂钩子整体换键，缓存应立即反映扩大的子树
	deptB, err := deptSvc.Create(ctx, nil, &CreateDepartmentRequest{Name: "dept-b", ParentID: deptA.ID})
	if err != nil {
		t.Fatalf("创建子部门失败: %v", err)
	}

	second, err := svc.ResolveAdminScope(ctx, a.UID)
	if err != nil {
		t.Fatalf("部门树变更后解析失败: %v", err)
	}
	if !containsInt(second.DepartmentIDs, deptB.ID) {
		t.Fatalf("部门树新增子部门后应立即扩大范围, got %v", second.DepartmentIDs)
	}
}

// P2 回归：缓存 JSON 反序列化后 departmentSet 丢失，containsDepartment 应自动重建
func TestEffectiveDataScope_ContainsDepartmentAfterUnmarshal(t *testing.T) {
	data, err := json.Marshal(&EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1, 2, 3}})
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var scope EffectiveDataScope
	if err := json.Unmarshal(data, &scope); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	if scope.departmentSet != nil {
		t.Fatal("非导出字段不应参与序列化")
	}
	if !scope.containsDepartment(2) {
		t.Fatal("惰性构建 set 后应命中")
	}
	if !scope.containsDepartment(1) {
		t.Fatal("二次调用应复用已构建 set")
	}
	if scope.containsDepartment(9) {
		t.Fatal("未命中部门应返回 false")
	}
}

// P1-2 回归：同部门多持有者去重后，越权结论不变（fail-closed 保持）
func TestEnsureRoleManageableForAssignedAdmins_SameDeptHolders(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	// 角色：Custom 范围部门 5
	r := client.Role.Create().
		SetName("custom-role").
		SetDisplayName("custom role").
		SetStatus(role.StatusActive).
		SetDataScope(role.DataScopeCustom).
		SetDataScopeDepartmentIds([]int{5}).
		SaveX(ctx)
	// 两个同部门（部门 3）持有者
	for i, uid := range []string{"holder-a", "holder-b"} {
		a := client.Admin.Create().
			SetEmail(uid + "@test.com").
			SetUID(uid).
			SetUsername(uid).
			SetPassword("pass").
			SetPasswordSalt("salt").
			SetStatus(admin.StatusActive).
			SetDepartmentID(3).
			SaveX(ctx)
		client.AdminRole.Create().
			SetAdminID(a.ID).
			SetRoleID(r.ID).
			SetIsPrimary(i == 0).
			SaveX(ctx)
	}

	// 操作者范围只覆盖部门 3：持有者部门 3 虽在范围内，但角色 Custom(5) 超出 op 范围 -> 必须拒绝
	op := &EffectiveDataScope{
		Scope:         DataScopeCustom,
		AdminUID:      "op-uid",
		DepartmentIDs: []int{3},
	}
	deptSvc := NewDepartmentService()
	if err := ensureRoleManageableForAssignedAdmins(ctx, client, deptSvc, op, r, DataScopeCustom, []int{5}, true); err == nil {
		t.Fatal("角色 Custom(5) 超出 op 范围(3)，应拒绝")
	}

	// 操作者范围覆盖部门 3、5：同部门持有者去重后应放行
	opWide := &EffectiveDataScope{
		Scope:         DataScopeCustom,
		AdminUID:      "op-uid",
		DepartmentIDs: []int{3, 5},
	}
	if err := ensureRoleManageableForAssignedAdmins(ctx, client, deptSvc, opWide, r, DataScopeCustom, []int{5}, true); err != nil {
		t.Fatalf("范围内更新应放行, got %v", err)
	}
}
