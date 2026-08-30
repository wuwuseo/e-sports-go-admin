package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/adminrole"
	"go-admin/internal/ent/casbinrule"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/systempermission"
	"go-admin/internal/ent/user"
	"go-admin/internal/ent/userrole"
	"go-admin/internal/global"
	"go-admin/internal/utils"

	"github.com/wuwuseo/cmf/cache"
)

var (
	// 权限相关错误
	ErrPermissionNotFound                  = errors.New("权限不存在")
	ErrInvalidPermissionType               = errors.New("无效的权限类型")
	ErrPermissionAlreadyExists             = errors.New("权限已存在")
	ErrInvalidPermissionRequest            = errors.New("无效的权限请求")
	ErrSuperAdminProtected                 = errors.New("超级管理员角色不允许修改权限")
	ErrSelfRolePermissionMutationForbidden = errors.New("不允许修改自己所属角色的权限")
	ErrPermissionOperatorRequired          = errors.New("permission operator identity required")
	ErrPermissionScopeEscalation           = errors.New("permission scope escalation forbidden")
)

// SuperAdminRoleName 超级管理员角色名称，禁止通过 API 修改其权限
const SuperAdminRoleName = "admin"

// isSuperAdminRole 判断是否为超级管理员角色
func isSuperAdminRole(roleName string) bool {
	return roleName == SuperAdminRoleName
}

// SensitivePermissionObjects 高风险权限 API 对象路径，非超级管理员不可见
//
// 安全说明：cron-jobs 管理面可创建 curl 型定时任务发起服务端 HTTP 请求，
// 属高危 SSRF 载体，故全部 API 仅限超级管理员。既有部署重启时，
// init.go 的 cleanupNormalAdminSeededExcludedPermissions 会自动清除
// 历史播种到 normal_admin 的 cron 规则。
var SensitivePermissionObjects = map[string]struct{}{
	// 权限定义 CRUD
	"/api/v1/admin/auth/system-permissions":      {},
	"/api/v1/admin/auth/system-permissions/{id}": {},
	"/api/v1/admin/auth/system-permissions/tree": {},
	// 角色权限保存/分配/移除
	"/api/v1/admin/auth/roles/{id}/permissions":   {},
	"/api/v1/admin/auth/roles/permissions/assign": {},
	"/api/v1/admin/auth/roles/permissions/remove": {},
	// 权限 CRUD（Casbin 规则）
	"/api/v1/admin/auth/permissions":        {},
	"/api/v1/admin/auth/permissions/detail": {},
	"/api/v1/admin/auth/permissions/all":    {},
	// OAuth client credentials and redirect configuration.
	"/api/v1/admin/auth/oauth/providers":            {},
	"/api/v1/admin/auth/oauth/providers/{provider}": {},
	// 定时任务管理（curl 型任务可发起服务端请求，禁止普通管理员持有）
	"/api/v1/admin/auth/cron-jobs":                      {},
	"/api/v1/admin/auth/cron-jobs/{id}":                 {},
	"/api/v1/admin/auth/cron-jobs/{id}/run":             {},
	"/api/v1/admin/auth/cron-jobs/{id}/enable":          {},
	"/api/v1/admin/auth/cron-jobs/{id}/disable":         {},
	"/api/v1/admin/auth/cron-jobs/{id}/logs":            {},
	"/api/v1/admin/auth/cron-jobs/{id}/logs/tail":       {},
	"/api/v1/admin/auth/cron-jobs/{id}/logs/{filename}": {},
	// 任务队列管理（手动入队可触发任意已注册任务，禁止普通管理员持有）
	"/api/v1/admin/auth/queue-jobs":                     {},
	"/api/v1/admin/auth/queue-jobs/stats":               {},
	"/api/v1/admin/auth/queue-jobs/handlers":            {},
	"/api/v1/admin/auth/queue-jobs/{queue}/{id}":        {},
	"/api/v1/admin/auth/queue-jobs/{queue}/{id}/retry":  {},
	"/api/v1/admin/auth/queue-jobs/{queue}/{id}/cancel": {},
	// 任务队列监控页（asynqmon 只读 UI，与队列管理同基线）
	"/api/v1/admin/auth/queue-monitor": {},
}

// IsSensitivePermissionObject 判断给定路径是否为高风险权限 API
func IsSensitivePermissionObject(path string) bool {
	_, ok := SensitivePermissionObjects[path]
	return ok
}

func sensitivePermissionObjectList() []string {
	objects := make([]string, 0, len(SensitivePermissionObjects))
	for object := range SensitivePermissionObjects {
		objects = append(objects, object)
	}
	sort.Strings(objects)
	return objects
}

type permissionSystemOperatorContextKey struct{}

func withPermissionSystemOperator(ctx context.Context) context.Context {
	return context.WithValue(ctx, permissionSystemOperatorContextKey{}, true)
}

func isPermissionSystemOperator(ctx context.Context) bool {
	trusted, _ := ctx.Value(permissionSystemOperatorContextKey{}).(bool)
	return trusted
}

func requirePermissionOperator(ctx context.Context, operatorUID string) error {
	if operatorUID != "" || isPermissionSystemOperator(ctx) {
		return nil
	}
	return ErrPermissionOperatorRequired
}

type operatorPermissionScope struct {
	includeAll bool
	lookup     map[string]struct{}
}

func (s *PermissionService) getOperatorPermissionScope(ctx context.Context, operatorUID, domain string) (*operatorPermissionScope, error) {
	if operatorUID == "" {
		return &operatorPermissionScope{includeAll: true}, nil
	}

	roleNames, err := s.getEffectivePermissionRoleNames(ctx, operatorUID, domain)
	if err != nil {
		return nil, err
	}
	for _, roleName := range roleNames {
		if isSuperAdminRole(roleName) {
			return &operatorPermissionScope{includeAll: true}, nil
		}
	}
	if len(roleNames) == 0 {
		return &operatorPermissionScope{lookup: map[string]struct{}{}}, nil
	}

	query := s.client.CasbinRule.Query().
		Where(
			casbinrule.PtypeEQ("p"),
			casbinrule.V0In(roleNames...),
		)
	if domain != "" {
		query = query.Where(casbinrule.V1EQ(domain))
	}

	rules, err := query.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("get operator permissions failed: %w", err)
	}

	lookup := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		permType := PermissionType(rule.V4)
		if !isValidPermissionType(permType) {
			permType = determinePermissionType(rule.V3)
		}
		addPermissionLookupKeys(lookup, string(permType), rule.V2, rule.V3)
		if rule.V2 == "*" && rule.V3 == "*" {
			for _, wildcardType := range []PermissionType{PermissionTypeAPI, PermissionTypeMenu, PermissionTypeButton} {
				addPermissionLookupKeys(lookup, string(wildcardType), rule.V2, rule.V3)
			}
		}
	}
	return &operatorPermissionScope{lookup: lookup}, nil
}

// getEffectivePermissionRoleNames uses active database role relations as the
// authorization source of truth. Casbin g rules are a derived runtime index and
// may temporarily retain a role after a failed synchronization.
func (s *PermissionService) getEffectivePermissionRoleNames(ctx context.Context, operatorUID, domain string) ([]string, error) {
	return s.getUserRoleNames(ctx, operatorUID)
}

func (s *PermissionService) ensureOperatorCanManagePermissions(ctx context.Context, operatorUID, domain string, permissions []PermissionRule) error {
	if err := requirePermissionOperator(ctx, operatorUID); err != nil {
		return err
	}

	scope, err := s.getOperatorPermissionScope(ctx, operatorUID, domain)
	if err != nil {
		return err
	}
	for _, perm := range permissions {
		if !scope.allowsRule(perm) {
			return ErrPermissionScopeEscalation
		}
	}
	return nil
}

