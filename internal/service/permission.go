package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/casbinrule"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/user"
	"go-admin/internal/ent/userrole"
	"go-admin/internal/global"

	"github.com/wuwuseo/cmf/cache"
)

var (
	// 权限相关错误
	ErrPermissionNotFound       = errors.New("权限不存在")
	ErrInvalidPermissionType    = errors.New("无效的权限类型")
	ErrPermissionAlreadyExists  = errors.New("权限已存在")
	ErrInvalidPermissionRequest = errors.New("无效的权限请求")
)

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
	Page     int            `query:"page" json:"page" validate:"min=1"`
	PageSize int            `query:"page_size" json:"page_size" validate:"min=1,max=100"`
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
type ListPermissionsResponse struct {
	Total int                   `json:"total"`
	Page  int                   `json:"page"`
	Data  []*PermissionResponse `json:"data"`
}

// PermissionServiceInterface 权限服务接口
type PermissionServiceInterface interface {
	// 权限管理
	CreatePermission(ctx context.Context, req *CreatePermissionRequest) error
	UpdatePermission(ctx context.Context, req *UpdatePermissionRequest) error
	DeletePermission(ctx context.Context, roleName, domain, object, action string) error
	GetPermission(ctx context.Context, roleName, domain, object, action string) (*PermissionResponse, error)
	ListPermissions(ctx context.Context, req *ListPermissionsRequest) (*ListPermissionsResponse, error)

	// 角色权限管理
	AssignPermissionsToRole(ctx context.Context, roleName, domain string, permissions []PermissionRule) error
	RemovePermissionsFromRole(ctx context.Context, roleName, domain string, permissions []PermissionRule) error
	GetRolePermissions(ctx context.Context, roleName, domain string) ([]*PermissionResponse, error)

	// 用户权限获取
	GetUserMenuPermissions(ctx context.Context, userID string, domain string) ([]*PermissionResponse, error)
	GetUserButtonPermissions(ctx context.Context, userID string, domain string) ([]*PermissionResponse, error)
	GetUserAPIPermissions(ctx context.Context, userID string, domain string) ([]*PermissionResponse, error)

	// 权限验证
	CheckPermission(ctx context.Context, userID string, domain, object, action string) (bool, error)
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

// cacheKey 生成缓存键
func (s *PermissionService) cacheKey(prefix string, parts ...string) string {
	return prefix + ":" + strings.Join(parts, ":")
}

// defaultCacheTTL 默认缓存过期时间
func (s *PermissionService) defaultCacheTTL() time.Duration {
	return 5 * time.Minute
}

// clearUserPermissionsCache 清除指定用户的所有权限缓存
func (s *PermissionService) clearUserPermissionsCache(ctx context.Context, userUID string, domain string) {
	if s.cache == nil {
		return
	}
	_ = s.cache.Delete(ctx, s.cacheKey("user", "menu", userUID, domain))
	_ = s.cache.Delete(ctx, s.cacheKey("user", "button", userUID, domain))
	_ = s.cache.Delete(ctx, s.cacheKey("user", "api", userUID, domain))
}

// clearRolePermissionsCache 清除指定角色的权限缓存
func (s *PermissionService) clearRolePermissionsCache(ctx context.Context, roleName, domain string) {
	if s.cache == nil {
		return
	}
	_ = s.cache.Delete(ctx, s.cacheKey("role", "perms", roleName, domain))
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
}

// clearRoleAndUsersCache 清除角色缓存及其关联的所有用户缓存
func (s *PermissionService) clearRoleAndUsersCache(ctx context.Context, roleName, domain string) {
	s.clearRolePermissionsCache(ctx, roleName, domain)
	s.clearAllUsersPermissionsByRole(ctx, roleName, domain)
}

// CreatePermission 创建权限
func (s *PermissionService) CreatePermission(ctx context.Context, req *CreatePermissionRequest) error {
	// 验证权限类型
	if !isValidPermissionType(req.Type) {
		return ErrInvalidPermissionType
	}

	// 检查角色是否存在
	exists, err := s.client.Role.Query().Where(role.NameEQ(req.RoleName)).Exist(ctx)
	if err != nil {
		return fmt.Errorf("检查角色失败: %w", err)
	}
	if !exists {
		return ErrRoleNotFound
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

	// 创建权限规则
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

	// 清除相关缓存
	s.clearRoleAndUsersCache(ctx, req.RoleName, req.Domain)

	return nil
}

// UpdatePermission 更新权限
func (s *PermissionService) UpdatePermission(ctx context.Context, req *UpdatePermissionRequest) error {
	// 验证权限类型
	if !isValidPermissionType(req.Type) {
		return ErrInvalidPermissionType
	}

	// 检查角色是否存在
	exists, err := s.client.Role.Query().Where(role.NameEQ(req.RoleName)).Exist(ctx)
	if err != nil {
		return fmt.Errorf("检查角色失败: %w", err)
	}
	if !exists {
		return ErrRoleNotFound
	}

	// 查找要更新的权限
	permission, err := s.client.CasbinRule.Query().
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

	// 构建更新
	update := s.client.CasbinRule.UpdateOne(permission)

	// 更新对象
	if req.NewObject != "" {
		update.SetV2(req.NewObject)
	}

	// 更新操作
	if req.NewAction != "" {
		update.SetV3(req.NewAction)
	}

	// 更新权限类型
	update.SetV4(string(req.Type))

	// 更新描述
	if req.Description != "" {
		update.SetV5(req.Description)
	}

	// 执行更新
	_, err = update.Save(ctx)
	if err != nil {
		return fmt.Errorf("更新权限失败: %w", err)
	}

	// 清除相关缓存
	s.clearRoleAndUsersCache(ctx, req.RoleName, req.Domain)

	return nil
}

// DeletePermission 删除权限
func (s *PermissionService) DeletePermission(ctx context.Context, roleName, domain, object, action string) error {
	// 查找要删除的权限
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
			return ErrPermissionNotFound
		}
		return fmt.Errorf("查找权限失败: %w", err)
	}

	// 执行删除
	err = s.client.CasbinRule.DeleteOne(permission).Exec(ctx)
	if err != nil {
		return fmt.Errorf("删除权限失败: %w", err)
	}

	// 清除相关缓存
	s.clearRoleAndUsersCache(ctx, roleName, domain)

	return nil
}

