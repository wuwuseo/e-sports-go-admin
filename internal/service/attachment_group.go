package service

import (
	"context"
	"errors"
	"fmt"

	"go-admin/internal/ent"
	"go-admin/internal/ent/attachment"
	"go-admin/internal/ent/attachmentgroup"
	"go-admin/internal/global"
)

var (
	ErrAttachmentGroupNotFound = errors.New("附件分组不存在")
	ErrAttachmentGroupNotEmpty = errors.New("附件分组下存在附件，无法删除")
	ErrAttachmentGroupExists   = errors.New("附件分组名称已存在")
)

// AttachmentGroupServiceInterface 附件分组服务接口
type AttachmentGroupServiceInterface interface {
	Create(ctx context.Context, req *CreateGroupRequest) (*GroupResponse, error)
	Update(ctx context.Context, id int, req *UpdateGroupRequest) error
	Delete(ctx context.Context, id int) error
	Get(ctx context.Context, id int) (*GroupResponse, error)
	List(ctx context.Context, req *ListGroupsRequest) (*ListGroupsResponse, error)
}

// AttachmentGroupService 附件分组服务
type AttachmentGroupService struct {
	client *ent.Client
}

// NewAttachmentGroupService 创建附件分组服务实例
func NewAttachmentGroupService() *AttachmentGroupService {
	return &AttachmentGroupService{
		client: global.ORM,
	}
}

// CreateGroupRequest 创建分组请求
type CreateGroupRequest struct {
	Name        string `json:"name" validate:"required,max=255"`
	Description string `json:"description" validate:"max=500"`
	Sort        int    `json:"sort"`
}

// UpdateGroupRequest 更新分组请求
type UpdateGroupRequest struct {
	Name        string `json:"name" validate:"max=255"`
	Description string `json:"description" validate:"max=500"`
	Sort        *int   `json:"sort"`
}

// GroupResponse 分组响应
type GroupResponse struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Sort        int    `json:"sort"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// ListGroupsRequest 分组列表请求
type ListGroupsRequest struct {
	Page     int    `query:"page"`
	PageSize int    `query:"page_size"`
	Keyword  string `query:"keyword"`
}

// ListGroupsResponse 分组列表响应
type ListGroupsResponse struct {
	Total int              `json:"total"`
	Page  int              `json:"page"`
	Data  []*GroupResponse `json:"data"`
}

// Create 创建附件分组
func (s *AttachmentGroupService) Create(ctx context.Context, req *CreateGroupRequest) (*GroupResponse, error) {
	// 检查名称唯一性
	exists, err := s.client.AttachmentGroup.Query().
		Where(attachmentgroup.NameEQ(req.Name)).
		Exist(ctx)
	if err != nil {
		return nil, fmt.Errorf("检查分组名称失败: %w", err)
	}
	if exists {
		return nil, ErrAttachmentGroupExists
	}

	builder := s.client.AttachmentGroup.Create().
		SetName(req.Name).
		SetSort(req.Sort)

	if req.Description != "" {
		builder.SetDescription(req.Description)
	}

	g, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("创建分组失败: %w", err)
	}

	return s.toGroupResponse(g), nil
}

// Update 更新附件分组
func (s *AttachmentGroupService) Update(ctx context.Context, id int, req *UpdateGroupRequest) error {
	g, err := s.client.AttachmentGroup.Query().
		Where(attachmentgroup.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrAttachmentGroupNotFound
		}
		return fmt.Errorf("查询分组失败: %w", err)
	}

	update := s.client.AttachmentGroup.UpdateOne(g)

	if req.Name != "" {
		// 检查名称唯一性（排除自身）
		exists, err := s.client.AttachmentGroup.Query().
			Where(
				attachmentgroup.NameEQ(req.Name),
				attachmentgroup.IDNEQ(id),
			).
			Exist(ctx)
		if err != nil {
			return fmt.Errorf("检查分组名称失败: %w", err)
		}
		if exists {
			return ErrAttachmentGroupExists
		}
		update.SetName(req.Name)
	}

	if req.Description != "" {
		update.SetDescription(req.Description)
	}

	if req.Sort != nil {
		update.SetSort(*req.Sort)
	}

	_, err = update.Save(ctx)
	if err != nil {
		return fmt.Errorf("更新分组失败: %w", err)
	}

	return nil
}

// Delete 删除附件分组
func (s *AttachmentGroupService) Delete(ctx context.Context, id int) error {
	// 检查分组是否存在
	exists, err := s.client.AttachmentGroup.Query().
		Where(attachmentgroup.IDEQ(id)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("查询分组失败: %w", err)
	}
	if !exists {
		return ErrAttachmentGroupNotFound
	}

	// 检查分组下是否有附件
	hasAttachments, err := s.client.Attachment.Query().
		Where(attachment.GroupIDEQ(id)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("检查分组附件失败: %w", err)
	}
	if hasAttachments {
		return ErrAttachmentGroupNotEmpty
	}

	// 删除分组
	err = s.client.AttachmentGroup.DeleteOneID(id).Exec(ctx)
	if err != nil {
		return fmt.Errorf("删除分组失败: %w", err)
	}

	return nil
}

// Get 获取附件分组详情
func (s *AttachmentGroupService) Get(ctx context.Context, id int) (*GroupResponse, error) {
	g, err := s.client.AttachmentGroup.Query().
		Where(attachmentgroup.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrAttachmentGroupNotFound
		}
		return nil, fmt.Errorf("查询分组失败: %w", err)
	}

	return s.toGroupResponse(g), nil
}

// List 获取附件分组列表
func (s *AttachmentGroupService) List(ctx context.Context, req *ListGroupsRequest) (*ListGroupsResponse, error) {
	if req.Page < 1 {
		req.Page = 1
	}
	if req.PageSize < 1 {
		req.PageSize = 10
	}
	if req.PageSize > 100 {
		req.PageSize = 100
	}

	query := s.client.AttachmentGroup.Query()

	if req.Keyword != "" {
		query = query.Where(
			attachmentgroup.Or(
				attachmentgroup.NameContains(req.Keyword),
				attachmentgroup.DescriptionContains(req.Keyword),
			),
		)
	}

	total, err := query.Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取分组总数失败: %w", err)
	}

	offset := (req.Page - 1) * req.PageSize
	groups, err := query.
		Offset(offset).
		Limit(req.PageSize).
		Order(ent.Asc(attachmentgroup.FieldSort), ent.Desc(attachmentgroup.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取分组列表失败: %w", err)
	}

	data := make([]*GroupResponse, 0, len(groups))
	for _, g := range groups {
		data = append(data, s.toGroupResponse(g))
	}

	return &ListGroupsResponse{
		Total: total,
		Page:  req.Page,
		Data:  data,
	}, nil
}

// toGroupResponse 将实体转换为响应格式
func (s *AttachmentGroupService) toGroupResponse(g *ent.AttachmentGroup) *GroupResponse {
	return &GroupResponse{
		ID:          g.ID,
		Name:        g.Name,
		Description: g.Description,
		Sort:        g.Sort,
		CreatedAt:   g.CreatedAt.Format("2006-01-02 15:04:05"),
		UpdatedAt:   g.UpdatedAt.Format("2006-01-02 15:04:05"),
	}
}