// ensureRolePermissionsNotEscalating requires every API/menu/button permission
// of the delegated roles to already belong to the operator. Department data
// scope and API permission scope are independent, so DataScopeAll does not
// bypass this check.
func ensureRolePermissionsNotEscalating(
	ctx context.Context,
	client *ent.Client,
	op *EffectiveDataScope,
	operatorUID, domain string,
	roleIDs []int,
) error {
	if op == nil || op.IsSuperAdmin || len(roleIDs) == 0 {
		return nil
	}
	if operatorUID == "" {
		return ErrRoleScopeEscalation
	}

	scope, err := (&PermissionService{client: client}).getOperatorPermissionScope(ctx, operatorUID, domain)
	if err != nil {
		return err
	}
	roles, err := client.Role.Query().Where(role.IDIn(roleIDs...)).All(ctx)
	if err != nil {
		return err
	}
	roleNames := make([]string, 0, len(roles))
	for _, r := range roles {
		roleNames = append(roleNames, r.Name)
	}
	if len(roleNames) == 0 {
		return nil
	}

	query := client.CasbinRule.Query().Where(
		casbinrule.PtypeEQ("p"),
		casbinrule.V0In(roleNames...),
	)
	if domain != "" {
		query = query.Where(casbinrule.V1EQ(domain))
	}
	rules, err := query.All(ctx)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if !scope.allowsRule(PermissionRule{Object: rule.V2, Action: rule.V3}) {
			return ErrRoleScopeEscalation
		}
	}
	return nil
}

func (scope *operatorPermissionScope) allowsRule(rule PermissionRule) bool {
	if scope == nil || scope.includeAll {
		return true
	}
	permType := string(determinePermissionType(rule.Action))
	for _, object := range []string{rule.Object, "*"} {
		for _, action := range []string{rule.Action, "*"} {
			for _, key := range permissionLookupKeys(permType, object, action) {
				if _, ok := scope.lookup[key]; ok {
					return true
				}
			}
		}
	}
	return false
}

func (scope *operatorPermissionScope) allowsSystemPermission(p *ent.SystemPermission) bool {
	if scope == nil || scope.includeAll {
		return true
	}
	switch p.Type {
	case string(PermissionTypeAPI):
		return scope.allowsRule(PermissionRule{Object: p.Path, Action: p.Method})
	case string(PermissionTypeMenu), string(PermissionTypeButton):
		return scope.allowsRule(PermissionRule{Object: systemPermissionObject(p), Action: p.Type})
	default:
		return false
	}
}

func addPermissionLookupKeys(lookup map[string]struct{}, permType, object, action string) {
	for _, key := range permissionLookupKeys(permType, object, action) {
		lookup[key] = struct{}{}
	}
}

func permissionLookupKeys(permType, object, action string) []string {
	keys := []string{permissionLookupKey(permType, object, action)}
	switch PermissionType(permType) {
	case PermissionTypeAPI:
		upperAction := strings.ToUpper(action)
		if upperAction != action {
			keys = append(keys, permissionLookupKey(permType, object, upperAction))
		}
	case PermissionTypeMenu:
		keys = append(keys,
			permissionLookupKey(permType, object, string(PermissionTypeMenu)),
			permissionLookupKey(permType, object, "MENU"),
		)
	case PermissionTypeButton:
		keys = append(keys,
			permissionLookupKey(permType, object, string(PermissionTypeButton)),
			permissionLookupKey(permType, object, "BUTTON"),
		)
	}
	return keys
}

// isUserSuperAdmin 检查用户是否拥有超级管理员角色（包级别辅助函数）
func isUserSuperAdmin(ctx context.Context, client *ent.Client, userUID string) (bool, error) {
	if userUID == "" {
		return false, nil
	}
	// 先查 admin 表
	a, err := client.Admin.Query().Where(admin.UIDEQ(userUID)).Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return false, fmt.Errorf("获取管理员信息失败: %w", err)
	}
	if err == nil {
		if a.IsSuperAdmin {
			return true, nil
		}
		adminRoles, err := client.AdminRole.Query().
			Where(adminrole.AdminIDEQ(a.ID)).
			WithRole(func(q *ent.RoleQuery) {
				q.Where(role.NameEQ(SuperAdminRoleName), role.StatusEQ(role.StatusActive))
			}).
			All(ctx)
		if err != nil {
			return false, fmt.Errorf("获取管理员角色失败: %w", err)
		}
		for _, ar := range adminRoles {
			if ar.Edges.Role != nil {
				return true, nil
			}
		}
		return false, nil
	}
	// 再查 user 表
	u, err := client.User.Query().Where(user.UIDEQ(userUID)).Only(ctx)
	if err != nil {
		return false, nil
	}
	userRoles, err := client.UserRole.Query().
		Where(userrole.UserIDEQ(u.ID)).
		WithRole(func(q *ent.RoleQuery) {
			q.Where(role.NameEQ(SuperAdminRoleName), role.StatusEQ(role.StatusActive))
		}).
		All(ctx)
	if err != nil {
		return false, fmt.Errorf("获取用户角色失败: %w", err)
	}
	for _, ur := range userRoles {
		if ur.Edges.Role != nil {
			return true, nil
		}
	}
	return false, nil
}

// PermissionType 权限类型
type PermissionType string

const (
	// PermissionTypeAPI API权限
	PermissionTypeAPI PermissionType = "api"
	// PermissionTypeMenu 菜单权限
	PermissionTypeMenu PermissionType = "menu"
	// PermissionTypeButton 按钮权限
	PermissionTypeButton PermissionType = "button"
)

// PermissionRule 权限规则
type PermissionRule struct {
	Object string `json:"object" validate:"required"` // API路径、菜单路径或按钮标识
	Action string `json:"action" validate:"required"` // HTTP方法、menu或button
}

// CreatePermissionRequest 创建权限请求
type CreatePermissionRequest struct {
	RoleName    string         `json:"role_name" validate:"required"`
	Domain      string         `json:"domain" validate:"required"`
	Type        PermissionType `json:"type" validate:"required,oneof=api menu button"`
	Object      string         `json:"object" validate:"required"`
	Action      string         `json:"action" validate:"required"`
	Description string         `json:"description"`
}

// UpdatePermissionRequest 更新权限请求
type UpdatePermissionRequest struct {
	RoleName    string         `json:"role_name" validate:"required"`
	Domain      string         `json:"domain" validate:"required"`
	Type        PermissionType `json:"type" validate:"required,oneof=api menu button"`
	Object      string         `json:"object" validate:"required"`
	Action      string         `json:"action" validate:"required"`
	NewObject   string         `json:"new_object"`
	NewAction   string         `json:"new_action"`
	Description string         `json:"description"`
}

// ListPermissionsRequest 权限列表请求
type ListPermissionsRequest struct {
	PaginationRequest
	Keyword  string         `query:"keyword" json:"keyword"`
	Type     PermissionType `query:"type" json:"type"`
	Domain   string         `query:"domain" json:"domain"`
	RoleName string         `query:"role_name" json:"role_name"`
}

// PermissionResponse 权限响应
type PermissionResponse struct {
	ID          int            `json:"id"`
	RoleName    string         `json:"role_name"`
	Domain      string         `json:"domain"`
	Type        PermissionType `json:"type"`
	Object      string         `json:"object"`
	Action      string         `json:"action"`
	Description string         `json:"description"`
}

// ListPermissionsResponse 权限列表响应
type ListPermissionsResponse = ListResponse[*PermissionResponse]

// SaveRolePermissionsRequest 保存角色权限请求
type SaveRolePermissionsRequest struct {
	Domain      string           `json:"domain" validate:"required"`
	Permissions []PermissionRule `json:"permissions" validate:"required"`
}

