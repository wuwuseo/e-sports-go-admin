package service

import (
	"context"
	"errors"
	"fmt"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/role"
	"go-admin/internal/global"
	"go-admin/internal/utils"

	"github.com/wuwuseo/cmf/crypto"
)

var (
	ErrAdminEmailExists    = errors.New("admin email already exists")
	ErrAdminUsernameExists = errors.New("admin username already exists")
)

// UpdateAdminRequest 更新管理员请求
type UpdateAdminRequest struct {
	Nickname     string `json:"nickname"`
	Name         string `json:"name"`
	Phone        string `json:"phone"`
	Email        string `json:"email" validate:"omitempty,email"`
	Password     string `json:"password" validate:"omitempty,min=6"`
	DepartmentID *int   `json:"department_id"`
}

// SuperAdminServiceInterface 超级管理员服务接口
type SuperAdminServiceInterface interface {
	CreateAdmin(ctx context.Context, req *CreateAdminRequest) (*AdminResponse, error)
	ListAdmins(ctx context.Context, req *AdminListRequest) (*ListResponse[*AdminListItem], error)
	GetAdminByID(ctx context.Context, adminID int) (*AdminResponse, error)
	UpdateAdmin(ctx context.Context, adminID int, req *UpdateAdminRequest) (*AdminResponse, error)
	ActivateAdmin(ctx context.Context, adminID int) error
	DeactivateAdmin(ctx context.Context, adminID int) error
}

// SuperAdminService 超级管理员服务
type SuperAdminService struct {
	client *ent.Client
}

// NewSuperAdminService 创建超级管理员服务实例
func NewSuperAdminService() *SuperAdminService {
	return &SuperAdminService{
		client: global.ORM,
	}
}

// CreateAdminRequest 创建管理员请求
type CreateAdminRequest struct {
	Email         string `json:"email" validate:"required,email"`
	Password      string `json:"password" validate:"required,min=6"`
	Username      string `json:"username" validate:"required,min=3"`
	Nickname      string `json:"nickname"`
	Name          string `json:"name"`
	Phone         string `json:"phone"`
	RoleIDs       []int  `json:"role_ids"`
	PrimaryRoleID int    `json:"primary_role_id"`
	DepartmentID  int    `json:"department_id"`
}

// AdminListRequest 管理员列表请求
type AdminListRequest struct {
	Page       int    `json:"page"`
	PageSize   int    `json:"page_size"`
	Keyword    string `json:"keyword"`
	Status     string `json:"status"`
	CurrentUID string `json:"current_uid,omitempty"` // 当前操作者UID，用于数据权限过滤
}

// AdminListItem 管理员列表项
type AdminListItem struct {
	ID           int    `json:"id"`
	UID          string `json:"uid"`
	Email        string `json:"email"`
	Username     string `json:"username"`
	Nickname     string `json:"nickname"`
	Status       string `json:"status"`
	DepartmentID int    `json:"department_id"`
}

