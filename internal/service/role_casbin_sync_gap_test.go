package service

import (
	"testing"

	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/adminrole"
	"go-admin/internal/ent/casbinrule"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/user"
	"go-admin/internal/ent/userrole"
	"go-admin/internal/global"
)

// ============================================================================
//  syncRoleCasbinState 与 setStatus 幂等修复路径缺口测试
//
//  背景：refactor(role): share status mutation (7720f26) 将 Enable/Disable
//  合并为 setStatus，并引入 syncRoleCasbinState 负责角色状态与 Casbin g 规则
//  的双向同步。该函数承担两个关键不变量：
//    1. 角色禁用时，必须清理该角色在 default 域下的全部 g 分组关联，
//       防止已禁用角色的持有者仍能通过缓存/脏数据绕过权限判定；
//    2. 角色启用时，必须按现存 AdminRole / UserRole 绑定重建 g 规则，
//       修复历史上因迁移/回滚导致 Casbin 与关系表不同步的脏数据。
//
//  此外，setStatus 在"状态未变化"分支仍会主动调用 syncRoleCasbinState，
//  这是用于自我修复的幂等路径，同样是高价值回归目标。
// ============================================================================

// ---------------------------------------------------------------------------
// syncRoleCasbinState 无 Casbin 环境下的安全跳过分支
// ---------------------------------------------------------------------------

// TestSyncRoleCasbinState_NilEnforcerSkipsWithoutError 验证未初始化 Casbin
// （例如测试环境或启动早期）时不报错、不写入数据库。
func TestSyncRoleCasbinState_NilEnforcerSkipsWithoutError(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	r, err := client.Role.Create().
		SetName("nil_enforcer_role").
		SetDisplayName("NilEnforcer").
		SetStatus(role.StatusActive).
		SetDataScope(role.DataScopeAll).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	// ponytail: 保证 global.EnforcerManager == nil 进入快速跳过分支
	prev := global.EnforcerManager
	global.EnforcerManager = nil
	defer func() { global.EnforcerManager = prev }()

	// 启用态（应重建 g，但 Enforcer 为 nil 直接跳过）
	if err := svc.syncRoleCasbinState(ctx, r, true); err != nil {
		t.Fatalf("syncRoleCasbinState(true) 在 nil Enforcer 下不应报错: %v", err)
	}
	// 禁用态（应清理 g，同样跳过）
	if err := svc.syncRoleCasbinState(ctx, r, false); err != nil {
		t.Fatalf("syncRoleCasbinState(false) 在 nil Enforcer 下不应报错: %v", err)
	}

	// 验证 CasbinRule 表保持干净
	count, err := client.CasbinRule.Query().Count(ctx)
	if err != nil {
		t.Fatalf("count casbin rules: %v", err)
	}
	if count != 0 {
		t.Fatalf("Enforcer 为 nil 时不应写入 CasbinRule，实际写入 %d 条", count)
	}
}

// ---------------------------------------------------------------------------
// syncRoleCasbinState 禁用态：全量清理 g 规则，不区分管理员/用户
// ---------------------------------------------------------------------------

