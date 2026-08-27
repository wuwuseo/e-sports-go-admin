package service

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/wuwuseo/cmf/cache"

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
	Domain        string
	AdminID       int
	AdminUID      string
	Username      string
	DepartmentID  int
	DepartmentIDs []int
	IsSuperAdmin  bool

	// departmentSet 是 DepartmentIDs 的集合索引，containsDepartment 惰性构建。
	// 非导出字段不参与缓存 JSON 序列化，命中缓存反序列化后自动重建
	departmentSet map[int]struct{}
}

// containsDepartment 判断部门是否在 DepartmentIDs 内。
// 惰性构建 set：单请求内首次判定 O(n) 构建，后续判定 O(1)，替代逐次线性扫描
func (s *EffectiveDataScope) containsDepartment(id int) bool {
	if s.departmentSet == nil && len(s.DepartmentIDs) > 0 {
		s.departmentSet = make(map[int]struct{}, len(s.DepartmentIDs))
		for _, d := range s.DepartmentIDs {
			s.departmentSet[d] = struct{}{}
		}
	}
	_, ok := s.departmentSet[id]
	return ok
}

// DataPermissionService 数据权限解析服务
type DataPermissionService struct {
	client            *ent.Client
	departmentService DepartmentServiceInterface
	cache             *cache.TypedCache[*EffectiveDataScope]
}

// NewDataPermissionService 创建数据权限解析服务
//
//	rawCache 可选注入共享缓存句柄，未注入（nil）时每次实时查库，兼容单测环境
func NewDataPermissionService(departmentService DepartmentServiceInterface, rawCache ...*cache.Cache[[]byte]) *DataPermissionService {
	var typedCache *cache.TypedCache[*EffectiveDataScope]
	if len(rawCache) > 0 && rawCache[0] != nil {
		typedCache = cache.NewTypedCache[*EffectiveDataScope](rawCache[0])
	}
	return &DataPermissionService{
		client:            global.ORM,
		departmentService: departmentService,
		cache:             typedCache,
	}
}

// deptTreeScopeVersion 部门树结构版本号：增删改/移动子树时递增，使所有数据范围缓存键整体失效。
//
// ponytail: 进程内变量，多实例部署下其他实例靠 30s TTL 自愈（与 casbin 无 watcher 的
// 最终一致模型对齐）；需要跨实例即时失效时，升级为版本号存共享缓存并拼入键
var deptTreeScopeVersion atomic.Int64

// InvalidateDataScopeByDeptTree 部门树结构变更后调用，使所有管理员的数据范围缓存立即失效。
// 部门增删改/移动会改变 DeptAndChild 角色的子树展开结果，无法按管理员精准定位，整体换键最简
func InvalidateDataScopeByDeptTree() {
	deptTreeScopeVersion.Add(1)
}

// dataScopeCacheKey 数据范围缓存键。数据范围与权限 domain 无关，仅按管理员 UID 区分；
// 键内拼入部门树版本号，部门结构变更后整体换键失效（旧键等待 TTL 自然回收）
func dataScopeCacheKey(uid string) string {
	return fmt.Sprintf("datascope:v%d:%s", deptTreeScopeVersion.Load(), uid)
}

// dataScopeCacheTTL 数据范围缓存过期时间。
// 角色分配/启停/范围变更、管理员部门变更、部门树结构调整均已挂失效钩子，
// 与 casbin g 规则重建同点执行，权限变更即时生效；TTL 仅作最后兜底（如多实例间版本号差异）
func dataScopeCacheTTL() time.Duration {
	return 30 * time.Second
}

// InvalidateAdminScope 失效指定管理员的数据范围缓存。
// 角色重分配、管理员部门变更后必须调用，确保权限变更即时生效而非等待 TTL 过期
func (s *DataPermissionService) InvalidateAdminScope(ctx context.Context, uids ...string) {
	if s.cache == nil {
		return
	}
	for _, uid := range uids {
		_ = s.cache.Delete(ctx, dataScopeCacheKey(uid))
	}
}

// ResolveAdminScope 解析管理员的有效数据权限范围（带缓存）
func (s *DataPermissionService) ResolveAdminScope(ctx context.Context, uid string) (*EffectiveDataScope, error) {
	if s.cache == nil {
		return s.resolveAdminScopeDB(ctx, uid)
	}
	key := dataScopeCacheKey(uid)
	if cached, err := s.cache.Get(ctx, key); err == nil && cached != nil {
		// 浅拷贝返回：调用方（如 NewOperatorContext 会写 Domain）不得污染缓存对象
		cp := *cached
		return &cp, nil
	}
	scope, err := s.resolveAdminScopeDB(ctx, uid)
	if err != nil {
		return nil, err
	}
	_ = s.cache.SetWithExpiration(ctx, key, scope, dataScopeCacheTTL())
	cp := *scope // 同样返回拷贝，写缓存后调用方的修改不回写缓存
	return &cp, nil
}

