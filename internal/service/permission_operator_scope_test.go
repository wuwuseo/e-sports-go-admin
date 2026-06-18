package service

import (
	"context"
	"errors"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/casbinrule"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/role"

	_ "github.com/mattn/go-sqlite3"
)

// setupOperatorScopeTestClient 为 operator scope 测试准备 SQLite 内存客户端
func setupOperatorScopeTestClient(t *testing.T) *ent.Client {
	client := enttest.Open(t, "sqlite3", "file:opscope?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	return client
}

func createOperatorScopeRole(t *testing.T, client *ent.Client, name string, status role.Status) *ent.Role {
	t.Helper()
	r, err := client.Role.Create().
		SetName(name).
		SetDisplayName(name).
		SetStatus(status).
		SetDataScope(role.DataScopeAll).
		Save(t.Context())
	if err != nil {
		t.Fatalf("create role: %v", err)
	}
	return r
}

func createOperatorScopeAdmin(t *testing.T, client *ent.Client, name, uid string, status admin.Status) *ent.Admin {
	t.Helper()
	a, err := client.Admin.Create().
		SetName(name).
		SetUID(uid).
		SetUsername(uid).
		SetEmail(uid + "@example.com").
		SetPassword("hash").
		SetPasswordSalt("salt").
		SetStatus(status).
		Save(t.Context())
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	return a
}

// -------------------------------------------------------------------
// 纯函数：scopeRank / mergeScope
// -------------------------------------------------------------------

func TestScopeRank_AllScopesOrderedByWidth(t *testing.T) {
	cases := []struct {
		scope DataScope
		want  int
	}{
		{DataScopeSelf, 1},
		{DataScopeDepartment, 2},
		{DataScopeDepartmentAndChild, 3},
		{DataScopeCustom, 4},
		{DataScopeAll, 5},
	}
	for _, c := range cases {
		if got := scopeRank(c.scope); got != c.want {
			t.Fatalf("scopeRank(%q) = %d, want %d", c.scope, got, c.want)
		}
	}
}

func TestScopeRank_UnknownScopeReturnsZeroFailClosed(t *testing.T) {
	if got := scopeRank(DataScope("made_up_scope")); got != 0 {
		t.Fatalf("未知 scope 应返回 0 以确保 fail-closed，got %d", got)
	}
}

func TestMergeScope_WidensTowardsBroaderScope(t *testing.T) {
	// DataScopeDepartment 与 DataScopeCustom 合并应取 Custom（rank 更高）
	merged := mergeScope(DataScopeDepartment, DataScopeCustom)
	if merged != DataScopeCustom {
		t.Fatalf("期望合并后为 Custom，got %q", merged)
	}

	// Self 与 Department 合并应返回 Department
	merged = mergeScope(DataScopeSelf, DataScopeDepartment)
	if merged != DataScopeDepartment {
		t.Fatalf("期望合并后为 Department，got %q", merged)
	}

	// next 范围比 current 窄时保留 current
	merged = mergeScope(DataScopeAll, DataScopeSelf)
	if merged != DataScopeAll {
		t.Fatalf("All 不应被 Self 覆盖，got %q", merged)
	}
}

func TestMergeScope_EqualScopeIdempotent(t *testing.T) {
	if got := mergeScope(DataScopeDepartment, DataScopeDepartment); got != DataScopeDepartment {
		t.Fatalf("合并相同 scope 应等幂，got %q", got)
	}
}

// -------------------------------------------------------------------
// 纯函数：appendUniqueInts
// -------------------------------------------------------------------

func TestAppendUniqueInts_NoDuplicatesPreservesOrder(t *testing.T) {
	base := []int{1, 2, 3}
	got := appendUniqueInts(base, 4, 5)
	want := []int{1, 2, 3, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("长度不匹配: got=%v want=%v", got, want)
	}
	for i, v := range want {
		if got[i] != v {
			t.Fatalf("索引 %d: got %d want %d", i, got[i], v)
		}
	}
}

func TestAppendUniqueInts_SkipsDuplicatesAndNegativeValues(t *testing.T) {
	got := appendUniqueInts([]int{1, 2}, 2, 3, -1, 0, 3)
	want := []int{1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("长度不匹配: got=%v want=%v", got, want)
	}
	seen := map[int]bool{}
	for _, v := range got {
		if seen[v] {
			t.Fatalf("存在重复值 %d", v)
		}
		seen[v] = true
	}
	for _, v := range want {
		if !seen[v] {
			t.Fatalf("缺少期望元素 %d", v)
		}
	}
}

func TestAppendUniqueInts_NilBaseAcceptable(t *testing.T) {
	got := appendUniqueInts(nil, 5, 6)
	if len(got) != 2 || got[0] != 5 || got[1] != 6 {
		t.Fatalf("nil base 时应仍可追加，got %v", got)
	}
}

// -------------------------------------------------------------------
// 纯函数：permissionLookupKey / permissionLookupKeys
// -------------------------------------------------------------------

func TestPermissionLookupKey_BuildsColonSeparatedKey(t *testing.T) {
	key := permissionLookupKey("api", "/users", "GET")
	want := "api\x00/users\x00GET"
	if key != want {
		t.Fatalf("期望 key=%q，got %q", want, key)
	}
}

func TestPermissionLookupKeys_APIUppercasesAction(t *testing.T) {
	keys := permissionLookupKeys("api", "/users", "get")
	// 至少同时包含小写与大写
	hasLower := false
	hasUpper := false
	for _, k := range keys {
		if k == permissionLookupKey("api", "/users", "get") {
			hasLower = true
		}
		if k == permissionLookupKey("api", "/users", "GET") {
			hasUpper = true
		}
	}
	if !hasLower || !hasUpper {
		t.Fatalf("API 类型应对 action 大小写都产生 key，got %v", keys)
	}
}

func TestPermissionLookupKeys_MenuButtonExpandsVariants(t *testing.T) {
	menuKeys := permissionLookupKeys("menu", "/dashboard", "menu")
	if len(menuKeys) < 2 {
		t.Fatalf("menu 应有多个 key 变体，got %v", menuKeys)
	}
	btnKeys := permissionLookupKeys("button", "/dashboard", "button")
	if len(btnKeys) < 2 {
		t.Fatalf("button 应有多个 key 变体，got %v", btnKeys)
	}
}

// -------------------------------------------------------------------
// operator scope 防御：allowsRule / 权限范围控制
// -------------------------------------------------------------------

func TestOperatorPermissionScope_IncludeAllAllowsAnyPermission(t *testing.T) {
	scope := &operatorPermissionScope{includeAll: true}
	if !scope.allowsRule(PermissionRule{Object: "/anything", Action: "DELETE"}) {
		t.Fatalf("includeAll=true 应对任何规则放行")
	}
}

func TestOperatorPermissionScope_NilScopeTreatedAsAll(t *testing.T) {
	// nil 或 includeAll 都应放行，以避免崩溃期间接放行
	if !((*operatorPermissionScope)(nil)).allowsRule(PermissionRule{Object: "/x", Action: "GET"}) {
		t.Fatalf("nil scope 应对规则放行以与现有用法一致")
	}
}

func TestOperatorPermissionScope_LookupMatches(t *testing.T) {
	lookup := map[string]struct{}{
		permissionLookupKey("api", "/users", "GET"): {},
	}
	scope := &operatorPermissionScope{lookup: lookup}
	if !scope.allowsRule(PermissionRule{Object: "/users", Action: "GET"}) {
		t.Fatalf("命中 lookup 的规则应放行")
	}
	if scope.allowsRule(PermissionRule{Object: "/users", Action: "DELETE"}) {
		t.Fatalf("未命中 lookup 的规则应被拒绝")
	}
}

// -------------------------------------------------------------------
// buildPermissionLookup / matchSystemPermission
// -------------------------------------------------------------------

func TestBuildPermissionLookup_ProducesSetMembershipMap(t *testing.T) {
	rules := []*PermissionResponse{
		{Type: "api", Object: "/users", Action: "GET"},
		{Type: "menu", Object: "/dashboard", Action: "MENU"},
	}
	lookup := buildPermissionLookup(rules)
	if len(lookup) == 0 {
		t.Fatalf("期望生成非空 lookup")
	}
	// 对 API 类型确保大小写都能命中
	if _, ok := lookup[permissionLookupKey("api", "/users", "GET")]; !ok {
		t.Fatalf("lookup 应包含 API 规则")
	}
}

func TestMatchSystemPermission_APIMatchesByObjectAction(t *testing.T) {
	// 直接使用 permission 服务内部的 matchSystemPermission 语义
	// 该函数依赖 buildPermissionLookup 产生的 key
	lookup := buildPermissionLookup([]*PermissionResponse{
		{Type: "api", Object: "/secret", Action: "GET"},
	})

	// 构造一个 SystemPermission 实体
	perm := &ent.SystemPermission{
		Type:   "api",
		Path:   "/secret",
		Method: "GET",
	}
	if !matchSystemPermission(perm, lookup) {
		t.Fatalf("应命中已配置的 API 权限")
	}

	// 换一个路径应该不命中
	perm.Path = "/other"
	if matchSystemPermission(perm, lookup) {
		t.Fatalf("未配置的路径不应命中")
	}
}

func TestMatchSystemPermission_MenuMatchesVariants(t *testing.T) {
	lookup := buildPermissionLookup([]*PermissionResponse{
		{Type: "menu", Object: "/dashboard", Action: "menu"},
	})
	perm := &ent.SystemPermission{
		Type:     "menu",
		MenuPath: "/dashboard",
		Method:   "GET",
	}
	// menu 权限的 object 来自 systemPermissionObject（MenuPath -> Code -> Name）
	if !matchSystemPermission(perm, lookup) {
		t.Fatalf("menu 权限变体应命中")
	}
	// 另外验证 Code 也能作为 object 匹配
	perm2 := &ent.SystemPermission{
		Type: "menu",
		Code: "dashboard:view",
	}
	lookup2 := buildPermissionLookup([]*PermissionResponse{
		{Type: "menu", Object: "dashboard:view", Action: "menu"},
	})
	if !matchSystemPermission(perm2, lookup2) {
		t.Fatalf("menu 权限应通过 code 匹配")
	}
}

// -------------------------------------------------------------------
// 数据库驱动：getOperatorPermissionScope / ensureOperatorCanManagePermissions
// -------------------------------------------------------------------

func TestGetOperatorPermissionScope_EmptyOperatorReturnsAll(t *testing.T) {
	client := setupOperatorScopeTestClient(t)
	_ = client
	svc := &PermissionService{client: client}
	ctx := t.Context()

	scope, err := svc.getOperatorPermissionScope(ctx, "", "default")
	if err != nil {
		t.Fatalf("未预期错误: %v", err)
	}
	if !scope.includeAll {
		t.Fatalf("空 operator 应返回全量 scope")
	}
}

func TestGetOperatorPermissionScope_SuperAdminReturnsAll(t *testing.T) {
	client := setupOperatorScopeTestClient(t)
	ctx := t.Context()
	svc := &PermissionService{client: client}

	adminRole := createOperatorScopeRole(t, client, "admin", role.StatusActive)
	ad := createOperatorScopeAdmin(t, client, "op", "uid-super", admin.StatusActive)
	_, err := client.AdminRole.Create().
		SetAdminID(ad.ID).
		SetRoleID(adminRole.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("assign role: %v", err)
	}

	scope, err := svc.getOperatorPermissionScope(ctx, "uid-super", "default")
	if err != nil {
		t.Fatalf("未预期错误: %v", err)
	}
	if !scope.includeAll {
		t.Fatalf("超级管理员应返回全量 scope")
	}
}

func TestGetOperatorPermissionScope_RegularOperatorCollectsRuleKeys(t *testing.T) {
	client := setupOperatorScopeTestClient(t)
	ctx := t.Context()
	svc := &PermissionService{client: client}

	roleEnt := createOperatorScopeRole(t, client, "editor", role.StatusActive)
	ad := createOperatorScopeAdmin(t, client, "op", "uid-op", admin.StatusActive)
	_, err := client.AdminRole.Create().
		SetAdminID(ad.ID).
		SetRoleID(roleEnt.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("assign role: %v", err)
	}

	// 直接创建 casbin 规则条目，模拟已授予的权限
	_, err = client.CasbinRule.Create().
		SetPtype("p").
		SetV0("editor").
		SetV1("default").
		SetV2("/pages").
		SetV3("GET").
		Save(ctx)
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}
	_, err = client.CasbinRule.Create().
		SetPtype("p").
		SetV0("editor").
		SetV1("default").
		SetV2("/pages").
		SetV3("POST").
		Save(ctx)
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}

	scope, err := svc.getOperatorPermissionScope(ctx, "uid-op", "default")
	if err != nil {
		t.Fatalf("未预期错误: %v", err)
	}
	if scope.includeAll {
		t.Fatalf("普通操作者不应得到全量 scope")
	}
	if len(scope.lookup) == 0 {
		t.Fatalf("普通操作者应收集到规则 key")
	}
	if !scope.allowsRule(PermissionRule{Object: "/pages", Action: "GET"}) {
		t.Fatalf("已授予的 GET /pages 应被允许")
	}
	if scope.allowsRule(PermissionRule{Object: "/secret", Action: "DELETE"}) {
		t.Fatalf("未授予的 secret DELETE 应被拒绝")
	}
}