// GetPermission 获取权限详情
func (s *PermissionService) GetPermission(ctx context.Context, roleName, domain, object, action string) (*PermissionResponse, error) {
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
func (s *PermissionService) ListPermissions(ctx context.Context, req *ListPermissionsRequest) (*ListPermissionsResponse, error) {
	// 设置默认值
	if req.Page < 1 {
		req.Page = 1
	}
	if req.PageSize < 1 {
		req.PageSize = 10
	}
	if req.PageSize > 100 {
		req.PageSize = 100
	}

	// 构建查询
	query := s.client.CasbinRule.Query().Where(casbinrule.PtypeEQ("p"))

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

	// 获取总数
	total, err := query.Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取权限总数失败: %w", err)
	}

	// 分页查询
	offset := (req.Page - 1) * req.PageSize
	permissions, err := query.
		Offset(offset).
		Limit(req.PageSize).
		All(ctx)
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

// AssignPermissionsToRole 为角色分配权限
func (s *PermissionService) AssignPermissionsToRole(ctx context.Context, roleName, domain string, permissions []PermissionRule) error {
	// 检查角色是否存在
	exists, err := s.client.Role.Query().Where(role.NameEQ(roleName)).Exist(ctx)
	if err != nil {
		return fmt.Errorf("检查角色失败: %w", err)
	}
	if !exists {
		return ErrRoleNotFound
	}

	// 批量创建权限
	for _, perm := range permissions {
		// 确定权限类型
		permType := determinePermissionType(perm.Action)

		// 检查权限是否已存在
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
			continue // 跳过已存在的权限
		}

		// 创建权限
		_, err = s.client.CasbinRule.Create().
			SetPtype("p").
			SetV0(roleName).
			SetV1(domain).
			SetV2(perm.Object).
			SetV3(perm.Action).
			SetV4(string(permType)).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("分配权限失败: %w", err)
		}
	}

	// 清除相关缓存
	s.clearRoleAndUsersCache(ctx, roleName, domain)

	return nil
}

