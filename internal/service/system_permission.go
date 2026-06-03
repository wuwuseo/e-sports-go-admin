package service

import (
	"context"
	"fmt"

	"go-admin/internal/ent"
	"go-admin/internal/ent/systempermission"
	"go-admin/internal/global"
)

// SystemPermissionType 权限类型
type SystemPermissionType string

const (
	SystemPermissionTypeAPI    SystemPermissionType = "api"
	SystemPermissionTypeMenu   SystemPermissionType = "menu"
	SystemPermissionTypeButton SystemPermissionType = "button"
)

// CreateSystemPermissionRequest 创建权限请求
type CreateSystemPermissionRequest struct {
	Domain      string               `json:"domain" validate:"required,max=100"`
	Type        SystemPermissionType `json:"type" validate:"required,oneof=api menu button"`
	Name        string               `json:"name" validate:"required,max=100"`
	Code        string               `json:"code" validate:"required,max=200"`
	Path        string               `json:"path" validate:"max=500"`
	Method      string               `json:"method" validate:"max=20"`
	MenuPath    string               `json:"menu_path" validate:"max=500"`
	Icon        string               `json:"icon" validate:"max=100"`
	Component   string               `json:"component" validate:"max=200"`
	ParentID    int                  `json:"parent_id"`
	SortOrder   int                  `json:"sort_order"`
	IsCommon    bool                 `json:"is_common"`
	Status      int8                 `json:"status"`
	Description string               `json:"description" validate:"max=500"`
}

// UpdateSystemPermissionRequest 更新权限请求
type UpdateSystemPermissionRequest struct {
	Type        SystemPermissionType `json:"type" validate:"oneof=api menu button"`
	Name        string               `json:"name" validate:"max=100"`
	Path        string               `json:"path" validate:"max=500"`
	Method      string               `json:"method" validate:"max=20"`
	MenuPath    string               `json:"menu_path" validate:"max=500"`
	Icon        string               `json:"icon" validate:"max=100"`
	Component   string               `json:"component" validate:"max=200"`
	ParentID    int                  `json:"parent_id"`
	SortOrder   int                  `json:"sort_order"`
	IsCommon    bool                 `json:"is_common"`
	Status      int8                 `json:"status"`
	Description string               `json:"description" validate:"max=500"`
}

// SystemPermissionResponse 权限响应
type SystemPermissionResponse struct {
	ID          int                  `json:"id"`
	Domain      string               `json:"domain"`
	Type        SystemPermissionType `json:"type"`
	Name        string               `json:"name"`
	Code        string               `json:"code"`
	Path        string               `json:"path"`
	Method      string               `json:"method"`
	MenuPath    string               `json:"menu_path"`
	Icon        string               `json:"icon"`
	Component   string               `json:"component"`
	ParentID    int                  `json:"parent_id"`
	SortOrder   int                  `json:"sort_order"`
	IsCommon    bool                 `json:"is_common"`
	Status      int8                 `json:"status"`
	Description string               `json:"description"`
	CreatedAt   string               `json:"created_at"`
	UpdatedAt   string               `json:"updated_at"`
}

// SystemPermissionTreeNode 权限树节点
type SystemPermissionTreeNode struct {
	ID          int                         `json:"id"`
	Type        SystemPermissionType        `json:"type"`
	Name        string                      `json:"name"`
	Code        string                      `json:"code"`
	Path        string                      `json:"path"`
	Method      string                      `json:"method"`
	MenuPath    string                      `json:"menu_path"`
	Icon        string                      `json:"icon"`
	Component   string                      `json:"component"`
	ParentID    int                         `json:"parent_id"`
	SortOrder   int                         `json:"sort_order"`
	IsCommon    bool                        `json:"is_common"`
	Status      int8                        `json:"status"`
	Description string                      `json:"description"`
	Children    []*SystemPermissionTreeNode `json:"children,omitempty"`
}

// ListSystemPermissionsRequest 权限列表请求
type ListSystemPermissionsRequest struct {
	ListRequest
	Domain string               `query:"domain" json:"domain"`
	Type   SystemPermissionType `query:"type" json:"type"`
	Status int8                 `query:"status" json:"status"`
}

// ListSystemPermissionsResponse 权限列表响应
type ListSystemPermissionsResponse = ListResponse[*SystemPermissionResponse]

// SeedPermission 种子权限结构
type SeedPermission struct {
	ID          int
	Domain      string
	Type        string
	Name        string
	Code        string
	Path        string
	Method      string
	MenuPath    string
	Icon        string
	Component   string
	ParentID    int
	SortOrder   int
	IsCommon    bool
	Status      int8
	Description string
}

// SystemPermissionServiceInterface 权限定义服务接口
type SystemPermissionServiceInterface interface {
	Create(ctx context.Context, req *CreateSystemPermissionRequest) (*SystemPermissionResponse, error)
	Update(ctx context.Context, id int, req *UpdateSystemPermissionRequest) error
	Delete(ctx context.Context, id int) error
	GetByID(ctx context.Context, id int) (*SystemPermissionResponse, error)
	List(ctx context.Context, req *ListSystemPermissionsRequest) (*ListSystemPermissionsResponse, error)
	GetTree(ctx context.Context, domain string) ([]*SystemPermissionTreeNode, error)
	InitSeedPermissions(ctx context.Context, seeds []SeedPermission) error
}

