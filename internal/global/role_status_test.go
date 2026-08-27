package global

import (
	"context"
	"sync"
	"testing"
	"time"

	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/role"

	_ "github.com/mattn/go-sqlite3"
)

// TestFilterActiveRoles_AllActive 验证所有角色都活跃时返回完整列表
func TestFilterActiveRoles_AllActive(t *testing.T) {
	InvalidateRoleStatusCache()
	// superAdminRoleName="admin" 硬编码为始终活跃
	roles, hasInactive := FilterActiveRoleNames(context.Background(), []string{"admin"})
	if hasInactive {
		t.Fatalf("admin 角色应视为活跃，但 hasInactive=true")
	}
	if len(roles) != 1 || roles[0] != "admin" {
		t.Fatalf("期望 [admin], 实际 %v", roles)
	}
}

// TestFilterActiveRoles_InactiveDetected 验证 ORM=nil(未初始化)
// 时，非 super-admin 角色都被视为禁用（fail-closed 防御）
func TestFilterActiveRoles_InactiveDetected(t *testing.T) {
	InvalidateRoleStatusCache()
	// global.ORM 为 nil 时，除 "admin" 外的角色查不到数据 => 视为禁用
	roles, hasInactive := FilterActiveRoleNames(context.Background(), []string{"admin", "editor", "viewer"})
	if !hasInactive {
		t.Fatalf("editor/viewer 在 ORM=nil 时应被识别为不活跃，但 hasInactive=false")
	}
	if len(roles) != 1 || roles[0] != "admin" {
		t.Fatalf("期望仅保留 admin, 实际 %v", roles)
	}
}

// TestFilterActiveRoles_EmptyInput 验证空输入安全
func TestFilterActiveRoles_EmptyInput(t *testing.T) {
	InvalidateRoleStatusCache()
	roles, hasInactive := FilterActiveRoleNames(context.Background(), nil)
	if hasInactive {
		t.Fatalf("空输入不应包含不活跃角色")
	}
	if len(roles) != 0 {
		t.Fatalf("期望空列表, 实际 %v", roles)
	}
	roles2, hasInactive2 := FilterActiveRoleNames(context.Background(), []string{})
	if hasInactive2 || len(roles2) != 0 {
		t.Fatalf("空切片输入行为不一致: roles=%v hasInactive=%v", roles2, hasInactive2)
	}
}

func TestFilterAssignedActiveRoleNamesRejectsStaleCasbinRole(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:assigned-role-filter?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	previousORM := ORM
	ORM = client
	t.Cleanup(func() { ORM = previousORM })
	InvalidateRoleStatusCache()

	ctx := t.Context()
	assignedRole := client.Role.Create().SetName("assigned-editor").SetDisplayName("Assigned editor").
		SetDataScope(role.DataScopeSelf).SetStatus(role.StatusActive).SaveX(ctx)
	staleRole := client.Role.Create().SetName("stale-power-role").SetDisplayName("Stale power role").
		SetDataScope(role.DataScopeAll).SetStatus(role.StatusActive).SaveX(ctx)
	a := client.Admin.Create().SetEmail("assigned-filter@example.com").SetUID("assigned-filter").
		SetUsername("assigned-filter").SetPassword("hash").SetStatus(admin.StatusActive).SaveX(ctx)
	client.AdminRole.Create().SetAdminID(a.ID).SetRoleID(assignedRole.ID).SaveX(ctx)

	roles, rejected, resolved := FilterAssignedActiveRoleNames(ctx, a.UID, []string{assignedRole.Name, staleRole.Name})
	if !resolved {
		t.Fatal("数据库角色绑定应成功解析")
	}
	if !rejected {
		t.Fatal("Casbin 中未绑定的陈旧角色必须被识别并拒绝")
	}
	if len(roles) != 1 || roles[0] != assignedRole.Name {
		t.Fatalf("只应保留数据库已绑定角色，got %v", roles)
	}
}

func TestFilterAssignedActiveRoleNamesReturnsMissingDatabaseRole(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:missing-runtime-role?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	previousORM := ORM
	ORM = client
	t.Cleanup(func() { ORM = previousORM })
	InvalidateRoleStatusCache()

	ctx := t.Context()
	r := client.Role.Create().SetName("db-only-role").SetDisplayName("DB only role").
		SetDataScope(role.DataScopeSelf).SetStatus(role.StatusActive).SaveX(ctx)
	a := client.Admin.Create().SetEmail("db-only@example.com").SetUID("db-only").
		SetUsername("db-only").SetPassword("hash").SetStatus(admin.StatusActive).SaveX(ctx)
	client.AdminRole.Create().SetAdminID(a.ID).SetRoleID(r.ID).SaveX(ctx)

	roles, differs, resolved := FilterAssignedActiveRoleNames(ctx, a.UID, nil)
	if !resolved || !differs || len(roles) != 1 || roles[0] != r.Name {
		t.Fatalf("缺失 g 时应返回数据库角色并标记不一致: roles=%v differs=%v resolved=%v", roles, differs, resolved)
	}
}

