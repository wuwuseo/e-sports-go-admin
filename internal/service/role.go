package service

import (
	"context"
	"errors"
	"fmt"

	"go-admin/internal/ent"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/userrole"
	"go-admin/internal/global"

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

// RoleServiceInterface 角色服务接口
type RoleServiceInterface interface {
	// 角色管理
	CreateRole(ctx context.Context, req *CreateRoleRequest) (*RoleResponse, error)
	GetRole(ctx context.Context, id int) (*RoleResponse, error)
	ListRoles(ctx context.Context, req *ListRolesRequest) (*ListRolesResponse, error)
	UpdateRole(ctx context.Context, id int, req *UpdateRoleRequest) error
	DeleteRole(ctx context.Context, id int) error

	// 角色状态管理
	EnableRole(ctx context.Context, id int) error
	DisableRole(ctx context.Context, id int) error
}

type RoleService struct {
	client *ent.Client
	cache  *cache.TypedCache[[]*PermissionResponse]
}

// NewRoleService 创建角色服务实例
func NewRoleService(rawCache *cache.Cache[[]byte]) *RoleService {
	var typedCache *cache.TypedCache[[]*PermissionResponse]
	if rawCache != nil {
		typedCache = cache.NewTypedCache[[]*PermissionResponse](rawCache)
	}
	return &RoleService{
		client: global.ORM,
		cache:  typedCache,
	}
}

// clearUserPermissionsByRoleID 清除拥有指定角色的所有用户权限缓存
func (s *RoleService) clearUserPermissionsByRoleID(ctx context.Context, roleID int, domain string) {
	if s.cache == nil {
		return
	}
	userRoles, err := s.client.UserRole.Query().Where(userrole.RoleIDEQ(roleID)).All(ctx)
	if err != nil {
		return
	}
	for _, ur := range userRoles {
		_ = s.cache.Delete(ctx, fmt.Sprintf("user:menu:%d:%s", ur.UserID, domain))
		_ = s.cache.Delete(ctx, fmt.Sprintf("user:button:%d:%s", ur.UserID, domain))
		_ = s.cache.Delete(ctx, fmt.Sprintf("user:api:%d:%s", ur.UserID, domain))
	}
}

// CreateRoleRequest 创建角色请求
type CreateRoleRequest struct {
	Name        string `json:"name" validate:"required,max=100"`
	DisplayName string `json:"display_name" validate:"required,max=255"`
	Description string `json:"description" validate:"max=500"`
	CreatedBy   string `json:"created_by,omitempty"`
}

// UpdateRoleRequest 更新角色请求
type UpdateRoleRequest struct {
	DisplayName string `json:"display_name" validate:"max=255"`
	Description string `json:"description" validate:"max=500"`
	UpdatedBy   string `json:"updated_by,omitempty"`
}

// RoleResponse 角色响应
type RoleResponse struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	IsSystem    bool   `json:"is_system"`
	Status      string `json:"status"`
	CreatedBy   string `json:"created_by,omitempty"`
	UpdatedBy   string `json:"updated_by,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// ListRolesRequest 角色列表请求
type ListRolesRequest struct {
	Page     int    `json:"page" validate:"min=1"`
	PageSize int    `json:"page_size" validate:"min=1,max=100"`
	Keyword  string `json:"keyword"`
	Status   string `json:"status"`
}

// ListRolesResponse 角色列表响应
type ListRolesResponse struct {
	Total int             `json:"total"`
	Page  int             `json:"page"`
	Data  []*RoleResponse `json:"data"`
}

// CreateRole 创建角色
// 需求: 1.1, 1.2, 1.3, 1.4
func (s *RoleService) CreateRole(ctx context.Context, req *CreateRoleRequest) (*RoleResponse, error) {
	// 验证角色名称不为空
	if req.Name == "" {
		return nil, ErrInvalidRoleName
	}

	// 验证角色名称长度
	if len(req.Name) > 100 {
		return nil, fmt.Errorf("角色名称不能超过100个字符")
	}

	// 验证显示名称不为空
	if req.DisplayName == "" {
		return nil, fmt.Errorf("显示名称不能为空")
	}

	// 检查角色名称唯一性
	exists, err := s.client.Role.Query().
		Where(role.NameEQ(req.Name)).
		Exist(ctx)
	if err != nil {
		return nil, fmt.Errorf("检查角色名称失败: %w", err)
	}
	if exists {
		return nil, ErrRoleNameExists
	}

	// 创建角色
	builder := s.client.Role.Create().
		SetName(req.Name).
		SetDisplayName(req.DisplayName).
		SetStatus(role.StatusActive)

	if req.Description != "" {
		builder.SetDescription(req.Description)
	}

	if req.CreatedBy != "" {
		builder.SetCreatedBy(req.CreatedBy)
	}

	r, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("创建角色失败: %w", err)
	}

	return s.toRoleResponse(r), nil
}

// GetRole 获取角色详情
// 需求: 2.1, 2.2
func (s *RoleService) GetRole(ctx context.Context, id int) (*RoleResponse, error) {
	r, err := s.client.Role.Query().
		Where(role.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrRoleNotFound
		}
		return nil, fmt.Errorf("获取角色失败: %w", err)
	}

	return s.toRoleResponse(r), nil
}

// ListRoles 获取角色列表
// 需求: 2.1, 2.3, 2.4
func (s *RoleService) ListRoles(ctx context.Context, req *ListRolesRequest) (*ListRolesResponse, error) {
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
	query := s.client.Role.Query()

	// 关键字搜索（名称或描述）
	if req.Keyword != "" {
		query = query.Where(
			role.Or(
				role.NameContains(req.Keyword),
				role.DisplayNameContains(req.Keyword),
				role.DescriptionContains(req.Keyword),
			),
		)
	}

	// 状态筛选
	if req.Status != "" {
		query = query.Where(role.StatusEQ(role.Status(req.Status)))
	}

	// 获取总数
	total, err := query.Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取角色总数失败: %w", err)
	}

	// 分页查询
	offset := (req.Page - 1) * req.PageSize
	roles, err := query.
		Offset(offset).
		Limit(req.PageSize).
		Order(ent.Desc(role.FieldCreateTime)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取角色列表失败: %w", err)
	}

	// 转换为响应格式
	data := make([]*RoleResponse, 0, len(roles))
	for _, r := range roles {
		data = append(data, s.toRoleResponse(r))
	}

	return &ListRolesResponse{
		Total: total,
		Page:  req.Page,
		Data:  data,
	}, nil
}

// UpdateRole 更新角色
// 需求: 3.1, 3.2, 3.3, 3.4, 3.5
func (s *RoleService) UpdateRole(ctx context.Context, id int, req *UpdateRoleRequest) error {
	// 检查角色是否存在
	r, err := s.client.Role.Query().
		Where(role.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrRoleNotFound
		}
		return fmt.Errorf("查询角色失败: %w", err)
	}

	// 系统内置角色不允许修改某些字段
	if r.IsSystem {
		// 系统角色只允许修改描述
		if req.DisplayName != "" && req.DisplayName != r.DisplayName {
			return fmt.Errorf("系统内置角色不允许修改显示名称")
		}
	}

	// 构建更新
	update := s.client.Role.UpdateOne(r)

	if req.DisplayName != "" {
		update.SetDisplayName(req.DisplayName)
	}

	if req.Description != "" {
		update.SetDescription(req.Description)
	}

	if req.UpdatedBy != "" {
		update.SetUpdatedBy(req.UpdatedBy)
	}

	_, err = update.Save(ctx)
	if err != nil {
		return fmt.Errorf("更新角色失败: %w", err)
	}

	// 清除拥有该角色的所有用户权限缓存
	s.clearUserPermissionsByRoleID(ctx, id, "default")

	return nil
}

// DeleteRole 删除角色
// 需求: 4.1, 4.2, 4.3, 4.4
func (s *RoleService) DeleteRole(ctx context.Context, id int) error {
	// 检查角色是否存在
	r, err := s.client.Role.Query().
		Where(role.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrRoleNotFound
		}
		return fmt.Errorf("查询角色失败: %w", err)
	}

	// 系统内置角色不可删除
	if r.IsSystem {
		return ErrSystemRoleProtected
	}

	// 检查角色是否被用户使用
	inUse, err := s.client.UserRole.Query().
		Where(userrole.RoleIDEQ(id)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("检查角色使用情况失败: %w", err)
	}
	if inUse {
		return ErrRoleInUse
	}

	// 删除角色
	err = s.client.Role.DeleteOne(r).Exec(ctx)
	if err != nil {
		return fmt.Errorf("删除角色失败: %w", err)
	}

	// TODO: 从Casbin策略中移除该角色的所有权限规则
	// 这将在任务4中实现权限服务时处理

	return nil
}

// EnableRole 启用角色
// 需求: 7.1, 7.4
func (s *RoleService) EnableRole(ctx context.Context, id int) error {
	// 检查角色是否存在
	r, err := s.client.Role.Query().
		Where(role.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrRoleNotFound
		}
		return fmt.Errorf("查询角色失败: %w", err)
	}

	// 系统内置角色不允许修改状态
	if r.IsSystem {
		return fmt.Errorf("系统内置角色不允许修改状态")
	}

	// 如果已经是启用状态，直接返回
	if r.Status == role.StatusActive {
		return nil
	}

	// 更新状态为启用
	_, err = s.client.Role.UpdateOne(r).
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("启用角色失败: %w", err)
	}

	// 清除拥有该角色的所有用户权限缓存
	s.clearUserPermissionsByRoleID(ctx, id, "default")

	return nil
}

// DisableRole 禁用角色
// 需求: 7.1, 7.2, 7.3
func (s *RoleService) DisableRole(ctx context.Context, id int) error {
	// 检查角色是否存在
	r, err := s.client.Role.Query().
		Where(role.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrRoleNotFound
		}
		return fmt.Errorf("查询角色失败: %w", err)
	}

	// 系统内置角色不允许修改状态
	if r.IsSystem {
		return fmt.Errorf("系统内置角色不允许修改状态")
	}

	// 如果已经是禁用状态，直接返回
	if r.Status == role.StatusInactive {
		return nil
	}

	// 更新状态为禁用
	_, err = s.client.Role.UpdateOne(r).
		SetStatus(role.StatusInactive).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("禁用角色失败: %w", err)
	}

	// 清除拥有该角色的所有用户权限缓存
	s.clearUserPermissionsByRoleID(ctx, id, "default")

	return nil
}

// toRoleResponse 将Ent实体转换为响应格式
func (s *RoleService) toRoleResponse(r *ent.Role) *RoleResponse {
	return &RoleResponse{
		ID:          r.ID,
		Name:        r.Name,
		DisplayName: r.DisplayName,
		Description: r.Description,
		IsSystem:    r.IsSystem,
		Status:      string(r.Status),
		CreatedBy:   r.CreatedBy,
		UpdatedBy:   r.UpdatedBy,
		CreatedAt:   r.CreateTime.Format("2006-01-02 15:04:05"),
		UpdatedAt:   r.UpdateTime.Format("2006-01-02 15:04:05"),
	}
}
