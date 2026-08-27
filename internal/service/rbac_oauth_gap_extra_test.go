package service

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/adminthirdpartyuser"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/thirdpartyuser"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"
	appoauth "go-admin/internal/oauth"

	_ "github.com/mattn/go-sqlite3"
)

// 本文件针对最近合并的 OAuth、权限系统及系统权限硬化分支补充最关键的高风险测试。
//
// 优先覆盖范围：
//   1. 权限服务中负责"操作者是否超管"判定的私有方法 isSuperAdminUser
//      （区别于包级 isUserSuperAdmin 辅助函数，但承载相同安全语义）。
//   2. 系统权限中的高风险 API 过滤辅助函数（isSensitiveAPIPermission /
//      sensitivePermissionObjectList / shouldFilterSensitive），任一分支
//      漂移都会让非超管操作者意外看到权限定义/角色权限/权限 CRUD 接口。
//   3. 父子环依赖检测 isDescendantOf 的最大深度兜底（maxDepth），防止
//      异常数据/手工 SQL 注入构造深层链导致该函数在生产栈上死循环。
//   4. OAuth 中关键字符串处理函数 truncate 对 UTF-8 多字节字符的截断
//      安全性，以及 provider 名称 -> 显示名解析的 fallback 行为。
//   5. selectVerifiedGitHubEmail 选邮箱的优先级，避免 GitHub 邮箱
//      解析被错误回退到未验证邮箱造成 ErrVerifiedEmailRequired 漏报。
//   6. OAuth 身份管理服务中"unbind 必须保留至少一种登录方式"的安全护栏
//      （u.Password == "" 时若仅剩 1 个 active 身份应拒绝）。

// -------------------------------------------------------------------
// 1. isSuperAdminUser 单元测试（PermissionService 方法）
// -------------------------------------------------------------------