// PermissionServiceInterface 权限服务接口
type PermissionServiceInterface interface {
	// 权限管理
	CreatePermission(ctx context.Context, operatorUID string, req *CreatePermissionRequest, operators ...*EffectiveDataScope) error
	UpdatePermission(ctx context.Context, operatorUID string, req *UpdatePermissionRequest, operators ...*EffectiveDataScope) error
	DeletePermission(ctx context.Context, operatorUID string, roleName, domain, object, action string, operators ...*EffectiveDataScope) error
	GetPermission(ctx context.Context, roleName, domain, object, action string, operators ...*EffectiveDataScope) (*PermissionResponse, error)
	ListPermissions(ctx context.Context, operatorUID string, req *ListPermissionsRequest, operators ...*EffectiveDataScope) (*ListPermissionsResponse, error)
	ListAllPermissions(ctx context.Context, operatorUID string, keyword string, permType PermissionType, domain string, roleName string, operators ...*EffectiveDataScope) ([]*PermissionResponse, error)

	// 角色权限管理
	AssignPermissionsToRole(ctx context.Context, operatorUID string, roleName, domain string, permissions []PermissionRule, operators ...*EffectiveDataScope) error
	RemovePermissionsFromRole(ctx context.Context, operatorUID string, roleName, domain string, permissions []PermissionRule, operators ...*EffectiveDataScope) error
	GetRolePermissions(ctx context.Context, roleName, domain string, operators ...*EffectiveDataScope) ([]*PermissionResponse, error)
	GetRolePermissionsByRoleID(ctx context.Context, roleID int, domain string, operators ...*EffectiveDataScope) ([]*PermissionResponse, error)
	SaveRolePermissions(ctx context.Context, operatorUID string, roleID int, req *SaveRolePermissionsRequest, operators ...*EffectiveDataScope) error

	// 用户权限获取
	GetUserMenuPermissions(ctx context.Context, userID string, domain string) ([]*PermissionResponse, error)
	GetUserMenuPermissionTree(ctx context.Context, userID string, domain string) ([]*SystemPermissionTreeNode, error)
	GetUserPermissionTree(ctx context.Context, userID string, domain string) ([]*SystemPermissionTreeNode, error)
	GetUserButtonPermissions(ctx context.Context, userID string, domain string) ([]*PermissionResponse, error)
	GetUserAPIPermissions(ctx context.Context, userID string, domain string) ([]*PermissionResponse, error)
	GetUserRoleNames(ctx context.Context, userUID string) ([]string, error)

	// 权限验证
	CheckPermission(ctx context.Context, userID string, domain, object, action string) (bool, error)

	// 检查用户是否拥有指定角色
	CheckUserHasRole(ctx context.Context, userUID string, domain string, roleName string) (bool, error)
}

// PermissionService 权限服务实现
type PermissionService struct {
	client *ent.Client
	cache  *cache.TypedCache[[]*PermissionResponse]
}

// NewPermissionService 创建权限服务实例
func NewPermissionService(rawCache *cache.Cache[[]byte]) *PermissionService {
	var typedCache *cache.TypedCache[[]*PermissionResponse]
	if rawCache != nil {
		typedCache = cache.NewTypedCache[[]*PermissionResponse](rawCache)
	}
	return &PermissionService{
		client: global.ORM,
		cache:  typedCache,
	}
}

func permissionDataScope(operators []*EffectiveDataScope) *EffectiveDataScope {
	if len(operators) == 0 {
		return nil
	}
	return operators[0]
}

func (s *PermissionService) ensurePermissionRoleInScope(ctx context.Context, roleName string, operators []*EffectiveDataScope) error {
	op := permissionDataScope(operators)
	if op == nil || op.IsSuperAdmin {
		return nil
	}
	r, err := s.client.Role.Query().Where(role.NameEQ(roleName)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrRoleNotFound
		}
		return err
	}
	if !roleVisibleToOperator(op, r) {
		return ErrRoleScopeEscalation
	}
	return nil
}

func (s *PermissionService) visiblePermissionRoleNames(ctx context.Context, operators []*EffectiveDataScope) ([]string, error) {
	op := permissionDataScope(operators)
	if op == nil || op.IsSuperAdmin {
		return nil, nil
	}
	roles, err := s.client.Role.Query().All(ctx)
	if err != nil {
		return nil, err
	}
	visible := filterRolesForOperator(roles, op)
	names := make([]string, 0, len(visible))
	for _, r := range visible {
		names = append(names, r.Name)
	}
	return names, nil
}

// defaultCacheTTL 默认缓存过期时间
func (s *PermissionService) defaultCacheTTL() time.Duration {
	return 5 * time.Minute
}

// isSelfRole 检查目标角色是否属于当前操作者
// Empty operatorUID is allowed only for explicit system contexts.
func (s *PermissionService) isSelfRole(ctx context.Context, operatorUID string, targetRoleName string) (bool, error) {
	if err := requirePermissionOperator(ctx, operatorUID); err != nil {
		return false, err
	}
	if operatorUID == "" && isPermissionSystemOperator(ctx) {
		return false, nil
	}
	roleNames, err := s.getUserRoleNames(ctx, operatorUID)
	if err != nil {
		return false, fmt.Errorf("获取操作者角色失败: %w", err)
	}
	for _, name := range roleNames {
		if name == targetRoleName {
			return true, nil
		}
	}
	return false, nil
}

// isSuperAdminUser 检查操作者是否为超级管理员
func (s *PermissionService) isSuperAdminUser(ctx context.Context, operatorUID string) (bool, error) {
	if operatorUID == "" {
		return false, nil
	}
	return s.CheckUserHasRole(ctx, operatorUID, "", SuperAdminRoleName)
}

// shouldFilterSensitivePermissions 判断是否需要过滤高风险权限（非超级管理员需要过滤）
func (s *PermissionService) shouldFilterSensitivePermissions(ctx context.Context, operatorUID string) (bool, error) {
	if operatorUID == "" {
		return !isPermissionSystemOperator(ctx), nil
	}
	isSuperAdmin, err := s.isSuperAdminUser(ctx, operatorUID)
	if err != nil {
		return false, err
	}
	return !isSuperAdmin, nil
}

// clearUserPermissionsCache 清除指定用户的所有权限缓存
func (s *PermissionService) clearUserPermissionsCache(ctx context.Context, userUID string, domain string) {
	if s.cache == nil {
		return
	}
	_ = s.cache.Delete(ctx, utils.BuildCacheKey("user", "menu", userUID, domain))
	_ = s.cache.Delete(ctx, utils.BuildCacheKey("user", "button", userUID, domain))
	_ = s.cache.Delete(ctx, utils.BuildCacheKey("user", "api", userUID, domain))
}

// clearRolePermissionsCache 清除指定角色的权限缓存
func (s *PermissionService) clearRolePermissionsCache(ctx context.Context, roleName, domain string) {
	if s.cache == nil {
		return
	}
	_ = s.cache.Delete(ctx, utils.BuildCacheKey("role", "perms", roleName, domain))
}

// clearAllUsersPermissionsByRole 清除拥有指定角色的所有用户权限缓存
func (s *PermissionService) clearAllUsersPermissionsByRole(ctx context.Context, roleName, domain string) {
	if s.cache == nil {
		return
	}
	// 查询拥有该角色的所有用户
	roleEntity, err := s.client.Role.Query().Where(role.NameEQ(roleName)).Only(ctx)
	if err != nil {
		return
	}
	userRoles, err := s.client.UserRole.Query().
		Where(userrole.RoleIDEQ(roleEntity.ID)).
		WithUser().
		All(ctx)
	if err != nil {
		return
	}
	for _, ur := range userRoles {
		if ur.Edges.User != nil {
			s.clearUserPermissionsCache(ctx, ur.Edges.User.UID, domain)
		}
	}

	adminRoles, err := s.client.AdminRole.Query().
		Where(adminrole.RoleIDEQ(roleEntity.ID)).
		WithAdmin().
		All(ctx)
	if err != nil {
		return
	}
	for _, ar := range adminRoles {
		if ar.Edges.Admin != nil {
			s.clearUserPermissionsCache(ctx, ar.Edges.Admin.UID, domain)
		}
	}
}

// clearRoleAndUsersCache 清除角色缓存及其关联的所有用户缓存
func (s *PermissionService) clearRoleAndUsersCache(ctx context.Context, roleName, domain string) {
	s.clearRolePermissionsCache(ctx, roleName, domain)
	s.clearAllUsersPermissionsByRole(ctx, roleName, domain)
}

func (s *PermissionService) syncRolePermissionState(ctx context.Context, roleName, domain string) error {
	s.clearRoleAndUsersCache(ctx, roleName, domain)

	if global.EnforcerManager == nil {
		return nil
	}

	enforcer, err := global.EnforcerManager.GetEnforcer(domain)
	if err != nil {
		return fmt.Errorf("刷新 Casbin 策略失败: %w", err)
	}
	if err := global.ReloadCasbinPolicy(domain, enforcer); err != nil {
		return fmt.Errorf("刷新 Casbin 策略失败: %w", err)
	}
	return nil
}

