package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/dictdata"
	"go-admin/internal/global"
	"go-admin/internal/utils"
)

var (
	ErrDictDataNotFound = errors.New("字典数据不存在")
	ErrDictDataType     = errors.New("字典类型编码不能为空")
)

// DictDataService 数据字典服务
type DictDataService struct {
	*CRUDBase
}

var _ CRUD[CreateDictDataRequest, UpdateDictDataRequest, ListDictDataRequest, DictDataResponse] = (*DictDataService)(nil)

// NewDictDataService 创建数据字典服务实例
func NewDictDataService() *DictDataService {
	return &DictDataService{
		CRUDBase: &CRUDBase{Client: global.ORM},
	}
}

// CreateDictDataRequest 创建字典数据请求
type CreateDictDataRequest struct {
	Label     string `json:"label" validate:"required,max=100"`
	Value     string `json:"value" validate:"required,max=100"`
	DictType  string `json:"dict_type" validate:"required,max=100"`
	Sort      *int   `json:"sort"`
	Status    *int   `json:"status"`
	CSSClass  string `json:"css_class" validate:"omitempty,max=100"`
	IsDefault *bool  `json:"is_default"`
	Remark    string `json:"remark" validate:"omitempty,max=500"`
}

// UpdateDictDataRequest 更新字典数据请求
type UpdateDictDataRequest struct {
	Label     *string `json:"label" validate:"omitempty,max=100"`
	Value     *string `json:"value" validate:"omitempty,max=100"`
	DictType  *string `json:"dict_type" validate:"omitempty,max=100"`
	Sort      *int    `json:"sort"`
	Status    *int    `json:"status"`
	CSSClass  *string `json:"css_class" validate:"omitempty,max=100"`
	IsDefault *bool   `json:"is_default"`
	Remark    *string `json:"remark" validate:"omitempty,max=500"`
}

// ListDictDataRequest 字典数据列表请求
type ListDictDataRequest struct {
	ListRequest
	DictType string `query:"dict_type"`
	Status   *int   `query:"status"`
}

