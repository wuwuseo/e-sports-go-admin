package service

import (
	"context"
	"errors"
	"fmt"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/adminrole"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/user"
	"go-admin/internal/ent/userrole"
	"go-admin/internal/global"

	"github.com/wuwuseo/cmf/cache"
)

var (
	ErrInvalidRoleIDs                    = errors.New("包含无效的角色ID")
	ErrPrimaryRoleInvalid                = errors.New("主角色必须在分配的角色列表内")
	ErrSuperAdminRoleAssignmentForbidden = errors.New("权限不足，禁止分配超级管理员角色")
)

const superAdminRoleName = "admin"

func rejectSuperAdminRoleIDs(ctx context.Context, client *ent.Client, roleIDs []int) error {
	if len(roleIDs) == 0 {
		return nil
	}

	exists, err := client.Role.Query().
		Where(role.IDIn(roleIDs...), role.NameEQ(superAdminRoleName)).
		Exist(ctx)
	if err != nil {
		return err
	}
	if exists {
		return ErrSuperAdminRoleAssignmentForbidden
	}
	return nil
}

func requestedRoleIDs(roleIDs []int, primaryRoleID int) []int {
	if primaryRoleID == 0 {
		return roleIDs
	}
	return append(append([]int{}, roleIDs...), primaryRoleID)
}

// RoleItem 角色项
type RoleItem struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	IsPrimary   bool   `json:"is_primary"`
}

// AssignRolesRequest 分配角色请求
type AssignRolesRequest struct {
	RoleIDs       []int `json:"role_ids" validate:"required,min=1"`
	PrimaryRoleID int   `json:"primary_role_id"`
}

// UserRolesResponse 用户角色响应
type UserRolesResponse struct {
	Roles         []*RoleItem `json:"roles"`
	PrimaryRoleID int         `json:"primary_role_id"`
	PrimaryRole   *RoleItem   `json:"primary_role"`
}

// RoleAssignmentService 角色分配服务
type RoleAssignmentService struct {
	client *ent.Client
	cache  *cache.Cache[[]byte]
}

// RoleAssignmentServiceInterface 角色分配服务接口
type RoleAssignmentServiceInterface interface {
	GetUserRoles(ctx context.Context, userID int) (*UserRolesResponse, error)
	AssignRoles(ctx context.Context, userID int, req *AssignRolesRequest, operatorUID string) error
	GetAdminRoles(ctx context.Context, op *EffectiveDataScope, adminID int) (*UserRolesResponse, error)
	AssignAdminRoles(ctx context.Context, op *EffectiveDataScope, adminID int, req *AssignRolesRequest, operatorUID string) error
}

// NewRoleAssignmentService 创建角色分配服务实例
func NewRoleAssignmentService(rawCache *cache.Cache[[]byte]) *RoleAssignmentService {
	return &RoleAssignmentService{
		client: global.ORM,
		cache:  rawCache,
	}
}

