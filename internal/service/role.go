package service

import (
	"context"
	"errors"
	"fmt"

	"go-admin/internal/ent"
	"go-admin/internal/ent/adminrole"
	"go-admin/internal/ent/casbinrule"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/userrole"
	"go-admin/internal/global"
	"go-admin/internal/utils"

	"github.com/wuwuseo/cmf/cache"
)

var (
	// 角色相关错误
	ErrRoleNotFound        = errors.New("角色不存在")
	ErrRoleNameExists      = errors.New("角色名称已存在")
	ErrRoleInUse           = errors.New("角色正在使用中，无法删除")
	ErrSystemRoleProtected = errors.New("系统内置角色不可删除")
	ErrRoleDisabled        = errors.New("角色已禁用")
	ErrInvalidRoleName     = errors.New("角色名称无效")
	ErrInvalidRoleStatus   = errors.New("角色状态无效")
)

// DataScope 数据权限范围类型
type DataScope string

const (
	DataScopeAll                DataScope = "all"
	DataScopeSelf               DataScope = "self"
	DataScopeDepartment         DataScope = "department"
	DataScopeDepartmentAndChild DataScope = "department_and_child"
	DataScopeCustom             DataScope = "custom"
)

// ListAllRolesRequest 全量角色列表请求
type ListAllRolesRequest struct {
	Keyword string `query:"keyword" json:"keyword"`
	Status  string `query:"status" json:"status"`
}

// RoleServiceInterface 角色服务接口
type RoleServiceInterface interface {
	// 角色管理
	Create(ctx context.Context, op *EffectiveDataScope, req *CreateRoleRequest) (*RoleResponse, error)
	Get(ctx context.Context, id int, operators ...*EffectiveDataScope) (*RoleResponse, error)
	List(ctx context.Context, req *ListRolesRequest, operators ...*EffectiveDataScope) (*ListRolesResponse, error)
	ListAll(ctx context.Context, req *ListAllRolesRequest, operators ...*EffectiveDataScope) ([]*RoleResponse, error)
	Update(ctx context.Context, op *EffectiveDataScope, id int, req *UpdateRoleRequest) error
	Delete(ctx context.Context, op *EffectiveDataScope, id int) error

	// 角色状态管理
	Enable(ctx context.Context, op *EffectiveDataScope, id int) error
	Disable(ctx context.Context, op *EffectiveDataScope, id int) error
}

type RoleService struct {
	*CRUDBase
	cache   *cache.TypedCache[[]*PermissionResponse]
	deptSvc DepartmentServiceInterface
}

// NewRoleService 创建角色服务实例
func NewRoleService(rawCache *cache.Cache[[]byte]) *RoleService {
	return NewRoleServiceWithDepartmentService(rawCache, NewDepartmentService())
}

func NewRoleServiceWithDepartmentService(rawCache *cache.Cache[[]byte], deptSvc DepartmentServiceInterface) *RoleService {
	var typedCache *cache.TypedCache[[]*PermissionResponse]
	if rawCache != nil {
		typedCache = cache.NewTypedCache[[]*PermissionResponse](rawCache)
	}
	if deptSvc == nil {
		deptSvc = NewDepartmentService()
	}
	return &RoleService{
		CRUDBase: &CRUDBase{Client: global.ORM},
		cache:    typedCache,
		deptSvc:  deptSvc,
	}
}

