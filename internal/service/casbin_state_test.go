// Copyright 2026 The go-admin Authors. All Rights Reserved.
//
// casbin_state_test.go 覆盖 internal/service/casbin_state.go 中的全局
// Casbin 辅助函数。该文件属于 fix(permission) 提交拆分出的新模块，
// 既承担 Casbin g 规则的角色级 / 用户级批改，也承担策略级 CRUD。
// 由于中间件和 service 层都依赖这些函数进行"先 reload 再 mutate"的串行
// 化操作，对应路径必须显式测试，避免未来重构破坏授权一致性。
package service

import (
	"errors"
	"strings"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"

	"github.com/casbin/casbin/v3"

	_ "github.com/mattn/go-sqlite3"
)

// newCasbinStateClient 构造同时初始化 ORM 与 EnforcerManager 的测试环境。
func newCasbinStateClient(t *testing.T) *ent.Client {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:casbin-state?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	t.Cleanup(func() { _ = client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
	setupPermissionTestEnforcer(t, client)
	return client
}

// getEnforcer 便捷函数，避免每个测试重复错误处理。
func getEnforcer(t *testing.T) *casbin.Enforcer {
	t.Helper()
	enforcer, err := global.EnforcerManager.GetEnforcer("default")
	if err != nil {
		t.Fatalf("get enforcer: %v", err)
	}
	return enforcer
}

// rolesForUser 安全地获取 Casbin g 规则。
func rolesForUser(enforcer *casbin.Enforcer, uid, domain string) []string {
	var roles []string
	global.WithCasbinRead(func() {
		roles = enforcer.GetRolesForUserInDomain(uid, domain)
	})
	return roles
}

// ---------------------------------------------------------------------------
// replaceCasbinUserRoles：删除并重建用户角色
// ---------------------------------------------------------------------------

// TestReplaceCasbinUserRolesOverwritesPreviousBindings 验证调用后用户只
// 持有传入的角色，原有角色被清空。这是 fix(permission) 中替换 delete+add
// 循环的关键路径。
func TestReplaceCasbinUserRolesOverwritesPreviousBindings(t *testing.T) {
	client := newCasbinStateClient(t)
	enforcer := getEnforcer(t)

	if _, err := enforcer.AddRoleForUserInDomain("uid-u1", "old-role", "default"); err != nil {
		t.Fatalf("seed old role: %v", err)
	}

	if err := replaceCasbinUserRoles(enforcer, "uid-u1", "default", []string{"new-a", "new-b"}); err != nil {
		t.Fatalf("replace: %v", err)
	}

	roles := rolesForUser(enforcer, "uid-u1", "default")
	if len(roles) != 2 {
		t.Fatalf("期望 2 个角色，实际 %v", roles)
	}
	for _, want := range []string{"new-a", "new-b"} {
		found := false
		for _, got := range roles {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("缺少角色 %q，got=%v", want, roles)
		}
	}
	for _, got := range roles {
		if got == "old-role" {
			t.Fatalf("旧角色未清理: %v", roles)
		}
	}
	_ = client
}

// TestReplaceCasbinUserRolesEmptyListClearsAll 验证传入空切片会清空所有
// 绑定（常用于角色降级场景）。
func TestReplaceCasbinUserRolesEmptyListClearsAll(t *testing.T) {
	newCasbinStateClient(t)
	enforcer := getEnforcer(t)

	for _, r := range []string{"a", "b", "c"} {
		if _, err := enforcer.AddRoleForUserInDomain("uid-empty", r, "default"); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if err := replaceCasbinUserRoles(enforcer, "uid-empty", "default", nil); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if roles := rolesForUser(enforcer, "uid-empty", "default"); len(roles) != 0 {
		t.Fatalf("期望清空，got %v", roles)
	}
}

// ---------------------------------------------------------------------------
// addCasbinUserRoles：仅追加，不影响既有角色
// ---------------------------------------------------------------------------

// TestAddCasbinUserRolesAppendsWithoutRemovingExisting 验证新增角色不会
// 覆盖已有角色，幂等（同角色多次添加不重复写入）。
func TestAddCasbinUserRolesAppendsWithoutRemovingExisting(t *testing.T) {
	newCasbinStateClient(t)
	enforcer := getEnforcer(t)

	if _, err := enforcer.AddRoleForUserInDomain("uid-add", "existing", "default"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := addCasbinUserRoles(enforcer, "uid-add", "default", []string{"new-role"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	roles := rolesForUser(enforcer, "uid-add", "default")
	if len(roles) != 2 {
		t.Fatalf("期望 existing + new-role，got %v", roles)
	}
	// 重复添加应保持集合稳定
	if err := addCasbinUserRoles(enforcer, "uid-add", "default", []string{"new-role"}); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	roles = rolesForUser(enforcer, "uid-add", "default")
	if len(roles) != 2 {
		t.Fatalf("重复添加应保持不变，got %v", roles)
	}
}

// ---------------------------------------------------------------------------
// removeCasbinUserRoles：清空所有
// ---------------------------------------------------------------------------

// TestRemoveCasbinUserRolesClearsAllBindings 验证调用后用户不再拥有
// 任何角色。
func TestRemoveCasbinUserRolesClearsAllBindings(t *testing.T) {
	newCasbinStateClient(t)
	enforcer := getEnforcer(t)

	for _, r := range []string{"alpha", "beta"} {
		if _, err := enforcer.AddRoleForUserInDomain("uid-rm", r, "default"); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if err := removeCasbinUserRoles(enforcer, "uid-rm", "default"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if roles := rolesForUser(enforcer, "uid-rm", "default"); len(roles) != 0 {
		t.Fatalf("期望清空，got %v", roles)
	}
}

// TestRemoveCasbinUserRolesIdempotent 验证对未持有任何角色的用户调用也
// 不应返回错误（与 enforcer.DeleteRolesForUserInDomain 行为一致）。
func TestRemoveCasbinUserRolesIdempotent(t *testing.T) {
	newCasbinStateClient(t)
	if err := removeCasbinUserRoles(getEnforcer(t), "uid-nonexistent", "default"); err != nil {
		t.Fatalf("idempotent remove failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// replaceCasbinRoleUsers：按角色重建用户列表
// ---------------------------------------------------------------------------

// TestReplaceCasbinRoleUsersReplacesMembership 验证旧成员被清空，新成员被加入。
func TestReplaceCasbinRoleUsersReplacesMembership(t *testing.T) {
	newCasbinStateClient(t)
	enforcer := getEnforcer(t)

	for _, uid := range []string{"old-1", "old-2"} {
		if _, err := enforcer.AddRoleForUserInDomain(uid, "team", "default"); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if err := replaceCasbinRoleUsers(enforcer, "team", "default", []string{"new-1"}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if roles := rolesForUser(enforcer, "old-1", "default"); len(roles) != 0 {
		t.Fatalf("old-1 未清理: %v", roles)
	}
	if roles := rolesForUser(enforcer, "old-2", "default"); len(roles) != 0 {
		t.Fatalf("old-2 未清理: %v", roles)
	}
	if roles := rolesForUser(enforcer, "new-1", "default"); len(roles) != 1 || roles[0] != "team" {
		t.Fatalf("new-1 未加入: %v", roles)
	}
}

// ---------------------------------------------------------------------------
// enforceCasbinWithRoles：先 sync 再 Enforce
// ---------------------------------------------------------------------------

// TestEnforceCasbinWithRolesAllowsAfterSync 验证通过该函数可以一次性完成
// "修复 g 规则 + Enforce" 流程，且新增角色后立即生效。
func TestEnforceCasbinWithRolesAllowsAfterSync(t *testing.T) {
	newCasbinStateClient(t)
	enforcer := getEnforcer(t)

	if _, err := enforcer.AddPolicy("editor", "default", "/articles", "GET"); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	// Casbin 内存中尚无 editor → 同步前 deny；同步后 allow
	allowed, err := enforceCasbinWithRoles(enforcer, "uid-auth", "default", "/articles", "GET", []string{"editor"})
	if err != nil {
		t.Fatalf("enforce: %v", err)
	}
	if !allowed {
		t.Fatal("同步后应允许访问 /articles GET")
	}
}

// TestEnforceCasbinWithRolesDeniesUnmatched 验证对未匹配的 obj/act 返回 false。
func TestEnforceCasbinWithRolesDeniesUnmatched(t *testing.T) {
	newCasbinStateClient(t)
	enforcer := getEnforcer(t)
	if _, err := enforcer.AddPolicy("editor", "default", "/articles", "GET"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	allowed, err := enforceCasbinWithRoles(enforcer, "uid-auth", "default", "/admin", "POST", []string{"editor"})
	if err != nil {
		t.Fatalf("enforce: %v", err)
	}
	if allowed {
		t.Fatal("无对应策略时必须 deny")
	}
}

// TestEnforceCasbinWithRolesRemovesStaleBinding 验证 stale 角色被清空。
func TestEnforceCasbinWithRolesRemovesStaleBinding(t *testing.T) {
	newCasbinStateClient(t)
	enforcer := getEnforcer(t)

	if _, err := enforcer.AddPolicy("editor", "default", "/articles", "GET"); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	// 预先塞入一个"虚假"角色
	if _, err := enforcer.AddRoleForUserInDomain("uid-stale", "ghost", "default"); err != nil {
		t.Fatalf("seed ghost: %v", err)
	}
	// desiredRoles 中不包含 ghost；同步后必须清空
	if _, err := enforceCasbinWithRoles(enforcer, "uid-stale", "default", "/articles", "GET", []string{"editor"}); err != nil {
		t.Fatalf("enforce: %v", err)
	}
	if roles := rolesForUser(enforcer, "uid-stale", "default"); len(roles) != 1 || roles[0] != "editor" {
		t.Fatalf("stale 角色未清空，got %v", roles)
	}
}

// ---------------------------------------------------------------------------
// mutateCasbinPolicy / addCasbinPolicy / removeCasbinPolicy
// ---------------------------------------------------------------------------

// TestMutateCasbinPolicyRequiresEnforcerManager 验证 EnforcerManager == nil
// 时直接返回错误，避免后续 nil 解引用。
func TestMutateCasbinPolicyRequiresEnforcerManager(t *testing.T) {
	prev := global.EnforcerManager
	global.EnforcerManager = nil
	t.Cleanup(func() { global.EnforcerManager = prev })

	_, err := addCasbinPolicy("default", []string{"r", "default", "/x", "GET"})
	if err == nil {
		t.Fatal("EnforcerManager 为 nil 时应返回错误")
	}
	if !strings.Contains(err.Error(), "EnforcerManager") {
		t.Fatalf("错误应提示 EnforcerManager：%v", err)
	}
}

// TestAddRemoveUpdateCasbinPolicyRoundTrip 验证增删改策略返回 changed=true
// 且 Enforce 行为符合预期。
func TestAddRemoveUpdateCasbinPolicyRoundTrip(t *testing.T) {
	newCasbinStateClient(t)
	enforcer, err := global.EnforcerManager.GetEnforcer("default")
	if err != nil {
		t.Fatalf("get enforcer: %v", err)
	}
	// 先建立 g 关系，否则 Enforce 时 g(r.sub, p.sub) 必为 false
	if _, err := enforcer.AddRoleForUserInDomain("anyone", "editor", "default"); err != nil {
		t.Fatalf("seed role binding: %v", err)
	}
	rule := []string{"editor", "default", "/api/test", "GET"}
	updated := []string{"editor", "default", "/api/test", "POST"}

	added, err := addCasbinPolicy("default", rule)
	if err != nil || !added {
		t.Fatalf("add: added=%v err=%v", added, err)
	}
	allowed, err := enforcer.Enforce("anyone", "default", "/api/test", "GET")
	if err != nil || !allowed {
		t.Fatalf("策略未生效：allowed=%v err=%v", allowed, err)
	}

	changed, err := updateCasbinPolicy("default", rule, updated)
	if err != nil || !changed {
		t.Fatalf("update: changed=%v err=%v", changed, err)
	}
	if allowed, _ := enforcer.Enforce("anyone", "default", "/api/test", "GET"); allowed {
		t.Fatal("旧策略未失效")
	}
	if allowed, _ := enforcer.Enforce("anyone", "default", "/api/test", "POST"); !allowed {
		t.Fatal("新策略未生效")
	}

	removed, err := removeCasbinPolicy("default", updated)
	if err != nil || !removed {
		t.Fatalf("remove: removed=%v err=%v", removed, err)
	}
	if allowed, _ := enforcer.Enforce("anyone", "default", "/api/test", "POST"); allowed {
		t.Fatal("删除后策略仍生效")
	}
}

// TestAddRemoveCasbinPoliciesBatch 验证批量增删多策略。
func TestAddRemoveCasbinPoliciesBatch(t *testing.T) {
	newCasbinStateClient(t)
	enforcer, err := global.EnforcerManager.GetEnforcer("default")
	if err != nil {
		t.Fatalf("get enforcer: %v", err)
	}
	// 建立 g 关系让 Enforce 通过
	if _, err := enforcer.AddRoleForUserInDomain("any", "a", "default"); err != nil {
		t.Fatalf("seed g(a): %v", err)
	}
	if _, err := enforcer.AddRoleForUserInDomain("any", "b", "default"); err != nil {
		t.Fatalf("seed g(b): %v", err)
	}
	rules := [][]string{
		{"a", "default", "/a", "GET"},
		{"b", "default", "/b", "GET"},
	}

	added, err := addCasbinPolicies("default", rules)
	if err != nil || !added {
		t.Fatalf("add batch: added=%v err=%v", added, err)
	}
	for _, r := range rules {
		if allowed, _ := enforcer.Enforce("any", "default", r[2], r[3]); !allowed {
			t.Fatalf("批量添加后策略 %v 未生效", r)
		}
	}

	removed, err := removeCasbinPolicies("default", rules)
	if err != nil || !removed {
		t.Fatalf("remove batch: removed=%v err=%v", removed, err)
	}
	for _, r := range rules {
		if allowed, _ := enforcer.Enforce("any", "default", r[2], r[3]); allowed {
			t.Fatalf("批量删除后策略 %v 仍生效", r)
		}
	}
}

// TestMutateCasbinPolicyPropagatesError 验证 mutate 闭包返回错误时，
// mutateCasbinPolicy 不会吞掉错误（关键 fail-closed 路径）。
func TestMutateCasbinPolicyPropagatesError(t *testing.T) {
	newCasbinStateClient(t)
	enforcer, err := global.EnforcerManager.GetEnforcer("default")
	if err != nil {
		t.Fatalf("get enforcer: %v", err)
	}

	sentinel := errors.New("mutate failed")
	_, err = mutateCasbinPolicy("default", func(e *casbin.Enforcer) (bool, error) {
		return false, sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("mutate 错误未传播：%v", err)
	}
	_ = enforcer
}