// GetUserRoles 获取用户角色列表
func (s *RoleAssignmentService) GetUserRoles(ctx context.Context, userID int) (*UserRolesResponse, error) {
	_, err := s.client.User.Query().Where(user.IDEQ(userID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}

	userRoles, err := s.client.UserRole.Query().
		Where(userrole.UserIDEQ(userID)).
		WithRole(func(q *ent.RoleQuery) {
			q.Where(role.StatusEQ(role.StatusActive))
		}).
		All(ctx)
	if err != nil {
		return nil, err
	}

	roles := make([]*RoleItem, 0, len(userRoles))
	var primaryRoleItem *RoleItem
	primaryRoleID := 0

	for _, ur := range userRoles {
		if ur.Edges.Role == nil {
			continue
		}
		item := &RoleItem{
			ID:          ur.Edges.Role.ID,
			Name:        ur.Edges.Role.Name,
			DisplayName: ur.Edges.Role.DisplayName,
			Description: ur.Edges.Role.Description,
			IsPrimary:   ur.IsPrimary,
		}
		roles = append(roles, item)
		if ur.IsPrimary {
			primaryRoleID = ur.Edges.Role.ID
			primaryRoleItem = item
		}
	}

	return &UserRolesResponse{
		Roles:         roles,
		PrimaryRoleID: primaryRoleID,
		PrimaryRole:   primaryRoleItem,
	}, nil
}

// AssignRoles 分配用户角色（事务操作，先删后插）
func (s *RoleAssignmentService) AssignRoles(ctx context.Context, userID int, req *AssignRolesRequest, operatorUID string) error {
	// 如果没有指定主角色，默认使用第一个角色
	if req.PrimaryRoleID == 0 {
		req.PrimaryRoleID = req.RoleIDs[0]
	}
	if err := rejectSuperAdminRoleIDs(ctx, s.client, requestedRoleIDs(req.RoleIDs, req.PrimaryRoleID)); err != nil {
		return err
	}
	found := false
	for _, id := range req.RoleIDs {
		if id == req.PrimaryRoleID {
			found = true
			break
		}
	}
	if !found {
		return ErrPrimaryRoleInvalid
	}

	count, err := s.client.Role.Query().
		Where(role.IDIn(req.RoleIDs...), role.StatusEQ(role.StatusActive)).
		Count(ctx)
	if err != nil {
		return err
	}
	if count != len(req.RoleIDs) {
		return ErrInvalidRoleIDs
	}

	u, err := s.client.User.Query().Where(user.IDEQ(userID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrUserNotFound
		}
		return err
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}

	_, err = tx.UserRole.Delete().Where(userrole.UserIDEQ(userID)).Exec(ctx)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("清除旧角色失败: %w", err)
	}

	for _, roleID := range req.RoleIDs {
		isPrimary := roleID == req.PrimaryRoleID
		_, err = tx.UserRole.Create().
			SetUserID(userID).
			SetRoleID(roleID).
			SetIsPrimary(isPrimary).
			SetCreatedBy(operatorUID).
			Save(ctx)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("分配角色失败: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交事务失败: %w", err)
	}

	domain := "default"
	enforcer, err := global.EnforcerManager.GetEnforcer(domain)
	if err == nil {
		_, _ = enforcer.DeleteRolesForUserInDomain(u.UID, domain)

		for _, roleID := range req.RoleIDs {
			roleObj, err := s.client.Role.Query().Where(role.IDEQ(roleID)).Only(ctx)
			if err == nil {
				_, _ = enforcer.AddRoleForUserInDomain(u.UID, roleObj.Name, domain)
			}
		}
	}

	if s.cache != nil {
		_ = s.cache.Delete(ctx, fmt.Sprintf("user_roles:%d", userID))
		_ = s.cache.Delete(ctx, fmt.Sprintf("user:menu:%s:%s", u.UID, domain))
		_ = s.cache.Delete(ctx, fmt.Sprintf("user:button:%s:%s", u.UID, domain))
		_ = s.cache.Delete(ctx, fmt.Sprintf("user:api:%s:%s", u.UID, domain))
	}

	return nil
}

