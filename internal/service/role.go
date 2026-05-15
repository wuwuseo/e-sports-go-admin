package service

import (
	"context"
	"errors"
	"fmt"

	"go-admin/internal/ent"
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

// ListAllRolesRequest 全量角色列表请求
type ListAllRolesRequest struct {
	Keyword string `query:"keyword" json:"keyword"`
	Status  string `query:"status" json:"status"`
}

// RoleServiceInterface 角色服务接口
type RoleServiceInterface interface {
	// 角色管理
	CreateRole(ctx context.Context, req *CreateRoleRequest) (*RoleResponse, error)
	GetRole(ctx context.Context, id int) (*RoleResponse, error)
	ListRoles(ctx context.Context, req *ListRolesRequest) (*ListRolesResponse, error)
	ListAllRoles(ctx context.Context, req *ListAllRolesRequest) ([]*RoleResponse, error)
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
	userRoles, err := s.client.UserRole.Query().
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
	Page     int    `query:"page" json:"page" validate:"min=1"`
	PageSize int    `query:"page_size" json:"page_size" validate:"min=1,max=100"`
	Keyword  string `query:"keyword" json:"keyword"`
	Status   string `query:"status" json:"status"`
}

// ListRolesResponse 角色列表响应
type ListRolesResponse struct {
	Total int             `json:"total"`
	Page  int             `json:"page"`
	Data  []*RoleResponse `json:"data"`
}

// CreateRole 创建角色
func (s *RoleService) CreateRole(ctx context.Context, req *CreateRoleRequest) (*RoleResponse, error) {
	if req.Name == "" {
		return nil, ErrInvalidRoleName
	}
	if len(req.Name) > 100 {
		return nil, fmt.Errorf("角色名称不能超过100个字符")
	}
	if req.DisplayName == "" {
		return nil, fmt.Errorf("显示名称不能为空")
	}

	exists, err := s.client.Role.Query().
		Where(role.NameEQ(req.Name)).
		Exist(ctx)
	if err != nil {
		return nil, fmt.Errorf("检查角色名称失败: %w", err)
	}
	if exists {
		return nil, ErrRoleNameExists
	}

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
func (s *RoleService) ListRoles(ctx context.Context, req *ListRolesRequest) (*ListRolesResponse, error) {
	req.Page, req.PageSize = utils.NormalizePagination(req.Page, req.PageSize)

	query := s.client.Role.Query()

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

	total, err := query.Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取角色总数失败: %w", err)
	}

	offset := (req.Page - 1) * req.PageSize
	roles, err := query.
		Offset(offset).
		Limit(req.PageSize).
		Order(ent.Desc(role.FieldCreateTime)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取角色列表失败: %w", err)
	}

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

// ListAllRoles 获取所有角色（不带分页）
func (s *RoleService) ListAllRoles(ctx context.Context, req *ListAllRolesRequest) ([]*RoleResponse, error) {
	query := s.client.Role.Query()

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

	data := make([]*RoleResponse, 0, len(roles))
	for _, r := range roles {
		data = append(data, s.toRoleResponse(r))
	}

	return data, nil
}

// UpdateRole 更新角色
func (s *RoleService) UpdateRole(ctx context.Context, id int, req *UpdateRoleRequest) error {
	r, err := s.client.Role.Query().
		Where(role.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrRoleNotFound
		}
		return fmt.Errorf("查询角色失败: %w", err)
	}

	if r.IsSystem {
		if req.DisplayName != "" && req.DisplayName != r.DisplayName {
			return fmt.Errorf("系统内置角色不允许修改显示名称")
		}
	}

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

	s.clearUserPermissionsByRoleID(ctx, id, "default")

	return nil
}

// DeleteRole 删除角色
func (s *RoleService) DeleteRole(ctx context.Context, id int) error {
	r, err := s.client.Role.Query().
		Where(role.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrRoleNotFound
		}
		return fmt.Errorf("查询角色失败: %w", err)
	}

	if r.IsSystem {
		return ErrSystemRoleProtected
	}

	inUse, err := s.client.UserRole.Query().
		Where(userrole.RoleIDEQ(id)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("检查角色使用情况失败: %w", err)
	}
	if inUse {
		return ErrRoleInUse
	}

	err = s.client.Role.DeleteOne(r).Exec(ctx)
	if err != nil {
		return fmt.Errorf("删除角色失败: %w", err)
	}

	return nil
}

// EnableRole 启用角色
func (s *RoleService) EnableRole(ctx context.Context, id int) error {
	r, err := s.client.Role.Query().
		Where(role.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrRoleNotFound
		}
		return fmt.Errorf("查询角色失败: %w", err)
	}

	if r.IsSystem {
		return fmt.Errorf("系统内置角色不允许修改状态")
	}

	if r.Status == role.StatusActive {
		return nil
	}

	_, err = s.client.Role.UpdateOne(r).
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("启用角色失败: %w", err)
	}

	s.clearUserPermissionsByRoleID(ctx, id, "default")

	return nil
}

// DisableRole 禁用角色
func (s *RoleService) DisableRole(ctx context.Context, id int) error {
	r, err := s.client.Role.Query().
		Where(role.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrRoleNotFound
		}
		return fmt.Errorf("查询角色失败: %w", err)
	}

	if r.IsSystem {
		return fmt.Errorf("系统内置角色不允许修改状态")
	}

	if r.Status == role.StatusInactive {
		return nil
	}

	_, err = s.client.Role.UpdateOne(r).
		SetStatus(role.StatusInactive).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("禁用角色失败: %w", err)
	}

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