// clearUserPermissionsByRoleID 清除拥有指定角色的所有用户权限缓存
// 同时覆盖普通用户（UserRole）与管理员（AdminRole）两类持有者
func (s *RoleService) clearUserPermissionsByRoleID(ctx context.Context, roleID int, domain string) {
	if s.cache == nil {
		return
	}
	userRoles, err := s.Client.UserRole.Query().
		Where(userrole.RoleIDEQ(roleID)).
		WithUser().
		All(ctx)
	if err != nil {
		return
	}
	for _, ur := range userRoles {
		if ur.Edges.User == nil {
			continue
		}
		uid := ur.Edges.User.UID
		_ = s.cache.Delete(ctx, fmt.Sprintf("user:menu:%s:%s", uid, domain))
		_ = s.cache.Delete(ctx, fmt.Sprintf("user:button:%s:%s", uid, domain))
		_ = s.cache.Delete(ctx, fmt.Sprintf("user:api:%s:%s", uid, domain))
	}

	adminRoles, err := s.Client.AdminRole.Query().
		Where(adminrole.RoleIDEQ(roleID)).
		WithAdmin().
		All(ctx)
	if err != nil {
		return
	}
	for _, ar := range adminRoles {
		if ar.Edges.Admin == nil {
			continue
		}
		uid := ar.Edges.Admin.UID
		_ = s.cache.Delete(ctx, fmt.Sprintf("user:menu:%s:%s", uid, domain))
		_ = s.cache.Delete(ctx, fmt.Sprintf("user:button:%s:%s", uid, domain))
		_ = s.cache.Delete(ctx, fmt.Sprintf("user:api:%s:%s", uid, domain))
		// 数据范围缓存一并失效：角色启停/data_scope 变更处已同步 casbin，数据范围同样须即时生效
		_ = s.cache.Delete(ctx, dataScopeCacheKey(uid))
	}
}

func ensureRoleNotHeldByOperator(ctx context.Context, client *ent.Client, op *EffectiveDataScope, roleID int) error {
	if op == nil || op.IsSuperAdmin || op.AdminID == 0 {
		return nil
	}
	exists, err := client.AdminRole.Query().
		Where(adminrole.AdminIDEQ(op.AdminID), adminrole.RoleIDEQ(roleID)).
		Exist(ctx)
	if err != nil {
		return err
	}
	if exists {
		return ErrCannotModifySelfRoles
	}
	return nil
}

// CreateRoleRequest 创建角色请求
type CreateRoleRequest struct {
	Name                   string `json:"name" validate:"required,max=100"`
	DisplayName            string `json:"display_name" validate:"required,max=255"`
	Description            string `json:"description" validate:"max=500"`
	CreatedBy              string `json:"created_by,omitempty"`
	DataScope              string `json:"data_scope"`
	DataScopeDepartmentIDs []int  `json:"data_scope_department_ids"`
}

// UpdateRoleRequest 更新角色请求
type UpdateRoleRequest struct {
	DisplayName            string `json:"display_name" validate:"max=255"`
	Description            string `json:"description" validate:"max=500"`
	UpdatedBy              string `json:"updated_by,omitempty"`
	DataScope              string `json:"data_scope"`
	DataScopeDepartmentIDs []int  `json:"data_scope_department_ids"`
}

// RoleResponse 角色响应
type RoleResponse struct {
	ID                     int    `json:"id"`
	Name                   string `json:"name"`
	DisplayName            string `json:"display_name"`
	Description            string `json:"description"`
	IsSystem               bool   `json:"is_system"`
	Status                 string `json:"status"`
	CreatedBy              string `json:"created_by,omitempty"`
	UpdatedBy              string `json:"updated_by,omitempty"`
	CreatedAt              string `json:"created_at"`
	UpdatedAt              string `json:"updated_at"`
	DataScope              string `json:"data_scope"`
	DataScopeDepartmentIDs []int  `json:"data_scope_department_ids"`
}

// ListRolesRequest 角色列表请求
type ListRolesRequest struct {
	PaginationRequest
	Keyword string `query:"keyword" json:"keyword"`
	Status  string `query:"status" json:"status"`
}

// ListRolesResponse 角色列表响应
type ListRolesResponse = ListResponse[*RoleResponse]