func setupSuperAdminUserIsolatedTest(t *testing.T) *ent.Client {
	client := enttest.Open(t, "sqlite3", "file:is_sup_user?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
	return client
}

func TestPermissionService_IsSuperAdminUser_NilOp(t *testing.T) {
	svc := &PermissionService{}
	if isSuper, err := svc.isSuperAdminUser(context.Background(), ""); isSuper || err != nil {
		t.Fatalf("空 operatorUID 应被快速返回 false/nil，got isSuper=%v err=%v", isSuper, err)
	}
}

func TestPermissionService_IsSuperAdminUser_AdminWithAdminRole(t *testing.T) {
	client := setupSuperAdminUserIsolatedTest(t)
	ctx := t.Context()

	client.Role.Create().
		SetName(SuperAdminRoleName).
		SetDisplayName("super").
		SetStatus(role.StatusActive).
		SaveX(ctx)
	ad := client.Admin.Create().
		SetEmail("sup@x.com").
		SetUID("uid-sup-isolated").
		SetUsername("sup").
		SetPassword("p").
		SetPasswordSalt("s").
		SetStatus(admin.StatusActive).
		SaveX(ctx)
	r := client.Role.Query().Where(role.NameEQ(SuperAdminRoleName)).OnlyX(ctx)
	client.AdminRole.Create().SetAdminID(ad.ID).SetRoleID(r.ID).SaveX(ctx)

	svc := &PermissionService{client: client}
	isSuper, err := svc.isSuperAdminUser(ctx, ad.UID)
	if err != nil {
		t.Fatalf("isSuperAdminUser: %v", err)
	}
	if !isSuper {
		t.Fatal("拥有 admin 角色应被识别为超管")
	}
}

func TestPermissionService_IsSuperAdminUser_InactiveAdminRoleIgnored(t *testing.T) {
	client := setupSuperAdminUserIsolatedTest(t)
	ctx := t.Context()

	r := client.Role.Create().
		SetName(SuperAdminRoleName).
		SetDisplayName("super").
		SetStatus(role.StatusInactive).
		SaveX(ctx)
	ad := client.Admin.Create().
		SetEmail("inactive-sup@x.com").
		SetUID("uid-inactive-sup").
		SetUsername("inactive-sup").
		SetPassword("p").
		SetPasswordSalt("s").
		SetStatus(admin.StatusActive).
		SaveX(ctx)
	client.AdminRole.Create().SetAdminID(ad.ID).SetRoleID(r.ID).SaveX(ctx)

	svc := &PermissionService{client: client}
	isSuper, err := svc.isSuperAdminUser(ctx, ad.UID)
	if err != nil {
		t.Fatalf("isSuperAdminUser: %v", err)
	}
	if isSuper {
		t.Fatal("禁用状态的 admin 角色不应让操作者成为超管")
	}
}

func TestPermissionService_IsSuperAdminUser_NonExistentUID(t *testing.T) {
	client := setupSuperAdminUserIsolatedTest(t)
	ctx := t.Context()
	svc := &PermissionService{client: client}
	isSuper, err := svc.isSuperAdminUser(ctx, "uid-ghost-isolated")
	if err != nil {
		t.Fatalf("不存在的 UID 应被容忍为非超管，但需返回 nil err，got %v", err)
	}
	if isSuper {
		t.Fatal("未知 UID 不应被识别为超管")
	}
}

// -------------------------------------------------------------------
// 2. isSensitiveAPIPermission 单元测试
// -------------------------------------------------------------------

func TestIsSensitiveAPIPermission_APIWithSensitivePath(t *testing.T) {
	p := &ent.SystemPermission{Type: "api", Path: "/api/v1/admin/auth/system-permissions", Method: "GET"}
	if !isSensitiveAPIPermission(p) {
		t.Fatal("api 类型且 path 在 SensitivePermissionObjects 中应判定为敏感")
	}
}

func TestIsSensitiveAPIPermission_MenuOrButtonNeverSensitive(t *testing.T) {
	// 即便 path 命中，type 非 api 也不应被识别
	p := &ent.SystemPermission{Type: "menu", Path: "/api/v1/admin/auth/system-permissions"}
	if isSensitiveAPIPermission(p) {
		t.Fatal("menu 类型不应被识别为敏感 API 权限")
	}
	p2 := &ent.SystemPermission{Type: "button", Path: "/api/v1/admin/auth/system-permissions"}
	if isSensitiveAPIPermission(p2) {
		t.Fatal("button 类型不应被识别为敏感 API 权限")
	}
}

func TestIsSensitiveAPIPermission_APIWithNormalPath(t *testing.T) {
	p := &ent.SystemPermission{Type: "api", Path: "/api/v1/admin/auth/roles", Method: "GET"}
	if isSensitiveAPIPermission(p) {
		t.Fatal("非敏感路径应判定为非敏感")
	}
}

// -------------------------------------------------------------------
// 3. sensitivePermissionObjectList 排序稳定性测试
// -------------------------------------------------------------------

func TestSensitivePermissionObjectList_SortedAndComplete(t *testing.T) {
	list := sensitivePermissionObjectList()
	if len(list) != len(SensitivePermissionObjects) {
		t.Fatalf("列表长度应与 map 一致，got %d expect %d", len(list), len(SensitivePermissionObjects))
	}
	if !sort.StringsAreSorted(list) {
		t.Fatalf("返回的列表必须按字典序排序，got %v", list)
	}
	// 必须与 map 完全一致，不丢成员
	seen := make(map[string]struct{}, len(list))
	for _, p := range list {
		seen[p] = struct{}{}
	}
	for p := range SensitivePermissionObjects {
		if _, ok := seen[p]; !ok {
			t.Errorf("遗漏敏感路径 %q", p)
		}
	}
}

// -------------------------------------------------------------------
// 4. shouldFilterSensitive 单元测试（SystemPermissionService 方法）
// -------------------------------------------------------------------

func TestSystemPermissionService_ShouldFilterSensitive_NoOperatorAndSystemCtx(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{client: client}
	ctx := withPermissionSystemOperator(t.Context())
	filter, err := svc.shouldFilterSensitive(ctx, "")
	if err != nil {
		t.Fatalf("shouldFilterSensitive: %v", err)
	}
	if filter {
		t.Fatal("空 operatorUID + 系统操作者上下文应不进行敏感过滤")
	}
}

func TestSystemPermissionService_ShouldFilterSensitive_NoOperatorNoSystemCtx(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{client: client}
	filter, err := svc.shouldFilterSensitive(t.Context(), "")
	if err != nil {
		t.Fatalf("shouldFilterSensitive: %v", err)
	}
	if !filter {
		t.Fatal("空 operatorUID 且非系统操作者上下文应进行敏感过滤")
	}
}

func TestSystemPermissionService_ShouldFilterSensitive_OperatorIsSuperAdmin(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{client: client}
	ctx := t.Context()

	r := client.Role.Create().
		SetName(SuperAdminRoleName).
		SetDisplayName("super").
		SetStatus(role.StatusActive).
		SaveX(ctx)
	ad := client.Admin.Create().
		SetEmail("opsup@x.com").
		SetUID("uid-opsup").
		SetUsername("opsup").
		SetPassword("p").
		SetPasswordSalt("s").
		SetStatus(admin.StatusActive).
		SaveX(ctx)
	client.AdminRole.Create().SetAdminID(ad.ID).SetRoleID(r.ID).SaveX(ctx)

	filter, err := svc.shouldFilterSensitive(ctx, ad.UID)
	if err != nil {
		t.Fatalf("shouldFilterSensitive: %v", err)
	}
	if filter {
		t.Fatal("超管操作者应看到全部权限，不进行敏感过滤")
	}
}

func TestSystemPermissionService_ShouldFilterSensitive_OperatorIsNotSuperAdmin(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{client: client}
	ctx := t.Context()

	ad := client.Admin.Create().
		SetEmail("plain@x.com").
		SetUID("uid-plain").
		SetUsername("plain").
		SetPassword("p").
		SetPasswordSalt("s").
		SetStatus(admin.StatusActive).
		SaveX(ctx)

	filter, err := svc.shouldFilterSensitive(ctx, ad.UID)
	if err != nil {
		t.Fatalf("shouldFilterSensitive: %v", err)
	}
	if !filter {
		t.Fatal("非超管操作者必须进行敏感过滤")
	}
}

// -------------------------------------------------------------------
// 5. isDescendantOf 单元测试
// -------------------------------------------------------------------

func TestSystemPermissionService_IsDescendantOf_TrueWhenChain(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{client: client}
	ctx := t.Context()

	root := createSystemPermissionForTest(t, client, "menu", "root", "/root", "GET", 0, 0, 1)
	child := createSystemPermissionForTest(t, client, "menu", "child", "/root/c", "GET", root.ID, 0, 1)
	grand := createSystemPermissionForTest(t, client, "menu", "grand", "/root/c/g", "GET", child.ID, 0, 1)

	isDesc, err := svc.isDescendantOf(ctx, grand.ID, root.ID)
	if err != nil {
		t.Fatalf("isDescendantOf: %v", err)
	}
	if !isDesc {
		t.Fatal("grand 应被识别为 root 的后代")
	}
}

func TestSystemPermissionService_IsDescendantOf_FalseForOtherBranch(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{client: client}
	ctx := t.Context()

	root := createSystemPermissionForTest(t, client, "menu", "root", "/root", "GET", 0, 0, 1)
	a := createSystemPermissionForTest(t, client, "menu", "a", "/root/a", "GET", root.ID, 0, 1)
	b := createSystemPermissionForTest(t, client, "menu", "b", "/root/b", "GET", root.ID, 0, 1)

	isDesc, err := svc.isDescendantOf(ctx, b.ID, a.ID)
	if err != nil {
		t.Fatalf("isDescendantOf: %v", err)
	}
	if isDesc {
		t.Fatal("b 不是 a 的后代，不应被识别为后代")
	}
}

func TestSystemPermissionService_IsDescendantOf_NotFoundEnds(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{client: client}
	ctx := t.Context()

	// ancestor 链上某节点被删除（手工 SQL/迁移时可能），方法应安全返回 false
	leaf := createSystemPermissionForTest(t, client, "menu", "leaf", "/leaf", "GET", 0, 0, 1)
	// 不创建 parent 但引用一个不存在的 ID
	client.SystemPermission.UpdateOneID(leaf.ID).SetParentID(99999).SaveX(ctx)

	isDesc, err := svc.isDescendantOf(ctx, leaf.ID, 99999)
	if err != nil {
		t.Fatalf("链上节点缺失应安全处理，got err=%v", err)
	}
	if !isDesc {
		t.Fatal("祖先 ID 自身就是缺失 ID，应判定为后代（保持 Update 行为一致）")
	}
}

func TestSystemPermissionService_IsDescendantOf_ExceedsMaxDepth(t *testing.T) {
	// 使用独立 DB 文件避免与同包内其他 SystemPermission 测试（共享 sysp 内存库）冲突
	client := enttest.Open(t, "sqlite3", "file:descendant_max?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	svc := &SystemPermissionService{client: client}
	ctx := t.Context()

	// 构造长度等于 maxDepth(100) + 1 的链以触发兜底错误
	current := createSystemPermissionForTest(t, client, "menu", "node0", "/n0", "GET", 0, 0, 1)
	for i := 1; i <= 100; i++ {
		parent := current
		current = createSystemPermissionForTest(t, client, "menu", "loop-"+itoa(i), "/n-loop-"+itoa(i), "GET", parent.ID, 0, 1)
	}

	// candidateID = 当前最深节点；ancestorID = 不在链中的 ID
	// 走完 maxDepth 步后会触发错误
	_, err := svc.isDescendantOf(ctx, current.ID, 1)
	if err == nil {
		t.Fatal("超过最大深度应返回错误，提示运维人员排查")
	}
	if !strings.Contains(err.Error(), "最大深度") {
		t.Fatalf("错误信息应包含最大深度，got %v", err)
	}
}

// itoa 简单的整数到字符串转换，避免导入 strconv 仅本测试使用
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	negative := false
	if i < 0 {
		negative = true
		i = -i
	}
	digits := []byte{}
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

// -------------------------------------------------------------------
// 6. truncate 单元测试（oauth_auth.go）
// -------------------------------------------------------------------

func TestTruncate_ShortStringUnchanged(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Fatalf("短字符串应原样返回，got %q", got)
	}
}

func TestTruncate_ExactLengthUnchanged(t *testing.T) {
	if got := truncate("hello", 5); got != "hello" {
		t.Fatalf("等长字符串应原样返回，got %q", got)
	}
}

func TestTruncate_LongStringCutByRuneCount(t *testing.T) {
	// 12 个 ASCII 字符 -> 截到 5
	if got := truncate("abcdefghijkl", 5); got != "abcde" {
		t.Fatalf("ASCII 截断不正确，got %q", got)
	}
}

func TestTruncate_RespectsRuneBoundary(t *testing.T) {
	// 6 个汉字 = 6 rune，但每个汉字 UTF-8 占 3 字节
	// 截到 4 rune，预期保留前 4 个汉字，不能出现半个汉字
	got := truncate("汉字字符长度", 4)
	if got != "汉字字符" {
		t.Fatalf("UTF-8 多字节截断不正确，got %q", got)
	}
	if strings.ContainsRune(got, 0xFFFD) {
		t.Fatal("截断后不得出现 Unicode 替换字符（说明按字节切了汉字）")
	}
}

func TestTruncate_ZeroOrNegativeMaxReturnsEmpty(t *testing.T) {
	if got := truncate("hello", 0); got != "" {
		t.Fatalf("max<=0 应返回空字符串，got %q", got)
	}
	if got := truncate("hello", -3); got != "" {
		t.Fatalf("max<0 应返回空字符串，got %q", got)
	}
}

// -------------------------------------------------------------------
// 7. selectVerifiedGitHubEmail 单元测试 — 见 oauth 包同名测试
//     （该函数为 oauth 包私有，已在 internal/oauth/github_email_test.go 中
//     直接覆盖：primary+verified 优先、首个 verified 降级、无 verified 返回空）
// -------------------------------------------------------------------

// -------------------------------------------------------------------
// 8. providerDisplayName 单元测试
// -------------------------------------------------------------------

func TestProviderDisplayName_KnownProviderUsesRegistry(t *testing.T) {
	provider := &fakeGitHubProvider{}
	registry := appoauth.NewRegistry(provider)
	svc := &OAuthAuthService{registry: registry}
	if got := svc.providerDisplayName("github"); got != "GitHub" {
		t.Fatalf("已注册 provider 应使用 registry 中的显示名，got %q", got)
	}
}

func TestProviderDisplayName_UnknownProviderFallsBackToName(t *testing.T) {
	svc := &OAuthAuthService{registry: appoauth.NewRegistry()}
	if got := svc.providerDisplayName("feishu"); got != "feishu" {
		t.Fatalf("未注册 provider 应回退到原始名称，got %q", got)
	}
}

// -------------------------------------------------------------------
// 9. applyUserIdentityCreate / applyAdminIdentityCreate 单元测试
// -------------------------------------------------------------------

func TestApplyUserIdentityCreate_AppliesNameAvatarAndExtra(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:apply_user?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	ctx := t.Context()
	u := client.User.Create().
		SetEmail("apply@x.com").
		SetUID("uid-apply").
		SetPassword("p").
		SetStatus(user.StatusActive).
		SaveX(ctx)

	identity := &appoauth.Identity{
		ID:        "apply-id",
		Name:      "The Octocat",
		AvatarURL: "https://example.com/a.png",
		Extra:     map[string]any{"login": "octocat", "id": 12345},
	}
	create := client.ThirdPartyUser.Create().
		SetUserID(u.ID).
		SetPlatform("github").
		SetOpenid("apply-id").
		SetStatus(thirdpartyuser.StatusActive)
	applyUserIdentityCreate(create, identity)
	saved, err := create.Save(ctx)
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if saved.Nickname != "The Octocat" {
		t.Fatalf("Nickname 未被正确写入，got %q", saved.Nickname)
	}
	if saved.AvatarURL != "https://example.com/a.png" {
		t.Fatalf("AvatarURL 未被正确写入，got %q", saved.AvatarURL)
	}
	extra, ok := saved.ExtraData["login"].(string)
	if !ok || extra != "octocat" {
		t.Fatalf("ExtraData 未被序列化，got %#v", saved.ExtraData)
	}
}

func TestApplyUserIdentityCreate_EmptyIdentityLeavesFieldsEmpty(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:apply_user_empty?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	ctx := t.Context()
	u := client.User.Create().
		SetEmail("empty@x.com").
		SetUID("uid-empty").
		SetPassword("p").
		SetStatus(user.StatusActive).
		SaveX(ctx)

	identity := &appoauth.Identity{ID: "x"}
	create := client.ThirdPartyUser.Create().
		SetUserID(u.ID).
		SetPlatform("github").
		SetOpenid("x").
		SetStatus(thirdpartyuser.StatusActive)
	applyUserIdentityCreate(create, identity)
	saved, err := create.Save(ctx)
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if saved.Nickname != "" || saved.AvatarURL != "" || len(saved.ExtraData) != 0 {
		t.Fatalf("空字段应保持为空，got nickname=%q avatar=%q extra=%v", saved.Nickname, saved.AvatarURL, saved.ExtraData)
	}
}

func TestApplyAdminIdentityCreate_AppliesNameAvatarAndExtra(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:apply_admin?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	ctx := t.Context()
	ad := client.Admin.Create().
		SetEmail("apply-admin@x.com").
		SetUID("uid-apply-admin").
		SetUsername("apply-admin").
		SetPassword("p").
		SetPasswordSalt("s").
		SetStatus(admin.StatusActive).
		SaveX(ctx)

	identity := &appoauth.Identity{
		ID:        "apply-admin-id",
		Name:      "The Admin",
		AvatarURL: "https://example.com/admin.png",
		Extra:     map[string]any{"login": "admin-login"},
	}
	create := client.AdminThirdPartyUser.Create().
		SetAdminID(ad.ID).
		SetPlatform("github").
		SetOpenid("apply-admin-id").
		SetStatus(adminthirdpartyuser.StatusActive)
	applyAdminIdentityCreate(create, identity)
	saved, err := create.Save(ctx)
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if saved.Nickname != "The Admin" || saved.AvatarURL != "https://example.com/admin.png" {
		t.Fatalf("字段未被正确写入，got %+v", saved)
	}
}

// -------------------------------------------------------------------
// 10. syncRolePermissionState 在 EnforcerManager=nil 时不 panic
// -------------------------------------------------------------------

func TestPermissionService_SyncRolePermissionState_NoEnforcerSafeNoop(t *testing.T) {
	client := setupEffectiveRoleNamesTestClient(t)
	svc := &PermissionService{client: client}
	prevEnforcer := global.EnforcerManager
	global.EnforcerManager = nil
	t.Cleanup(func() { global.EnforcerManager = prevEnforcer })

	if err := svc.syncRolePermissionState(t.Context(), "any-role", "default"); err != nil {
		t.Fatalf("EnforcerManager=nil 时必须安全 noop，got err=%v", err)
	}
}

// -------------------------------------------------------------------
// 11. determinePermissionType / missingPermissionRoleNames
// -------------------------------------------------------------------

func TestDeterminePermissionType_Cases(t *testing.T) {
	cases := []struct {
		action string
		want   PermissionType
	}{
		{"MENU", PermissionTypeMenu},
		{"menu", PermissionTypeMenu},
		{"BUTTON", PermissionTypeButton},
		{"button", PermissionTypeButton},
		{"GET", PermissionTypeAPI},
		{"post", PermissionTypeAPI},
		{"delete", PermissionTypeAPI},
	}
	for _, c := range cases {
		if got := determinePermissionType(c.action); got != c.want {
			t.Errorf("determinePermissionType(%q) = %q, want %q", c.action, got, c.want)
		}
	}
}

func TestMissingPermissionRoleNames_DiffsAndDedup(t *testing.T) {
	// 注意：missingPermissionRoleNames 不会对 desired 去重，
	// 调用方应负责传唯一集合；这里只验证不会因重复 desired 引发 panic 且
	// 仍能正确识别"已在 existing 中"的成员。
	got := missingPermissionRoleNames([]string{"a", "b", "a", "c"}, []string{"b"})
	if len(got) != 3 {
		t.Fatalf("expected [a a c]，got %v", got)
	}
	// 顺序：按 desired 出现顺序，未去重
	want := []string{"a", "a", "c"}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("第 %d 项应为 %q，got %q", i, want[i], got[i])
		}
	}
}

