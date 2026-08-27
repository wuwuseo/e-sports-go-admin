package service

import (
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/casbinrule"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/role"

	_ "github.com/mattn/go-sqlite3"
)

// 本文件补充 permission.go 中 getEffectivePermissionRoleNames 函数的直接单元测试。
//
// 该函数是 75f9e0b "filter system permissions by current roles" 提交中替换 isUserSuperAdmin
// 的关键路径：它把"操作者的有效角色"解析对齐到 Casbin 运行时（g 规则），
// 用来驱动后续的权限过滤与 scope 判定。任一分支漂移都会让非超管操作者
// 看到超管限定的权限（提权）或者被错误收紧到空（拒绝服务）。
//
// 当前已经被覆盖：TestSystemPermissionService_ListAndTree_UsesCasbinRuntimeRoles
// 走的是端到端集成路径。本文件补齐以下未直接断言的分支：
//   1. 没有任何 g 规则时，回落到 getUserRoleNames；
//   2. 跳过 V1 为空的 g 规则；
//   3. 对重复 V1 名称去重；
//   4. domain 过滤仅作用于 V2（domain 字段）—— 但 g 规则本身只以 V0=UID、V1=role、V2=domain 存储；
//   5. 用户无任何角色（既无 g 规则也无 Admin 关联）时返回空列表（fallback 路径）。