// TestRoleStatusCache_TTLExpiry 验证缓存条目过期后重新查询
func TestRoleStatusCache_TTLExpiry(t *testing.T) {
	shortTTL := 20 * time.Millisecond
	c := &roleStatusCache{
		m:   make(map[string]roleStatusEntry),
		ttl: shortTTL,
	}
	roleName := "expiring-role"
	_ = c.isActive(context.Background(), roleName)
	c.mu.RLock()
	entry, ok := c.m[roleName]
	c.mu.RUnlock()
	if !ok {
		t.Fatalf("缓存未写入 %s", roleName)
	}
	time.Sleep(shortTTL + 10*time.Millisecond)
	now := time.Now()
	if !now.After(entry.expireAt) {
		t.Fatalf("缓存条目应该已过期: expireAt=%v now=%v", entry.expireAt, now)
	}
	if c.isActive(context.Background(), roleName) {
		t.Fatalf("过期后重新查询，ORM=nil 时应返回 false")
	}
}

// TestRoleStatusCache_SuperAdminBypass 验证 admin 角色绕过缓存直接返回 true
func TestRoleStatusCache_SuperAdminBypass(t *testing.T) {
	c := &roleStatusCache{
		m:   make(map[string]roleStatusEntry),
		ttl: RoleStatusCacheTTL,
	}
	if !c.isActive(context.Background(), superAdminRoleName) {
		t.Fatalf("super-admin(%s) 必须始终为活跃状态", superAdminRoleName)
	}
}

// TestRoleStatusCache_CapacityEviction 验证缓存达到上限时整体重置
func TestRoleStatusCache_CapacityEviction(t *testing.T) {
	c := &roleStatusCache{
		m:   make(map[string]roleStatusEntry),
		ttl: RoleStatusCacheTTL,
	}
	for i := 0; i < roleStatusCacheMaxEntries; i++ {
		key := "role-" + string(rune('A'+i%26)) + "-" + string(rune(i))
		c.mu.Lock()
		c.m[key] = roleStatusEntry{active: true, expireAt: time.Now().Add(c.ttl)}
		c.mu.Unlock()
	}
	beforeSize := len(c.m)
	if beforeSize < roleStatusCacheMaxEntries {
		t.Fatalf("预填充大小不足: %d < %d", beforeSize, roleStatusCacheMaxEntries)
	}
	_ = c.isActive(context.Background(), "brand-new-role")
	c.mu.RLock()
	afterSize := len(c.m)
	c.mu.RUnlock()
	if afterSize != 1 {
		t.Fatalf("缓存达上限后应整体重置，新大小期望 1, 实际 %d", afterSize)
	}
}

// TestInvalidateRoleStatusCache_ClearsGlobal 验证 InvalidateRoleStatusCache 清空全局缓存
func TestInvalidateRoleStatusCache_ClearsGlobal(t *testing.T) {
	theRoleStatusCache.mu.Lock()
	theRoleStatusCache.m["dummy-role"] = roleStatusEntry{
		active:   true,
		expireAt: time.Now().Add(time.Hour),
	}
	sizeBefore := len(theRoleStatusCache.m)
	theRoleStatusCache.mu.Unlock()
	if sizeBefore == 0 {
		t.Fatalf("预填充失败")
	}

	InvalidateRoleStatusCache()

	theRoleStatusCache.mu.RLock()
	sizeAfter := len(theRoleStatusCache.m)
	theRoleStatusCache.mu.RUnlock()
	if sizeAfter != 0 {
		t.Fatalf("Invalidate 后缓存应为空, 实际大小=%d", sizeAfter)
	}
}

// TestRoleStatusCache_ConcurrentReads 验证并发读不会 panic（读锁正确性）
func TestRoleStatusCache_ConcurrentReads(t *testing.T) {
	c := &roleStatusCache{
		m:   make(map[string]roleStatusEntry),
		ttl: RoleStatusCacheTTL,
	}
	for i := 0; i < 10; i++ {
		_ = c.isActive(context.Background(), "role-"+string(rune('0'+i)))
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				_ = c.isActive(context.Background(), "role-0")
			}
		}()
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("并发读超时，疑似死锁")
	}
}