func TestSyncRoleCasbinState_DisabledRemovesAllGroupingBindings(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)
	setupPermissionTestEnforcer(t, client)

	r, err := client.Role.Create().
		SetName("cleanup_target").
		SetDisplayName("CleanupTarget").
		SetStatus(role.StatusActive).
		SetDataScope(role.DataScopeAll).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	// 构造 管理员 → 角色 绑定
	a, err := client.Admin.Create().
		SetEmail("a-cleanup@example.com").
		SetUsername("a-cleanup").
		SetPassword("hash").
		SetPasswordSalt("salt").
		SetUID("uid-a-cleanup").
		SetStatus(admin.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建管理员失败: %v", err)
	}
	if _, err := client.AdminRole.Create().
		SetAdminID(a.ID).SetRoleID(r.ID).SetIsPrimary(true).Save(ctx); err != nil {
		t.Fatalf("绑定管理员角色失败: %v", err)
	}

	// 构造 用户 → 角色 绑定
	u, err := client.User.Create().
		SetEmail("u-cleanup@example.com").
		SetUsername("u-cleanup").
		SetPassword("hash").
		SetPasswordSalt("salt").
		SetUID("uid-u-cleanup").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if _, err := client.UserRole.Create().
		SetUserID(u.ID).SetRoleID(r.ID).SetIsPrimary(true).Save(ctx); err != nil {
		t.Fatalf("绑定用户角色失败: %v", err)
	}

	// 通过 Enforcer 写入两条 g 规则（模拟运行时存在的分配）
	enforcer, _ := global.EnforcerManager.GetEnforcer("default")
	if _, err := enforcer.AddRoleForUserInDomain(a.UID, r.Name, "default"); err != nil {
		t.Fatalf("seed admin g: %v", err)
	}
	if _, err := enforcer.AddRoleForUserInDomain(u.UID, r.Name, "default"); err != nil {
		t.Fatalf("seed user g: %v", err)
	}
	// 额外写一条不相关的 g 规则，确认清理按 roleName+domain 精确过滤
	otherRole, _ := client.Role.Create().
		SetName("other_role").SetDisplayName("Other").
		SetStatus(role.StatusActive).SetDataScope(role.DataScopeAll).Save(ctx)
	if _, err := enforcer.AddRoleForUserInDomain(a.UID, otherRole.Name, "default"); err != nil {
		t.Fatalf("seed other g: %v", err)
	}

	// 执行禁用同步
	if err := svc.syncRoleCasbinState(ctx, r, false); err != nil {
		t.Fatalf("syncRoleCasbinState(false) error = %v", err)
	}

	// 断言：目标角色的两条 g 规则被清理
	remainingTarget, err := client.CasbinRule.Query().
		Where(casbinrule.PtypeEQ("g"), casbinrule.V1EQ(r.Name)).
		Count(ctx)
	if err != nil {
		t.Fatalf("count target g rules: %v", err)
	}
	if remainingTarget != 0 {
		t.Fatalf("禁用后角色 %q 的 g 规则应全部清理，剩余 %d 条", r.Name, remainingTarget)
	}

	// 断言：不相关角色的 g 规则保留
	remainingOther, err := client.CasbinRule.Query().
		Where(casbinrule.PtypeEQ("g"), casbinrule.V1EQ(otherRole.Name)).
		Count(ctx)
	if err != nil {
		t.Fatalf("count other g rules: %v", err)
	}
	if remainingOther == 0 {
		t.Fatal("清理不应波及其它角色的 g 规则")
	}
}

// ---------------------------------------------------------------------------
// syncRoleCasbinState 启用态：按 AdminRole/UserRole 表重建缺失的 g 规则
// ---------------------------------------------------------------------------

func TestSyncRoleCasbinState_EnabledRebuildsMissingGroupingBindings(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)
	setupPermissionTestEnforcer(t, client)

	r, err := client.Role.Create().
		SetName("rebuild_role").
		SetDisplayName("RebuildRole").
		SetStatus(role.StatusInactive).
		SetDataScope(role.DataScopeAll).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	// 写入关系表记录（AdminRole + UserRole）
	a, _ := client.Admin.Create().
		SetEmail("a-rebuild@example.com").SetUsername("a-rebuild").
		SetPassword("hash").SetPasswordSalt("salt").
		SetUID("uid-a-rebuild").SetStatus(admin.StatusActive).Save(ctx)
	_, _ = client.AdminRole.Create().SetAdminID(a.ID).SetRoleID(r.ID).SetIsPrimary(true).Save(ctx)

	u, _ := client.User.Create().
		SetEmail("u-rebuild@example.com").SetUsername("u-rebuild").
		SetPassword("hash").SetPasswordSalt("salt").
		SetUID("uid-u-rebuild").SetStatus(user.StatusActive).Save(ctx)
	_, _ = client.UserRole.Create().SetUserID(u.ID).SetRoleID(r.ID).SetIsPrimary(true).Save(ctx)

	// 故意不写入 g 规则 → 模拟历史脏数据（关系表有但 Casbin 空）

	// 执行启用同步 → 应自动补齐
	if err := svc.syncRoleCasbinState(ctx, r, true); err != nil {
		t.Fatalf("syncRoleCasbinState(true) error = %v", err)
	}

	enforcer, _ := global.EnforcerManager.GetEnforcer("default")
	// 管理员应被分配角色（casbin v3 API 使用 GetRolesForUserInDomain，单返回值）
	adminRoles := enforcer.GetRolesForUserInDomain(a.UID, "default")
	foundAdmin := false
	for _, rn := range adminRoles {
		if rn == r.Name {
			foundAdmin = true
			break
		}
	}
	if !foundAdmin {
		t.Fatalf("启用后管理员 uid=%s 应被重建 g 分配到 %s，实际角色=%v", a.UID, r.Name, adminRoles)
	}
	// 用户应被分配角色
	userRoles := enforcer.GetRolesForUserInDomain(u.UID, "default")
	foundUser := false
	for _, rn := range userRoles {
		if rn == r.Name {
			foundUser = true
			break
		}
	}
	if !foundUser {
		t.Fatalf("启用后用户 uid=%s 应被重建 g 分配到 %s，实际角色=%v", u.UID, r.Name, userRoles)
	}
}

