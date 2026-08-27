package global

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/adminrole"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/user"
	"go-admin/internal/ent/userrole"
)

// roleStatusEntry 角色状态缓存条目
type roleStatusEntry struct {
	active   bool
	expireAt time.Time
}

// roleStatusCache 进程内角色状态缓存（权限防御纵深）。
// 即使 Casbin g 规则因异常未同步，权限中间件也按 DB 中角色状态过滤，
// 禁用/已删除角色的权限不参与判定（fail-closed）。
type roleStatusCache struct {
	mu  sync.RWMutex
	m   map[string]roleStatusEntry
	ttl time.Duration
}

const (
	// RoleStatusCacheTTL 角色状态缓存时长
	RoleStatusCacheTTL = 30 * time.Second
	// roleStatusCacheMaxEntries 缓存容量上限，超限时整体重建防止膨胀
	roleStatusCacheMaxEntries = 4096
	// superAdminRoleName 系统超级管理员角色名，不可禁用，恒为活跃
	superAdminRoleName = "admin"
)

var theRoleStatusCache = &roleStatusCache{
	m:   make(map[string]roleStatusEntry),
	ttl: RoleStatusCacheTTL,
}

// isActive 判断角色在 DB 中是否处于启用状态（带 TTL 缓存；角色不存在视为禁用）
func (c *roleStatusCache) isActive(ctx context.Context, roleName string) bool {
	if roleName == superAdminRoleName {
		return true
	}
	now := time.Now()
	c.mu.RLock()
	entry, ok := c.m[roleName]
	c.mu.RUnlock()
	if ok && now.Before(entry.expireAt) {
		return entry.active
	}

	active := false
	if ORM != nil {
		exists, err := ORM.Role.Query().
			Where(role.NameEQ(roleName), role.StatusEQ(role.StatusActive)).
			Exist(ctx)
		if err == nil && exists {
			active = true
		}
	}

	c.mu.Lock()
	if len(c.m) >= roleStatusCacheMaxEntries {
		c.m = make(map[string]roleStatusEntry)
	}
	c.m[roleName] = roleStatusEntry{active: active, expireAt: now.Add(c.ttl)}
	c.mu.Unlock()
	return active
}

// RoleStatusIsActive 判断角色在 DB 中是否处于启用状态（带 TTL 缓存；角色不存在视为禁用）
func RoleStatusIsActive(ctx context.Context, roleName string) bool {
	return theRoleStatusCache.isActive(ctx, roleName)
}

// FilterActiveRoleNames 按角色状态过滤，返回活跃角色列表及是否存在非活跃角色
func FilterActiveRoleNames(ctx context.Context, roleNames []string) (active []string, hasInactive bool) {
	active = make([]string, 0, len(roleNames))
	for _, name := range roleNames {
		if RoleStatusIsActive(ctx, name) {
			active = append(active, name)
		} else {
			hasInactive = true
		}
	}
	return active, hasInactive
}

// FilterAssignedActiveRoleNames resolves current active database assignments
// and reports whether Casbin's derived g roles differ. Any lookup failure
// rejects all roles so stale Casbin state cannot authorize.
func FilterAssignedActiveRoleNames(ctx context.Context, uid string, roleNames []string) (active []string, differs, resolved bool) {
	assigned, err := assignedRoleNames(ctx, uid)
	if err != nil {
		return []string{}, true, false
	}
	active = make([]string, 0, len(assigned))
	activeSet := make(map[string]struct{}, len(assigned))
	for name := range assigned {
		if RoleStatusIsActive(ctx, name) {
			active = append(active, name)
			activeSet[name] = struct{}{}
		}
	}
	sort.Strings(active)
	runtimeSet := make(map[string]struct{}, len(roleNames))
	for _, name := range roleNames {
		runtimeSet[name] = struct{}{}
	}
	if len(runtimeSet) != len(activeSet) {
		return active, true, true
	}
	for name := range activeSet {
		if _, ok := runtimeSet[name]; !ok {
			return active, true, true
		}
	}
	return active, false, true
}

func assignedRoleNames(ctx context.Context, uid string) (map[string]struct{}, error) {
	if ORM == nil || uid == "" {
		return nil, errors.New("role assignment source unavailable")
	}
	a, err := ORM.Admin.Query().Where(admin.UIDEQ(uid), admin.StatusEQ(admin.StatusActive)).Only(ctx)
	if err == nil {
		assigned := make(map[string]struct{})
		if a.IsSuperAdmin {
			assigned[superAdminRoleName] = struct{}{}
		}
		rows, err := ORM.AdminRole.Query().
			Where(adminrole.AdminIDEQ(a.ID)).
			WithRole().
			All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.Edges.Role != nil {
				assigned[row.Edges.Role.Name] = struct{}{}
			}
		}
		return assigned, nil
	}
	if !ent.IsNotFound(err) {
		return nil, err
	}

	u, err := ORM.User.Query().Where(user.UIDEQ(uid), user.StatusEQ(user.StatusActive)).Only(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := ORM.UserRole.Query().
		Where(userrole.UserIDEQ(u.ID)).
		WithRole().
		All(ctx)
	if err != nil {
		return nil, err
	}
	assigned := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if row.Edges.Role != nil {
			assigned[row.Edges.Role.Name] = struct{}{}
		}
	}
	return assigned, nil
}

// InvalidateRoleStatusCache 供角色状态/删除/分配变更路径主动失效缓存
func InvalidateRoleStatusCache() {
	theRoleStatusCache.mu.Lock()
	theRoleStatusCache.m = make(map[string]roleStatusEntry)
	theRoleStatusCache.mu.Unlock()
}