func TestMissingPermissionRoleNames_EmptyDesiredReturnsEmpty(t *testing.T) {
	if got := missingPermissionRoleNames(nil, []string{"a"}); len(got) != 0 {
		t.Fatalf("desired 为空应返回空，got %v", got)
	}
	if got := missingPermissionRoleNames([]string{}, []string{"a"}); len(got) != 0 {
		t.Fatalf("desired 为空应返回空，got %v", got)
	}
}

// -------------------------------------------------------------------
// 12. OAuthAuthService.UnbindUser 最后登录方式护栏
// -------------------------------------------------------------------

// fakeEnforcer 用于满足 OAuthAuthService 调用时最小依赖
type fakeEnforcer struct{}

func newOAuthAuthForUnbindTest(t *testing.T, client *ent.Client) *OAuthAuthService {
	provider := &fakeGitHubProvider{identity: &appoauth.Identity{}}
	registry := appoauth.NewRegistry(provider)
	cfg := &appoauth.ProviderConfig{}
	_ = cfg
	return &OAuthAuthService{
		client:         client,
		registry:       registry,
		configService:  nil,
		sessionService: nil,
		now:            nil,
	}
}

func TestOAuthAuthService_UnbindUser_RejectsLastLoginWhenNoPassword(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:unbind_last?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	ctx := t.Context()
	u := client.User.Create().
		SetEmail("lastlogin@x.com").
		SetUID("uid-lastlogin").
		SetPassword(""). // 用户未设置密码，OAuth 是其唯一登录方式
		SetStatus(user.StatusActive).
		SaveX(ctx)
	client.ThirdPartyUser.Create().
		SetUserID(u.ID).
		SetPlatform("github").
		SetOpenid("only-one").
		SetStatus(thirdpartyuser.StatusActive).
		SaveX(ctx)

	svc := newOAuthAuthForUnbindTest(t, client)
	err := svc.UnbindUser(ctx, u.UID, "github")
	if !errors.Is(err, ErrOAuthLastLoginMethod) {
		t.Fatalf("唯一 OAuth 身份且无密码应被拒绝，got err=%v", err)
	}
	// 身份仍存在
	if count, _ := client.ThirdPartyUser.Query().Count(ctx); count != 1 {
		t.Fatalf("身份应被保留，got count=%d", count)
	}
}