// CreateRole 创建角色
func (s *RoleService) Create(ctx context.Context, op *EffectiveDataScope, req *CreateRoleRequest) (*RoleResponse, error) {
	if req.Name == "" {
		return nil, ErrInvalidRoleName
	}
	if len(req.Name) > 100 {
		return nil, fmt.Errorf("角色名称不能超过100个字符")
	}
	if req.DisplayName == "" {
		return nil, fmt.Errorf("显示名称不能为空")
	}

	exists, err := s.Client.Role.Query().
		Where(role.NameEQ(req.Name)).
		Exist(ctx)
	if err != nil {
		return nil, fmt.Errorf("检查角色名称失败: %w", err)
	}
	if exists {
		return nil, ErrRoleNameExists
	}

	builder := s.Client.Role.Create().
		SetName(req.Name).
		SetDisplayName(req.DisplayName).
		SetStatus(role.StatusActive)

	if req.Description != "" {
		builder.SetDescription(req.Description)
	}
	if req.CreatedBy != "" {
		builder.SetCreatedBy(req.CreatedBy)
	}

	dataScope := req.DataScope
	if dataScope == "" {
		dataScope = string(DataScopeSelf)
	}
	// P1-A 防御：非超管不得创建超过自身范围的角色
	if err := ensureRoleScopeWritable(op, DataScope(dataScope), req.DataScopeDepartmentIDs); err != nil {
		return nil, err
	}
	// P2 防御：当数据范围为自定义时，校验所有指定部门 ID 真实存在，
	// 防止脏数据写入导致后续数据权限失效或越权
	if dataScope == string(DataScopeCustom) && len(req.DataScopeDepartmentIDs) > 0 {
		for _, deptID := range req.DataScopeDepartmentIDs {
			if err := ensureDepartmentExists(ctx, s.Client, deptID); err != nil {
				return nil, err
			}
		}
	}
	builder.SetDataScope(role.DataScope(dataScope))
	builder.SetDataScopeDepartmentIds(req.DataScopeDepartmentIDs)

	r, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("创建角色失败: %w", err)
	}

	return s.toRoleResponse(r), nil
}

// Get 获取角色详情
func (s *RoleService) Get(ctx context.Context, id int, operators ...*EffectiveDataScope) (*RoleResponse, error) {
	r, err := s.Client.Role.Query().Where(role.IDEQ(id)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrRoleNotFound
		}
		return nil, fmt.Errorf("获取角色失败: %w", err)
	}
	if len(operators) > 0 && !roleVisibleToOperator(operators[0], r) {
		return nil, ErrRoleScopeEscalation
	}
	return s.toRoleResponse(r), nil
}

// ListRoles 获取角色列表
func (s *RoleService) List(ctx context.Context, req *ListRolesRequest, operators ...*EffectiveDataScope) (*ListRolesResponse, error) {
	req.Page, req.PageSize = utils.NormalizePagination(req.Page, req.PageSize)

	query := s.Client.Role.Query()

	if req.Keyword != "" {
		query = query.Where(
			role.Or(
				role.NameContains(req.Keyword),
				role.DisplayNameContains(req.Keyword),
				role.DescriptionContains(req.Keyword),
			),
		)
	}

	if req.Status != "" {
		query = query.Where(role.StatusEQ(role.Status(req.Status)))
	}

	roles, err := query.Order(ent.Desc(role.FieldCreateTime)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取角色列表失败: %w", err)
	}
	if len(operators) > 0 {
		roles = filterRolesForOperator(roles, operators[0])
	}
	total := len(roles)
	if req.PageSize > 0 {
		start := (req.Page - 1) * req.PageSize
		if start >= len(roles) {
			roles = nil
		} else {
			roles = roles[start:min(start+req.PageSize, len(roles))]
		}
	}

	return BuildList(roles, total, req.Page, s.toRoleResponse), nil
}