func TestGetOperatorPermissionScope_NoRolesGivesEmptyLookup(t *testing.T) {
	client := setupOperatorScopeTestClient(t)
	svc := &PermissionService{client: client}
	ctx := t.Context()

	// admin 无 role 关联
	_ = createOperatorScopeAdmin(t, client, "op", "uid-no-role", admin.StatusActive)
	scope, err := svc.getOperatorPermissionScope(ctx, "uid-no-role", "default")
	if err != nil {
		t.Fatalf("未预期错误: %v", err)
	}
	if scope.includeAll {
		t.Fatalf("无 role 的 operator 不应得到全量 scope")
	}
	if len(scope.lookup) != 0 {
		t.Fatalf("无 role 的 operator 应得到空 lookup，got %v", scope.lookup)
	}
}

func TestEnsureOperatorCanManagePermissions_EmptyOperatorRequiresExplicitSystemCtx(t *testing.T) {
	client := setupOperatorScopeTestClient(t)
	svc := &PermissionService{client: client}
	ctx := t.Context()

	// 空 operator 且未标记为系统上下文时，requirePermissionOperator 会拒绝
	err := svc.ensureOperatorCanManagePermissions(ctx, "", "default", []PermissionRule{{Object: "/x", Action: "GET"}})
	if !errors.Is(err, ErrPermissionOperatorRequired) {
		t.Fatalf("期望 ErrPermissionOperatorRequired，got %v", err)
	}

	// 标记为系统上下文后放行
	sysCtx := withPermissionSystemOperator(ctx)
	if err := svc.ensureOperatorCanManagePermissions(sysCtx, "", "default", []PermissionRule{{Object: "/x", Action: "GET"}}); err != nil {
		t.Fatalf("系统上下文空 operator 应放行，got %v", err)
	}
}

