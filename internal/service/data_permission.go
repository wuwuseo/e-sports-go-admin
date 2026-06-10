package service

import (
	"context"
	"errors"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/adminrole"
	"go-admin/internal/ent/role"
	"go-admin/internal/global"
)

// ErrDataPermissionAdminNotFound 数据权限解析时管理员不存在
var ErrDataPermissionAdminNotFound = errors.New("data permission admin not found")

// EffectiveDataScope 管理员的有效数据权限范围
type EffectiveDataScope struct {
	Scope         DataScope
	AdminID       int
	AdminUID      string
	DepartmentID  int
	DepartmentIDs []int
}

// DataPermissionService 数据权限解析服务
type DataPermissionService struct {
	client            *ent.Client
	departmentService DepartmentServiceInterface
}

// NewDataPermissionService 创建数据权限解析服务
func NewDataPermissionService(departmentService DepartmentServiceInterface) *DataPermissionService {
	return &DataPermissionService{
		client:            global.ORM,
		departmentService: departmentService,
	}
}

// ResolveAdminScope 解析管理员的有效数据权限范围
func (s *DataPermissionService) ResolveAdminScope(ctx context.Context, uid string) (*EffectiveDataScope, error) {
	a, err := s.client.Admin.Query().Where(admin.UIDEQ(uid)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrDataPermissionAdminNotFound
		}
		return nil, err
	}
	// 超级管理员拥有全部数据权限
	if a.IsSuperAdmin {
		return &EffectiveDataScope{Scope: DataScopeAll, AdminID: a.ID, AdminUID: a.UID, DepartmentID: a.DepartmentID}, nil
	}

	// 查询管理员关联的活跃角色
	adminRoles, err := s.client.AdminRole.Query().
		Where(adminrole.AdminIDEQ(a.ID)).
		WithRole(func(q *ent.RoleQuery) {
			q.Where(role.StatusEQ(role.StatusActive))
		}).
		All(ctx)
	if err != nil {
		return nil, err
	}

	result := &EffectiveDataScope{
		Scope:        DataScopeSelf,
		AdminID:      a.ID,
		AdminUID:     a.UID,
		DepartmentID: a.DepartmentID,
	}

	// 合并所有角色的数据权限范围，取最宽的范围
	for _, ar := range adminRoles {
		r := ar.Edges.Role
		if r == nil {
			continue
		}
		switch DataScope(r.DataScope) {
		case DataScopeAll:
			return &EffectiveDataScope{Scope: DataScopeAll, AdminID: a.ID, AdminUID: a.UID, DepartmentID: a.DepartmentID}, nil
		case DataScopeCustom:
			result.Scope = mergeScope(result.Scope, DataScopeCustom)
			result.DepartmentIDs = appendUniqueInts(result.DepartmentIDs, r.DataScopeDepartmentIds...)
		case DataScopeDepartmentAndChild:
			result.Scope = mergeScope(result.Scope, DataScopeDepartmentAndChild)
			if a.DepartmentID > 0 {
				ids, err := s.departmentService.ChildIDs(ctx, a.DepartmentID, true)
				if err != nil {
					return nil, err
				}
				result.DepartmentIDs = appendUniqueInts(result.DepartmentIDs, ids...)
			}
		case DataScopeDepartment:
			result.Scope = mergeScope(result.Scope, DataScopeDepartment)
			if a.DepartmentID > 0 {
				result.DepartmentIDs = appendUniqueInts(result.DepartmentIDs, a.DepartmentID)
			}
		}
	}

	return result, nil
}

// mergeScope 合并数据权限范围，取更宽的范围
func mergeScope(current, next DataScope) DataScope {
	rank := map[DataScope]int{
		DataScopeSelf:               1,
		DataScopeDepartment:         2,
		DataScopeDepartmentAndChild: 3,
		DataScopeCustom:             4,
		DataScopeAll:                5,
	}
	if rank[next] > rank[current] {
		return next
	}
	return current
}

// appendUniqueInts 向切片追加不重复的整数
func appendUniqueInts(base []int, values ...int) []int {
	seen := make(map[int]struct{}, len(base)+len(values))
	for _, id := range base {
		seen[id] = struct{}{}
	}
	for _, id := range values {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		base = append(base, id)
	}
	return base
}