// CreateAdmin 创建管理员
func (s *SuperAdminService) CreateAdmin(ctx context.Context, req *CreateAdminRequest) (*AdminResponse, error) {
	if err := rejectSuperAdminRoleIDs(ctx, s.client, requestedRoleIDs(req.RoleIDs, req.PrimaryRoleID)); err != nil {
		return nil, err
	}

	emailExists, err := s.client.Admin.Query().
		Where(admin.EmailEQ(req.Email)).
		Exist(ctx)
	if err != nil {
		return nil, err
	}
	if emailExists {
		return nil, ErrAdminEmailExists
	}

	usernameExists, err := s.client.Admin.Query().
		Where(admin.UsernameEQ(req.Username)).
		Exist(ctx)
	if err != nil {
		return nil, err
	}
	if usernameExists {
		return nil, ErrAdminUsernameExists
	}

	hashedPassword, salt, err := crypto.HashPassword(req.Password)
	if err != nil {
		return nil, err
	}

	a, err := s.client.Admin.Create().
		SetEmail(req.Email).
		SetPassword(hashedPassword).
		SetPasswordSalt(salt).
		SetUID(utils.GenerateUID()).
		SetUsername(req.Username).
		SetNickname(req.Nickname).
		SetName(req.Name).
		SetPhone(req.Phone).
		SetDepartmentID(req.DepartmentID).
		SetStatus(admin.StatusInactive).
		Save(ctx)
	if err != nil {
		return nil, err
	}

	// 如果请求中携带了 RoleIDs，创建 admin_role 关联并同步到 Casbin
	if len(req.RoleIDs) > 0 {
		primaryID := req.PrimaryRoleID
		if primaryID == 0 {
			primaryID = req.RoleIDs[0]
		}
		// 获取角色信息（用于后续写入 Casbin g 规则）
		roles, err := s.client.Role.Query().
			Where(role.IDIn(req.RoleIDs...), role.StatusEQ(role.StatusActive)).
			All(ctx)
		if err != nil {
			return nil, fmt.Errorf("查询角色失败: %w", err)
		}
		if len(roles) == 0 {
			return nil, fmt.Errorf("没有有效的可分配角色")
		}

		// 事务：创建 admin_role 关联
		tx, err := s.client.Tx(ctx)
		if err != nil {
			return nil, fmt.Errorf("开启事务失败: %w", err)
		}
		for _, r := range roles {
			isPrimary := r.ID == primaryID
			_, err = tx.AdminRole.Create().
				SetAdminID(a.ID).
				SetRoleID(r.ID).
				SetIsPrimary(isPrimary).
				Save(ctx)
			if err != nil {
				_ = tx.Rollback()
				return nil, fmt.Errorf("分配角色失败: %w", err)
			}
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("提交事务失败: %w", err)
		}

		// 同步写入 Casbin g 规则（uid -> role_name in domain）
		if global.EnforcerManager != nil {
			enforcer, enfErr := global.EnforcerManager.GetEnforcer("default")
			if enfErr == nil {
				for _, r := range roles {
					_, _ = enforcer.AddRoleForUserInDomain(a.UID, r.Name, "default")
				}
			}
		}
	}

	return &AdminResponse{
		ID:           a.UID,
		Email:        a.Email,
		Username:     a.Username,
		Nickname:     a.Nickname,
		Name:         a.Name,
		Phone:        a.Phone,
		Avatar:       a.Avatar,
		Department:   a.Department,
		DepartmentID: a.DepartmentID,
		Position:     a.Position,
		Status:       string(a.Status),
	}, nil
}

// ListAdmins 管理员列表
func (s *SuperAdminService) ListAdmins(ctx context.Context, req *AdminListRequest) (*ListResponse[*AdminListItem], error) {
	if req.Page <= 0 {
		req.Page = 1
	}
	if req.PageSize <= 0 {
		req.PageSize = 20
	}

	query := s.client.Admin.Query()

	if req.Keyword != "" {
		query = query.Where(
			admin.Or(
				admin.EmailContains(req.Keyword),
				admin.UsernameContains(req.Keyword),
				admin.NicknameContains(req.Keyword),
			),
		)
	}

	if req.Status != "" {
		query = query.Where(admin.StatusEQ(admin.Status(req.Status)))
	}

	// 应用数据权限过滤
	if req.CurrentUID != "" {
		dataPermSvc := NewDataPermissionService(NewDepartmentService())
		scope, err := dataPermSvc.ResolveAdminScope(ctx, req.CurrentUID)
		if err != nil {
			return nil, fmt.Errorf("解析数据权限失败: %w", err)
		}
		switch scope.Scope {
		case DataScopeAll:
			// 不做过滤
		case DataScopeSelf:
			query = query.Where(admin.UIDEQ(scope.AdminUID))
		case DataScopeDepartment, DataScopeDepartmentAndChild, DataScopeCustom:
			if len(scope.DepartmentIDs) == 0 {
				query = query.Where(admin.IDEQ(-1)) // 无可见部门，返回空结果
			} else {
				query = query.Where(admin.DepartmentIDIn(scope.DepartmentIDs...))
			}
		}
	}

	total, err := query.Count(ctx)
	if err != nil {
		return nil, err
	}

	offset := (req.Page - 1) * req.PageSize
	admins, err := query.
		Offset(offset).
		Limit(req.PageSize).
		All(ctx)
	if err != nil {
		return nil, err
	}

	items := make([]*AdminListItem, len(admins))
	for i, a := range admins {
		items[i] = &AdminListItem{
			ID:           a.ID,
			UID:          a.UID,
			Email:        a.Email,
			Username:     a.Username,
			Nickname:     a.Nickname,
			Status:       string(a.Status),
			DepartmentID: a.DepartmentID,
		}
	}

	return &ListResponse[*AdminListItem]{
		Data:  items,
		Total: total,
		Page:  req.Page,
	}, nil
}