// getUserRoleNames 获取用户的角色名称列表
func (s *PermissionService) getUserRoleNames(ctx context.Context, userUID string) ([]string, error) {
	a, err := s.client.Admin.Query().
		Where(admin.UIDEQ(userUID), admin.StatusEQ(admin.StatusActive)).
		Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("获取管理员信息失败: %w", err)
	}
	if err == nil {
		adminRoles, err := s.client.AdminRole.Query().
			Where(adminrole.AdminIDEQ(a.ID)).
			WithRole(func(q *ent.RoleQuery) {
				q.Where(role.StatusEQ(role.StatusActive))
			}).
			All(ctx)
		if err != nil {
			return nil, fmt.Errorf("获取管理员角色失败: %w", err)
		}

		roleNames := make([]string, 0, len(adminRoles)+1)
		if a.IsSuperAdmin {
			roleNames = append(roleNames, SuperAdminRoleName)
		}
		for _, ar := range adminRoles {
			if ar.Edges.Role != nil {
				roleNames = append(roleNames, ar.Edges.Role.Name)
			}
		}
		return roleNames, nil
	}

	u, err := s.client.User.Query().
		Where(user.UIDEQ(userUID), user.StatusEQ(user.StatusActive)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取用户信息失败: %w", err)
	}

	userRoles, err := s.client.UserRole.Query().
		Where(userrole.UserIDEQ(u.ID)).
		WithRole(func(q *ent.RoleQuery) {
			q.Where(role.StatusEQ(role.StatusActive))
		}).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取用户角色失败: %w", err)
	}

	roleNames := make([]string, 0, len(userRoles))
	for _, ur := range userRoles {
		if ur.Edges.Role != nil {
			roleNames = append(roleNames, ur.Edges.Role.Name)
		}
	}

	return roleNames, nil
}

func (s *PermissionService) GetUserRoleNames(ctx context.Context, userUID string) ([]string, error) {
	return s.getUserRoleNames(ctx, userUID)
}

// getUserPermissionsByType 获取用户指定类型的权限（内部通用方法）
func (s *PermissionService) getUserPermissionsByType(ctx context.Context, userUID, domain, permType string) ([]*PermissionResponse, error) {
	cacheKey := utils.BuildCacheKey("user", permType, userUID, domain)

	// 尝试从缓存获取
	if s.cache != nil {
		cached, err := s.cache.Get(ctx, cacheKey)
		if err == nil && cached != nil {
			return cached, nil
		}
	}

	// 获取用户角色
	roleNames, err := s.getUserRoleNames(ctx, userUID)
	if err != nil {
		return nil, err
	}

	if len(roleNames) == 0 {
		return []*PermissionResponse{}, nil
	}

	// 构建查询
	query := s.client.CasbinRule.Query().
		Where(
			casbinrule.PtypeEQ("p"),
			casbinrule.V0In(roleNames...),
			casbinrule.V1EQ(domain),
		)

	// 根据权限类型添加过滤条件
	switch permType {
	case "menu":
		query = query.Where(casbinrule.V4EQ(string(PermissionTypeMenu)))
	case "button":
		query = query.Where(casbinrule.V4EQ(string(PermissionTypeButton)))
	case "api":
		query = query.Where(casbinrule.V4EQ(string(PermissionTypeAPI)))
	}

	permissions, err := query.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取%s权限失败: %w", permType, err)
	}

	// 转换为响应格式
	data := make([]*PermissionResponse, 0, len(permissions))
	for _, p := range permissions {
		data = append(data, s.toPermissionResponse(p))
	}

	// 写入缓存
	if s.cache != nil {
		_ = s.cache.SetWithExpiration(ctx, cacheKey, data, s.defaultCacheTTL())
	}

	return data, nil
}

// CreatePermission 创建权限
func (s *PermissionService) CreatePermission(ctx context.Context, operatorUID string, req *CreatePermissionRequest, operators ...*EffectiveDataScope) error {
	// 验证权限类型
	if !isValidPermissionType(req.Type) {
		return ErrInvalidPermissionType
	}

	// 禁止为超级管理员分配/创建权限
	if isSuperAdminRole(req.RoleName) {
		return ErrSuperAdminProtected
	}

	// 禁止修改自己所属角色的权限
	isSelf, err := s.isSelfRole(ctx, operatorUID, req.RoleName)
	if err != nil {
		return fmt.Errorf("检查角色归属失败: %w", err)
	}
	if isSelf {
		return ErrSelfRolePermissionMutationForbidden
	}

	// 检查角色是否存在
	exists, err := s.client.Role.Query().Where(role.NameEQ(req.RoleName)).Exist(ctx)
	if err != nil {
		return fmt.Errorf("检查角色失败: %w", err)
	}
	if !exists {
		return ErrRoleNotFound
	}
	if err := s.ensurePermissionRoleInScope(ctx, req.RoleName, operators); err != nil {
		return err
	}

	if err := s.ensureOperatorCanManagePermissions(ctx, operatorUID, req.Domain, []PermissionRule{{Object: req.Object, Action: req.Action}}); err != nil {
		return err
	}

	// 检查权限是否已存在
	exists, err = s.client.CasbinRule.Query().
		Where(
			casbinrule.PtypeEQ("p"),
			casbinrule.V0EQ(req.RoleName),
			casbinrule.V1EQ(req.Domain),
			casbinrule.V2EQ(req.Object),
			casbinrule.V3EQ(req.Action),
		).Exist(ctx)
	if err != nil {
		return fmt.Errorf("检查权限失败: %w", err)
	}
	if exists {
		return ErrPermissionAlreadyExists
	}

	rule := []string{req.RoleName, req.Domain, req.Object, req.Action}
	if global.EnforcerManager == nil {
		_, err = s.client.CasbinRule.Create().
			SetPtype("p").
			SetV0(req.RoleName).
			SetV1(req.Domain).
			SetV2(req.Object).
			SetV3(req.Action).
			SetV4(string(req.Type)).
			SetV5(req.Description).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("创建权限失败: %w", err)
		}
		s.clearRoleAndUsersCache(ctx, req.RoleName, req.Domain)
		return nil
	}
	added, err := addCasbinPolicy(req.Domain, rule)
	if err != nil {
		return fmt.Errorf("创建权限失败: %w", err)
	}
	if !added {
		return ErrPermissionAlreadyExists
	}
	created, err := s.client.CasbinRule.Query().Where(
		casbinrule.PtypeEQ("p"),
		casbinrule.V0EQ(req.RoleName),
		casbinrule.V1EQ(req.Domain),
		casbinrule.V2EQ(req.Object),
		casbinrule.V3EQ(req.Action),
	).Only(ctx)
	if err == nil {
		_, err = s.client.CasbinRule.UpdateOne(created).
			SetV4(string(req.Type)).
			SetV5(req.Description).
			Save(ctx)
	}
	if err != nil {
		_, _ = removeCasbinPolicy(req.Domain, rule)
		return fmt.Errorf("保存权限元数据失败: %w", err)
	}
	s.clearRoleAndUsersCache(ctx, req.RoleName, req.Domain)
	return nil
}