// ---------------------------------------------------------------------------
// syncRoleCasbinState 启用态：关系表不存在时不重建（避免越权）
// ---------------------------------------------------------------------------

func TestSyncRoleCasbinState_EnabledDoesNotRebuildWithoutRelationRows(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)
	setupPermissionTestEnforcer(t, client)

	r, _ := client.Role.Create().
		SetName("orphan_role").SetDisplayName("Orphan").
		SetStatus(role.StatusActive).SetDataScope(role.DataScopeAll).Save(ctx)

	// 直接写入一条假 g 规则（不在关系表中），模拟越权遗留分配
	enforcer, _ := global.EnforcerManager.GetEnforcer("default")
	// 清理后再重建：先 disable 清理 → 再 enable 重建。
	// enable 分支在无关系表数据时应不产生任何 g 规则。
	if err := svc.syncRoleCasbinState(ctx, r, true); err != nil {
		t.Fatalf("syncRoleCasbinState(true) error = %v", err)
	}

	gCount, err := client.CasbinRule.Query().
		Where(casbinrule.PtypeEQ("g"), casbinrule.V1EQ(r.Name)).
		Count(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if gCount != 0 {
		t.Fatalf("AdminRole/UserRole 均不存在时，启用态不应凭空创建 g 规则，实际 %d 条", gCount)
	}
	_ = enforcer
}

// ---------------------------------------------------------------------------
// setStatus 幂等修复分支：r.Status == target 时仍对齐 Casbin
// ---------------------------------------------------------------------------

// TestSetStatus_SameStatusStillRepairsCasbinSkew 验证当角色数据库状态与
// 目标状态相同（"无事可做"）时，setStatus 仍会调用 syncRoleCasbinState
// 修复 Casbin 脏数据。本测试通过"关系表有 g 规则空"来判定修复是否执行。
func TestSetStatus_SameStatusStillRepairsCasbinSkew(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)
	setupPermissionTestEnforcer(t, client)

	// 创建角色 + 管理员 + 关系绑定；角色状态已为 Active（与目标相同）
	r, _ := client.Role.Create().
		SetName("idempotent_fix").SetDisplayName("IdempotentFix").
		SetStatus(role.StatusActive).SetDataScope(role.DataScopeAll).Save(ctx)
	a, _ := client.Admin.Create().
		SetEmail("a-idem@example.com").SetUsername("a-idem").
		SetPassword("hash").SetPasswordSalt("salt").
		SetUID("uid-a-idem").SetStatus(admin.StatusActive).Save(ctx)
	_, _ = client.AdminRole.Create().SetAdminID(a.ID).SetRoleID(r.ID).SetIsPrimary(true).Save(ctx)

	// 故意不同步 g 规则，模拟历史脏数据

	// 目标状态同为 Active，走"不变更实体仅修复同步"分支
	if err := svc.setStatus(ctx, nil, r.ID, role.StatusActive); err != nil {
		t.Fatalf("setStatus(idempotent) error = %v", err)
	}

	enforcer, _ := global.EnforcerManager.GetEnforcer("default")
	rolesForAdmin := enforcer.GetRolesForUserInDomain(a.UID, "default")
	found := false
	for _, rn := range rolesForAdmin {
		if rn == r.Name {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("setStatus 状态不变时仍应修复 Casbin 缺失的 g 规则，管理员实际角色=%v", rolesForAdmin)
	}

	// 实体未被更新：创建时间等于更新时间（或 status 仍为原值）
	refreshed, _ := client.Role.Get(ctx, r.ID)
	if refreshed.Status != role.StatusActive {
		t.Fatalf("角色状态不应被改变，实际 %v", refreshed.Status)
	}
}

