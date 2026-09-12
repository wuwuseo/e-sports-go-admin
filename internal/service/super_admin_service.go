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
	ErrAdminEmailExists        = errors.New("admin email already exists")
	ErrAdminUsernameExists     = errors.New("admin username already exists")
	ErrCannotResetSelfPassword = errors.New("不能通过初始化密码重置自己的密码，请使用修改密码功能")
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
	CreateAdmin(ctx context.Context, op *EffectiveDataScope, req *CreateAdminRequest) (*AdminResponse, error)
	ListAdmins(ctx context.Context, req *AdminListRequest) (*ListResponse[*AdminListItem], error)
	GetAdminByID(ctx context.Context, op *EffectiveDataScope, adminID int) (*AdminResponse, error)
	UpdateAdmin(ctx context.Context, op *EffectiveDataScope, adminID int, req *UpdateAdminRequest) (*AdminResponse, error)
	ActivateAdmin(ctx context.Context, op *EffectiveDataScope, adminID int) error
	DeactivateAdmin(ctx context.Context, op *EffectiveDataScope, adminID int) error
	ResetAdminPassword(ctx context.Context, op *EffectiveDataScope, adminID int) (*AdminResponse, string, error)
}

// SuperAdminService 超级管理员服务
type SuperAdminService struct {
	client      *ent.Client
	dataPermSvc *DataPermissionService
	deptSvc     DepartmentServiceInterface
}