// ListAllRoles 获取所有角色（不带分页）
func (s *RoleService) ListAll(ctx context.Context, req *ListAllRolesRequest, operators ...*EffectiveDataScope) ([]*RoleResponse, error) {
	query := s.Client.Role.Query().Where(role.NameNEQ(superAdminRoleName))

	if req.Keyword != "" {
		query = query.Where(
			role.Or(
				role.NameContains(req.Keyword),
				role.DisplayNameContains(req.Keyword),
				role.DescriptionContains(req.Keyword),
			),
		)
	}

	if req.Status != "" {
		query = query.Where(role.StatusEQ(role.Status(req.Status)))
	}

	roles, err := query.
		Order(ent.Desc(role.FieldCreateTime)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取角色列表失败: %w", err)
	}
	if len(operators) > 0 {
		roles = filterRolesForOperator(roles, operators[0])
	}

	data := make([]*RoleResponse, 0, len(roles))
	for _, r := range roles {
		data = append(data, s.toRoleResponse(r))
	}

	return data, nil
}

func filterRolesForOperator(roles []*ent.Role, op *EffectiveDataScope) []*ent.Role {
	filtered := make([]*ent.Role, 0, len(roles))
	for _, r := range roles {
		if roleVisibleToOperator(op, r) {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

func roleVisibleToOperator(op *EffectiveDataScope, r *ent.Role) bool {
	if op == nil || op.IsSuperAdmin {
		return true
	}
	if r == nil || r.Name == SuperAdminRoleName {
		return false
	}
	return ensureRoleScopeWritable(op, DataScope(r.DataScope), r.DataScopeDepartmentIds) == nil
}

// UpdateRole 更新角色
func (s *RoleService) Update(ctx context.Context, op *EffectiveDataScope, id int, req *UpdateRoleRequest) error {
	r, err := s.Client.Role.Query().
		Where(role.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrRoleNotFound
		}
		return fmt.Errorf("查询角色失败: %w", err)
	}
	if err := ensureRoleNotHeldByOperator(ctx, s.Client, op, id); err != nil {
		return err
	}

	if r.IsSystem {
		if req.DisplayName != "" && req.DisplayName != r.DisplayName {
			return fmt.Errorf("系统内置角色不允许修改显示名称")
		}
	}

	if err := ensureRoleScopeWritable(op, DataScope(r.DataScope), r.DataScopeDepartmentIds); err != nil {
		return err
	}
	if err := ensureRoleManageableForAssignedAdmins(ctx, s.Client, s.deptSvc, op, r, DataScope(req.DataScope), req.DataScopeDepartmentIDs, req.DataScope != ""); err != nil {
		return err
	}

	update := s.Client.Role.UpdateOne(r)

	if req.DisplayName != "" {
		update.SetDisplayName(req.DisplayName)
	}
	if req.Description != "" {
		update.SetDescription(req.Description)
	}
	if req.UpdatedBy != "" {
		update.SetUpdatedBy(req.UpdatedBy)
	}
	if req.DataScope != "" {
		// P1-A 防御：非超管不得修改 role.data_scope 至超过自身范围
		if err := ensureRoleScopeWritable(op, DataScope(req.DataScope), req.DataScopeDepartmentIDs); err != nil {
			return err
		}
		// P2 防御：更新为自定义范围时，校验所有指定部门 ID 真实存在，
		// 防止脏数据写入导致后续数据权限失效或越权
		if req.DataScope == string(DataScopeCustom) && len(req.DataScopeDepartmentIDs) > 0 {
			for _, deptID := range req.DataScopeDepartmentIDs {
				if err := ensureDepartmentExists(ctx, s.Client, deptID); err != nil {
					return err
				}
			}
		}
		update.SetDataScope(role.DataScope(req.DataScope))
		update.SetDataScopeDepartmentIds(req.DataScopeDepartmentIDs)
	}

	_, err = update.Save(ctx)
	if err != nil {
		return fmt.Errorf("更新角色失败: %w", err)
	}

	s.clearUserPermissionsByRoleID(ctx, id, permissionDomainForScope(op))

	return nil
}

// DeleteRole 删除角色
func (s *RoleService) Delete(ctx context.Context, op *EffectiveDataScope, id int) error {
	r, err := s.Client.Role.Query().
		Where(role.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrRoleNotFound
		}
		return fmt.Errorf("查询角色失败: %w", err)
	}
	if err := ensureRoleNotHeldByOperator(ctx, s.Client, op, id); err != nil {
		return err
	}

	if r.IsSystem {
		return ErrSystemRoleProtected
	}
	if err := ensureRoleScopeWritable(op, DataScope(r.DataScope), r.DataScopeDepartmentIds); err != nil {
		return err
	}
	if err := ensureRoleManageableForAssignedAdmins(ctx, s.Client, s.deptSvc, op, r, "", nil, false); err != nil {
		return err
	}

	inUse, err := s.Client.UserRole.Query().
		Where(userrole.RoleIDEQ(id)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("检查角色使用情况失败: %w", err)
	}
	if inUse {
		return ErrRoleInUse
	}
	adminInUse, err := s.Client.AdminRole.Query().
		Where(adminrole.RoleIDEQ(id)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("检查管理员角色使用情况失败: %w", err)
	}
	if adminInUse {
		return ErrRoleInUse
	}

	domain := permissionDomainForScope(op)
	tx, err := s.Client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("开启删除角色事务失败: %w", err)
	}
	if _, err = tx.CasbinRule.Delete().Where(casbinrule.Or(
		casbinrule.And(casbinrule.PtypeEQ("p"), casbinrule.V0EQ(r.Name), casbinrule.V1EQ(domain)),
		casbinrule.And(casbinrule.PtypeEQ("g"), casbinrule.V1EQ(r.Name), casbinrule.V2EQ(domain)),
	)).Exec(ctx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("清理角色 Casbin 策略失败: %w", err)
	}
	if err = tx.Role.DeleteOneID(r.ID).Exec(ctx); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("删除角色失败: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("提交删除角色事务失败: %w", err)
	}
	global.InvalidateRoleStatusCache()
	if global.EnforcerManager != nil {
		enforcer, enforcerErr := global.EnforcerManager.GetEnforcer(domain)
		if enforcerErr != nil {
			return fmt.Errorf("角色已删除但获取 Casbin 执行器失败: %w", enforcerErr)
		}
		if enforcerErr = global.ReloadCasbinPolicy(domain, enforcer); enforcerErr != nil {
			return fmt.Errorf("角色已删除但刷新 Casbin 策略失败: %w", enforcerErr)
		}
	}
	return nil
}