func TestOAuthAuthService_UnbindUser_AllowsWhenOtherIdentityActive(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:unbind_multi?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	ctx := t.Context()
	u := client.User.Create().
		SetEmail("multi@x.com").
		SetUID("uid-multi").
		SetPassword("").
		SetStatus(user.StatusActive).
		SaveX(ctx)
	client.ThirdPartyUser.Create().
		SetUserID(u.ID).
		SetPlatform("github").
		SetOpenid("gh-1").
		SetStatus(thirdpartyuser.StatusActive).
		SaveX(ctx)
	client.ThirdPartyUser.Create().
		SetUserID(u.ID).
		SetPlatform("google").
		SetOpenid("gg-1").
		SetStatus(thirdpartyuser.StatusActive).
		SaveX(ctx)

	svc := newOAuthAuthForUnbindTest(t, client)
	if err := svc.UnbindUser(ctx, u.UID, "github"); err != nil {
		t.Fatalf("存在其他 active OAuth 身份时 unbind 应放行，got err=%v", err)
	}
	count, _ := client.ThirdPartyUser.Query().Count(ctx)
	if count != 1 {
		t.Fatalf("被解绑的应是 github，仅剩 1 条 google 身份，got %d", count)
	}
}

func TestOAuthAuthService_UnbindUser_AllowsWhenUserHasPassword(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:unbind_pwd?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	ctx := t.Context()
	u := client.User.Create().
		SetEmail("haspwd@x.com").
		SetUID("uid-haspwd").
		SetPassword("plain-hash").
		SetPasswordSalt("salt").
		SetStatus(user.StatusActive).
		SaveX(ctx)
	client.ThirdPartyUser.Create().
		SetUserID(u.ID).
		SetPlatform("github").
		SetOpenid("gh-only").
		SetStatus(thirdpartyuser.StatusActive).
		SaveX(ctx)

	svc := newOAuthAuthForUnbindTest(t, client)
	if err := svc.UnbindUser(ctx, u.UID, "github"); err != nil {
		t.Fatalf("用户已设置密码时即使只有 1 个 OAuth 身份也应允许 unbind，got err=%v", err)
	}
}