type SystemPermissionService struct {
	client *ent.Client
}

var (
	_ Creator[CreateSystemPermissionRequest, SystemPermissionResponse] = (*SystemPermissionService)(nil)
	_ Updater[UpdateSystemPermissionRequest]                           = (*SystemPermissionService)(nil)
	_ Deleter                                                          = (*SystemPermissionService)(nil)
	_ Lister[ListSystemPermissionsRequest, SystemPermissionResponse]   = (*SystemPermissionService)(nil)
)

func NewSystemPermissionService() *SystemPermissionService {
	return &SystemPermissionService{
		client: global.ORM,
	}
}

// Create 创建权限
func (s *SystemPermissionService) Create(ctx context.Context, req *CreateSystemPermissionRequest) (*SystemPermissionResponse, error) {
	exists, err := s.client.SystemPermission.Query().
		Where(
			systempermission.DomainEQ(req.Domain),
			systempermission.CodeEQ(req.Code),
		).Exist(ctx)
	if err != nil {
		return nil, fmt.Errorf("检查权限代码失败: %w", err)
	}
	if exists {
		return nil, fmt.Errorf("权限代码已存在: %s", req.Code)
	}

	builder := s.client.SystemPermission.Create().
		SetDomain(req.Domain).
		SetType(string(req.Type)).
		SetName(req.Name).
		SetCode(req.Code).
		SetParentID(req.ParentID).
		SetSortOrder(req.SortOrder).
		SetIsCommon(req.IsCommon).
		SetStatus(req.Status)

	if req.Path != "" {
		builder.SetPath(req.Path)
	}
	if req.Method != "" {
		builder.SetMethod(req.Method)
	}
	if req.MenuPath != "" {
		builder.SetMenuPath(req.MenuPath)
	}
	if req.Icon != "" {
		builder.SetIcon(req.Icon)
	}
	if req.Component != "" {
		builder.SetComponent(req.Component)
	}
	if req.Description != "" {
		builder.SetDescription(req.Description)
	}

	perm, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("创建权限失败: %w", err)
	}

	return s.toResponse(perm), nil
}

// Update 更新权限
func (s *SystemPermissionService) Update(ctx context.Context, id int, req *UpdateSystemPermissionRequest) error {
	_, err := s.client.SystemPermission.Query().Where(systempermission.IDEQ(id)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return fmt.Errorf("权限不存在")
		}
		return fmt.Errorf("查询权限失败: %w", err)
	}

	update := s.client.SystemPermission.UpdateOneID(id)

	if req.Type != "" {
		update.SetType(string(req.Type))
	}
	if req.Name != "" {
		update.SetName(req.Name)
	}
	if req.Path != "" {
		update.SetPath(req.Path)
	}
	if req.Method != "" {
		update.SetMethod(req.Method)
	}
	if req.MenuPath != "" {
		update.SetMenuPath(req.MenuPath)
	}
	if req.Icon != "" {
		update.SetIcon(req.Icon)
	}
	if req.Component != "" {
		update.SetComponent(req.Component)
	}
	update.SetParentID(req.ParentID)
	update.SetSortOrder(req.SortOrder)
	update.SetIsCommon(req.IsCommon)
	update.SetStatus(req.Status)
	if req.Description != "" {
		update.SetDescription(req.Description)
	}

	_, err = update.Save(ctx)
	if err != nil {
		return fmt.Errorf("更新权限失败: %w", err)
	}

	return nil
}

// Delete 删除权限
func (s *SystemPermissionService) Delete(ctx context.Context, id int) error {
	_, err := s.client.SystemPermission.Query().Where(systempermission.IDEQ(id)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return fmt.Errorf("权限不存在")
		}
		return fmt.Errorf("查询权限失败: %w", err)
	}

	hasChildren, err := s.client.SystemPermission.Query().
		Where(systempermission.ParentIDEQ(id)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("检查子权限失败: %w", err)
	}
	if hasChildren {
		return fmt.Errorf("请先删除子权限")
	}

	err = s.client.SystemPermission.DeleteOneID(id).Exec(ctx)
	if err != nil {
		return fmt.Errorf("删除权限失败: %w", err)
	}

	return nil
}

// GetByID 获取权限详情
func (s *SystemPermissionService) GetByID(ctx context.Context, id int) (*SystemPermissionResponse, error) {
	perm, err := s.client.SystemPermission.Query().Where(systempermission.IDEQ(id)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("权限不存在")
		}
		return nil, fmt.Errorf("查询权限失败: %w", err)
	}

	return s.toResponse(perm), nil
}

