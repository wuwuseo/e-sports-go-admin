package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/dictdata"
	"go-admin/internal/ent/slide"
	"go-admin/internal/global"
)

// SlideGroupDictType 幻灯片分组的字典类型编码。幻灯片实体通过 group_code 指向该类型下的字典值，
// 因此分组的唯一数据源是数据字典，不另建表；这样幻灯片表单的分组下拉与分组管理抽屉始终一致。
const SlideGroupDictType = "slide_group"

// slideGroupValuePattern 分组编码只允许字母开头，后接字母/数字/下划线/中划线，
// 与前端 SlideGroupDrawer 的校验规则保持一致。
var slideGroupValuePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

var (
	ErrSlideGroupNotFound     = errors.New("幻灯片分组不存在")
	ErrSlideGroupInUse        = errors.New("该分组下仍有幻灯片,无法删除")
	ErrSlideGroupValueExists  = errors.New("分组编码已存在")
	ErrSlideGroupValueInUse   = errors.New("该分组已关联幻灯片,无法修改分组编码")
	ErrSlideGroupValueInvalid = errors.New("分组编码需以字母开头,且只能包含字母、数字、下划线和中划线")
)

// SlideGroupService 幻灯片分组服务(数据源为 dict_type=slide_group 的字典项)
type SlideGroupService struct {
	*CRUDBase
}

// NewSlideGroupService 创建幻灯片分组服务实例
func NewSlideGroupService() *SlideGroupService {
	return &SlideGroupService{
		CRUDBase: &CRUDBase{Client: global.ORM},
	}
}

// CreateSlideGroupRequest 创建幻灯片分组请求
type CreateSlideGroupRequest struct {
	Label  string `json:"label" validate:"required,max=100"`
	Value  string `json:"value" validate:"required,max=100"`
	Sort   *int   `json:"sort"`
	Status *int   `json:"status"`
	Remark string `json:"remark" validate:"omitempty,max=500"`
}

// UpdateSlideGroupRequest 更新幻灯片分组请求
type UpdateSlideGroupRequest struct {
	Label  *string `json:"label" validate:"omitempty,max=100"`
	Value  *string `json:"value" validate:"omitempty,max=100"`
	Sort   *int    `json:"sort"`
	Status *int    `json:"status"`
	Remark *string `json:"remark" validate:"omitempty,max=500"`
}

