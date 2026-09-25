package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/slide"
	"go-admin/internal/global"
	"go-admin/internal/utils"
)

var ErrSlideNotFound = errors.New("幻灯片不存在")

// SlideService 幻灯片服务
type SlideService struct {
	*CRUDBase
}

var _ CRUD[CreateSlideRequest, UpdateSlideRequest, ListSlideRequest, SlideResponse] = (*SlideService)(nil)

// NewSlideService 创建幻灯片服务实例
func NewSlideService() *SlideService {
	return &SlideService{
		CRUDBase: &CRUDBase{Client: global.ORM},
	}
}

// CreateSlideRequest 创建幻灯片请求
type CreateSlideRequest struct {
	Title     string `json:"title" validate:"required,max=100"`
	Image     string `json:"image" validate:"required,max=500"`
	Link      string `json:"link" validate:"omitempty,max=500"`
	GroupCode string `json:"group_code" validate:"required,max=100"`
	Sort      *int   `json:"sort"`
	Status    *int   `json:"status"`
	Remark    string `json:"remark" validate:"omitempty,max=500"`
}

// UpdateSlideRequest 更新幻灯片请求
type UpdateSlideRequest struct {
	Title     *string `json:"title" validate:"omitempty,max=100"`
	Image     *string `json:"image" validate:"omitempty,max=500"`
	Link      *string `json:"link" validate:"omitempty,max=500"`
	GroupCode *string `json:"group_code" validate:"omitempty,max=100"`
	Sort      *int    `json:"sort"`
	Status    *int    `json:"status"`
	Remark    *string `json:"remark" validate:"omitempty,max=500"`
}

// ListSlideRequest 幻灯片列表请求
type ListSlideRequest struct {
	ListRequest
	GroupCode string `query:"group_code"`
	Status    *int   `query:"status"`
}

// SlideResponse 幻灯片响应(管理端)
type SlideResponse struct {
	ID        int       `json:"id"`
	Title     string    `json:"title"`
	Image     string    `json:"image"`
	Link      string    `json:"link,omitempty"`
	GroupCode string    `json:"group_code"`
	Sort      int       `json:"sort"`
	Status    int       `json:"status"`
	Remark    string    `json:"remark,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ListSlideResponse 幻灯片列表响应
type ListSlideResponse = ListResponse[*SlideResponse]

// SlidePublicResponse 公开幻灯片响应(仅安全字段)
type SlidePublicResponse struct {
	ID        int    `json:"id"`
	Title     string `json:"title"`
	Image     string `json:"image"`
	Link      string `json:"link,omitempty"`
	GroupCode string `json:"group_code"`
}

// Create 创建幻灯片
func (s *SlideService) Create(ctx context.Context, req *CreateSlideRequest) (*SlideResponse, error) {
	builder := s.Client.Slide.Create().
		SetTitle(req.Title).
		SetImage(req.Image).
		SetGroupCode(req.GroupCode)

	if req.Link != "" {
		builder.SetLink(req.Link)
	}
	if req.Sort != nil {
		builder.SetSort(*req.Sort)
	}
	if req.Status != nil {
		builder.SetStatus(*req.Status)
	}
	if req.Remark != "" {
		builder.SetRemark(req.Remark)
	}

	item, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("创建幻灯片失败: %w", err)
	}

	return toSlideResponse(item), nil
}

// Get 获取幻灯片详情
func (s *SlideService) Get(ctx context.Context, id int) (*SlideResponse, error) {
	return GetByID(ctx,
		func(ctx context.Context) (*ent.Slide, error) {
			return s.Client.Slide.Query().Where(slide.IDEQ(id)).Only(ctx)
		}, toSlideResponse, ErrSlideNotFound, "幻灯片")
}

// List 获取幻灯片列表(管理端,含禁用)
func (s *SlideService) List(ctx context.Context, req *ListSlideRequest) (*ListSlideResponse, error) {
	req.Page, req.PageSize = utils.NormalizePagination(req.Page, req.PageSize)

	query := s.Client.Slide.Query()

	if req.GroupCode != "" {
		query = query.Where(slide.GroupCodeEQ(req.GroupCode))
	}
	if req.Status != nil {
		query = query.Where(slide.StatusEQ(*req.Status))
	}

	total, err := query.Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取幻灯片总数失败: %w", err)
	}

	query = query.Order(ent.Asc(slide.FieldSort), ent.Asc(slide.FieldID))
	if req.PageSize > 0 {
		offset := (req.Page - 1) * req.PageSize
		query = query.Offset(offset).Limit(req.PageSize)
	}
	items, err := query.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取幻灯片列表失败: %w", err)
	}

	return BuildList(items, total, req.Page, toSlideResponse), nil
}

// Update 更新幻灯片
func (s *SlideService) Update(ctx context.Context, id int, req *UpdateSlideRequest) error {
	item, err := s.Client.Slide.Query().
		Where(slide.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrSlideNotFound
		}
		return fmt.Errorf("查询幻灯片失败: %w", err)
	}

	update := s.Client.Slide.UpdateOne(item)

	if req.Title != nil {
		update.SetTitle(*req.Title)
	}
	if req.Image != nil {
		update.SetImage(*req.Image)
	}
	if req.Link != nil {
		update.SetLink(*req.Link)
	}
	if req.GroupCode != nil {
		update.SetGroupCode(*req.GroupCode)
	}
	if req.Sort != nil {
		update.SetSort(*req.Sort)
	}
	if req.Status != nil {
		update.SetStatus(*req.Status)
	}
	if req.Remark != nil {
		update.SetRemark(*req.Remark)
	}

	if _, err := update.Save(ctx); err != nil {
		return fmt.Errorf("更新幻灯片失败: %w", err)
	}

	return nil
}

// Delete 删除幻灯片
func (s *SlideService) Delete(ctx context.Context, id int) error {
	return DeleteByID(ctx,
		func(ctx context.Context) (*ent.Slide, error) {
			return s.Client.Slide.Query().Where(slide.IDEQ(id)).Only(ctx)
		},
		func(e *ent.Slide) error {
			return s.Client.Slide.DeleteOne(e).Exec(ctx)
		},
		ErrSlideNotFound, "幻灯片")
}

// ListPublic 公开获取启用中的幻灯片(免登录);groupCode 为空返回全部分组,按 sort 升序
func (s *SlideService) ListPublic(ctx context.Context, groupCode string) ([]*SlidePublicResponse, error) {
	query := s.Client.Slide.Query().Where(slide.StatusEQ(1))
	if groupCode != "" {
		query = query.Where(slide.GroupCodeEQ(groupCode))
	}

	items, err := query.
		Order(ent.Asc(slide.FieldSort), ent.Asc(slide.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询公开幻灯片失败: %w", err)
	}

	data := make([]*SlidePublicResponse, 0, len(items))
	for _, item := range items {
		data = append(data, &SlidePublicResponse{
			ID:        item.ID,
			Title:     item.Title,
			Image:     item.Image,
			Link:      item.Link,
			GroupCode: item.GroupCode,
		})
	}

	return data, nil
}

// toSlideResponse 转换为响应格式
func toSlideResponse(item *ent.Slide) *SlideResponse {
	return &SlideResponse{
		ID:        item.ID,
		Title:     item.Title,
		Image:     item.Image,
		Link:      item.Link,
		GroupCode: item.GroupCode,
		Sort:      item.Sort,
		Status:    item.Status,
		Remark:    item.Remark,
		CreatedAt: item.CreatedAt,
		UpdatedAt: item.UpdatedAt,
	}
}