// List 获取权限列表
func (s *SystemPermissionService) List(ctx context.Context, req *ListSystemPermissionsRequest) (*ListSystemPermissionsResponse, error) {
	if req.Page < 1 {
		req.Page = 1
	}

	query := s.client.SystemPermission.Query()

	if req.Domain != "" {
		query = query.Where(systempermission.Or(
			systempermission.DomainEQ(req.Domain),
			systempermission.IsCommonEQ(true),
		))
	}
	if req.Type != "" {
		query = query.Where(systempermission.TypeEQ(string(req.Type)))
	}
	if req.Keyword != "" {
		query = query.Where(
			systempermission.Or(
				systempermission.NameContains(req.Keyword),
				systempermission.CodeContains(req.Keyword),
			),
		)
	}
	if req.Status != 0 {
		query = query.Where(systempermission.StatusEQ(req.Status))
	}

	total, err := query.Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取权限总数失败: %w", err)
	}

	var perms []*ent.SystemPermission

	if req.PageSize == 0 {
		perms, err = query.
			Order(ent.Asc(systempermission.FieldSortOrder)).
			All(ctx)
	} else {
		if req.PageSize > 100 {
			req.PageSize = 100
		}
		offset := (req.Page - 1) * req.PageSize
		perms, err = query.
			Offset(offset).
			Limit(req.PageSize).
			Order(ent.Asc(systempermission.FieldSortOrder)).
			All(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("获取权限列表失败: %w", err)
	}

	data := make([]*SystemPermissionResponse, 0, len(perms))
	for _, p := range perms {
		data = append(data, s.toResponse(p))
	}

	return &ListSystemPermissionsResponse{
		Total: total,
		Page:  req.Page,
		Data:  data,
	}, nil
}

// GetTree 获取权限树
func (s *SystemPermissionService) GetTree(ctx context.Context, domain string) ([]*SystemPermissionTreeNode, error) {
	query := s.client.SystemPermission.Query()

	if domain != "" {
		query = query.Where(systempermission.Or(
			systempermission.DomainEQ(domain),
			systempermission.IsCommonEQ(true),
		))
	}

	perms, err := query.
		Order(ent.Asc(systempermission.FieldSortOrder)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取权限列表失败: %w", err)
	}

	nodeMap := make(map[int]*SystemPermissionTreeNode)
	rootNodes := make([]*SystemPermissionTreeNode, 0)

	for _, p := range perms {
		node := &SystemPermissionTreeNode{
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
		nodeMap[p.ID] = node
	}

	for _, node := range nodeMap {
		if node.ParentID == 0 {
			rootNodes = append(rootNodes, node)
		} else {
			if parent, ok := nodeMap[node.ParentID]; ok {
				parent.Children = append(parent.Children, node)
			} else {
				rootNodes = append(rootNodes, node)
			}
		}
	}

	return rootNodes, nil
}

// InitSeedPermissions 初始化种子权限（菜单、API、按钮）
func (s *SystemPermissionService) InitSeedPermissions(ctx context.Context, seeds []SeedPermission) error {
	seedByID := make(map[int]SeedPermission, len(seeds))
	actualIDBySeedID := make(map[int]int, len(seeds))
	for _, seed := range seeds {
		if seed.ID > 0 {
			seedByID[seed.ID] = seed
		}
	}

	for _, seed := range seeds {
		parentID, err := s.resolveSeedParentID(ctx, seed, seedByID, actualIDBySeedID)
		if err != nil {
			return err
		}

		existing, err := s.client.SystemPermission.Query().
			Where(
				systempermission.DomainEQ(seed.Domain),
				systempermission.CodeEQ(seed.Code),
			).Only(ctx)
		if err != nil {
			if !ent.IsNotFound(err) {
				return fmt.Errorf("检查权限失败: %w", err)
			}
		}

		if existing != nil {
			_, err = existing.Update().
				SetType(seed.Type).
				SetName(seed.Name).
				SetPath(seed.Path).
				SetMethod(seed.Method).
				SetMenuPath(seed.MenuPath).
				SetIcon(seed.Icon).
				SetComponent(seed.Component).
				SetParentID(parentID).
				SetSortOrder(seed.SortOrder).
				SetIsCommon(seed.IsCommon).
				SetStatus(seed.Status).
				SetDescription(seed.Description).
				Save(ctx)
			if err != nil {
				return fmt.Errorf("更新种子权限失败: %w", err)
			}
			if seed.ID > 0 {
				actualIDBySeedID[seed.ID] = existing.ID
			}
			continue
		}

		create := s.client.SystemPermission.Create().
			SetDomain(seed.Domain).
			SetType(seed.Type).
			SetName(seed.Name).
			SetCode(seed.Code).
			SetPath(seed.Path).
			SetMethod(seed.Method).
			SetMenuPath(seed.MenuPath).
			SetIcon(seed.Icon).
			SetComponent(seed.Component).
			SetSortOrder(seed.SortOrder).
			SetIsCommon(seed.IsCommon).
			SetStatus(seed.Status).
			SetDescription(seed.Description)

		create.SetParentID(parentID)

		created, err := create.Save(ctx)
		if err != nil {
			return fmt.Errorf("创建权限失败: %w", err)
		}
		if seed.ID > 0 {
			actualIDBySeedID[seed.ID] = created.ID
		}
	}

	return nil
}

func (s *SystemPermissionService) resolveSeedParentID(ctx context.Context, seed SeedPermission, seedByID map[int]SeedPermission, actualIDBySeedID map[int]int) (int, error) {
	if seed.ParentID <= 0 {
		return 0, nil
	}

	if actualID, ok := actualIDBySeedID[seed.ParentID]; ok {
		return actualID, nil
	}

	parentSeed, ok := seedByID[seed.ParentID]
	if !ok {
		return seed.ParentID, nil
	}

	parent, err := s.client.SystemPermission.Query().
		Where(
			systempermission.DomainEQ(parentSeed.Domain),
			systempermission.CodeEQ(parentSeed.Code),
		).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return seed.ParentID, nil
		}
		return 0, fmt.Errorf("查询父级种子权限失败: %w", err)
	}

	actualIDBySeedID[seed.ParentID] = parent.ID
	return parent.ID, nil
}