func TestEnsureOperatorCanManagePermissions_SuperAdminGrantsAnyPermission(t *testing.T) {
	client := setupOperatorScopeTestClient(t)
	ctx := t.Context()
	svc := &PermissionService{client: client}

	roleEnt := createOperatorScopeRole(t, client, "admin", role.StatusActive)
	ad := createOperatorScopeAdmin(t, client, "sup", "uid-sup", admin.StatusActive)
	_, err := client.AdminRole.Create().
		SetAdminID(ad.ID).
		SetRoleID(roleEnt.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("assign: %v", err)
	}

	err = svc.ensureOperatorCanManagePermissions(ctx, "uid-sup", "default", []PermissionRule{
		{Object: "/system/users", Action: "DELETE"},
	})
	if err != nil {
		t.Fatalf("超管应能管理任意权限，got %v", err)
	}
}

func TestEnsureOperatorCanManagePermissions_RegularOperatorRejectsEscalation(t *testing.T) {
	client := setupOperatorScopeTestClient(t)
	ctx := t.Context()
	svc := &PermissionService{client: client}

	roleEnt := createOperatorScopeRole(t, client, "viewer", role.StatusActive)
	ad := createOperatorScopeAdmin(t, client, "op", "uid-viewer", admin.StatusActive)
	_, err := client.AdminRole.Create().
		SetAdminID(ad.ID).
		SetRoleID(roleEnt.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	_, err = client.CasbinRule.Create().
		SetPtype("p").
		SetV0("viewer").
		SetV1("default").
		SetV2("/pages").
		SetV3("GET").
		Save(ctx)
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}

	// 尝试授予超出其范围的权限
	err = svc.ensureOperatorCanManagePermissions(ctx, "uid-viewer", "default", []PermissionRule{
		{Object: "/pages", Action: "GET"}, // 已持有
		{Object: "/secret", Action: "DELETE"}, // 未持有
	})
	if !errors.Is(err, ErrPermissionScopeEscalation) {
		t.Fatalf("期望 ErrPermissionScopeEscalation，got %v", err)
	}

	// 完全在范围内的规则应被允许
	err = svc.ensureOperatorCanManagePermissions(ctx, "uid-viewer", "default", []PermissionRule{
		{Object: "/pages", Action: "GET"},
	})
	if err != nil {
		t.Fatalf("在范围内的权限应通过，got %v", err)
	}
}