// GetAdminRoles 获取管理员角色列表
func (s *RoleAssignmentService) GetAdminRoles(ctx context.Context, op *EffectiveDataScope, adminID int) (*UserRolesResponse, error) {
	if err := ensureAdminInScope(ctx, s.client, op, adminID); err != nil {
		return nil, err
	}
	_, err := s.client.Admin.Query().Where(admin.IDEQ(adminID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}

	adminRoles, err := s.client.AdminRole.Query().
		Where(adminrole.AdminIDEQ(adminID)).
		WithRole(func(q *ent.RoleQuery) {
			q.Where(role.StatusEQ(role.StatusActive))
		}).
		All(ctx)
	if err != nil {
		return nil, err
	}

	roles := make([]*RoleItem, 0, len(adminRoles))
	var primaryRoleItem *RoleItem
	primaryRoleID := 0

	for _, ar := range adminRoles {
		if ar.Edges.Role == nil {
			continue
		}
		item := &RoleItem{
			ID:          ar.Edges.Role.ID,
			Name:        ar.Edges.Role.Name,
			DisplayName: ar.Edges.Role.DisplayName,
			Description: ar.Edges.Role.Description,
			IsPrimary:   ar.IsPrimary,
		}
		roles = append(roles, item)
		if ar.IsPrimary {
			primaryRoleID = ar.Edges.Role.ID
			primaryRoleItem = item
		}
	}

	return &UserRolesResponse{
		Roles:         roles,
		PrimaryRoleID: primaryRoleID,
		PrimaryRole:   primaryRoleItem,
	}, nil
}

// AssignAdminRoles 分配管理员角色（事务操作，先删后插）
func (s *RoleAssignmentService) AssignAdminRoles(ctx context.Context, op *EffectiveDataScope, adminID int, req *AssignRolesRequest, operatorUID string) error {
	if err := ensureAdminInScope(ctx, s.client, op, adminID); err != nil {
		return err
	}
	if op != nil && op.AdminID == adminID {
		return ErrCannotModifySelfRoles
	}
	// 如果没有指定主角色，默认使用第一个角色
	if req.PrimaryRoleID == 0 {
		req.PrimaryRoleID = req.RoleIDs[0]
	}
	if err := rejectSuperAdminRoleIDs(ctx, s.client, requestedRoleIDs(req.RoleIDs, req.PrimaryRoleID)); err != nil {
		return err
	}
	// P1 防御：禁止越权分配——所授角色的数据范围必须被 op 完全包含
	if err := ensureRolesNotEscalating(ctx, s.client, op, requestedRoleIDs(req.RoleIDs, req.PrimaryRoleID)); err != nil {
		return err
	}
	found := false
	for _, id := range req.RoleIDs {
		if id == req.PrimaryRoleID {
			found = true
			break
		}
	}
	if !found {
		return ErrPrimaryRoleInvalid
	}

	count, err := s.client.Role.Query().
		Where(role.IDIn(req.RoleIDs...), role.StatusEQ(role.StatusActive)).
		Count(ctx)
	if err != nil {
		return err
	}
	if count != len(req.RoleIDs) {
		return ErrInvalidRoleIDs
	}

	a, err := s.client.Admin.Query().Where(admin.IDEQ(adminID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrUserNotFound
		}
		return err
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}

	_, err = tx.AdminRole.Delete().Where(adminrole.AdminIDEQ(adminID)).Exec(ctx)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("清除旧角色失败: %w", err)
	}

	for _, roleID := range req.RoleIDs {
		isPrimary := roleID == req.PrimaryRoleID
		_, err = tx.AdminRole.Create().
			SetAdminID(adminID).
			SetRoleID(roleID).
			SetIsPrimary(isPrimary).
			SetCreatedBy(operatorUID).
			Save(ctx)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("分配角色失败: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交事务失败: %w", err)
	}

	domain := "default"
	enforcer, err := global.EnforcerManager.GetEnforcer(domain)
	if err == nil {
		_, _ = enforcer.DeleteRolesForUserInDomain(a.UID, domain)

		for _, roleID := range req.RoleIDs {
			roleObj, err := s.client.Role.Query().Where(role.IDEQ(roleID)).Only(ctx)
			if err == nil {
				_, _ = enforcer.AddRoleForUserInDomain(a.UID, roleObj.Name, domain)
			}
		}
	}

	if s.cache != nil {
		_ = s.cache.Delete(ctx, fmt.Sprintf("admin_roles:%d", adminID))
		_ = s.cache.Delete(ctx, fmt.Sprintf("user:menu:%s:%s", a.UID, domain))
		_ = s.cache.Delete(ctx, fmt.Sprintf("user:button:%s:%s", a.UID, domain))
		_ = s.cache.Delete(ctx, fmt.Sprintf("user:api:%s:%s", a.UID, domain))
	}

	return nil
}