// EnableRole 启用角色
func (s *RoleService) Enable(ctx context.Context, op *EffectiveDataScope, id int) error {
	return s.setStatus(ctx, op, id, role.StatusActive)
}

// DisableRole 禁用角色
func (s *RoleService) Disable(ctx context.Context, op *EffectiveDataScope, id int) error {
	return s.setStatus(ctx, op, id, role.StatusInactive)
}

// syncRoleCasbinState 按角色状态对齐 Casbin g 规则
// 禁用 -> 删除该角色全部 g 关联（enforce 立即失效）；启用 -> 按现存绑定重建 g。
// 先全量清理再按需重建，天然幂等并可修复历史不同步的脏数据。
func (s *RoleService) syncRoleCasbinState(ctx context.Context, r *ent.Role, active bool, domains ...string) error {
	if global.EnforcerManager == nil {
		// 单元测试等未初始化 Casbin 的环境跳过同步
		return nil
	}
	domain := configuredPermissionDomain()
	if len(domains) > 0 && domains[0] != "" {
		domain = domains[0]
	}
	enforcer, err := global.EnforcerManager.GetEnforcer(domain)
	if err != nil {
		return fmt.Errorf("获取权限执行器失败: %w", err)
	}
	userIDs := make([]string, 0)
	if !active {
		if err := replaceCasbinRoleUsers(enforcer, r.Name, domain, userIDs); err != nil {
			return fmt.Errorf("清理角色 Casbin 关联失败: %w", err)
		}
		return nil
	}
	adminRoles, err := s.Client.AdminRole.Query().
		Where(adminrole.RoleIDEQ(r.ID)).
		WithAdmin().
		All(ctx)
	if err != nil {
		return fmt.Errorf("查询角色持有管理员失败: %w", err)
	}
	for _, ar := range adminRoles {
		if ar.Edges.Admin == nil {
			continue
		}
		userIDs = append(userIDs, ar.Edges.Admin.UID)
	}
	userRoles, err := s.Client.UserRole.Query().
		Where(userrole.RoleIDEQ(r.ID)).
		WithUser().
		All(ctx)
	if err != nil {
		return fmt.Errorf("查询角色持有用户失败: %w", err)
	}
	for _, ur := range userRoles {
		if ur.Edges.User == nil {
			continue
		}
		userIDs = append(userIDs, ur.Edges.User.UID)
	}
	if err := replaceCasbinRoleUsers(enforcer, r.Name, domain, userIDs); err != nil {
		return fmt.Errorf("重建角色 Casbin 关联失败: %w", err)
	}
	return nil
}