// UpdatePermission 更新权限
func (s *PermissionService) UpdatePermission(ctx context.Context, operatorUID string, req *UpdatePermissionRequest, operators ...*EffectiveDataScope) error {
	// 验证权限类型
	if !isValidPermissionType(req.Type) {
		return ErrInvalidPermissionType
	}

	// 禁止修改超级管理员权限
	if isSuperAdminRole(req.RoleName) {
		return ErrSuperAdminProtected
	}

	// 禁止修改自己所属角色的权限
	isSelf, err := s.isSelfRole(ctx, operatorUID, req.RoleName)
	if err != nil {
		return fmt.Errorf("检查角色归属失败: %w", err)
	}
	if isSelf {
		return ErrSelfRolePermissionMutationForbidden
	}

	// 检查角色是否存在
	exists, err := s.client.Role.Query().Where(role.NameEQ(req.RoleName)).Exist(ctx)
	if err != nil {
		return fmt.Errorf("检查角色失败: %w", err)
	}
	if !exists {
		return ErrRoleNotFound
	}
	if err := s.ensurePermissionRoleInScope(ctx, req.RoleName, operators); err != nil {
		return err
	}

	// 查找要更新的权限
	_, err = s.client.CasbinRule.Query().
		Where(
			casbinrule.PtypeEQ("p"),
			casbinrule.V0EQ(req.RoleName),
			casbinrule.V1EQ(req.Domain),
			casbinrule.V2EQ(req.Object),
			casbinrule.V3EQ(req.Action),
		).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrPermissionNotFound
		}
		return fmt.Errorf("查找权限失败: %w", err)
	}

	targetObject := req.Object
	if req.NewObject != "" {
		targetObject = req.NewObject
	}
	targetAction := req.Action
	if req.NewAction != "" {
		targetAction = req.NewAction
	}
	if err := s.ensureOperatorCanManagePermissions(ctx, operatorUID, req.Domain, []PermissionRule{
		{Object: req.Object, Action: req.Action},
		{Object: targetObject, Action: targetAction},
	}); err != nil {
		return err
	}

	oldRule := []string{req.RoleName, req.Domain, req.Object, req.Action}
	newRule := []string{req.RoleName, req.Domain, targetObject, targetAction}
	if global.EnforcerManager == nil {
		update := s.client.CasbinRule.Update().Where(
			casbinrule.PtypeEQ("p"),
			casbinrule.V0EQ(req.RoleName),
			casbinrule.V1EQ(req.Domain),
			casbinrule.V2EQ(req.Object),
			casbinrule.V3EQ(req.Action),
		).SetV2(targetObject).SetV3(targetAction).SetV4(string(req.Type))
		if req.Description != "" {
			update.SetV5(req.Description)
		}
		if _, err = update.Save(ctx); err != nil {
			return fmt.Errorf("更新权限失败: %w", err)
		}
		s.clearRoleAndUsersCache(ctx, req.RoleName, req.Domain)
		return nil
	}
	if targetObject != req.Object || targetAction != req.Action {
		updated, err := updateCasbinPolicy(req.Domain, oldRule, newRule)
		if err != nil {
			return fmt.Errorf("更新权限失败: %w", err)
		}
		if !updated {
			return ErrPermissionNotFound
		}
	}
	metadata, err := s.client.CasbinRule.Query().Where(
		casbinrule.PtypeEQ("p"),
		casbinrule.V0EQ(req.RoleName),
		casbinrule.V1EQ(req.Domain),
		casbinrule.V2EQ(targetObject),
		casbinrule.V3EQ(targetAction),
	).Only(ctx)
	if err != nil {
		if targetObject != req.Object || targetAction != req.Action {
			_, _ = updateCasbinPolicy(req.Domain, newRule, oldRule)
		}
		return fmt.Errorf("查找更新后权限失败: %w", err)
	}
	metadataUpdate := s.client.CasbinRule.UpdateOne(metadata).SetV4(string(req.Type))
	if req.Description != "" {
		metadataUpdate.SetV5(req.Description)
	}
	if _, err = metadataUpdate.Save(ctx); err != nil {
		if targetObject != req.Object || targetAction != req.Action {
			_, _ = updateCasbinPolicy(req.Domain, newRule, oldRule)
		}
		return fmt.Errorf("更新权限元数据失败: %w", err)
	}
	s.clearRoleAndUsersCache(ctx, req.RoleName, req.Domain)
	return nil
}

// DeletePermission 删除权限
func (s *PermissionService) DeletePermission(ctx context.Context, operatorUID string, roleName, domain, object, action string, operators ...*EffectiveDataScope) error {
	// 禁止删除超级管理员权限
	if isSuperAdminRole(roleName) {
		return ErrSuperAdminProtected
	}

	// 禁止修改自己所属角色的权限
	isSelf, err := s.isSelfRole(ctx, operatorUID, roleName)
	if err != nil {
		return fmt.Errorf("检查角色归属失败: %w", err)
	}
	if isSelf {
		return ErrSelfRolePermissionMutationForbidden
	}
	if err := s.ensurePermissionRoleInScope(ctx, roleName, operators); err != nil {
		return err
	}

	// 查找要删除的权限
	_, err = s.client.CasbinRule.Query().
		Where(
			casbinrule.PtypeEQ("p"),
			casbinrule.V0EQ(roleName),
			casbinrule.V1EQ(domain),
			casbinrule.V2EQ(object),
			casbinrule.V3EQ(action),
		).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrPermissionNotFound
		}
		return fmt.Errorf("查找权限失败: %w", err)
	}

	if err := s.ensureOperatorCanManagePermissions(ctx, operatorUID, domain, []PermissionRule{{Object: object, Action: action}}); err != nil {
		return err
	}
	if global.EnforcerManager == nil {
		if _, err = s.client.CasbinRule.Delete().Where(
			casbinrule.PtypeEQ("p"),
			casbinrule.V0EQ(roleName),
			casbinrule.V1EQ(domain),
			casbinrule.V2EQ(object),
			casbinrule.V3EQ(action),
		).Exec(ctx); err != nil {
			return fmt.Errorf("删除权限失败: %w", err)
		}
		s.clearRoleAndUsersCache(ctx, roleName, domain)
		return nil
	}

	removed, err := removeCasbinPolicy(domain, []string{roleName, domain, object, action})
	if err != nil {
		return fmt.Errorf("删除权限失败: %w", err)
	}
	if !removed {
		return ErrPermissionNotFound
	}
	s.clearRoleAndUsersCache(ctx, roleName, domain)
	return nil
}

// GetPermission 获取权限详情
func (s *PermissionService) GetPermission(ctx context.Context, roleName, domain, object, action string, operators ...*EffectiveDataScope) (*PermissionResponse, error) {
	if err := s.ensurePermissionRoleInScope(ctx, roleName, operators); err != nil {
		return nil, err
	}
	// 查找权限
	permission, err := s.client.CasbinRule.Query().
		Where(
			casbinrule.PtypeEQ("p"),
			casbinrule.V0EQ(roleName),
			casbinrule.V1EQ(domain),
			casbinrule.V2EQ(object),
			casbinrule.V3EQ(action),
		).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrPermissionNotFound
		}
		return nil, fmt.Errorf("查找权限失败: %w", err)
	}

	return s.toPermissionResponse(permission), nil
}

// ListPermissions 获取权限列表
func (s *PermissionService) ListPermissions(ctx context.Context, operatorUID string, req *ListPermissionsRequest, operators ...*EffectiveDataScope) (*ListPermissionsResponse, error) {
	req.Page, req.PageSize = utils.NormalizePagination(req.Page, req.PageSize)

	// 构建查询
	query := s.client.CasbinRule.Query().Where(casbinrule.PtypeEQ("p"))
	visibleRoleNames, err := s.visiblePermissionRoleNames(ctx, operators)
	if err != nil {
		return nil, fmt.Errorf("获取可见角色失败: %w", err)
	}
	if op := permissionDataScope(operators); op != nil && !op.IsSuperAdmin && len(visibleRoleNames) == 0 {
		return &ListPermissionsResponse{Page: req.Page, Data: []*PermissionResponse{}}, nil
	}
	if len(visibleRoleNames) > 0 {
		query = query.Where(casbinrule.V0In(visibleRoleNames...))
	}

	// 按角色名称筛选
	if req.RoleName != "" {
		query = query.Where(casbinrule.V0EQ(req.RoleName))
	}

	// 按域筛选
	if req.Domain != "" {
		query = query.Where(casbinrule.V1EQ(req.Domain))
	}

	// 按权限类型筛选
	if req.Type != "" {
		query = query.Where(casbinrule.V4EQ(string(req.Type)))
	}

	// 按关键字筛选（对象或描述）
	if req.Keyword != "" {
		query = query.Where(
			casbinrule.Or(
				casbinrule.V2Contains(req.Keyword),
				casbinrule.V5Contains(req.Keyword),
			),
		)
	}

	// 非超级管理员过滤高风险 API 权限，必须先于 Count 和分页。
	shouldFilter, err := s.shouldFilterSensitivePermissions(ctx, operatorUID)
	if err != nil {
		return nil, fmt.Errorf("检查操作者权限失败: %w", err)
	}
	if shouldFilter {
		query = query.Where(casbinrule.V2NotIn(sensitivePermissionObjectList()...))
	}

	// 获取总数
	total, err := query.Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取权限总数失败: %w", err)
	}

	// 分页查询
	if req.PageSize > 0 {
		offset := (req.Page - 1) * req.PageSize
		query = query.Offset(offset).Limit(req.PageSize)
	}
	permissions, err := query.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取权限列表失败: %w", err)
	}

	// 转换为响应格式
	data := make([]*PermissionResponse, 0, len(permissions))
	for _, p := range permissions {
		data = append(data, s.toPermissionResponse(p))
	}

	return &ListPermissionsResponse{
		Total: total,
		Page:  req.Page,
		Data:  data,
	}, nil
}