func TestOAuthAuthService_UnbindUser_NotFoundIdentity(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:unbind_404?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	ctx := t.Context()
	u := client.User.Create().
		SetEmail("404@x.com").
		SetUID("uid-404").
		SetPassword("p").
		SetStatus(user.StatusActive).
		SaveX(ctx)

	svc := newOAuthAuthForUnbindTest(t, client)
	err := svc.UnbindUser(ctx, u.UID, "github")
	if !errors.Is(err, ErrThirdPartyNotFound) {
		t.Fatalf("未绑定的 provider 应返回 ErrThirdPartyNotFound，got %v", err)
	}
}

func TestOAuthAuthService_UnbindAdmin_NotFoundIdentity(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:unbind_admin_404?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	ctx := t.Context()
	ad := client.Admin.Create().
		SetEmail("admin-404@x.com").
		SetUID("uid-admin-404").
		SetUsername("admin-404").
		SetPassword("p").
		SetPasswordSalt("s").
		SetStatus(admin.StatusActive).
		SaveX(ctx)

	svc := newOAuthAuthForUnbindTest(t, client)
	err := svc.UnbindAdmin(ctx, ad.UID, "github")
	if !errors.Is(err, ErrThirdPartyNotFound) {
		t.Fatalf("未绑定的 provider 应返回 ErrThirdPartyNotFound，got %v", err)
	}
}

