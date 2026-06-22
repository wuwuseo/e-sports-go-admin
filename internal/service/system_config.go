package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/dictdata"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/global"
	"go-admin/internal/utils"

	"github.com/wuwuseo/cmf/cache"
)

var (
	ErrConfigNotFound  = errors.New("配置不存在")
	ErrConfigKeyExists = errors.New("配置键已存在")
	ErrConfigReserved  = errors.New("reserved config key")
)

const reservedCaptchaConfigPrefix = "captcha."

func isReservedConfigKey(key string) bool {
	return strings.HasPrefix(key, reservedCaptchaConfigPrefix)
}

// ConfigService 动态配置服务
type ConfigService struct {
	*CRUDBase
	cache *cache.TypedCache[*ConfigResponse]
}

var _ CRUD[CreateConfigRequest, UpdateConfigRequest, ListConfigRequest, ConfigResponse] = (*ConfigService)(nil)

// NewConfigService 创建动态配置服务实例
func NewConfigService(rawCache *cache.Cache[[]byte]) *ConfigService {
	var typedCache *cache.TypedCache[*ConfigResponse]
	if rawCache != nil {
		typedCache = cache.NewTypedCache[*ConfigResponse](rawCache)
	}
	return &ConfigService{
		CRUDBase: &CRUDBase{Client: global.ORM},
		cache:    typedCache,
	}
}

// CreateConfigRequest 创建配置请求
type CreateConfigRequest struct {
	Name   string `json:"name" validate:"required,max=100"`
	Key    string `json:"key" validate:"required,max=100"`
	Value  string `json:"value" validate:"max=2048"`
	Type   string `json:"type" validate:"omitempty,oneof=string number boolean json array array2d"`
	Group  string `json:"group" validate:"omitempty,max=50"`
	Status *int   `json:"status"`
	Remark string `json:"remark" validate:"omitempty,max=500"`
}

// UpdateConfigRequest 更新配置请求
type UpdateConfigRequest struct {
	Name   *string `json:"name" validate:"omitempty,max=100"`
	Key    *string `json:"key" validate:"omitempty,max=100"`
	Value  *string `json:"value" validate:"omitempty,max=2048"`
	Type   *string `json:"type" validate:"omitempty,oneof=string number boolean json array array2d"`
	Group  *string `json:"group" validate:"omitempty,max=50"`
	Status *int    `json:"status"`
	Remark *string `json:"remark" validate:"omitempty,max=500"`
}

// ListConfigRequest 配置列表请求
type ListConfigRequest struct {
	ListRequest
	Group  string `query:"group"`
	Status *int   `query:"status"`
}

