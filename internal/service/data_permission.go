package service

import (
	"context"
	"errors"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/adminrole"
	"go-admin/internal/ent/department"
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

// ErrDepartmentOutOfScope 目标部门超出当前操作者数据权限范围
var ErrDepartmentOutOfScope = errors.New("department out of data scope")

// ErrRoleScopeEscalation 目标角色的数据范围超出当前操作者
var ErrRoleScopeEscalation = errors.New("role data scope escalation")

// 注：ErrDepartmentNotFound 已在 department.go 中定义，复用同一错误值

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
	if scopeRank(next) > scopeRank(current) {
		return next
	}
	return current
}

// scopeRank 返回数据权限范围的"宽度"等级，与 mergeScope 共用同一张表
func scopeRank(s DataScope) int {
	switch s {
	case DataScopeSelf:
		return 1
	case DataScopeDepartment:
		return 2
	case DataScopeDepartmentAndChild:
		return 3
	case DataScopeCustom:
		return 4
	case DataScopeAll:
		return 5
	default:
		return 0 // 未知范围 fail-closed
	}
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

// ensureDepartmentInScope 校验目标部门是否在 op 的数据范围内
//
//	op == nil          → 放行（内部调用）
//	op.IsSuperAdmin    → 放行
//	DataScopeAll       → 放行
//	DataScopeSelf      → 拒绝
//	DataScopeDepartment/DepartmentAndChild/Custom：
//	                     targetDepartmentID == 0 → 拒绝（未分配仅超管可设）
//	                     其他要求命中 op.DepartmentIDs
//	未知 scope         → 拒绝（fail-closed）
func ensureDepartmentInScope(op *EffectiveDataScope, targetDepartmentID int) error {
	if op == nil || op.IsSuperAdmin {
		return nil
	}
	switch op.Scope {
	case DataScopeAll:
		return nil
	case DataScopeSelf:
		return ErrDepartmentOutOfScope
	case DataScopeDepartment, DataScopeDepartmentAndChild, DataScopeCustom:
		if targetDepartmentID == 0 {
			return ErrDepartmentOutOfScope
		}
		if !containsInt(op.DepartmentIDs, targetDepartmentID) {
			return ErrDepartmentOutOfScope
		}
		return nil
	default:
		return ErrDepartmentOutOfScope
	}
}

// ensureRolesNotEscalating 校验候选角色的 data_scope 不超过操作者
//   op == nil               → 放行（内部调用）
//   op.IsSuperAdmin || All  → 放行
//   非 All：要求 rank(role) ≤ rank(op)；Custom 角色还要求 role.DeptIDs ⊆ op.DeptIDs
//   未知 op.Scope           → 拒绝（fail-closed）
func ensureRolesNotEscalating(ctx context.Context, client *ent.Client, op *EffectiveDataScope, roleIDs []int) error {
	if op == nil || op.IsSuperAdmin || op.Scope == DataScopeAll {
		return nil
	}
	if len(roleIDs) == 0 {
		return nil
	}
	opRank := scopeRank(op.Scope)
	if opRank == 0 {
		return ErrRoleScopeEscalation
	}
	roles, err := client.Role.Query().Where(role.IDIn(roleIDs...)).All(ctx)
	if err != nil {
		return err
	}
	for _, r := range roles {
		rs := DataScope(r.DataScope)
		if scopeRank(rs) > opRank {
			return ErrRoleScopeEscalation
		}
		if rs == DataScopeCustom {
			for _, deptID := range r.DataScopeDepartmentIds {
				if !containsInt(op.DepartmentIDs, deptID) {
					return ErrRoleScopeEscalation
				}
			}
		}
	}
	return nil
}

// ensureDepartmentExists 校验目标部门真实存在
//   departmentID == 0 → 放行（仅超管/All 可设由 ensureDepartmentInScope 保证）
func ensureDepartmentExists(ctx context.Context, client *ent.Client, departmentID int) error {
	if departmentID == 0 {
		return nil
	}
	exists, err := client.Department.Query().Where(department.IDEQ(departmentID)).Exist(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return ErrDepartmentNotFound
	}
	return nil
}