// resolveAdminScopeDB 实时查库解析管理员的有效数据权限范围
func (s *DataPermissionService) resolveAdminScopeDB(ctx context.Context, uid string) (*EffectiveDataScope, error) {
	a, err := s.client.Admin.Query().Where(admin.UIDEQ(uid), admin.StatusEQ(admin.StatusActive)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrDataPermissionAdminNotFound
		}
		return nil, err
	}
	// 超级管理员拥有全部数据权限
	if a.IsSuperAdmin {
		return &EffectiveDataScope{Scope: DataScopeAll, AdminID: a.ID, AdminUID: a.UID, Username: a.Username, DepartmentID: a.DepartmentID, IsSuperAdmin: true}, nil
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
		Username:     a.Username,
		DepartmentID: a.DepartmentID,
		IsSuperAdmin: false,
	}

	// 合并所有角色的数据权限范围，取最宽的范围
	for _, ar := range adminRoles {
		r := ar.Edges.Role
		if r == nil {
			continue
		}
		if r.Name == SuperAdminRoleName {
			return &EffectiveDataScope{Scope: DataScopeAll, AdminID: a.ID, AdminUID: a.UID, Username: a.Username, DepartmentID: a.DepartmentID, IsSuperAdmin: true}, nil
		}
		switch DataScope(r.DataScope) {
		case DataScopeAll:
			return &EffectiveDataScope{Scope: DataScopeAll, AdminID: a.ID, AdminUID: a.UID, Username: a.Username, DepartmentID: a.DepartmentID, IsSuperAdmin: false}, nil
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

// shouldBypassCheck 判断是否应跳过权限校验。
// allowAllScope=true 时，DataScopeAll 也跳过（用于角色相关校验）；
// allowAllScope=false 时，仅 nil 和超管跳过（DataScopeAll 由调用方的 switch 处理）。
func shouldBypassCheck(op *EffectiveDataScope, allowAllScope bool) bool {
	if op == nil || op.IsSuperAdmin {
		return true
	}
	if allowAllScope && op.Scope == DataScopeAll {
		return true
	}
	return false
}

// isDepartmentInScope 判断部门是否在操作者数据范围内。
// 调用前必须已通过 shouldBypassCheck 校验（op 非 nil 且非超管）。
func isDepartmentInScope(op *EffectiveDataScope, departmentID int) bool {
	switch op.Scope {
	case DataScopeAll:
		return true
	case DataScopeSelf:
		return false
	case DataScopeDepartment, DataScopeDepartmentAndChild, DataScopeCustom:
		if departmentID == 0 {
			return false
		}
		return op.containsDepartment(departmentID)
	default:
		return false
	}
}

// isCustomDeptSubset 判断 deptIDs 是否为 op.DepartmentIDs 的子集。
func isCustomDeptSubset(op *EffectiveDataScope, deptIDs []int) bool {
	for _, id := range deptIDs {
		if !op.containsDepartment(id) {
			return false
		}
	}
	return true
}

// NewOperatorContext 从 currentUID 构造请求操作者上下文。内部调用若需绕过，
// 应显式向服务方法传 nil，而不是借空 UID 隐式放行。
func (s *DataPermissionService) NewOperatorContext(ctx context.Context, currentUID string, domains ...string) (*EffectiveDataScope, error) {
	if currentUID == "" {
		return nil, ErrPermissionOperatorRequired
	}
	op, err := s.ResolveAdminScope(ctx, currentUID)
	if err != nil {
		return nil, err
	}
	domain := configuredPermissionDomain()
	if len(domains) > 0 {
		if domains[0] == "" {
			return nil, ErrPermissionOperatorRequired
		}
		domain = domains[0]
	}
	op.Domain = domain
	return op, nil
}

// ensureAdminInScope 校验 targetAdminID 是否在 op 的数据范围内
//
//	op == nil          → 放行（内部调用）
//	op.IsSuperAdmin    → 放行
//	target.UID == op.AdminUID → 放行（访问自己）
//	target.DepartmentID == 0  → 在 Department/DepartmentAndChild/Custom 模式下拒绝（未分配账号在部门模式下不可见）
//	其他               → 按 op.Scope 校验
func ensureAdminInScope(ctx context.Context, client *ent.Client, op *EffectiveDataScope, targetAdminID int) error {
	if shouldBypassCheck(op, false) {
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
	if !isDepartmentInScope(op, target.DepartmentID) {
		return ErrAdminOutOfScope
	}
	return nil
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
	if shouldBypassCheck(op, false) {
		return nil
	}
	if !isDepartmentInScope(op, targetDepartmentID) {
		return ErrDepartmentOutOfScope
	}
	return nil
}

// ensureRolesNotEscalating 校验候选角色的 data_scope 不超过操作者
//
//	op == nil               → 放行（内部调用）
//	op.IsSuperAdmin || All  → 放行
//	非 All：要求 rank(role) ≤ rank(op)；Custom 角色还要求 role.DeptIDs ⊆ op.DeptIDs
//	未知 op.Scope           → 拒绝（fail-closed）
func ensureRolesNotEscalating(ctx context.Context, client *ent.Client, op *EffectiveDataScope, roleIDs []int) error {
	if shouldBypassCheck(op, true) {
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
		if rs == DataScopeCustom && !isCustomDeptSubset(op, r.DataScopeDepartmentIds) {
			return ErrRoleScopeEscalation
		}
	}
	return nil
}

// ensureDepartmentExists 校验目标部门真实存在
//
//	departmentID == 0 → 放行（仅超管/All 可设由 ensureDepartmentInScope 保证）
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

// resolveRoleEffectiveDepartments 计算指定角色分配给目标管理员后目标登录时实际可见的部门集合
//
//	scopeIsAll=true 表示该角色让目标可看全部数据
//	targetAdminDepartmentID==0 时 Department/DepartmentAndChild 退化为空集
func resolveRoleEffectiveDepartments(
	ctx context.Context,
	deptSvc DepartmentServiceInterface,
	r *ent.Role,
	targetAdminDepartmentID int,
) (effective []int, scopeIsAll bool, err error) {
	switch DataScope(r.DataScope) {
	case DataScopeAll:
		return nil, true, nil
	case DataScopeSelf:
		return nil, false, nil
	case DataScopeDepartment:
		if targetAdminDepartmentID == 0 {
			return nil, false, nil
		}
		return []int{targetAdminDepartmentID}, false, nil
	case DataScopeDepartmentAndChild:
		if targetAdminDepartmentID == 0 {
			return nil, false, nil
		}
		ids, err := deptSvc.ChildIDs(ctx, targetAdminDepartmentID, true)
		if err != nil {
			return nil, false, err
		}
		return ids, false, nil
	case DataScopeCustom:
		return r.DataScopeDepartmentIds, false, nil
	default:
		return nil, false, ErrRoleScopeEscalation
	}
}

// ensureRolesNotEscalatingForTarget 校验候选角色分配给目标管理员后"有效部门集合" ⊆ op.DepartmentIDs
//
//	op == nil / IsSuperAdmin / op.Scope=All → 放行
//	scopeIsAll → 拒绝；effective 任一元素不在 op.DepartmentIDs → 拒绝
//	未知 op.Scope → 拒绝
func ensureRolesNotEscalatingForTarget(
	ctx context.Context,
	client *ent.Client,
	deptSvc DepartmentServiceInterface,
	op *EffectiveDataScope,
	roleIDs []int,
	targetAdminDepartmentID int,
) error {
	if shouldBypassCheck(op, true) {
		return nil
	}
	if len(roleIDs) == 0 {
		return nil
	}
	if scopeRank(op.Scope) == 0 {
		return ErrRoleScopeEscalation
	}
	roles, err := client.Role.Query().Where(role.IDIn(roleIDs...)).All(ctx)
	if err != nil {
		return err
	}
	opSet := make(map[int]struct{}, len(op.DepartmentIDs))
	for _, id := range op.DepartmentIDs {
		opSet[id] = struct{}{}
	}
	for _, r := range roles {
		effective, scopeIsAll, err := resolveRoleEffectiveDepartments(ctx, deptSvc, r, targetAdminDepartmentID)
		if err != nil {
			return err
		}
		if scopeIsAll {
			return ErrRoleScopeEscalation
		}
		for _, deptID := range effective {
			if _, ok := opSet[deptID]; !ok {
				return ErrRoleScopeEscalation
			}
		}
	}
	return nil
}

// ensureAdminRolesManageable applies both independent delegation constraints to
// an existing administrator: its roles must not expand beyond the operator's
// department data scope or API permission scope.
func ensureAdminRolesManageable(
	ctx context.Context,
	client *ent.Client,
	deptSvc DepartmentServiceInterface,
	op *EffectiveDataScope,
	targetAdminID, targetDepartmentID int,
) error {
	if op == nil || op.IsSuperAdmin {
		return nil
	}
	adminRoles, err := client.AdminRole.Query().
		Where(adminrole.AdminIDEQ(targetAdminID)).
		WithRole(func(q *ent.RoleQuery) {
			q.Where(role.StatusEQ(role.StatusActive))
		}).
		All(ctx)
	if err != nil {
		return err
	}
	roleIDs := make([]int, 0, len(adminRoles))
	for _, ar := range adminRoles {
		if ar.Edges.Role != nil {
			roleIDs = append(roleIDs, ar.Edges.Role.ID)
		}
	}
	if err := ensureRolesNotEscalatingForTarget(ctx, client, deptSvc, op, roleIDs, targetDepartmentID); err != nil {
		return err
	}
	return ensureRolePermissionsNotEscalating(ctx, client, op, op.AdminUID, permissionDomainForScope(op), roleIDs)
}

func adminInOperatorScope(op *EffectiveDataScope, target *ent.Admin) bool {
	if shouldBypassCheck(op, false) {
		return true
	}
	if target == nil {
		return false
	}
	if target.UID == op.AdminUID {
		return true
	}
	return isDepartmentInScope(op, target.DepartmentID)
}

func ensureRoleEffectiveDepartmentsInScope(
	ctx context.Context,
	deptSvc DepartmentServiceInterface,
	op *EffectiveDataScope,
	r *ent.Role,
	targetAdminDepartmentID int,
) error {
	effective, scopeIsAll, err := resolveRoleEffectiveDepartments(ctx, deptSvc, r, targetAdminDepartmentID)
	if err != nil {
		return err
	}
	if scopeIsAll {
		return ErrRoleScopeEscalation
	}
	for _, deptID := range effective {
		if !op.containsDepartment(deptID) {
			return ErrRoleScopeEscalation
		}
	}
	return nil
}

// ensureRoleManageableForAssignedAdmins 校验非超管修改角色时不会影响超出 op 范围的管理员。
// 角色是全局对象；更新已分配角色时，必须同时保证：
//  1. 当前所有管理员持有者都在 op 可管理范围内；
//  2. 角色当前 data_scope 对每个持有者展开后的有效部门集合仍在 op.DepartmentIDs 内；
//  3. 若本次修改 data_scope，新 data_scope 对每个持有者展开后也必须在 op.DepartmentIDs 内。
func ensureRoleManageableForAssignedAdmins(
	ctx context.Context,
	client *ent.Client,
	deptSvc DepartmentServiceInterface,
	op *EffectiveDataScope,
	currentRole *ent.Role,
	newScope DataScope,
	newDeptIDs []int,
	validateNewScope bool,
) error {
	if shouldBypassCheck(op, true) {
		return nil
	}
	if scopeRank(op.Scope) == 0 || currentRole == nil || deptSvc == nil {
		return ErrRoleScopeEscalation
	}
	holders, err := client.AdminRole.Query().
		Where(adminrole.RoleIDEQ(currentRole.ID)).
		WithAdmin().
		All(ctx)
	if err != nil {
		return err
	}
	var candidate *ent.Role
	if validateNewScope {
		candidate = &ent.Role{
			DataScope:              role.DataScope(newScope),
			DataScopeDepartmentIds: newDeptIDs,
		}
	}
	// 按持有者部门去重：ensureRoleEffectiveDepartmentsInScope 仅依赖 target.DepartmentID，
	// 相同部门持有者结论相同，避免逐人重复展开部门子树（N+1 查询）
	seenDept := make(map[int]struct{})
	for _, holder := range holders {
		target := holder.Edges.Admin
		if !adminInOperatorScope(op, target) {
			return ErrRoleScopeEscalation
		}
		if _, ok := seenDept[target.DepartmentID]; ok {
			continue
		}
		seenDept[target.DepartmentID] = struct{}{}
		if err := ensureRoleEffectiveDepartmentsInScope(ctx, deptSvc, op, currentRole, target.DepartmentID); err != nil {
			return err
		}
		if validateNewScope {
			if err := ensureRoleEffectiveDepartmentsInScope(ctx, deptSvc, op, candidate, target.DepartmentID); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensureRoleScopeWritable 校验非超管修改 role.data_scope 时不得超过自身 op 范围
//
//	op == nil / IsSuperAdmin / op.Scope=All → 放行
//	非 All：rank(newScope) ≤ rank(op.Scope)；Custom 还要求 newDeptIDs ⊆ op.DepartmentIDs
//	未知 newScope → 拒绝
//
// 说明：Role 字段写入阶段无"目标管理员"，Dept/DeptAndChild 用 rank 防御，
// 实际分配阶段由 ensureRolesNotEscalatingForTarget 兜底。
func ensureRoleScopeWritable(op *EffectiveDataScope, newScope DataScope, newDeptIDs []int) error {
	if shouldBypassCheck(op, true) {
		return nil
	}
	opRank := scopeRank(op.Scope)
	if opRank == 0 {
		return ErrRoleScopeEscalation
	}
	newRank := scopeRank(newScope)
	if newRank == 0 || newRank > opRank {
		return ErrRoleScopeEscalation
	}
	if newScope == DataScopeCustom && !isCustomDeptSubset(op, newDeptIDs) {
		return ErrRoleScopeEscalation
	}
	return nil
}