// ConfigResponse 配置响应
type ConfigResponse struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	Key       string    `json:"key"`
	Value     string    `json:"value,omitempty"`
	Type      string    `json:"type"`
	Group     string    `json:"group,omitempty"`
	Status    int       `json:"status"`
	Remark    string    `json:"remark,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ConfigGroup 配置分组选项
type ConfigGroup struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// ListConfigResponse 配置列表响应
type ListConfigResponse = ListResponse[*ConfigResponse]

// configCacheKey 生成缓存键
func (s *ConfigService) configCacheKey(prefix string, parts ...string) string {
	return "config:" + prefix + ":" + strings.Join(parts, ":")
}

// clearConfigCache 清除指定配置的缓存
func (s *ConfigService) clearConfigCache(ctx context.Context, key string) {
	if s.cache == nil {
		return
	}
	_ = s.cache.Delete(ctx, s.configCacheKey("key", key))
}

// Create 创建配置
func (s *ConfigService) Create(ctx context.Context, req *CreateConfigRequest) (*ConfigResponse, error) {
	if isReservedConfigKey(req.Key) {
		return nil, ErrConfigReserved
	}
	// 检查 key 唯一性
	exists, err := s.Client.SystemConfig.Query().
		Where(systemconfig.KeyEQ(req.Key)).
		Exist(ctx)
	if err != nil {
		return nil, fmt.Errorf("检查配置键失败: %w", err)
	}
	if exists {
		return nil, ErrConfigKeyExists
	}

	builder := s.Client.SystemConfig.Create().
		SetName(req.Name).
		SetKey(req.Key).
		SetValue(req.Value)

	if req.Type != "" {
		builder.SetType(req.Type)
	}
	if req.Group != "" {
		builder.SetGroup(req.Group)
	}
	if req.Status != nil {
		builder.SetStatus(*req.Status)
	}
	if req.Remark != "" {
		builder.SetRemark(req.Remark)
	}

	item, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("创建配置失败: %w", err)
	}

	return toConfigResponse(item), nil
}

// Get 获取配置详情
func (s *ConfigService) Get(ctx context.Context, id int) (*ConfigResponse, error) {
	return GetByID(ctx,
		func(ctx context.Context) (*ent.SystemConfig, error) {
			return s.Client.SystemConfig.Query().
				Where(
					systemconfig.IDEQ(id),
					systemconfig.Not(systemconfig.KeyHasPrefix(reservedCaptchaConfigPrefix)),
				).
				Only(ctx)
		}, toConfigResponse, ErrConfigNotFound, "配置")
}

// List 获取配置列表
func (s *ConfigService) List(ctx context.Context, req *ListConfigRequest) (*ListConfigResponse, error) {
	req.Page, req.PageSize = utils.NormalizePagination(req.Page, req.PageSize)

	query := s.Client.SystemConfig.Query().
		Where(systemconfig.Not(systemconfig.KeyHasPrefix(reservedCaptchaConfigPrefix)))

	if req.Keyword != "" {
		query = query.Where(
			systemconfig.Or(
				systemconfig.NameContains(req.Keyword),
				systemconfig.KeyContains(req.Keyword),
			),
		)
	}

	if req.Group != "" {
		query = query.Where(systemconfig.GroupEQ(req.Group))
	}

	if req.Status != nil {
		query = query.Where(systemconfig.StatusEQ(*req.Status))
	}

	total, err := query.Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取配置总数失败: %w", err)
	}

	query = query.Order(ent.Desc(systemconfig.FieldCreatedAt))
	if req.PageSize > 0 {
		offset := (req.Page - 1) * req.PageSize
		query = query.Offset(offset).Limit(req.PageSize)
	}
	items, err := query.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取配置列表失败: %w", err)
	}

	return BuildList(items, total, req.Page, toConfigResponse), nil
}

// GetByKey 按 key 获取单个配置值（带缓存）
func (s *ConfigService) GetByKey(ctx context.Context, key string) (*ConfigResponse, error) {
	if isReservedConfigKey(key) {
		return nil, ErrConfigNotFound
	}
	cacheKey := s.configCacheKey("key", key)

	if s.cache != nil {
		cached, err := s.cache.Get(ctx, cacheKey)
		if err == nil && cached != nil {
			return cached, nil
		}
	}

	item, err := s.Client.SystemConfig.Query().
		Where(
			systemconfig.KeyEQ(key),
			systemconfig.StatusEQ(1),
		).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrConfigNotFound
		}
		return nil, fmt.Errorf("按key获取配置失败: %w", err)
	}

	resp := toConfigResponse(item)

	if s.cache != nil {
		_ = s.cache.SetWithExpiration(ctx, cacheKey, resp, 5*time.Minute)
	}

	return resp, nil
}

// GetByGroup 按分组获取所有启用配置
func (s *ConfigService) GetByGroup(ctx context.Context, group string) ([]*ConfigResponse, error) {
	items, err := s.Client.SystemConfig.Query().
		Where(
			systemconfig.GroupEQ(group),
			systemconfig.StatusEQ(1),
			systemconfig.Not(systemconfig.KeyHasPrefix(reservedCaptchaConfigPrefix)),
		).
		Order(ent.Asc(systemconfig.FieldKey)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("按分组获取配置失败: %w", err)
	}

	data := make([]*ConfigResponse, 0, len(items))
	for _, item := range items {
		data = append(data, toConfigResponse(item))
	}

	return data, nil
}

// GetGroups 获取配置分组选项（来自字典数据 sys_config_group）
func (s *ConfigService) GetGroups(ctx context.Context) ([]*ConfigGroup, error) {
	items, err := s.Client.DictData.Query().
		Where(
			dictdata.DictTypeEQ("sys_config_group"),
			dictdata.StatusEQ(1),
		).
		Order(ent.Asc(dictdata.FieldSort)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取配置分组失败: %w", err)
	}

	groups := make([]*ConfigGroup, 0, len(items))
	for _, item := range items {
		groups = append(groups, &ConfigGroup{
			Label: item.Label,
			Value: item.Value,
		})
	}

	return groups, nil
}

// Update 更新配置
func (s *ConfigService) Update(ctx context.Context, id int, req *UpdateConfigRequest) error {
	item, err := s.Client.SystemConfig.Query().
		Where(systemconfig.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrConfigNotFound
		}
		return fmt.Errorf("查询配置失败: %w", err)
	}
	if isReservedConfigKey(item.Key) {
		return ErrConfigReserved
	}

	update := s.Client.SystemConfig.UpdateOne(item)

	if req.Name != nil {
		update.SetName(*req.Name)
	}
	if req.Key != nil {
		if isReservedConfigKey(*req.Key) {
			return ErrConfigReserved
		}
		exists, err := s.Client.SystemConfig.Query().
			Where(systemconfig.KeyEQ(*req.Key), systemconfig.IDNEQ(id)).
			Exist(ctx)
		if err != nil {
			return fmt.Errorf("检查配置键失败: %w", err)
		}
		if exists {
			return ErrConfigKeyExists
		}
		update.SetKey(*req.Key)
	}
	if req.Value != nil {
		update.SetValue(*req.Value)
	}
	if req.Type != nil {
		update.SetType(*req.Type)
	}
	if req.Group != nil {
		update.SetGroup(*req.Group)
	}
	if req.Status != nil {
		update.SetStatus(*req.Status)
	}
	if req.Remark != nil {
		update.SetRemark(*req.Remark)
	}

	if _, err := update.Save(ctx); err != nil {
		return fmt.Errorf("更新配置失败: %w", err)
	}

	// 更新后清除缓存
	s.clearConfigCache(ctx, item.Key)
	if req.Key != nil {
		s.clearConfigCache(ctx, *req.Key)
	}

	return nil
}

// Delete 删除配置
func (s *ConfigService) Delete(ctx context.Context, id int) error {
	item, err := s.Client.SystemConfig.Query().Where(systemconfig.IDEQ(id)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrConfigNotFound
		}
		return fmt.Errorf("查询配置失败: %w", err)
	}
	if isReservedConfigKey(item.Key) {
		return ErrConfigReserved
	}

	if err := s.Client.SystemConfig.DeleteOne(item).Exec(ctx); err != nil {
		return fmt.Errorf("删除配置失败: %w", err)
	}

	s.clearConfigCache(ctx, item.Key)
	return nil
}

// toConfigResponse 转换为响应格式
func toConfigResponse(item *ent.SystemConfig) *ConfigResponse {
	return &ConfigResponse{
		ID:        item.ID,
		Name:      item.Name,
		Key:       item.Key,
		Value:     item.Value,
		Type:      item.Type,
		Group:     item.Group,
		Status:    item.Status,
		Remark:    item.Remark,
		CreatedAt: item.CreatedAt,
		UpdatedAt: item.UpdatedAt,
	}
}