// ListAllPermissions 获取所有权限（不带分页）
func (s *PermissionService) ListAllPermissions(ctx context.Context, operatorUID string, keyword string, permType PermissionType, domain string, roleName string, operators ...*EffectiveDataScope) ([]*PermissionResponse, error) {
	// 构建查询
	query := s.client.CasbinRule.Query().Where(casbinrule.PtypeEQ("p"))
	visibleRoleNames, err := s.visiblePermissionRoleNames(ctx, operators)
	if err != nil {
		return nil, fmt.Errorf("获取可见角色失败: %w", err)
	}
	if op := permissionDataScope(operators); op != nil && !op.IsSuperAdmin && len(visibleRoleNames) == 0 {
		return []*PermissionResponse{}, nil
	}
	if len(visibleRoleNames) > 0 {
		query = query.Where(casbinrule.V0In(visibleRoleNames...))
	}

	// 按角色名称筛选
	if roleName != "" {
		query = query.Where(casbinrule.V0EQ(roleName))
	}

	// 按域筛选
	if domain != "" {
		query = query.Where(casbinrule.V1EQ(domain))
	}

	// 按权限类型筛选
	if permType != "" {
		query = query.Where(casbinrule.V4EQ(string(permType)))
	}

	// 按关键字筛选（对象或描述）
	if keyword != "" {
		query = query.Where(
			casbinrule.Or(
				casbinrule.V2Contains(keyword),
				casbinrule.V5Contains(keyword),
			),
		)
	}

	// 查询所有权限
	permissions, err := query.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取权限列表失败: %w", err)
	}

	// 非超级管理员过滤高风险 API 权限
	shouldFilter, err := s.shouldFilterSensitivePermissions(ctx, operatorUID)
	if err != nil {
		return nil, fmt.Errorf("检查操作者权限失败: %w", err)
	}

	// 转换为响应格式
	data := make([]*PermissionResponse, 0, len(permissions))
	for _, p := range permissions {
		if shouldFilter && IsSensitivePermissionObject(p.V2) {
			continue
		}
		data = append(data, s.toPermissionResponse(p))
	}

	return data, nil
}

// AssignPermissionsToRole 为角色分配权限
func (s *PermissionService) AssignPermissionsToRole(ctx context.Context, operatorUID string, roleName, domain string, permissions []PermissionRule, operators ...*EffectiveDataScope) error {
	// 禁止为超级管理员分配权限
	if isSuperAdminRole(roleName) {
		return ErrSuperAdminProtected
	}

	// 禁止修改自己所属角色的权限
	isSelf, err := s.isSelfRole(ctx, operatorUID, roleName)
	if err != nil {
		return fmt.Errorf("检查角色归属失败: %w", err)
	}
	if isSelf {
		return ErrSelfRolePermissionMutationForbidden
	}

	// 检查角色是否存在
	exists, err := s.client.Role.Query().Where(role.NameEQ(roleName)).Exist(ctx)
	if err != nil {
		return fmt.Errorf("检查角色失败: %w", err)
	}
	if !exists {
		return ErrRoleNotFound
	}
	if err := s.ensurePermissionRoleInScope(ctx, roleName, operators); err != nil {
		return err
	}

	if err := s.ensureOperatorCanManagePermissions(ctx, operatorUID, domain, permissions); err != nil {
		return err
	}

	newRules := make([][]string, 0, len(permissions))
	permissionTypes := make(map[string]PermissionType, len(permissions))
	seenRules := make(map[string]struct{}, len(permissions))
	for _, perm := range permissions {
		permType := determinePermissionType(perm.Action)
		key := perm.Object + "\x00" + perm.Action
		if _, seen := seenRules[key]; seen {
			continue
		}
		seenRules[key] = struct{}{}
		exists, err := s.client.CasbinRule.Query().
			Where(
				casbinrule.PtypeEQ("p"),
				casbinrule.V0EQ(roleName),
				casbinrule.V1EQ(domain),
				casbinrule.V2EQ(perm.Object),
				casbinrule.V3EQ(perm.Action),
			).Exist(ctx)
		if err != nil {
			return fmt.Errorf("检查权限失败: %w", err)
		}
		if exists {
			continue
		}
		rule := []string{roleName, domain, perm.Object, perm.Action}
		newRules = append(newRules, rule)
		permissionTypes[key] = permType
	}
	if len(newRules) == 0 {
		return nil
	}
	if global.EnforcerManager == nil {
		for _, rule := range newRules {
			if _, err := s.client.CasbinRule.Create().
				SetPtype("p").
				SetV0(rule[0]).
				SetV1(rule[1]).
				SetV2(rule[2]).
				SetV3(rule[3]).
				SetV4(string(permissionTypes[rule[2]+"\x00"+rule[3]])).
				Save(ctx); err != nil {
				return fmt.Errorf("分配权限失败: %w", err)
			}
		}
		s.clearRoleAndUsersCache(ctx, roleName, domain)
		return nil
	}
	added, err := addCasbinPolicies(domain, newRules)
	if err != nil {
		return fmt.Errorf("分配权限失败: %w", err)
	}
	if !added {
		return ErrPermissionAlreadyExists
	}
	for _, rule := range newRules {
		stored, queryErr := s.client.CasbinRule.Query().Where(
			casbinrule.PtypeEQ("p"),
			casbinrule.V0EQ(rule[0]),
			casbinrule.V1EQ(rule[1]),
			casbinrule.V2EQ(rule[2]),
			casbinrule.V3EQ(rule[3]),
		).Only(ctx)
		if queryErr == nil {
			_, queryErr = s.client.CasbinRule.UpdateOne(stored).
				SetV4(string(permissionTypes[rule[2]+"\x00"+rule[3]])).
				Save(ctx)
		}
		if queryErr != nil {
			_, _ = removeCasbinPolicies(domain, newRules)
			return fmt.Errorf("保存权限元数据失败: %w", queryErr)
		}
	}
	s.clearRoleAndUsersCache(ctx, roleName, domain)
	return nil
}

// RemovePermissionsFromRole 从角色移除权限
func (s *PermissionService) RemovePermissionsFromRole(ctx context.Context, operatorUID string, roleName, domain string, permissions []PermissionRule, operators ...*EffectiveDataScope) error {
	// 禁止从超级管理员角色移除权限
	if isSuperAdminRole(roleName) {
		return ErrSuperAdminProtected
	}

	// 禁止修改自己所属角色的权限
	isSelf, err := s.isSelfRole(ctx, operatorUID, roleName)
	if err != nil {
		return fmt.Errorf("检查角色归属失败: %w", err)
	}
	if isSelf {
		return ErrSelfRolePermissionMutationForbidden
	}

	// 检查角色是否存在
	exists, err := s.client.Role.Query().Where(role.NameEQ(roleName)).Exist(ctx)
	if err != nil {
		return fmt.Errorf("检查角色失败: %w", err)
	}
	if !exists {
		return ErrRoleNotFound
	}
	if err := s.ensurePermissionRoleInScope(ctx, roleName, operators); err != nil {
		return err
	}

	if err := s.ensureOperatorCanManagePermissions(ctx, operatorUID, domain, permissions); err != nil {
		return err
	}

	rules := make([][]string, 0, len(permissions))
	seenRules := make(map[string]struct{}, len(permissions))
	for _, perm := range permissions {
		key := perm.Object + "\x00" + perm.Action
		if _, seen := seenRules[key]; seen {
			continue
		}
		seenRules[key] = struct{}{}
		rules = append(rules, []string{roleName, domain, perm.Object, perm.Action})
	}
	if len(rules) > 0 {
		if global.EnforcerManager == nil {
			for _, rule := range rules {
				if _, err := s.client.CasbinRule.Delete().Where(
					casbinrule.PtypeEQ("p"),
					casbinrule.V0EQ(rule[0]),
					casbinrule.V1EQ(rule[1]),
					casbinrule.V2EQ(rule[2]),
					casbinrule.V3EQ(rule[3]),
				).Exec(ctx); err != nil {
					return fmt.Errorf("移除权限失败: %w", err)
				}
			}
		} else if _, err := removeCasbinPolicies(domain, rules); err != nil {
			return fmt.Errorf("移除权限失败: %w", err)
		}
	}
	s.clearRoleAndUsersCache(ctx, roleName, domain)
	return nil
}