// -------------------------------------------------------------------
// ensureOperatorCanManagePermissions / getUserRoleNames 一致性
// -------------------------------------------------------------------

func TestGetUserRoleNames_ReturnsEmptyForUnknownAdmin(t *testing.T) {
	client := setupOperatorScopeTestClient(t)
	svc := &PermissionService{client: client}
	ctx := t.Context()

	names, err := svc.getUserRoleNames(ctx, "uid-unknown")
	// 未知管理员应返回错误
	if err == nil {
		t.Fatalf("未知管理员应返回错误，got names=%v", names)
	}
}

func TestGetUserRoleNames_ReturnsAssignedActiveRoles(t *testing.T) {
	client := setupOperatorScopeTestClient(t)
	ctx := t.Context()
	svc := &PermissionService{client: client}

	active := createOperatorScopeRole(t, client, "active", role.StatusActive)
	_ = createOperatorScopeRole(t, client, "inactive", role.StatusInactive)
	ad := createOperatorScopeAdmin(t, client, "u", "uid-u", admin.StatusActive)
	_, err := client.AdminRole.Create().SetAdminID(ad.ID).SetRoleID(active.ID).Save(ctx)
	if err != nil {
		t.Fatalf("assign role: %v", err)
	}

	names, err := svc.getUserRoleNames(ctx, "uid-u")
	if err != nil {
		t.Fatalf("未预期错误: %v", err)
	}
	if len(names) != 1 || names[0] != "active" {
		t.Fatalf("期望仅返回 active role，got %v", names)
	}
}