// ActivateAdmin 激活管理员
func (s *SuperAdminService) ActivateAdmin(ctx context.Context, adminID int) error {
	_, err := s.client.Admin.UpdateOneID(adminID).
		SetStatus(admin.StatusActive).
		Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrAdminNotFound
		}
		return err
	}
	return nil
}

// DeactivateAdmin 停用管理员
func (s *SuperAdminService) DeactivateAdmin(ctx context.Context, adminID int) error {
	_, err := s.client.Admin.UpdateOneID(adminID).
		SetStatus(admin.StatusInactive).
		Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrAdminNotFound
		}
		return err
	}
	return nil
}

// GetAdminByID 获取管理员详情
func (s *SuperAdminService) GetAdminByID(ctx context.Context, adminID int) (*AdminResponse, error) {
	a, err := s.client.Admin.Query().
		Where(admin.IDEQ(adminID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrAdminNotFound
		}
		return nil, err
	}

	return &AdminResponse{
		ID:           a.UID,
		Email:        a.Email,
		Username:     a.Username,
		Nickname:     a.Nickname,
		Name:         a.Name,
		Phone:        a.Phone,
		Avatar:       a.Avatar,
		Department:   a.Department,
		DepartmentID: a.DepartmentID,
		Position:     a.Position,
		Status:       string(a.Status),
	}, nil
}

// UpdateAdmin 更新管理员信息
func (s *SuperAdminService) UpdateAdmin(ctx context.Context, adminID int, req *UpdateAdminRequest) (*AdminResponse, error) {
	a, err := s.client.Admin.Query().
		Where(admin.IDEQ(adminID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrAdminNotFound
		}
		return nil, err
	}

	// 检查邮箱是否已被其他管理员使用
	if req.Email != "" && req.Email != a.Email {
		emailExists, err := s.client.Admin.Query().
			Where(admin.EmailEQ(req.Email), admin.IDNEQ(adminID)).
			Exist(ctx)
		if err != nil {
			return nil, err
		}
		if emailExists {
			return nil, ErrAdminEmailExists
		}
	}

	update := s.client.Admin.UpdateOne(a)

	if req.Nickname != "" {
		update.SetNickname(req.Nickname)
	}
	if req.Name != "" {
		update.SetName(req.Name)
	}
	if req.Phone != "" {
		update.SetPhone(req.Phone)
	}
	if req.Email != "" {
		update.SetEmail(req.Email)
	}
	if req.Password != "" {
		hashedPassword, salt, err := crypto.HashPassword(req.Password)
		if err != nil {
			return nil, err
		}
		update.SetPassword(hashedPassword)
		update.SetPasswordSalt(salt)
	}
	if req.DepartmentID != nil {
		update.SetDepartmentID(*req.DepartmentID)
	}

	updated, err := update.Save(ctx)
	if err != nil {
		return nil, err
	}

	return &AdminResponse{
		ID:           updated.UID,
		Email:        updated.Email,
		Username:     updated.Username,
		Nickname:     updated.Nickname,
		Name:         updated.Name,
		Phone:        updated.Phone,
		Avatar:       updated.Avatar,
		Department:   updated.Department,
		DepartmentID: updated.DepartmentID,
		Position:     updated.Position,
		Status:       string(updated.Status),
	}, nil
}