// GetRolePermissions 获取角色拥有的权限
func (s *PermissionService) GetRolePermissions(ctx context.Context, roleName, domain string, operators ...*EffectiveDataScope) ([]*PermissionResponse, error) {
	if err := s.ensurePermissionRoleInScope(ctx, roleName, operators); err != nil {
		return nil, err
	}
	cacheKey := utils.BuildCacheKey("role", "perms", roleName, domain)

	// 尝试从缓存获取
	if s.cache != nil {
		cached, err := s.cache.Get(ctx, cacheKey)
		if err == nil && cached != nil {
			return cached, nil
		}
	}

	// 检查角色是否存在
	exists, err := s.client.Role.Query().Where(role.NameEQ(roleName)).Exist(ctx)
	if err != nil {
		return nil, fmt.Errorf("检查角色失败: %w", err)
	}
	if !exists {
		return nil, ErrRoleNotFound
	}

	// 查询角色权限
	permissions, err := s.client.CasbinRule.Query().
		Where(
			casbinrule.PtypeEQ("p"),
			casbinrule.V0EQ(roleName),
			casbinrule.V1EQ(domain),
		).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取角色权限失败: %w", err)
	}

	// 转换为响应格式
	data := make([]*PermissionResponse, 0, len(permissions))
	for _, p := range permissions {
		data = append(data, s.toPermissionResponse(p))
	}

	// 写入缓存
	if s.cache != nil {
		_ = s.cache.SetWithExpiration(ctx, cacheKey, data, s.defaultCacheTTL())
	}

	return data, nil
}

// GetRolePermissionsByRoleID 通过角色ID获取权限
func (s *PermissionService) GetRolePermissionsByRoleID(ctx context.Context, roleID int, domain string, operators ...*EffectiveDataScope) ([]*PermissionResponse, error) {
	// 先通过ID获取角色名称
	roleEntity, err := s.client.Role.Query().
		Where(role.IDEQ(roleID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrRoleNotFound
		}
		return nil, fmt.Errorf("查询角色失败: %w", err)
	}

	// 复用现有的按名称查询的方法
	return s.GetRolePermissions(ctx, roleEntity.Name, domain, operators...)
}

// SaveRolePermissions 覆盖式保存角色权限
func (s *PermissionService) SaveRolePermissions(ctx context.Context, operatorUID string, roleID int, req *SaveRolePermissionsRequest, operators ...*EffectiveDataScope) error {
	// 先通过ID获取角色名称
	roleEntity, err := s.client.Role.Query().
		Where(role.IDEQ(roleID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrRoleNotFound
		}
		return fmt.Errorf("查询角色失败: %w", err)
	}

	roleName := roleEntity.Name
	domain := req.Domain
	if err := s.ensurePermissionRoleInScope(ctx, roleName, operators); err != nil {
		return err
	}

	// 禁止覆盖式保存超级管理员权限
	if isSuperAdminRole(roleName) {
		return ErrSuperAdminProtected
	}

	// 禁止修改自己所属角色的权限
	isSelf, err := s.isSelfRole(ctx, operatorUID, roleName)
	if err != nil {
		return fmt.Errorf("检查角色归属失败: %w", err)
	}
	if isSelf {
		return ErrSelfRolePermissionMutationForbidden
	}

	seen := make(map[string]struct{}, len(req.Permissions))
	deduped := make([]PermissionRule, 0, len(req.Permissions))
	for _, perm := range req.Permissions {
		key := perm.Object + "|" + perm.Action
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, perm)
	}

	if err := requirePermissionOperator(ctx, operatorUID); err != nil {
		return err
	}
	scope, err := s.getOperatorPermissionScope(ctx, operatorUID, domain)
	if err != nil {
		return err
	}
	for _, perm := range deduped {
		if !scope.allowsRule(perm) {
			return ErrPermissionScopeEscalation
		}
	}

	// 开启事务
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}

	// 删除操作者可管理范围内的旧权限；超出范围的权限保留不动。
	if scope.includeAll {
		_, err = tx.CasbinRule.Delete().
			Where(
				casbinrule.PtypeEQ("p"),
				casbinrule.V0EQ(roleName),
				casbinrule.V1EQ(domain),
			).Exec(ctx)
	} else {
		existing, queryErr := tx.CasbinRule.Query().
			Where(
				casbinrule.PtypeEQ("p"),
				casbinrule.V0EQ(roleName),
				casbinrule.V1EQ(domain),
			).All(ctx)
		if queryErr != nil {
			err = queryErr
		} else {
			ids := make([]int, 0, len(existing))
			for _, rule := range existing {
				if scope.allowsRule(PermissionRule{Object: rule.V2, Action: rule.V3}) {
					ids = append(ids, rule.ID)
				}
			}
			if len(ids) > 0 {
				_, err = tx.CasbinRule.Delete().
					Where(casbinrule.IDIn(ids...)).
					Exec(ctx)
			}
		}
	}
	if err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("删除权限失败: %v, 回滚失败: %v", err, rbErr)
		}
		return fmt.Errorf("删除权限失败: %w", err)
	}

	// 批量创建新权限
	for _, perm := range deduped {
		permType := determinePermissionType(perm.Action)

		_, err = tx.CasbinRule.Create().
			SetPtype("p").
			SetV0(roleName).
			SetV1(domain).
			SetV2(perm.Object).
			SetV3(perm.Action).
			SetV4(string(permType)).
			Save(ctx)
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil {
				return fmt.Errorf("创建权限失败: %v, 回滚失败: %v", err, rbErr)
			}
			return fmt.Errorf("创建权限失败: %w", err)
		}
	}

	// 提交事务
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("提交事务失败: %w", err)
	}

	// 清除相关缓存
	return s.syncRolePermissionState(ctx, roleName, domain)
}

// CheckUserHasRole 检查用户是否拥有指定角色
func (s *PermissionService) CheckUserHasRole(ctx context.Context, userUID string, domain string, roleName string) (bool, error) {
	roleNames, err := s.getUserRoleNames(ctx, userUID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return false, nil
		}
		return false, err
	}
	for _, name := range roleNames {
		if name == roleName {
			return true, nil
		}
	}
	return false, nil
}

// GetUserMenuPermissions 获取用户的菜单权限
func (s *PermissionService) GetUserMenuPermissions(ctx context.Context, userID string, domain string) ([]*PermissionResponse, error) {
	isSuperAdmin, err := s.CheckUserHasRole(ctx, userID, domain, "admin")
	if err != nil {
		return nil, err
	}
	if isSuperAdmin {
		return getEnabledSystemMenuPermissions(ctx, domain)
	}
	return s.getUserPermissionsByType(ctx, userID, domain, "menu")
}

// GetUserMenuPermissionTree 获取用户菜单权限树
func (s *PermissionService) GetUserMenuPermissionTree(ctx context.Context, userID string, domain string) ([]*SystemPermissionTreeNode, error) {
	permissions, err := s.GetUserMenuPermissions(ctx, userID, domain)
	if err != nil {
		return nil, err
	}
	return s.buildUserPermissionTree(ctx, domain, permissions, PermissionTypeMenu)
}