// SlideGroupResponse 幻灯片分组响应
type SlideGroupResponse struct {
	ID         int       `json:"id"`
	Label      string    `json:"label"`
	Value      string    `json:"value"`
	Sort       int       `json:"sort"`
	Status     int       `json:"status"`
	Remark     string    `json:"remark,omitempty"`
	SlideCount int       `json:"slide_count"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// List 获取全部分组(按 sort 升序),并附带分组下的幻灯片数量
func (s *SlideGroupService) List(ctx context.Context) ([]*SlideGroupResponse, error) {
	groups, err := s.Client.DictData.Query().
		Where(dictdata.DictTypeEQ(SlideGroupDictType)).
		Order(ent.Asc(dictdata.FieldSort), ent.Asc(dictdata.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询幻灯片分组失败: %w", err)
	}

	counts, err := s.slideCounts(ctx)
	if err != nil {
		return nil, err
	}

	items := make([]*SlideGroupResponse, 0, len(groups))
	for _, group := range groups {
		items = append(items, toSlideGroupResponse(group, counts[group.Value]))
	}

	return items, nil
}

// Create 创建分组
func (s *SlideGroupService) Create(ctx context.Context, req *CreateSlideGroupRequest) (*SlideGroupResponse, error) {
	if err := validateSlideGroupValue(req.Value); err != nil {
		return nil, err
	}
	if err := s.ensureValueAvailable(ctx, req.Value, 0); err != nil {
		return nil, err
	}

	builder := s.Client.DictData.Create().
		SetLabel(req.Label).
		SetValue(req.Value).
		SetDictType(SlideGroupDictType).
		SetIsDefault(false)

	if req.Sort != nil {
		builder.SetSort(*req.Sort)
	} else {
		sort, err := s.nextSort(ctx)
		if err != nil {
			return nil, err
		}
		builder.SetSort(sort)
	}
	if req.Status != nil {
		builder.SetStatus(*req.Status)
	}
	if req.Remark != "" {
		builder.SetRemark(req.Remark)
	}

	created, err := builder.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, ErrSlideGroupValueExists
		}
		return nil, fmt.Errorf("创建幻灯片分组失败: %w", err)
	}

	return toSlideGroupResponse(created, 0), nil
}

// Update 更新分组
func (s *SlideGroupService) Update(ctx context.Context, id int, req *UpdateSlideGroupRequest) (*SlideGroupResponse, error) {
	group, err := s.groupByID(ctx, id)
	if err != nil {
		return nil, err
	}

	builder := group.Update()
	if req.Label != nil {
		if *req.Label == "" {
			return nil, ErrSlideGroupValueInvalid
		}
		builder.SetLabel(*req.Label)
	}
	if req.Value != nil && *req.Value != group.Value {
		if err := validateSlideGroupValue(*req.Value); err != nil {
			return nil, err
		}
		if err := s.ensureValueAvailable(ctx, *req.Value, group.ID); err != nil {
			return nil, err
		}
		// 分组编码是幻灯片的引用键，直接改值会让既有幻灯片指向不存在的分组，
		// 因此只要还有幻灯片在用就拒绝改码（改标签、排序、状态不受影响）。
		inUse, err := s.Client.Slide.Query().
			Where(slide.GroupCodeEQ(group.Value)).
			Exist(ctx)
		if err != nil {
			return nil, fmt.Errorf("检查幻灯片引用失败: %w", err)
		}
		if inUse {
			return nil, ErrSlideGroupValueInUse
		}
		builder.SetValue(*req.Value)
	}
	if req.Sort != nil {
		builder.SetSort(*req.Sort)
	}
	if req.Status != nil {
		builder.SetStatus(*req.Status)
	}
	if req.Remark != nil {
		builder.SetRemark(*req.Remark)
	}

	updated, err := builder.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, ErrSlideGroupValueExists
		}
		return nil, fmt.Errorf("更新幻灯片分组失败: %w", err)
	}

	count, err := s.Client.Slide.Query().
		Where(slide.GroupCodeEQ(updated.Value)).
		Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("统计分组幻灯片数量失败: %w", err)
	}

	return toSlideGroupResponse(updated, count), nil
}

// Delete 删除分组(分组下仍有幻灯片时拒绝)
func (s *SlideGroupService) Delete(ctx context.Context, id int) error {
	group, err := s.groupByID(ctx, id)
	if err != nil {
		return err
	}

	count, err := s.Client.Slide.Query().
		Where(slide.GroupCodeEQ(group.Value)).
		Count(ctx)
	if err != nil {
		return fmt.Errorf("统计分组幻灯片数量失败: %w", err)
	}
	if count > 0 {
		return ErrSlideGroupInUse
	}

	if err := s.Client.DictData.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("删除幻灯片分组失败: %w", err)
	}

	return nil
}

// groupByID 按ID查询组内字典项,非 slide_group 类型的字典项视为不存在
func (s *SlideGroupService) groupByID(ctx context.Context, id int) (*ent.DictData, error) {
	group, err := s.Client.DictData.Query().
		Where(dictdata.IDEQ(id), dictdata.DictTypeEQ(SlideGroupDictType)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrSlideGroupNotFound
		}
		return nil, fmt.Errorf("查询幻灯片分组失败: %w", err)
	}
	return group, nil
}

// ensureValueAvailable 校验同类型下分组编码唯一,excludeID>0 时排除自身
func (s *SlideGroupService) ensureValueAvailable(ctx context.Context, value string, excludeID int) error {
	query := s.Client.DictData.Query().
		Where(dictdata.DictTypeEQ(SlideGroupDictType), dictdata.ValueEQ(value))
	if excludeID > 0 {
		query = query.Where(dictdata.IDNEQ(excludeID))
	}
	exists, err := query.Exist(ctx)
	if err != nil {
		return fmt.Errorf("检查分组编码失败: %w", err)
	}
	if exists {
		return ErrSlideGroupValueExists
	}
	return nil
}

// nextSort 未指定排序时追加到末尾(现有最大 sort + 10)
func (s *SlideGroupService) nextSort(ctx context.Context) (int, error) {
	groups, err := s.Client.DictData.Query().
		Where(dictdata.DictTypeEQ(SlideGroupDictType)).
		Order(ent.Desc(dictdata.FieldSort), ent.Desc(dictdata.FieldID)).
		Limit(1).
		All(ctx)
	if err != nil {
		return 0, fmt.Errorf("查询分组排序失败: %w", err)
	}
	if len(groups) == 0 {
		return 0, nil
	}
	return groups[0].Sort + 10, nil
}

// slideCounts 一次聚合出每个分组编码下的幻灯片数量
func (s *SlideGroupService) slideCounts(ctx context.Context) (map[string]int, error) {
	var rows []struct {
		GroupCode  string `json:"group_code"`
		SlideCount int    `json:"slide_count"`
	}
	if err := s.Client.Slide.Query().
		GroupBy(slide.FieldGroupCode).
		Aggregate(ent.As(ent.Count(), "slide_count")).
		Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("统计分组幻灯片数量失败: %w", err)
	}

	counts := make(map[string]int, len(rows))
	for _, row := range rows {
		counts[row.GroupCode] = row.SlideCount
	}
	return counts, nil
}

func validateSlideGroupValue(value string) error {
	if !slideGroupValuePattern.MatchString(value) {
		return ErrSlideGroupValueInvalid
	}
	return nil
}

func toSlideGroupResponse(group *ent.DictData, slideCount int) *SlideGroupResponse {
	return &SlideGroupResponse{
		ID:         group.ID,
		Label:      group.Label,
		Value:      group.Value,
		Sort:       group.Sort,
		Status:     group.Status,
		Remark:     group.Remark,
		SlideCount: slideCount,
		CreatedAt:  group.CreatedAt,
		UpdatedAt:  group.UpdatedAt,
	}
}