// DictDataResponse 字典数据响应
type DictDataResponse struct {
	ID        int       `json:"id"`
	Label     string    `json:"label"`
	Value     string    `json:"value"`
	DictType  string    `json:"dict_type"`
	Sort      int       `json:"sort"`
	Status    int       `json:"status"`
	CSSClass  string    `json:"css_class,omitempty"`
	IsDefault bool      `json:"is_default"`
	Remark    string    `json:"remark,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ListDictDataResponse 字典数据列表响应
type ListDictDataResponse = ListResponse[*DictDataResponse]

// Create 创建字典数据
func (s *DictDataService) Create(ctx context.Context, req *CreateDictDataRequest) (*DictDataResponse, error) {
	builder := s.Client.DictData.Create().
		SetLabel(req.Label).
		SetValue(req.Value).
		SetDictType(req.DictType)

	if req.Sort != nil {
		builder.SetSort(*req.Sort)
	}
	if req.Status != nil {
		builder.SetStatus(*req.Status)
	}
	if req.CSSClass != "" {
		builder.SetCSSClass(req.CSSClass)
	}
	if req.IsDefault != nil {
		builder.SetIsDefault(*req.IsDefault)
	}
	if req.Remark != "" {
		builder.SetRemark(req.Remark)
	}

	item, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("创建字典数据失败: %w", err)
	}

	return toDictDataResponse(item), nil
}

// Get 获取字典数据详情
func (s *DictDataService) Get(ctx context.Context, id int) (*DictDataResponse, error) {
	return GetByID(ctx,
		func(ctx context.Context) (*ent.DictData, error) {
			return s.Client.DictData.Query().Where(dictdata.IDEQ(id)).Only(ctx)
		}, toDictDataResponse, ErrDictDataNotFound, "字典数据")
}

// List 获取字典数据列表
func (s *DictDataService) List(ctx context.Context, req *ListDictDataRequest) (*ListDictDataResponse, error) {
	req.Page, req.PageSize = utils.NormalizePagination(req.Page, req.PageSize)

	query := s.Client.DictData.Query()

	// 关键字搜索
	if req.Keyword != "" {
		query = query.Where(
			dictdata.Or(
				dictdata.LabelContains(req.Keyword),
				dictdata.ValueContains(req.Keyword),
			),
		)
	}

	// 字典类型筛选
	if req.DictType != "" {
		query = query.Where(dictdata.DictTypeEQ(req.DictType))
	}

	// 状态筛选
	if req.Status != nil {
		query = query.Where(dictdata.StatusEQ(*req.Status))
	}

	// 获取总数
	total, err := query.Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取字典数据总数失败: %w", err)
	}

	// 分页查询，按排序字段升序
	query = query.Order(ent.Asc(dictdata.FieldSort), ent.Desc(dictdata.FieldCreatedAt))
	if req.PageSize > 0 {
		offset := (req.Page - 1) * req.PageSize
		query = query.Offset(offset).Limit(req.PageSize)
	}
	items, err := query.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取字典数据列表失败: %w", err)
	}

	return BuildList(items, total, req.Page, toDictDataResponse), nil
}

// GetByType 按字典类型获取启用状态的数据列表（按排序升序）
func (s *DictDataService) GetByType(ctx context.Context, dictType string) ([]*DictDataResponse, error) {
	items, err := s.Client.DictData.Query().
		Where(
			dictdata.DictTypeEQ(dictType),
			dictdata.StatusEQ(1),
		).
		Order(ent.Asc(dictdata.FieldSort), ent.Asc(dictdata.FieldLabel)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("按类型查询字典数据失败: %w", err)
	}

	data := make([]*DictDataResponse, 0, len(items))
	for _, item := range items {
		data = append(data, toDictDataResponse(item))
	}

	return data, nil
}

// Update 更新字典数据
func (s *DictDataService) Update(ctx context.Context, id int, req *UpdateDictDataRequest) error {
	item, err := s.Client.DictData.Query().
		Where(dictdata.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrDictDataNotFound
		}
		return fmt.Errorf("查询字典数据失败: %w", err)
	}

	update := s.Client.DictData.UpdateOne(item)

	if req.Label != nil {
		update.SetLabel(*req.Label)
	}
	if req.Value != nil {
		update.SetValue(*req.Value)
	}
	if req.DictType != nil {
		update.SetDictType(*req.DictType)
	}
	if req.Sort != nil {
		update.SetSort(*req.Sort)
	}
	if req.Status != nil {
		update.SetStatus(*req.Status)
	}
	if req.CSSClass != nil {
		update.SetCSSClass(*req.CSSClass)
	}
	if req.IsDefault != nil {
		update.SetIsDefault(*req.IsDefault)
	}
	if req.Remark != nil {
		update.SetRemark(*req.Remark)
	}

	if _, err := update.Save(ctx); err != nil {
		return fmt.Errorf("更新字典数据失败: %w", err)
	}

	return nil
}

// Delete 删除字典数据
func (s *DictDataService) Delete(ctx context.Context, id int) error {
	return DeleteByID(ctx,
		func(ctx context.Context) (*ent.DictData, error) {
			return s.Client.DictData.Query().Where(dictdata.IDEQ(id)).Only(ctx)
		},
		func(e *ent.DictData) error {
			return s.Client.DictData.DeleteOne(e).Exec(ctx)
		},
		ErrDictDataNotFound, "字典数据")
}

// toDictDataResponse 转换为响应格式
func toDictDataResponse(item *ent.DictData) *DictDataResponse {
	return &DictDataResponse{
		ID:        item.ID,
		Label:     item.Label,
		Value:     item.Value,
		DictType:  item.DictType,
		Sort:      item.Sort,
		Status:    item.Status,
		CSSClass:  item.CSSClass,
		IsDefault: item.IsDefault,
		Remark:    item.Remark,
		CreatedAt: item.CreatedAt,
		UpdatedAt: item.UpdatedAt,
	}
}