func (s *RoleService) setStatus(ctx context.Context, op *EffectiveDataScope, id int, target role.Status) error {
	r, err := s.Client.Role.Query().
		Where(role.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrRoleNotFound
		}
		return fmt.Errorf("查询角色失败: %w", err)
	}
	if err := ensureRoleNotHeldByOperator(ctx, s.Client, op, id); err != nil {
		return err
	}

	if r.IsSystem {
		return fmt.Errorf("系统内置角色不允许修改状态")
	}

	if err := ensureRoleScopeWritable(op, DataScope(r.DataScope), r.DataScopeDepartmentIds); err != nil {
		return err
	}
	if err := ensureRoleManageableForAssignedAdmins(ctx, s.Client, s.deptSvc, op, r, "", nil, false); err != nil {
		return err
	}
	if r.Status == target {
		// 状态未变化时仍对齐 Casbin，但必须先通过与真实状态变更相同的范围校验。
		global.InvalidateRoleStatusCache()
		s.clearUserPermissionsByRoleID(ctx, id, permissionDomainForScope(op))
		return s.syncRoleCasbinState(ctx, r, target == role.StatusActive, permissionDomainForScope(op))
	}

	action := "启用"
	if target == role.StatusInactive {
		action = "禁用"
	}
	if _, err := s.Client.Role.UpdateOne(r).SetStatus(target).Save(ctx); err != nil {
		return fmt.Errorf("%s角色失败: %w", action, err)
	}
	// DB 状态已经生效；先清缓存再同步 Casbin，确保后续同步失败也不会继续使用旧授权。
	global.InvalidateRoleStatusCache()
	s.clearUserPermissionsByRoleID(ctx, id, permissionDomainForScope(op))

	if err := s.syncRoleCasbinState(ctx, r, target == role.StatusActive, permissionDomainForScope(op)); err != nil {
		return fmt.Errorf("%s角色已生效但权限同步失败，请重试以完成同步: %w", action, err)
	}

	return nil
}

// toRoleResponse 将Ent实体转换为响应格式
func (s *RoleService) toRoleResponse(r *ent.Role) *RoleResponse {
	return &RoleResponse{
		ID:                     r.ID,
		Name:                   r.Name,
		DisplayName:            r.DisplayName,
		Description:            r.Description,
		IsSystem:               r.IsSystem,
		Status:                 string(r.Status),
		CreatedBy:              r.CreatedBy,
		UpdatedBy:              r.UpdatedBy,
		CreatedAt:              r.CreateTime.Format("2006-01-02 15:04:05"),
		UpdatedAt:              r.UpdateTime.Format("2006-01-02 15:04:05"),
		DataScope:              string(r.DataScope),
		DataScopeDepartmentIDs: r.DataScopeDepartmentIds,
	}
}