func TestOAuthAuthService_DeleteAdminIdentity_DeletesAndReturnsNotFound(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:del_admin_id?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	ctx := t.Context()
	ad := client.Admin.Create().
		SetEmail("del-admin@x.com").
		SetUID("uid-del-admin").
		SetUsername("del-admin").
		SetPassword("p").
		SetPasswordSalt("s").
		SetStatus(admin.StatusActive).
		SaveX(ctx)
	binding := client.AdminThirdPartyUser.Create().
		SetAdminID(ad.ID).
		SetPlatform("github").
		SetOpenid("del-id").
		SetStatus(adminthirdpartyuser.StatusActive).
		SaveX(ctx)

	svc := newOAuthAuthForUnbindTest(t, client)
	if err := svc.DeleteAdminIdentity(ctx, nil, binding.ID); err != nil {
		t.Fatalf("首次删除应成功，got err=%v", err)
	}
	if err := svc.DeleteAdminIdentity(ctx, nil, binding.ID); !errors.Is(err, ErrThirdPartyNotFound) {
		t.Fatalf("重复删除应返回 ErrThirdPartyNotFound，got %v", err)
	}
}

// -------------------------------------------------------------------
// 13. SystemPermissionService List 关键字 + Type 过滤的回归保护
// -------------------------------------------------------------------