// RemovePermissionsFromRole 从角色移除权限
func (s *PermissionService) RemovePermissionsFromRole(ctx context.Context, roleName, domain string, permissions []PermissionRule) error {
	// 检查角色是否存在
	exists, err := s.client.Role.Query().Where(role.NameEQ(roleName)).Exist(ctx)
	if err != nil {
		return fmt.Errorf("检查角色失败: %w", err)
	}
	if !exists {
		return ErrRoleNotFound
	}

	// 批量删除权限
	for _, perm := range permissions {
		// 删除权限
		_, err := s.client.CasbinRule.Delete().
			Where(
				casbinrule.PtypeEQ("p"),
				casbinrule.V0EQ(roleName),
				casbinrule.V1EQ(domain),
				casbinrule.V2EQ(perm.Object),
				casbinrule.V3EQ(perm.Action),
			).Exec(ctx)
		if err != nil {
			return fmt.Errorf("移除权限失败: %w", err)
		}
	}

	// 清除相关缓存
	s.clearRoleAndUsersCache(ctx, roleName, domain)

	return nil
}

// GetRolePermissions 获取角色拥有的权限
func (s *PermissionService) GetRolePermissions(ctx context.Context, roleName, domain string) ([]*PermissionResponse, error) {
	cacheKey := s.cacheKey("role", "perms", roleName, domain)

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

// GetUserMenuPermissions 获取用户的菜单权限
func (s *PermissionService) GetUserMenuPermissions(ctx context.Context, userID string, domain string) ([]*PermissionResponse, error) {
	cacheKey := s.cacheKey("user", "menu", userID, domain)

	// 尝试从缓存获取
	if s.cache != nil {
		cached, err := s.cache.Get(ctx, cacheKey)
		if err == nil && cached != nil {
			return cached, nil
		}
	}

	// 通过UID查找用户的数据库ID
	u, err := s.client.User.Query().Where(user.UIDEQ(userID)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取用户信息失败: %w", err)
	}

	// 获取用户角色（使用eager loading预加载角色）
	userRoles, err := s.client.UserRole.Query().
		Where(userrole.UserIDEQ(u.ID)).
		WithRole().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取用户角色失败: %w", err)
	}

	if len(userRoles) == 0 {
		return []*PermissionResponse{}, nil
	}

	// 提取角色名称
	roleNames := make([]string, 0, len(userRoles))
	for _, ur := range userRoles {
		if ur.Edges.Role != nil {
			roleNames = append(roleNames, ur.Edges.Role.Name)
		}
	}

	// 查询菜单权限
	permissions, err := s.client.CasbinRule.Query().
		Where(
			casbinrule.PtypeEQ("p"),
			casbinrule.V0In(roleNames...),
			casbinrule.V1EQ(domain),
			casbinrule.V3EQ("menu"),
		).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取菜单权限失败: %w", err)
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

// GetUserButtonPermissions 获取用户的按钮权限
func (s *PermissionService) GetUserButtonPermissions(ctx context.Context, userID string, domain string) ([]*PermissionResponse, error) {
	cacheKey := s.cacheKey("user", "button", userID, domain)

	// 尝试从缓存获取
	if s.cache != nil {
		cached, err := s.cache.Get(ctx, cacheKey)
		if err == nil && cached != nil {
			return cached, nil
		}
	}

	// 通过UID查找用户的数据库ID
	u, err := s.client.User.Query().Where(user.UIDEQ(userID)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取用户信息失败: %w", err)
	}

	// 获取用户角色（使用eager loading预加载角色）
	userRoles, err := s.client.UserRole.Query().
		Where(userrole.UserIDEQ(u.ID)).
		WithRole().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取用户角色失败: %w", err)
	}

	if len(userRoles) == 0 {
		return []*PermissionResponse{}, nil
	}

	// 提取角色名称
	roleNames := make([]string, 0, len(userRoles))
	for _, ur := range userRoles {
		if ur.Edges.Role != nil {
			roleNames = append(roleNames, ur.Edges.Role.Name)
		}
	}

	// 查询按钮权限
	permissions, err := s.client.CasbinRule.Query().
		Where(
			casbinrule.PtypeEQ("p"),
			casbinrule.V0In(roleNames...),
			casbinrule.V1EQ(domain),
			casbinrule.V3EQ("button"),
		).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取按钮权限失败: %w", err)
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

// GetUserAPIPermissions 获取用户的API权限
func (s *PermissionService) GetUserAPIPermissions(ctx context.Context, userID string, domain string) ([]*PermissionResponse, error) {
	cacheKey := s.cacheKey("user", "api", userID, domain)

	// 尝试从缓存获取
	if s.cache != nil {
		cached, err := s.cache.Get(ctx, cacheKey)
		if err == nil && cached != nil {
			return cached, nil
		}
	}

	// 通过UID查找用户的数据库ID
	u, err := s.client.User.Query().Where(user.UIDEQ(userID)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取用户信息失败: %w", err)
	}

	// 获取用户角色（使用eager loading预加载角色）
	userRoles, err := s.client.UserRole.Query().
		Where(userrole.UserIDEQ(u.ID)).
		WithRole().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取用户角色失败: %w", err)
	}

	if len(userRoles) == 0 {
		return []*PermissionResponse{}, nil
	}

	// 提取角色名称
	roleNames := make([]string, 0, len(userRoles))
	for _, ur := range userRoles {
		if ur.Edges.Role != nil {
			roleNames = append(roleNames, ur.Edges.Role.Name)
		}
	}

	// 查询API权限（排除menu和button类型）
	permissions, err := s.client.CasbinRule.Query().
		Where(
			casbinrule.PtypeEQ("p"),
			casbinrule.V0In(roleNames...),
			casbinrule.V1EQ(domain),
			casbinrule.V3NEQ("menu"),
			casbinrule.V3NEQ("button"),
		).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取API权限失败: %w", err)
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

// CheckPermission 检查用户是否有某个权限
func (s *PermissionService) CheckPermission(ctx context.Context, userID string, domain, object, action string) (bool, error) {
	// 通过UID查找用户的数据库ID
	u, err := s.client.User.Query().Where(user.UIDEQ(userID)).Only(ctx)
	if err != nil {
		return false, fmt.Errorf("获取用户信息失败: %w", err)
	}

	// 获取用户角色（使用eager loading预加载角色）
	userRoles, err := s.client.UserRole.Query().
		Where(userrole.UserIDEQ(u.ID)).
		WithRole().
		All(ctx)
	if err != nil {
		return false, fmt.Errorf("获取用户角色失败: %w", err)
	}

	if len(userRoles) == 0 {
		return false, nil
	}

	// 提取角色名称
	roleNames := make([]string, 0, len(userRoles))
	for _, ur := range userRoles {
		if ur.Edges.Role != nil {
			roleNames = append(roleNames, ur.Edges.Role.Name)
		}
	}

	// 检查权限
	exists, err := s.client.CasbinRule.Query().
		Where(
			casbinrule.PtypeEQ("p"),
			casbinrule.V0In(roleNames...),
			casbinrule.V1EQ(domain),
			casbinrule.V2EQ(object),
			casbinrule.V3EQ(action),
		).Exist(ctx)
	if err != nil {
		return false, fmt.Errorf("检查权限失败: %w", err)
	}

	return exists, nil
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