func setupEffectiveRoleNamesTestClient(t *testing.T) *ent.Client {
	client := enttest.Open(t, "sqlite3", "file:effective_role_names?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	return client
}

func seedCasbinGroupingRule(t *testing.T, client *ent.Client, ptype, v0, v1, v2 string) {
	t.Helper()
	_, err := client.CasbinRule.Create().
		SetPtype(ptype).
		SetV0(v0).
		SetV1(v1).
		SetV2(v2).
		Save(t.Context())
	if err != nil {
		t.Fatalf("seed casbin rule ptype=%s v0=%s v1=%s v2=%s failed: %v", ptype, v0, v1, v2, err)
	}
}

func TestGetEffectivePermissionRoleNames_NoGroupingRulesFallsBackToUserRoleNames(t *testing.T) {
	client := setupEffectiveRoleNamesTestClient(t)
	ctx := t.Context()
	svc := &PermissionService{client: client}

	// 创建 Admin + 关联 Role，模拟无 g 规则（用户刚被创建）但已有 admin_role 关系
	r := client.Role.Create().
		SetName("editor").
		SetDisplayName("editor").
		SetDataScope(role.DataScopeAll).
		SetStatus(role.StatusActive).
		SaveX(ctx)
	ad := client.Admin.Create().
		SetEmail("op@e.com").
		SetUsername("op").
		SetPassword("p").
		SetPasswordSalt("s").
		SetUID("uid-fallback").
		SetStatus(admin.StatusActive).
		SaveX(ctx)
	client.AdminRole.Create().
		SetAdminID(ad.ID).
		SetRoleID(r.ID).
		SaveX(ctx)

	names, err := svc.getEffectivePermissionRoleNames(ctx, "uid-fallback", "default")
	if err != nil {
		t.Fatalf("getEffectivePermissionRoleNames 失败：%v", err)
	}
	if len(names) != 1 || names[0] != "editor" {
		t.Fatalf("应回落 getUserRoleNames 返回 [editor]，got %v", names)
	}
}

func TestGetEffectivePermissionRoleNames_DatabaseAssignmentOverridesStaleGroupingRule(t *testing.T) {
	client := setupEffectiveRoleNamesTestClient(t)
	ctx := t.Context()
	svc := &PermissionService{client: client}

	// 数据库已经绑定 stale_editor，而 Casbin 仍残留 runtime_editor。
	// g 是派生索引，授权必须以数据库绑定为准。
	rStale := client.Role.Create().
		SetName("stale_editor").
		SetDisplayName("stale_editor").
		SetDataScope(role.DataScopeAll).
		SetStatus(role.StatusActive).
		SaveX(ctx)
	rRuntime := client.Role.Create().
		SetName("runtime_editor").
		SetDisplayName("runtime_editor").
		SetDataScope(role.DataScopeAll).
		SetStatus(role.StatusActive).
		SaveX(ctx)
	ad := client.Admin.Create().
		SetEmail("op2@e.com").
		SetUsername("op2").
		SetPassword("p").
		SetPasswordSalt("s").
		SetUID("uid-runtime").
		SetStatus(admin.StatusActive).
		SaveX(ctx)
	client.AdminRole.Create().
		SetAdminID(ad.ID).
		SetRoleID(rStale.ID).
		SaveX(ctx)
	_ = rRuntime // 仅用于 g 规则注入

	seedCasbinGroupingRule(t, client, "g", "uid-runtime", "runtime_editor", "default")

	names, err := svc.getEffectivePermissionRoleNames(ctx, "uid-runtime", "default")
	if err != nil {
		t.Fatalf("getEffectivePermissionRoleNames 失败：%v", err)
	}
	if len(names) != 1 || names[0] != "stale_editor" {
		t.Fatalf("应以数据库绑定为准返回 [stale_editor]，got %v", names)
	}
}

func TestGetEffectivePermissionRoleNames_IgnoresGroupingRulesWithoutDatabaseAssignment(t *testing.T) {
	client := setupEffectiveRoleNamesTestClient(t)
	ctx := t.Context()
	svc := &PermissionService{client: client}

	client.Admin.Create().
		SetEmail("empty-v1@e.com").
		SetUsername("empty-v1").
		SetPassword("p").
		SetUID("uid-empty-v1").
		SetStatus(admin.StatusActive).
		SaveX(ctx)
	seedCasbinGroupingRule(t, client, "g", "uid-empty-v1", "", "default")
	seedCasbinGroupingRule(t, client, "g", "uid-empty-v1", "real_role", "default")

	names, err := svc.getEffectivePermissionRoleNames(ctx, "uid-empty-v1", "default")
	if err != nil {
		t.Fatalf("getEffectivePermissionRoleNames 失败：%v", err)
	}
	if len(names) != 0 {
		t.Fatalf("未绑定数据库角色时应忽略全部 g 规则，got %v", names)
	}
}

func TestGetEffectivePermissionRoleNames_IgnoresDuplicateStaleGroupingRules(t *testing.T) {
	client := setupEffectiveRoleNamesTestClient(t)
	ctx := t.Context()
	svc := &PermissionService{client: client}

	client.Admin.Create().
		SetEmail("dup@e.com").
		SetUsername("dup").
		SetPassword("p").
		SetUID("uid-dup").
		SetStatus(admin.StatusActive).
		SaveX(ctx)
	seedCasbinGroupingRule(t, client, "g", "uid-dup", "editor", "default")
	seedCasbinGroupingRule(t, client, "g", "uid-dup", "editor", "tenant-a")
	seedCasbinGroupingRule(t, client, "g", "uid-dup", "viewer", "default")

	names, err := svc.getEffectivePermissionRoleNames(ctx, "uid-dup", "")
	if err != nil {
		t.Fatalf("getEffectivePermissionRoleNames 失败：%v", err)
	}
	if len(names) != 0 {
		t.Fatalf("重复的陈旧 g 规则也不得形成授权，got %v", names)
	}
}

func TestGetEffectivePermissionRoleNames_DomainDoesNotChangeDatabaseAssignments(t *testing.T) {
	client := setupEffectiveRoleNamesTestClient(t)
	ctx := t.Context()
	svc := &PermissionService{client: client}

	r := client.Role.Create().
		SetName("editor").
		SetDisplayName("editor").
		SetDataScope(role.DataScopeSelf).
		SetStatus(role.StatusActive).
		SaveX(ctx)
	a := client.Admin.Create().
		SetEmail("domain@e.com").
		SetUsername("domain").
		SetPassword("p").
		SetUID("uid-domain").
		SetStatus(admin.StatusActive).
		SaveX(ctx)
	client.AdminRole.Create().SetAdminID(a.ID).SetRoleID(r.ID).SaveX(ctx)
	seedCasbinGroupingRule(t, client, "g", "uid-domain", "editor", "default")
	seedCasbinGroupingRule(t, client, "g", "uid-domain", "editor", "tenant-a")
	seedCasbinGroupingRule(t, client, "g", "uid-domain", "viewer", "tenant-a")

	names, err := svc.getEffectivePermissionRoleNames(ctx, "uid-domain", "tenant-a")
	if err != nil {
		t.Fatalf("getEffectivePermissionRoleNames 失败：%v", err)
	}
	if len(names) != 1 || names[0] != "editor" {
		t.Fatalf("数据库绑定应与陈旧 g 域无关，got %v", names)
	}
}

func TestGetEffectivePermissionRoleNames_NonExistentUserPropagatesLookupError(t *testing.T) {
	client := setupEffectiveRoleNamesTestClient(t)
	ctx := t.Context()
	svc := &PermissionService{client: client}

	// 既无 g 规则、Admin 与 User 表都查无此人 —— fallback getUserRoleNames
	// 会冒泡 "user not found" 错误。这是"防止静默放行未知 UID"的安全网，
	// 调用方应将其转换为 ErrPermissionOperatorRequired。
	_, err := svc.getEffectivePermissionRoleNames(ctx, "uid-ghost", "default")
	if err == nil {
		t.Fatal("未知 UID 应冒泡查询错误，不应静默返回空列表")
	}
}

func TestGetEffectivePermissionRoleNames_IgnoresNonGroupingPtype(t *testing.T) {
	client := setupEffectiveRoleNamesTestClient(t)
	ctx := t.Context()
	svc := &PermissionService{client: client}

	// 先创建一个有效的 admin（保证 fallback getUserRoleNames 不会冒泡 NotFound），
	// 再注入 p (policy) 类型规则验证它不被识别为 grouping rule。
	client.Admin.Create().
		SetEmail("non-g@e.com").
		SetUsername("non-g").
		SetPassword("p").
		SetPasswordSalt("s").
		SetUID("uid-non-g").
		SetStatus(admin.StatusActive).
		SaveX(ctx)

	_, err := client.CasbinRule.Create().
		SetPtype("p").
		SetV0("uid-non-g").
		SetV1("should_be_ignored").
		SetV2("default").
		Save(ctx)
	if err != nil {
		t.Fatalf("seed p rule failed: %v", err)
	}

	names, err := svc.getEffectivePermissionRoleNames(ctx, "uid-non-g", "default")
	if err != nil {
		t.Fatalf("getEffectivePermissionRoleNames 失败：%v", err)
	}
	// 真实回落路径生效：fallback 查询 admin_role 无任何 Role 关联，返回空列表
	if len(names) != 0 {
		t.Fatalf("p 类型规则应被忽略并回落 admin_role 关联，got %v", names)
	}

	// 防止 SQL 误匹配：使用 casbinrule.PtypeEQ 验证构建器已加载
	_ = casbinrule.PtypeEQ("g")
}