// NewSuperAdminService 创建超级管理员服务实例
//
//	dataPermSvc 与 deptSvc 由路由层注入，避免在请求路径上现场 new 导致依赖不一致（如测试、多连接场景误用 DB）
func NewSuperAdminService(dataPermSvc *DataPermissionService, deptSvc DepartmentServiceInterface) *SuperAdminService {
	return &SuperAdminService{
		client:      global.ORM,
		dataPermSvc: dataPermSvc,
		deptSvc:     deptSvc,
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
func (s *SuperAdminService) CreateAdmin(ctx context.Context, op *EffectiveDataScope, req *CreateAdminRequest) (*AdminResponse, error) {
	// P1-A：目标部门必须在 op 的数据范围内
	if err := ensureDepartmentInScope(op, req.DepartmentID); err != nil {
		return nil, err
	}
	// P2 防御：目标部门必须真实存在（防止伪造 department_id 绕过外键校验时机）
	if err := ensureDepartmentExists(ctx, s.client, req.DepartmentID); err != nil {
		return nil, err
	}

	if err := rejectSuperAdminRoleIDs(ctx, s.client, requestedRoleIDs(req.RoleIDs, req.PrimaryRoleID)); err != nil {
		return nil, err
	}
	// P1-B 防御：按目标管理员部门展开角色有效集，要求 ⊆ op.DepartmentIDs
	if err := ensureRolesNotEscalatingForTarget(ctx, s.client, s.deptSvc, op, requestedRoleIDs(req.RoleIDs, req.PrimaryRoleID), req.DepartmentID); err != nil {
		return nil, err
	}
	operatorUID := ""
	if op != nil {
		operatorUID = op.AdminUID
	}
	domain := permissionDomainForScope(op)
	if err := ensureRolePermissionsNotEscalating(ctx, s.client, op, operatorUID, domain, requestedRoleIDs(req.RoleIDs, req.PrimaryRoleID)); err != nil {
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

	// P2：预校验 RoleIDs 全部存在且 Active；PrimaryRoleID 必须在 RoleIDs 中
	var validRoles []*ent.Role
	primaryID := 0
	if len(req.RoleIDs) > 0 {
		count, err := s.client.Role.Query().
			Where(role.IDIn(req.RoleIDs...), role.StatusEQ(role.StatusActive)).
			Count(ctx)
		if err != nil {
			return nil, fmt.Errorf("查询角色失败: %w", err)
		}
		if count != len(req.RoleIDs) {
			return nil, ErrInvalidRoleIDs
		}
		validRoles, err = s.client.Role.Query().
			Where(role.IDIn(req.RoleIDs...)).
			All(ctx)
		if err != nil {
			return nil, err
		}
		primaryID = req.PrimaryRoleID
		if primaryID == 0 {
			primaryID = req.RoleIDs[0]
		} else if !containsInt(req.RoleIDs, primaryID) {
			return nil, ErrPrimaryRoleInvalid
		}
	}

	// P2：admin Create + admin_role Insert 同事务
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("开启事务失败: %w", err)
	}

	a, err := tx.Admin.Create().
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
		_ = tx.Rollback()
		return nil, err
	}

	for _, r := range validRoles {
		_, err = tx.AdminRole.Create().
			SetAdminID(a.ID).
			SetRoleID(r.ID).
			SetIsPrimary(r.ID == primaryID).
			Save(ctx)
		if err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("分配角色失败: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("提交事务失败: %w", err)
	}

	// Casbin 同步（事务外，与 AssignAdminRoles 行为一致）
	if len(validRoles) > 0 && global.EnforcerManager != nil {
		enforcer, enfErr := global.EnforcerManager.GetEnforcer(domain)
		if enfErr == nil {
			roleNames := make([]string, 0, len(validRoles))
			for _, r := range validRoles {
				roleNames = append(roleNames, r.Name)
			}
			_ = addCasbinUserRoles(enforcer, a.UID, domain, roleNames)
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
	if req == nil || req.CurrentUID == "" {
		return nil, ErrPermissionOperatorRequired
	}
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

	// 使用注入的 dataPermSvc，避免现场 new 导致 client 不一致（DI 闭环）
	scope, err := s.dataPermSvc.ResolveAdminScope(ctx, req.CurrentUID)
	if err != nil {
		return nil, fmt.Errorf("解析数据权限失败: %w", err)
	}
	switch scope.Scope {
	case DataScopeAll:
		// 不做过滤
	case DataScopeSelf:
		query = query.Where(admin.UIDEQ(scope.AdminUID))
	case DataScopeDepartment, DataScopeDepartmentAndChild, DataScopeCustom:
		// 与详情侧 adminInOperatorScope 保持一致：列表也必须包含"自己"，
		// 避免自己 DepartmentID=0 或不在可见部门范围内时从列表中消失（详情却能看到）
		if len(scope.DepartmentIDs) == 0 {
			query = query.Where(admin.UIDEQ(scope.AdminUID)) // 无可见部门时仅能看到自己
		} else {
			query = query.Where(admin.Or(
				admin.DepartmentIDIn(scope.DepartmentIDs...),
				admin.UIDEQ(scope.AdminUID),
			))
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
func (s *SuperAdminService) ActivateAdmin(ctx context.Context, op *EffectiveDataScope, adminID int) error {
	// 先做范围检查，再做自我检查，避免越权者通过错误码探测目标管理员是否存在
	if err := ensureAdminInScope(ctx, s.client, op, adminID); err != nil {
		return err
	}
	if op != nil && op.AdminID == adminID {
		return ErrCannotToggleSelfStatus
	}
	target, err := s.client.Admin.Query().Where(admin.IDEQ(adminID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrAdminNotFound
		}
		return err
	}
	if err := ensureAdminRolesManageable(ctx, s.client, s.deptSvc, op, target.ID, target.DepartmentID); err != nil {
		return err
	}
	_, err = s.client.Admin.UpdateOneID(adminID).
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
func (s *SuperAdminService) DeactivateAdmin(ctx context.Context, op *EffectiveDataScope, adminID int) error {
	// 先做范围检查，再做自我检查，避免越权者通过错误码探测目标管理员是否存在
	if err := ensureAdminInScope(ctx, s.client, op, adminID); err != nil {
		return err
	}
	if op != nil && op.AdminID == adminID {
		return ErrCannotToggleSelfStatus
	}
	target, err := s.client.Admin.Query().Where(admin.IDEQ(adminID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrAdminNotFound
		}
		return err
	}
	if err := ensureAdminRolesManageable(ctx, s.client, s.deptSvc, op, target.ID, target.DepartmentID); err != nil {
		return err
	}
	_, err = s.client.Admin.UpdateOneID(adminID).
		SetStatus(admin.StatusInactive).
		Save(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrAdminNotFound
		}
		return err
	}
	// DB 状态已经生效；即使后续 Casbin 同步失败，也不能继续返回旧的数据范围。
	s.dataPermSvc.InvalidateAdminScope(ctx, target.UID)
	if global.EnforcerManager != nil {
		domain := permissionDomainForScope(op)
		enforcer, enforcerErr := global.EnforcerManager.GetEnforcer(domain)
		if enforcerErr != nil {
			return fmt.Errorf("管理员已停用但获取 Casbin 执行器失败: %w", enforcerErr)
		}
		if enforcerErr = removeCasbinUserRoles(enforcer, target.UID, domain); enforcerErr != nil {
			return fmt.Errorf("管理员已停用但撤销 Casbin 角色失败: %w", enforcerErr)
		}
	}
	return nil
}

// ResetAdminPassword 初始化管理员密码：生成随机强密码（一次性返回给操作者），
// 旧密码即行失效。自我重置被禁止（改密需验证旧密码，走 /password 接口）；
// 令牌吊销由 HTTP 层在成功后调用 AdminService.Logout 完成。
func (s *SuperAdminService) ResetAdminPassword(ctx context.Context, op *EffectiveDataScope, adminID int) (*AdminResponse, string, error) {
	if err := ensureAdminInScope(ctx, s.client, op, adminID); err != nil {
		return nil, "", err
	}
	if op != nil && op.AdminID == adminID {
		return nil, "", ErrCannotResetSelfPassword
	}
	a, err := s.client.Admin.Query().
		Where(admin.IDEQ(adminID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, "", ErrAdminNotFound
		}
		return nil, "", err
	}

	newPwd, err := utils.GenerateStrongPassword(resetPasswordLength)
	if err != nil {
		return nil, "", err
	}
	hashedPassword, salt, err := crypto.HashPassword(newPwd)
	if err != nil {
		return nil, "", err
	}
	updated, err := s.client.Admin.UpdateOne(a).
		SetPassword(hashedPassword).
		SetPasswordSalt(salt).
		Save(ctx)
	if err != nil {
		return nil, "", err
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
	}, newPwd, nil
}

// GetAdminByID 获取管理员详情
func (s *SuperAdminService) GetAdminByID(ctx context.Context, op *EffectiveDataScope, adminID int) (*AdminResponse, error) {
	if err := ensureAdminInScope(ctx, s.client, op, adminID); err != nil {
		return nil, err
	}
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
func (s *SuperAdminService) UpdateAdmin(ctx context.Context, op *EffectiveDataScope, adminID int, req *UpdateAdminRequest) (*AdminResponse, error) {
	if err := ensureAdminInScope(ctx, s.client, op, adminID); err != nil {
		return nil, err
	}
	// P1-B：若 req 要改 department_id，新部门也必须在 op 范围内
	if req.DepartmentID != nil {
		if err := ensureDepartmentInScope(op, *req.DepartmentID); err != nil {
			return nil, err
		}
		// P2 防御：新部门必须真实存在
		if err := ensureDepartmentExists(ctx, s.client, *req.DepartmentID); err != nil {
			return nil, err
		}
	}
	a, err := s.client.Admin.Query().
		Where(admin.IDEQ(adminID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrAdminNotFound
		}
		return nil, err
	}
	targetDepartmentID := a.DepartmentID
	if req.DepartmentID != nil {
		targetDepartmentID = *req.DepartmentID
	}
	if err := ensureAdminRolesManageable(ctx, s.client, s.deptSvc, op, a.ID, targetDepartmentID); err != nil {
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

	// 部门变更会改变该管理员的数据范围（DeptAndChild 展开等），同步失效其数据范围缓存
	if req.DepartmentID != nil && *req.DepartmentID != a.DepartmentID {
		s.dataPermSvc.InvalidateAdminScope(ctx, a.UID)
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