// -------------------------------------------------------------------
// isSensitiveAPIPermission / SensitivePermissionObjects 覆盖
// -------------------------------------------------------------------

func TestIsSensitiveAPIPermission_FlagsAdminObjectsOnly(t *testing.T) {
	// 普通 API 不应被标记为敏感
	nonSensitive := &ent.SystemPermission{Type: "api", Path: "/api/v1/pages"}
	if isSensitiveAPIPermission(nonSensitive) {
		t.Fatalf("普通 API 不应被标记为敏感")
	}
	// menu 类型即使路径命中也不应该被视为敏感 API
	menuLike := &ent.SystemPermission{Type: "menu", Path: "/api/v1/admin/auth/system-permissions"}
	if isSensitiveAPIPermission(menuLike) {
		t.Fatalf("menu 类型不应被 isSensitiveAPIPermission 视为敏感 API")
	}

	sensitivePaths := []string{
		"/api/v1/admin/auth/system-permissions",
		"/api/v1/admin/auth/system-permissions/{id}",
		"/api/v1/admin/auth/roles/permissions/assign",
		"/api/v1/admin/auth/permissions/detail",
	}
	for _, p := range sensitivePaths {
		perm := &ent.SystemPermission{Type: "api", Path: p}
		if !isSensitiveAPIPermission(perm) {
			t.Fatalf("%q 应被标记为敏感 API 权限", p)
		}
	}
}

// 确保 context 辅助函数按预期工作
func TestPermissionSystemOperatorContext_PreservesValue(t *testing.T) {
	ctx := context.Background()
	if isPermissionSystemOperator(ctx) {
		t.Fatalf("原始 context 不应为系统 operator")
	}
	wrapped := withPermissionSystemOperator(ctx)
	if !isPermissionSystemOperator(wrapped) {
		t.Fatalf("包装后的 context 应标记为系统 operator")
	}
}

// 确保 casbinrule 查询字段与我们写入的结构一致
func TestCasbinRuleFieldsConsistentWithServiceLogic(t *testing.T) {
	client := setupOperatorScopeTestClient(t)
	ctx := t.Context()

	_, err := client.CasbinRule.Create().
		SetPtype("p").
		SetV0("role-x").
		SetV1("domain-x").
		SetV2("/obj").
		SetV3("GET").
		SetV4("api").
		SetV5("desc").
		Save(ctx)
	if err != nil {
		t.Fatalf("写入 casbin rule 失败: %v", err)
	}
	count, err := client.CasbinRule.Query().
		Where(
			casbinrule.PtypeEQ("p"),
			casbinrule.V0EQ("role-x"),
			casbinrule.V1EQ("domain-x"),
			casbinrule.V2EQ("/obj"),
			casbinrule.V3EQ("GET"),
		).Count(ctx)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("期望计数 1，got %d", count)
	}
}