// GetUserPermissionTree 获取用户混合权限树
func (s *PermissionService) GetUserPermissionTree(ctx context.Context, userID string, domain string) ([]*SystemPermissionTreeNode, error) {
	isSuperAdmin, err := s.CheckUserHasRole(ctx, userID, domain, "admin")
	if err != nil {
		return nil, err
	}
	if isSuperAdmin {
		return s.buildUserPermissionTree(ctx, domain, nil, "")
	}

	permissions := make([]*PermissionResponse, 0)
	for _, permType := range []string{"menu", "button", "api"} {
		items, err := s.getUserPermissionsByType(ctx, userID, domain, permType)
		if err != nil {
			return nil, err
		}
		permissions = append(permissions, items...)
	}
	return s.buildUserPermissionTree(ctx, domain, permissions, "")
}

func (s *PermissionService) buildUserPermissionTree(ctx context.Context, domain string, permissions []*PermissionResponse, onlyType PermissionType) ([]*SystemPermissionTreeNode, error) {
	query := s.client.SystemPermission.Query().
		Where(systempermission.StatusEQ(1))

	if domain != "" {
		query = query.Where(systempermission.Or(
			systempermission.DomainEQ(domain),
			systempermission.IsCommonEQ(true),
		))
	}
	if onlyType != "" {
		query = query.Where(systempermission.TypeEQ(string(onlyType)))
	}

	systemPermissions, err := query.
		Order(ent.Asc(systempermission.FieldSortOrder), ent.Asc(systempermission.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取系统权限树失败: %w", err)
	}

	includeAll := permissions == nil
	permissionSet := buildPermissionLookup(permissions)
	permissionByID := make(map[int]*ent.SystemPermission, len(systemPermissions))
	included := make(map[int]struct{})

	for _, p := range systemPermissions {
		permissionByID[p.ID] = p
	}

	for _, p := range systemPermissions {
		if includeAll || matchSystemPermission(p, permissionSet) {
			for current := p; current != nil; current = permissionByID[current.ParentID] {
				included[current.ID] = struct{}{}
				if current.ParentID == 0 {
					break
				}
			}
		}
	}

	return buildSystemPermissionTree(systemPermissions, included), nil
}

func buildPermissionLookup(permissions []*PermissionResponse) map[string]struct{} {
	lookup := make(map[string]struct{}, len(permissions))
	for _, p := range permissions {
		lookup[permissionLookupKey(string(p.Type), p.Object, p.Action)] = struct{}{}
	}
	return lookup
}

func matchSystemPermission(p *ent.SystemPermission, lookup map[string]struct{}) bool {
	permType := p.Type
	switch permType {
	case string(PermissionTypeAPI):
		_, ok := lookup[permissionLookupKey(permType, p.Path, p.Method)]
		return ok
	case string(PermissionTypeMenu):
		if _, ok := lookup[permissionLookupKey(permType, systemPermissionObject(p), string(PermissionTypeMenu))]; ok {
			return true
		}
		_, ok := lookup[permissionLookupKey(permType, systemPermissionObject(p), "MENU")]
		return ok
	case string(PermissionTypeButton):
		_, ok := lookup[permissionLookupKey(permType, systemPermissionObject(p), string(PermissionTypeButton))]
		return ok
	default:
		return false
	}
}

func permissionLookupKey(permType, object, action string) string {
	return permType + "\x00" + object + "\x00" + action
}

func buildSystemPermissionTree(systemPermissions []*ent.SystemPermission, included map[int]struct{}) []*SystemPermissionTreeNode {
	nodeMap := make(map[int]*SystemPermissionTreeNode, len(included))
	rootNodes := make([]*SystemPermissionTreeNode, 0)

	for _, p := range systemPermissions {
		if _, ok := included[p.ID]; !ok {
			continue
		}
		nodeMap[p.ID] = systemPermissionToTreeNode(p)
	}

	for _, p := range systemPermissions {
		node, ok := nodeMap[p.ID]
		if !ok {
			continue
		}
		parent, hasParent := nodeMap[p.ParentID]
		if p.ParentID == 0 || !hasParent {
			rootNodes = append(rootNodes, node)
			continue
		}
		parent.Children = append(parent.Children, node)
	}

	sortSystemPermissionTree(rootNodes)
	return rootNodes
}

func systemPermissionToTreeNode(p *ent.SystemPermission) *SystemPermissionTreeNode {
	return &SystemPermissionTreeNode{
		ID:          p.ID,
		Type:        SystemPermissionType(p.Type),
		Name:        p.Name,
		Code:        p.Code,
		Path:        p.Path,
		Method:      p.Method,
		MenuPath:    p.MenuPath,
		Icon:        p.Icon,
		Component:   p.Component,
		ParentID:    p.ParentID,
		SortOrder:   p.SortOrder,
		IsCommon:    p.IsCommon,
		Status:      p.Status,
		Description: p.Description,
		Children:    make([]*SystemPermissionTreeNode, 0),
	}
}

func sortSystemPermissionTree(nodes []*SystemPermissionTreeNode) {
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].SortOrder == nodes[j].SortOrder {
			return nodes[i].ID < nodes[j].ID
		}
		return nodes[i].SortOrder < nodes[j].SortOrder
	})
	for _, node := range nodes {
		sortSystemPermissionTree(node.Children)
	}
}

// GetUserButtonPermissions 获取用户的按钮权限
func (s *PermissionService) GetUserButtonPermissions(ctx context.Context, userID string, domain string) ([]*PermissionResponse, error) {
	isSuperAdmin, err := s.CheckUserHasRole(ctx, userID, domain, "admin")
	if err != nil {
		return nil, err
	}
	if isSuperAdmin {
		return getEnabledSystemButtonPermissions(ctx, domain)
	}
	return s.getUserPermissionsByType(ctx, userID, domain, "button")
}

// GetUserAPIPermissions 获取用户的API权限
func (s *PermissionService) GetUserAPIPermissions(ctx context.Context, userID string, domain string) ([]*PermissionResponse, error) {
	return s.getUserPermissionsByType(ctx, userID, domain, "api")
}

// CheckPermission 检查用户是否有某个权限
func (s *PermissionService) CheckPermission(ctx context.Context, userID string, domain, object, action string) (bool, error) {
	roleNames, err := s.getUserRoleNames(ctx, userID)
	if err != nil {
		return false, err
	}
	if len(roleNames) == 0 {
		return false, nil
	}

	enforcer, err := global.EnforcerManager.GetEnforcer(domain)
	if err != nil {
		return false, fmt.Errorf("获取权限执行器失败: %w", err)
	}
	if err := global.RefreshCasbinPolicyIfStale(domain, enforcer); err != nil {
		return false, fmt.Errorf("刷新权限策略失败: %w", err)
	}

	allowed, err := enforceCasbinWithRoles(enforcer, userID, domain, object, action, roleNames)
	if err != nil {
		return false, fmt.Errorf("检查权限失败: %w", err)
	}
	return allowed, nil
}

func missingPermissionRoleNames(desired, existing []string) []string {
	existingSet := make(map[string]struct{}, len(existing))
	for _, roleName := range existing {
		existingSet[roleName] = struct{}{}
	}

	missing := make([]string, 0)
	for _, roleName := range desired {
		if _, ok := existingSet[roleName]; !ok {
			missing = append(missing, roleName)
		}
	}
	return missing
}

// toPermissionResponse 将CasbinRule转换为PermissionResponse
func (s *PermissionService) toPermissionResponse(rule *ent.CasbinRule) *PermissionResponse {
	return &PermissionResponse{
		ID:          rule.ID,
		RoleName:    rule.V0,
		Domain:      rule.V1,
		Object:      rule.V2,
		Action:      rule.V3,
		Type:        PermissionType(rule.V4),
		Description: rule.V5,
	}
}

// isValidPermissionType 验证权限类型是否有效
func isValidPermissionType(permType PermissionType) bool {
	return permType == PermissionTypeAPI || permType == PermissionTypeMenu || permType == PermissionTypeButton
}

// determinePermissionType 根据action确定权限类型
func determinePermissionType(action string) PermissionType {
	action = strings.ToUpper(action)
	if action == "MENU" {
		return PermissionTypeMenu
	} else if action == "BUTTON" {
		return PermissionTypeButton
	} else {
		return PermissionTypeAPI
	}
}