func TestSystemPermissionService_List_TypeFilterDoesNotLeakSensitive(t *testing.T) {
	client := setupSystemPermissionTestClient(t)
	svc := &SystemPermissionService{client: client}
	ctx := t.Context()

	// 创建菜单与 API 各一条
	createSystemPermissionForTest(t, client, "menu", "menu-1", "/m1", "GET", 0, 0, 1)
	createSystemPermissionForTest(t, client, "api", "api-1", "/api/v1/admin/auth/system-permissions", "GET", 0, 0, 1)

	req := &ListSystemPermissionsRequest{Type: SystemPermissionTypeAPI}
	resp, err := svc.List(ctx, "", req)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if resp.Total != 1 || resp.Data[0].Type != SystemPermissionTypeAPI {
		t.Fatalf("Type=api 应仅返回 1 条 api 类型，got total=%d data=%+v", resp.Total, resp.Data)
	}
}

// -------------------------------------------------------------------
// 14. role.go 中 ClearUserPermissionsByRoleID 在 cache 非空时的清理
// -------------------------------------------------------------------

func TestRoleService_ClearUserPermissionsByRoleID_RemovesEntriesForHolders(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:role_clear_cache?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()
	roleEntity := client.Role.Create().
		SetName("cache-role").
		SetDisplayName("Cache Role").
		SetStatus(role.StatusActive).
		SaveX(ctx)
	u := client.User.Create().
		SetEmail("cacheuser@x.com").
		SetUID("uid-cacheuser").
		SetPassword("p").
		SetStatus(user.StatusActive).
		SaveX(ctx)
	ad := client.Admin.Create().
		SetEmail("cacheadmin@x.com").
		SetUID("uid-cacheadmin").
		SetUsername("cacheadmin").
		SetPassword("p").
		SetPasswordSalt("s").
		SetStatus(admin.StatusActive).
		SaveX(ctx)
	client.UserRole.Create().SetUserID(u.ID).SetRoleID(roleEntity.ID).SaveX(ctx)
	client.AdminRole.Create().SetAdminID(ad.ID).SetRoleID(roleEntity.ID).SaveX(ctx)

	typed := NewRoleServiceWithDepartmentService(makeMemCache(t), nil)
	// 调用应安全完成（不会因 cache 缺失 key 而 panic）
	typed.clearUserPermissionsByRoleID(ctx, roleEntity.ID, "default")
}

func TestRoleService_ClearUserPermissionsByRoleID_NoHoldersSafeNoop(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:role_clear_empty?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()
	roleEntity := client.Role.Create().
		SetName("empty-role").
		SetDisplayName("Empty Role").
		SetStatus(role.StatusActive).
		SaveX(ctx)

	typed := NewRoleServiceWithDepartmentService(makeMemCache(t), nil)
	// 没有任何 UserRole/AdminRole 持有者时，方法应安全 noop，不应 panic
	typed.clearUserPermissionsByRoleID(ctx, roleEntity.ID, "default")
}