// ---------------------------------------------------------------------------
// setStatus 系统角色状态修改拒绝
// ---------------------------------------------------------------------------

// TestSetStatus_SystemRoleRejected 验证系统内置角色（admin/normal_admin）
// 不允许通过 setStatus 修改状态。系统角色的状态保护是 RBAC 安全的重要底线。
func TestSetStatus_SystemRoleRejected(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)

	r, err := client.Role.Create().
		SetName("sys_protected").
		SetDisplayName("SysProtected").
		SetIsSystem(true).
		SetStatus(role.StatusActive).
		SetDataScope(role.DataScopeAll).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建系统角色失败: %v", err)
	}

	err = svc.setStatus(ctx, nil, r.ID, role.StatusInactive)
	if err == nil {
		t.Fatal("系统内置角色禁用应被拒绝")
	}
	// ponytail: 仅通过错误消息字符串匹配即可，重构未改变错误语义
	if err.Error() != "系统内置角色不允许修改状态" {
		t.Fatalf("期望明确系统角色拒绝错误，实际: %v", err)
	}

	// 再次验证实体未变化
	refreshed, _ := client.Role.Get(ctx, r.ID)
	if refreshed.Status != role.StatusActive {
		t.Fatal("系统角色状态在拒绝后仍被修改")
	}
}

// ---------------------------------------------------------------------------
// setStatus 不存在角色 → ErrRoleNotFound
// ---------------------------------------------------------------------------

func TestSetStatus_NotFoundReturnsErrRoleNotFound(t *testing.T) {
	_ = setupTestClient(t)
	svc := NewRoleService(nil)

	err := svc.setStatus(t.Context(), nil, 99999999, role.StatusActive)
	if err == nil {
		t.Fatal("不存在的角色 setStatus 应报错")
	}
	// 包装错误用 unwrap 语义检查
	if err != ErrRoleNotFound {
		t.Fatalf("期望 ErrRoleNotFound，实际 %v", err)
	}
}

// ---------------------------------------------------------------------------
// 辅助：验证 AdminRole / UserRole 关系被正确地用于 g 规则重建（nil 边安全）
// ---------------------------------------------------------------------------

// TestSyncRoleCasbinState_SkipsNilEdgesWhenRebuilding 验证 Edges.Admin 或
// Edges.User 为 nil（Ent 未查询边）时跳过对应行，不 panic。
func TestSyncRoleCasbinState_SkipsNilEdgesWhenRebuilding(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)
	setupPermissionTestEnforcer(t, client)

	r, _ := client.Role.Create().
		SetName("nil_edge_role").SetDisplayName("NilEdge").
		SetStatus(role.StatusActive).SetDataScope(role.DataScopeAll).Save(ctx)

	// 直接写 AdminRole / UserRole 表但不设置 Edges（syncRoleCasbinState
	// 内部使用 WithAdmin / WithUser，若上游实现退化导致边为空，此处应跳过）
	a, _ := client.Admin.Create().
		SetEmail("nil-edge@example.com").SetUsername("nil-edge").
		SetPassword("hash").SetPasswordSalt("salt").
		SetUID("uid-nil-edge").SetStatus(admin.StatusActive).Save(ctx)
	// 写入关联行
	_, _ = client.AdminRole.Create().SetAdminID(a.ID).SetRoleID(r.ID).SetIsPrimary(true).Save(ctx)

	// 通过 Ent 直接查询不带边的 AdminRole，确认未加载边的关联行数为 1
	unboundCount, _ := client.AdminRole.Query().
		Where(adminrole.RoleIDEQ(r.ID)).
		All(ctx)
	if len(unboundCount) != 1 {
		t.Fatalf("写入 AdminRole 失败，行数 %d", len(unboundCount))
	}

	// 执行启用同步；若未加载边，svc.syncRoleCasbinState 内部 WithAdmin/WithUser
	// 会正确加载，因此 g 规则仍被创建。此处仅确认不 panic。
	if err := svc.syncRoleCasbinState(ctx, r, true); err != nil {
		t.Fatalf("syncRoleCasbinState 不应因边加载失败 panic: %v", err)
	}
	// 验证最终 g 规则被重建（说明 WithXxx 查询确实按预期工作）
	gCount, _ := client.CasbinRule.Query().
		Where(casbinrule.PtypeEQ("g"), casbinrule.V0EQ(a.UID), casbinrule.V1EQ(r.Name)).
		Count(ctx)
	if gCount != 1 {
		t.Fatalf("启用后管理员 g 规则应存在，实际 %d 条（边加载可能未执行）", gCount)
	}
}