// InitSystemPermissions 系统启动时初始化所有权限（包含菜单、按钮、API）
func InitSystemPermissions(ctx context.Context) error {
	svc := NewSystemPermissionService()

	seedData := []SeedPermission{
		{ID: 1, Domain: "default", Type: "menu", Name: "仪表盘", Code: "dashboard", MenuPath: "/dashboard/workplace", Component: "dashboard/workplace/index", Icon: "icon-dashboard", ParentID: 0, SortOrder: 0, IsCommon: true, Status: 1, Description: "系统仪表盘首页"},
		{ID: 2, Domain: "default", Type: "menu", Name: "工作台", Code: "workplace", MenuPath: "/dashboard/workplace", Component: "dashboard/workplace/index", ParentID: 1, SortOrder: 0, IsCommon: true, Status: 1, Description: "工作台页面"},
		{ID: 3, Domain: "default", Type: "menu", Name: "系统管理", Code: "system", MenuPath: "/system", Icon: "icon-settings", ParentID: 0, SortOrder: 2, IsCommon: true, Status: 1, Description: "系统管理模块"},
		{ID: 4, Domain: "default", Type: "menu", Name: "角色管理", Code: "system:role", MenuPath: "/system/role", Component: "system/role/index", ParentID: 3, SortOrder: 0, IsCommon: false, Status: 1, Description: "角色管理页面"},
		{ID: 5, Domain: "default", Type: "menu", Name: "权限定义", Code: "system:permission", MenuPath: "/system/permission", Component: "system/permission/index", ParentID: 3, SortOrder: 1, IsCommon: false, Status: 1, Description: "权限定义管理页面"},
		{ID: 6, Domain: "default", Type: "menu", Name: "附件管理", Code: "system:attachment", MenuPath: "/system/attachment", Component: "system/attachment/index", ParentID: 3, SortOrder: 2, IsCommon: false, Status: 1, Description: "附件管理页面"},
		{ID: 7, Domain: "default", Type: "menu", Name: "定时任务", Code: "system:cron-job", MenuPath: "/system/cron-job", Component: "system/cron-job/index", ParentID: 3, SortOrder: 3, IsCommon: false, Status: 1, Description: "定时任务管理页面"},
		{ID: 8, Domain: "default", Type: "menu", Name: "操作日志", Code: "system:operation-log", MenuPath: "/system/operation-log", Component: "system/operation-log/index", ParentID: 3, SortOrder: 4, IsCommon: false, Status: 1, Description: "操作日志页面"},
		{ID: 9, Domain: "default", Type: "menu", Name: "字典数据", Code: "system:dict-data", MenuPath: "/system/dict-data", Component: "system/dict-data/index", ParentID: 3, SortOrder: 5, IsCommon: false, Status: 1, Description: "字典数据管理页面"},
		{ID: 10, Domain: "default", Type: "menu", Name: "系统配置", Code: "system:config", MenuPath: "/system/config", Component: "system/config/index", ParentID: 3, SortOrder: 6, IsCommon: false, Status: 1, Description: "系统配置管理页面"},
		{ID: 11, Domain: "default", Type: "api", Name: "获取角色列表", Code: "role:list", Path: "/api/v1/admin/auth/roles", Method: "GET", ParentID: 4, SortOrder: 0, IsCommon: false, Status: 1, Description: "获取角色列表API"},
		{ID: 12, Domain: "default", Type: "api", Name: "获取角色详情", Code: "role:get", Path: "/api/v1/admin/auth/roles/{id}", Method: "GET", ParentID: 4, SortOrder: 1, IsCommon: false, Status: 1, Description: "获取角色详情API"},
		{ID: 13, Domain: "default", Type: "api", Name: "创建角色", Code: "role:create", Path: "/api/v1/admin/auth/roles", Method: "POST", ParentID: 4, SortOrder: 2, IsCommon: false, Status: 1, Description: "创建角色API"},
		{ID: 14, Domain: "default", Type: "api", Name: "更新角色", Code: "role:update", Path: "/api/v1/admin/auth/roles/{id}", Method: "PUT", ParentID: 4, SortOrder: 3, IsCommon: false, Status: 1, Description: "更新角色API"},
		{ID: 15, Domain: "default", Type: "api", Name: "删除角色", Code: "role:delete", Path: "/api/v1/admin/auth/roles/{id}", Method: "DELETE", ParentID: 4, SortOrder: 4, IsCommon: false, Status: 1, Description: "删除角色API"},
		{ID: 16, Domain: "default", Type: "api", Name: "获取权限列表", Code: "permission:list", Path: "/api/v1/admin/auth/system-permissions", Method: "GET", ParentID: 5, SortOrder: 0, IsCommon: false, Status: 1, Description: "获取权限列表API"},
		{ID: 17, Domain: "default", Type: "api", Name: "获取权限详情", Code: "permission:get", Path: "/api/v1/admin/auth/system-permissions/{id}", Method: "GET", ParentID: 5, SortOrder: 1, IsCommon: false, Status: 1, Description: "获取权限详情API"},
		{ID: 18, Domain: "default", Type: "api", Name: "创建权限", Code: "permission:create", Path: "/api/v1/admin/auth/system-permissions", Method: "POST", ParentID: 5, SortOrder: 2, IsCommon: false, Status: 1, Description: "创建权限API"},
		{ID: 19, Domain: "default", Type: "api", Name: "更新权限", Code: "permission:update", Path: "/api/v1/admin/auth/system-permissions/{id}", Method: "PUT", ParentID: 5, SortOrder: 3, IsCommon: false, Status: 1, Description: "更新权限API"},
		{ID: 20, Domain: "default", Type: "api", Name: "删除权限", Code: "permission:delete", Path: "/api/v1/admin/auth/system-permissions/{id}", Method: "DELETE", ParentID: 5, SortOrder: 4, IsCommon: false, Status: 1, Description: "删除权限API"},
		{ID: 21, Domain: "default", Type: "api", Name: "获取权限树", Code: "permission:tree", Path: "/api/v1/admin/auth/system-permissions/tree", Method: "GET", ParentID: 5, SortOrder: 5, IsCommon: false, Status: 1, Description: "获取权限树API"},
		{ID: 22, Domain: "default", Type: "api", Name: "获取附件列表", Code: "attachment:list", Path: "/api/v1/admin/auth/attachments", Method: "GET", ParentID: 6, SortOrder: 0, IsCommon: false, Status: 1, Description: "获取附件列表API"},
		{ID: 23, Domain: "default", Type: "api", Name: "上传附件", Code: "attachment:upload", Path: "/api/v1/admin/auth/attachments/upload", Method: "POST", ParentID: 6, SortOrder: 1, IsCommon: false, Status: 1, Description: "上传附件API"},
		{ID: 24, Domain: "default", Type: "api", Name: "删除附件", Code: "attachment:delete", Path: "/api/v1/admin/auth/attachments/{id}", Method: "DELETE", ParentID: 6, SortOrder: 2, IsCommon: false, Status: 1, Description: "删除附件API"},
		{ID: 25, Domain: "default", Type: "api", Name: "获取定时任务列表", Code: "cron-job:list", Path: "/api/v1/admin/auth/cron-jobs", Method: "GET", ParentID: 7, SortOrder: 0, IsCommon: false, Status: 1, Description: "获取定时任务列表API"},
		{ID: 26, Domain: "default", Type: "api", Name: "创建定时任务", Code: "cron-job:create", Path: "/api/v1/admin/auth/cron-jobs", Method: "POST", ParentID: 7, SortOrder: 1, IsCommon: false, Status: 1, Description: "创建定时任务API"},
		{ID: 27, Domain: "default", Type: "api", Name: "执行定时任务", Code: "cron-job:run", Path: "/api/v1/admin/auth/cron-jobs/{id}/run", Method: "POST", ParentID: 7, SortOrder: 2, IsCommon: false, Status: 1, Description: "执行定时任务API"},
		{ID: 28, Domain: "default", Type: "api", Name: "获取操作日志", Code: "operation-log:list", Path: "/api/v1/admin/auth/operation-logs", Method: "GET", ParentID: 8, SortOrder: 0, IsCommon: false, Status: 1, Description: "获取操作日志API"},
		{ID: 29, Domain: "default", Type: "api", Name: "获取字典数据", Code: "dict-data:list", Path: "/api/v1/admin/auth/dict-data", Method: "GET", ParentID: 9, SortOrder: 0, IsCommon: false, Status: 1, Description: "获取字典数据API"},
		{ID: 30, Domain: "default", Type: "api", Name: "获取系统配置", Code: "config:list", Path: "/api/v1/admin/auth/configs", Method: "GET", ParentID: 10, SortOrder: 0, IsCommon: false, Status: 1, Description: "获取系统配置API"},
		{ID: 31, Domain: "default", Type: "button", Name: "角色管理-新增按钮", Code: "role:add", ParentID: 4, SortOrder: 0, IsCommon: false, Status: 1, Description: "角色管理页面新增按钮"},
		{ID: 32, Domain: "default", Type: "button", Name: "角色管理-编辑按钮", Code: "role:edit", ParentID: 4, SortOrder: 1, IsCommon: false, Status: 1, Description: "角色管理页面编辑按钮"},
		{ID: 33, Domain: "default", Type: "button", Name: "角色管理-删除按钮", Code: "role:delete", ParentID: 4, SortOrder: 2, IsCommon: false, Status: 1, Description: "角色管理页面删除按钮"},
		{ID: 34, Domain: "default", Type: "button", Name: "角色管理-权限分配按钮", Code: "role:assign-permission", ParentID: 4, SortOrder: 3, IsCommon: false, Status: 1, Description: "角色管理页面权限分配按钮"},
		{ID: 35, Domain: "default", Type: "button", Name: "权限管理-新增按钮", Code: "permission:add", ParentID: 5, SortOrder: 0, IsCommon: false, Status: 1, Description: "权限管理页面新增按钮"},
		{ID: 36, Domain: "default", Type: "button", Name: "权限管理-编辑按钮", Code: "permission:edit", ParentID: 5, SortOrder: 1, IsCommon: false, Status: 1, Description: "权限管理页面编辑按钮"},
		{ID: 37, Domain: "default", Type: "button", Name: "权限管理-删除按钮", Code: "permission:delete", ParentID: 5, SortOrder: 2, IsCommon: false, Status: 1, Description: "权限管理页面删除按钮"},
		{ID: 38, Domain: "default", Type: "button", Name: "附件管理-上传按钮", Code: "attachment:upload-btn", ParentID: 6, SortOrder: 0, IsCommon: false, Status: 1, Description: "附件管理页面上传按钮"},
		{ID: 39, Domain: "default", Type: "button", Name: "附件管理-删除按钮", Code: "attachment:delete-btn", ParentID: 6, SortOrder: 1, IsCommon: false, Status: 1, Description: "附件管理页面删除按钮"},
		{ID: 40, Domain: "default", Type: "button", Name: "定时任务-新增按钮", Code: "cron-job:add", ParentID: 7, SortOrder: 0, IsCommon: false, Status: 1, Description: "定时任务页面新增按钮"},
		{ID: 41, Domain: "default", Type: "button", Name: "定时任务-编辑按钮", Code: "cron-job:edit", ParentID: 7, SortOrder: 1, IsCommon: false, Status: 1, Description: "定时任务页面编辑按钮"},
		{ID: 42, Domain: "default", Type: "button", Name: "定时任务-删除按钮", Code: "cron-job:delete", ParentID: 7, SortOrder: 2, IsCommon: false, Status: 1, Description: "定时任务页面删除按钮"},
		{ID: 43, Domain: "default", Type: "button", Name: "定时任务-执行按钮", Code: "cron-job:execute", ParentID: 7, SortOrder: 3, IsCommon: false, Status: 1, Description: "定时任务页面执行按钮"},
		{ID: 44, Domain: "default", Type: "button", Name: "字典管理-新增按钮", Code: "dict-data:add", ParentID: 9, SortOrder: 0, IsCommon: false, Status: 1, Description: "字典管理页面新增按钮"},
		{ID: 45, Domain: "default", Type: "button", Name: "字典管理-编辑按钮", Code: "dict-data:edit", ParentID: 9, SortOrder: 1, IsCommon: false, Status: 1, Description: "字典管理页面编辑按钮"},
		{ID: 46, Domain: "default", Type: "button", Name: "字典管理-删除按钮", Code: "dict-data:delete", ParentID: 9, SortOrder: 2, IsCommon: false, Status: 1, Description: "字典管理页面删除按钮"},
		{ID: 47, Domain: "default", Type: "button", Name: "配置管理-编辑按钮", Code: "config:edit", ParentID: 10, SortOrder: 0, IsCommon: false, Status: 1, Description: "配置管理页面编辑按钮"},
		{ID: 48, Domain: "default", Type: "menu", Name: "插件管理", Code: "plugin", MenuPath: "/system/plugin", Component: "system/plugin/index", ParentID: 3, SortOrder: 7, IsCommon: false, Status: 1, Description: "插件管理页面"},
		{ID: 49, Domain: "default", Type: "api", Name: "获取插件列表", Code: "plugin:list", Path: "/api/v1/admin/auth/plugins", Method: "GET", ParentID: 48, SortOrder: 0, IsCommon: false, Status: 1, Description: "获取插件列表API"},
		{ID: 50, Domain: "default", Type: "api", Name: "获取插件详情", Code: "plugin:get", Path: "/api/v1/admin/auth/plugins/{plugin_id}", Method: "GET", ParentID: 48, SortOrder: 1, IsCommon: false, Status: 1, Description: "获取插件详情API"},
		{ID: 51, Domain: "default", Type: "api", Name: "安装插件", Code: "plugin:install", Path: "/api/v1/admin/auth/plugins/install", Method: "POST", ParentID: 48, SortOrder: 2, IsCommon: false, Status: 1, Description: "安装插件API"},
		{ID: 52, Domain: "default", Type: "api", Name: "启用插件", Code: "plugin:enable", Path: "/api/v1/admin/auth/plugins/{plugin_id}/enable", Method: "POST", ParentID: 48, SortOrder: 3, IsCommon: false, Status: 1, Description: "启用插件API"},
		{ID: 53, Domain: "default", Type: "api", Name: "禁用插件", Code: "plugin:disable", Path: "/api/v1/admin/auth/plugins/{plugin_id}/disable", Method: "POST", ParentID: 48, SortOrder: 4, IsCommon: false, Status: 1, Description: "禁用插件API"},
		{ID: 54, Domain: "default", Type: "api", Name: "卸载插件", Code: "plugin:uninstall", Path: "/api/v1/admin/auth/plugins/{plugin_id}", Method: "DELETE", ParentID: 48, SortOrder: 5, IsCommon: false, Status: 1, Description: "卸载插件API"},
		{ID: 55, Domain: "default", Type: "button", Name: "插件管理-新增按钮", Code: "plugin:add", ParentID: 48, SortOrder: 0, IsCommon: false, Status: 1, Description: "插件管理页面新增按钮"},
		{ID: 56, Domain: "default", Type: "button", Name: "插件管理-编辑按钮", Code: "plugin:edit", ParentID: 48, SortOrder: 1, IsCommon: false, Status: 1, Description: "插件管理页面编辑按钮"},
		{ID: 57, Domain: "default", Type: "button", Name: "插件管理-删除按钮", Code: "plugin:delete", ParentID: 48, SortOrder: 2, IsCommon: false, Status: 1, Description: "插件管理页面删除按钮"},
		{ID: 59, Domain: "default", Type: "menu", Name: "消息中心", Code: "message", MenuPath: "/message", Icon: "icon-message", ParentID: 0, SortOrder: 3, IsCommon: false, Status: 1, Description: "消息中心模块"},
		{ID: 60, Domain: "default", Type: "menu", Name: "消息列表", Code: "messagecenter", MenuPath: "/message/center", Component: "message/index", ParentID: 59, SortOrder: 0, IsCommon: false, Status: 1, Description: "消息列表页面"},
		{ID: 61, Domain: "default", Type: "menu", Name: "消息管理", Code: "messagemanage", MenuPath: "/message/manage", Component: "message/manage", ParentID: 59, SortOrder: 1, IsCommon: false, Status: 1, Description: "消息管理页面"},
		{ID: 62, Domain: "default", Type: "menu", Name: "发送消息", Code: "messagebroadcast", MenuPath: "/message/broadcast", Component: "message/broadcast", ParentID: 59, SortOrder: 2, IsCommon: false, Status: 1, Description: "发送消息页面"},
		{ID: 63, Domain: "default", Type: "menu", Name: "消息偏好", Code: "messagepreference", MenuPath: "/message/preference", Component: "message/preference", ParentID: 59, SortOrder: 3, IsCommon: false, Status: 1, Description: "消息偏好页面"},
		{ID: 64, Domain: "default", Type: "api", Name: "获取消息列表", Code: "message:list", Path: "/api/v1/admin/auth/messages", Method: "GET", ParentID: 60, SortOrder: 0, IsCommon: false, Status: 1, Description: "获取消息列表API"},
		{ID: 65, Domain: "default", Type: "api", Name: "获取消息详情", Code: "message:get", Path: "/api/v1/admin/auth/messages/{id}", Method: "GET", ParentID: 60, SortOrder: 1, IsCommon: false, Status: 1, Description: "获取消息详情API"},
		{ID: 66, Domain: "default", Type: "api", Name: "标记消息已读", Code: "message:read", Path: "/api/v1/admin/auth/messages/read", Method: "POST", ParentID: 60, SortOrder: 2, IsCommon: false, Status: 1, Description: "标记消息已读API"},
		{ID: 67, Domain: "default", Type: "api", Name: "全部标记已读", Code: "message:read-all", Path: "/api/v1/admin/auth/messages/read-all", Method: "POST", ParentID: 60, SortOrder: 3, IsCommon: false, Status: 1, Description: "全部标记已读API"},
		{ID: 68, Domain: "default", Type: "api", Name: "标记消息未读", Code: "message:unread", Path: "/api/v1/admin/auth/messages/unread", Method: "POST", ParentID: 60, SortOrder: 4, IsCommon: false, Status: 1, Description: "标记消息未读API"},
		{ID: 69, Domain: "default", Type: "api", Name: "获取未读数量", Code: "message:unread-count", Path: "/api/v1/admin/auth/messages/unread-count", Method: "GET", ParentID: 60, SortOrder: 5, IsCommon: false, Status: 1, Description: "获取未读数量API"},
		{ID: 70, Domain: "default", Type: "api", Name: "删除消息", Code: "message:delete-api", Path: "/api/v1/admin/auth/messages/{id}", Method: "DELETE", ParentID: 60, SortOrder: 6, IsCommon: false, Status: 1, Description: "删除消息API"},
		{ID: 71, Domain: "default", Type: "api", Name: "批量删除消息", Code: "message:delete-batch", Path: "/api/v1/admin/auth/messages/delete-batch", Method: "POST", ParentID: 61, SortOrder: 0, IsCommon: false, Status: 1, Description: "批量删除消息API"},
		{ID: 72, Domain: "default", Type: "api", Name: "清空消息", Code: "message:clear", Path: "/api/v1/admin/auth/messages/clear", Method: "POST", ParentID: 61, SortOrder: 1, IsCommon: false, Status: 1, Description: "清空消息API"},
		{ID: 73, Domain: "default", Type: "api", Name: "获取消息偏好", Code: "messagepreference:get", Path: "/api/v1/admin/auth/messages/preference", Method: "GET", ParentID: 63, SortOrder: 0, IsCommon: false, Status: 1, Description: "获取消息偏好API"},
		{ID: 74, Domain: "default", Type: "api", Name: "更新消息偏好", Code: "messagepreference:update", Path: "/api/v1/admin/auth/messages/preference", Method: "PUT", ParentID: 63, SortOrder: 1, IsCommon: false, Status: 1, Description: "更新消息偏好API"},
		{ID: 75, Domain: "default", Type: "api", Name: "发送广播消息", Code: "messagebroadcast:send", Path: "/api/v1/admin/auth/messages/broadcast", Method: "POST", ParentID: 62, SortOrder: 0, IsCommon: false, Status: 1, Description: "发送广播消息API"},
		{ID: 76, Domain: "default", Type: "api", Name: "测试推送消息", Code: "message:test-push", Path: "/api/v1/admin/auth/messages/test-push", Method: "POST", ParentID: 62, SortOrder: 1, IsCommon: false, Status: 1, Description: "测试推送消息API"},
		{ID: 77, Domain: "default", Type: "api", Name: "签发SSE票据", Code: "sse:ticket", Path: "/api/v1/admin/auth/sse/ticket", Method: "POST", ParentID: 60, SortOrder: 7, IsCommon: false, Status: 1, Description: "签发SSE票据API"},
		{ID: 78, Domain: "default", Type: "button", Name: "消息中心-新增按钮", Code: "message:add", ParentID: 59, SortOrder: 0, IsCommon: false, Status: 1, Description: "消息中心新增按钮"},
		{ID: 79, Domain: "default", Type: "button", Name: "消息中心-编辑按钮", Code: "message:edit", ParentID: 59, SortOrder: 1, IsCommon: false, Status: 1, Description: "消息中心编辑按钮"},
		{ID: 80, Domain: "default", Type: "button", Name: "消息中心-删除按钮", Code: "message:delete", ParentID: 59, SortOrder: 2, IsCommon: false, Status: 1, Description: "消息中心删除按钮"},
		{ID: 81, Domain: "default", Type: "button", Name: "消息列表-新增按钮", Code: "messagecenter:add", ParentID: 60, SortOrder: 0, IsCommon: false, Status: 1, Description: "消息列表新增按钮"},
		{ID: 82, Domain: "default", Type: "button", Name: "消息列表-编辑按钮", Code: "messagecenter:edit", ParentID: 60, SortOrder: 1, IsCommon: false, Status: 1, Description: "消息列表编辑按钮"},
		{ID: 83, Domain: "default", Type: "button", Name: "消息列表-删除按钮", Code: "messagecenter:delete", ParentID: 60, SortOrder: 2, IsCommon: false, Status: 1, Description: "消息列表删除按钮"},
		{ID: 84, Domain: "default", Type: "button", Name: "消息管理-新增按钮", Code: "messagemanage:add", ParentID: 61, SortOrder: 0, IsCommon: false, Status: 1, Description: "消息管理新增按钮"},
		{ID: 85, Domain: "default", Type: "button", Name: "消息管理-编辑按钮", Code: "messagemanage:edit", ParentID: 61, SortOrder: 1, IsCommon: false, Status: 1, Description: "消息管理编辑按钮"},
		{ID: 86, Domain: "default", Type: "button", Name: "消息管理-删除按钮", Code: "messagemanage:delete", ParentID: 61, SortOrder: 2, IsCommon: false, Status: 1, Description: "消息管理删除按钮"},
		{ID: 87, Domain: "default", Type: "button", Name: "发送消息-新增按钮", Code: "messagebroadcast:add", ParentID: 62, SortOrder: 0, IsCommon: false, Status: 1, Description: "发送消息新增按钮"},
		{ID: 88, Domain: "default", Type: "button", Name: "发送消息-编辑按钮", Code: "messagebroadcast:edit", ParentID: 62, SortOrder: 1, IsCommon: false, Status: 1, Description: "发送消息编辑按钮"},
		{ID: 89, Domain: "default", Type: "button", Name: "发送消息-删除按钮", Code: "messagebroadcast:delete", ParentID: 62, SortOrder: 2, IsCommon: false, Status: 1, Description: "发送消息删除按钮"},
		{ID: 90, Domain: "default", Type: "button", Name: "消息偏好-新增按钮", Code: "messagepreference:add", ParentID: 63, SortOrder: 0, IsCommon: false, Status: 1, Description: "消息偏好新增按钮"},
		{ID: 91, Domain: "default", Type: "button", Name: "消息偏好-编辑按钮", Code: "messagepreference:edit", ParentID: 63, SortOrder: 1, IsCommon: false, Status: 1, Description: "消息偏好编辑按钮"},
		{ID: 92, Domain: "default", Type: "button", Name: "消息偏好-删除按钮", Code: "messagepreference:delete", ParentID: 63, SortOrder: 2, IsCommon: false, Status: 1, Description: "消息偏好删除按钮"},
		{ID: 93, Domain: "default", Type: "api", Name: "获取配置分组", Code: "config:groups", Path: "/api/v1/admin/auth/configs/groups", Method: "GET", ParentID: 10, SortOrder: 1, IsCommon: false, Status: 1, Description: "获取配置分组API"},
		{ID: 94, Domain: "default", Type: "api", Name: "获取附件分组列表", Code: "attachment-group:list", Path: "/api/v1/admin/auth/attachment-groups", Method: "GET", ParentID: 6, SortOrder: 3, IsCommon: false, Status: 1, Description: "获取附件分组列表API"},
		{ID: 95, Domain: "default", Type: "api", Name: "按类型获取字典数据", Code: "dict-data:type", Path: "/api/v1/admin/auth/dict-data/type/{type}", Method: "GET", ParentID: 9, SortOrder: 1, IsCommon: false, Status: 1, Description: "按类型获取字典数据API"},
	}

	if err := svc.InitSeedPermissions(ctx, seedData); err != nil {
		return fmt.Errorf("初始化种子权限失败: %w", err)
	}

	return nil
}

// toResponse 转换为响应格式
func (s *SystemPermissionService) toResponse(p *ent.SystemPermission) *SystemPermissionResponse {
	return &SystemPermissionResponse{
		ID:          p.ID,
		Domain:      p.Domain,
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
		CreatedAt:   p.CreatedAt.Format("2006-01-02 15:04:05"),
		UpdatedAt:   p.UpdatedAt.Format("2006-01-02 15:04:05"),
	}
}
