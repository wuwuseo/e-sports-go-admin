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

// ErrAdminOutOfScope 目标管理员超出当前操作者数据权限范围
var ErrAdminOutOfScope = errors.New("admin out of data scope")

// ErrCannotToggleSelfStatus 禁止启用/禁用自己
var ErrCannotToggleSelfStatus = errors.New("cannot activate or deactivate yourself")

// ErrCannotModifySelfRoles 禁止修改自己的角色
var ErrCannotModifySelfRoles = errors.New("cannot modify your own roles")

// EffectiveDataScope 管理员的有效数据权限范围
type EffectiveDataScope struct {
	Scope         DataScope
	AdminID       int
	AdminUID      string
	DepartmentID  int
	DepartmentIDs []int
	IsSuperAdmin  bool
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
		return &EffectiveDataScope{Scope: DataScopeAll, AdminID: a.ID, AdminUID: a.UID, DepartmentID: a.DepartmentID, IsSuperAdmin: true}, nil
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
		IsSuperAdmin: false,
	}

	// 合并所有角色的数据权限范围，取最宽的范围
	for _, ar := range adminRoles {
		r := ar.Edges.Role
		if r == nil {
			continue
		}
		switch DataScope(r.DataScope) {
		case DataScopeAll:
			return &EffectiveDataScope{Scope: DataScopeAll, AdminID: a.ID, AdminUID: a.UID, DepartmentID: a.DepartmentID, IsSuperAdmin: false}, nil
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

// containsInt 判断切片中是否包含目标整数
func containsInt(haystack []int, needle int) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

// NewOperatorContext 从 currentUID 构造操作者上下文
// currentUID 为空时返回 nil（内部调用绕过校验）
func (s *DataPermissionService) NewOperatorContext(ctx context.Context, currentUID string) (*EffectiveDataScope, error) {
	if currentUID == "" {
		return nil, nil
	}
	return s.ResolveAdminScope(ctx, currentUID)
}

// ensureAdminInScope 校验 targetAdminID 是否在 op 的数据范围内
//   op == nil          → 放行（内部调用）
//   op.IsSuperAdmin    → 放行
//   target.UID == op.AdminUID → 放行（访问自己）
//   target.DepartmentID == 0  → 在 Department/DepartmentAndChild/Custom 模式下拒绝（未分配账号在部门模式下不可见）
//   其他               → 按 op.Scope 校验
func ensureAdminInScope(ctx context.Context, client *ent.Client, op *EffectiveDataScope, targetAdminID int) error {
	if op == nil || op.IsSuperAdmin {
		return nil
	}
	target, err := client.Admin.Query().Where(admin.IDEQ(targetAdminID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrAdminNotFound
		}
		return err
	}
	if target.UID == op.AdminUID {
		return nil
	}
	switch op.Scope {
	case DataScopeAll:
		return nil
	case DataScopeSelf:
		// Self 已在上方 target.UID == op.AdminUID 短路放行；走到这里说明 target 不是自己，直接拒绝
		return ErrAdminOutOfScope
	case DataScopeDepartment, DataScopeDepartmentAndChild, DataScopeCustom:
		if target.DepartmentID == 0 {
			return ErrAdminOutOfScope
		}
		if !containsInt(op.DepartmentIDs, target.DepartmentID) {
			return ErrAdminOutOfScope
		}
		return nil
	default:
		// 未知 DataScope 默认拒绝，避免未来新增枚举值时 fail-open
		return ErrAdminOutOfScope
	}
}