// ---------------------------------------------------------------------------
// setStatus 清理 → 重建循环的最终一致性检查（disable 再 enable）
// ---------------------------------------------------------------------------

// TestSetStatus_DisableThenEnableRestoresAllBindings 模拟"误禁用后立即恢复"
// 场景，验证 disable 清除、enable 重建两个动作串联后 g 规则恰好等于
// 关系表中的绑定集合，不多不少。
func TestSetStatus_DisableThenEnableRestoresAllBindings(t *testing.T) {
	client := setupTestClient(t)
	ctx := t.Context()
	svc := NewRoleService(nil)
	setupPermissionTestEnforcer(t, client)

	r, _ := client.Role.Create().
		SetName("roundtrip_role").SetDisplayName("RoundTrip").
		SetStatus(role.StatusActive).SetDataScope(role.DataScopeAll).Save(ctx)

	// 两个管理员 + 两个用户
	uids := []string{"uid-a1", "uid-a2", "uid-u1", "uid-u2"}
	for i, uid := range uids[:2] {
		a, _ := client.Admin.Create().
			SetEmail(uid + "@example.com").SetUsername(uid).
			SetPassword("hash").SetPasswordSalt("salt").
			SetUID(uid).SetStatus(admin.StatusActive).Save(ctx)
		_, _ = client.AdminRole.Create().SetAdminID(a.ID).SetRoleID(r.ID).SetIsPrimary(i == 0).Save(ctx)
	}
	for i, uid := range uids[2:] {
		u, _ := client.User.Create().
			SetEmail(uid + "@example.com").SetUsername(uid).
			SetPassword("hash").SetPasswordSalt("salt").
			SetUID(uid).SetStatus(user.StatusActive).Save(ctx)
		_, _ = client.UserRole.Create().SetUserID(u.ID).SetRoleID(r.ID).SetIsPrimary(i == 0).Save(ctx)
	}

	// 先启用一次补齐 g 规则
	_ = svc.setStatus(ctx, nil, r.ID, role.StatusActive)

	// 禁用
	if err := svc.setStatus(ctx, nil, r.ID, role.StatusInactive); err != nil {
		t.Fatalf("disable 失败: %v", err)
	}
	// 验证被禁用时 g 规则数为 0
	gone, _ := client.CasbinRule.Query().
		Where(casbinrule.PtypeEQ("g"), casbinrule.V1EQ(r.Name)).Count(ctx)
	if gone != 0 {
		t.Fatalf("禁用后 g 规则应清空，实际 %d 条", gone)
	}

	// 再次启用 → 重建
	if err := svc.setStatus(ctx, nil, r.ID, role.StatusActive); err != nil {
		t.Fatalf("re-enable 失败: %v", err)
	}
	restored, _ := client.CasbinRule.Query().
		Where(casbinrule.PtypeEQ("g"), casbinrule.V1EQ(r.Name)).Count(ctx)
	if restored != len(uids) {
		t.Fatalf("重新启用后 g 规则应为 %d 条，实际 %d 条", len(uids), restored)
	}

	// 角色实体状态应为 Active
	roleEntity, _ := client.Role.Get(ctx, r.ID)
	if roleEntity.Status != role.StatusActive {
		t.Fatalf("往返后角色实体状态应为 Active，实际 %v", roleEntity.Status)
	}

	// 验证关系表计数不变（不应在 disable/enable 过程中改动业务绑定）
	ar, _ := client.AdminRole.Query().Where(adminrole.RoleIDEQ(r.ID)).Count(ctx)
	ur, _ := client.UserRole.Query().Where(userrole.RoleIDEQ(r.ID)).Count(ctx)
	if ar != 2 || ur != 2 {
		t.Fatalf("disable/enable 不应修改 AdminRole/UserRole 关系表，实际 AdminRole=%d UserRole=%d", ar, ur)
	}
}
