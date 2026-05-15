package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go-admin/internal/cron"
	"go-admin/internal/ent"
	"go-admin/internal/ent/cronjob"
	"go-admin/internal/global"
	"go-admin/internal/tasks"
	"go-admin/internal/utils"

	robfigcron "github.com/robfig/cron/v3"
)

var (
	// 定时任务相关错误
	ErrCronJobNotFound     = errors.New("定时任务不存在")
	ErrCronJobNameExists   = errors.New("定时任务名称已存在")
	ErrCronJobInvalidExpr  = errors.New("无效的cron表达式")
	ErrCronJobInvalidType  = errors.New("无效的任务类型")
	ErrCronJobTaskNotFound = errors.New("Go task未注册")
	ErrCronJobLogNotFound  = errors.New("日志不存在")
)

// CronJobServiceInterface 定时任务服务接口
type CronJobServiceInterface interface {
	Create(ctx context.Context, req *CreateCronJobRequest) (*CronJobResponse, error)
	Update(ctx context.Context, id int, req *UpdateCronJobRequest) error
	Delete(ctx context.Context, id int) error
	Get(ctx context.Context, id int) (*CronJobResponse, error)
	List(ctx context.Context, req *ListCronJobsRequest) (*ListCronJobsResponse, error)
	Enable(ctx context.Context, id int) error
	Disable(ctx context.Context, id int) error
	RunNow(ctx context.Context, id int) error
	GetLogs(ctx context.Context, id int) ([]cron.LogFileInfo, error)
	GetLogContent(ctx context.Context, id int, filename string) (string, error)
	TailLog(ctx context.Context, id int, lines int) (string, error)
}

// CronJobService 定时任务服务
type CronJobService struct {
	*CRUDBase
	manager *cron.CronManager
}

var _ CRUD[CreateCronJobRequest, UpdateCronJobRequest, ListCronJobsRequest, CronJobResponse] = (*CronJobService)(nil)

// NewCronJobService 创建定时任务服务
func NewCronJobService(manager *cron.CronManager) *CronJobService {
	return &CronJobService{
		CRUDBase: &CRUDBase{Client: global.ORM},
		manager:  manager,
	}
}

// CreateCronJobRequest 创建定时任务请求
type CreateCronJobRequest struct {
	Name        string `json:"name" validate:"required,max=100"`
	Description string `json:"description" validate:"max=500"`
	Type        string `json:"type" validate:"required,oneof=shell curl task"`
	Expression  string `json:"expression" validate:"required"`
	Command     string `json:"command" validate:"required"`
	Enabled     *bool  `json:"enabled"`
}

// UpdateCronJobRequest 更新定时任务请求
type UpdateCronJobRequest struct {
	Name        *string `json:"name" validate:"omitempty,max=100"`
	Description *string `json:"description" validate:"omitempty,max=500"`
	Type        *string `json:"type" validate:"omitempty,oneof=shell curl task"`
	Expression  *string `json:"expression"`
	Command     *string `json:"command"`
}

// ListCronJobsRequest 定时任务列表请求
type ListCronJobsRequest struct {
	ListRequest
	Type    string `query:"type"`
	Enabled *bool  `query:"enabled"`
}

// CronJobResponse 定时任务响应
type CronJobResponse struct {
	ID          int        `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Type        string     `json:"type"`
	Expression  string     `json:"expression"`
	Command     string     `json:"command"`
	Enabled     bool       `json:"enabled"`
	LastRunAt   *time.Time `json:"last_run_at"`
	NextRunAt   *time.Time `json:"next_run_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// ListCronJobsResponse 定时任务列表响应
type ListCronJobsResponse = ListResponse[*CronJobResponse]

// Create 创建定时任务
func (s *CronJobService) Create(ctx context.Context, req *CreateCronJobRequest) (*CronJobResponse, error) {
	// 验证 cron 表达式
	parser := robfigcron.NewParser(robfigcron.Second | robfigcron.Minute | robfigcron.Hour | robfigcron.Dom | robfigcron.Month | robfigcron.Dow)
	if _, err := parser.Parse(req.Expression); err != nil {
		return nil, ErrCronJobInvalidExpr
	}

	// 如果 type=task，验证 task 名称已注册
	if req.Type == "task" {
		if _, ok := tasks.Get(req.Command); !ok {
			return nil, ErrCronJobTaskNotFound
		}
	}

	// 检查名称唯一性
	exists, err := s.Client.CronJob.Query().
		Where(cronjob.NameEQ(req.Name)).
		Exist(ctx)
	if err != nil {
		return nil, fmt.Errorf("检查任务名称失败: %w", err)
	}
	if exists {
		return nil, ErrCronJobNameExists
	}

	// 创建 DB 记录
	builder := s.Client.CronJob.Create().
		SetName(req.Name).
		SetType(cronjob.Type(req.Type)).
		SetExpression(req.Expression).
		SetCommand(req.Command)

	if req.Description != "" {
		builder.SetDescription(req.Description)
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	builder.SetEnabled(enabled)

	job, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("创建定时任务失败: %w", err)
	}

	// 如果 enabled，注册到调度器
	if enabled {
		if addErr := s.manager.AddJob(job); addErr != nil {
			// 任务已创建到 DB，记录日志但不返回错误
			fmt.Printf("注册定时任务到调度器失败: %v\n", addErr)
		}
	}

	return s.toCronJobResponse(job), nil
}

// Update 更新定时任务
func (s *CronJobService) Update(ctx context.Context, id int, req *UpdateCronJobRequest) error {
	job, err := s.Client.CronJob.Query().
		Where(cronjob.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrCronJobNotFound
		}
		return fmt.Errorf("查询定时任务失败: %w", err)
	}

	update := s.Client.CronJob.UpdateOne(job)

	if req.Name != nil {
		// 检查名称唯一性
		exists, err := s.Client.CronJob.Query().
			Where(cronjob.NameEQ(*req.Name), cronjob.IDNEQ(id)).
			Exist(ctx)
		if err != nil {
			return fmt.Errorf("检查任务名称失败: %w", err)
		}
		if exists {
			return ErrCronJobNameExists
		}
		update.SetName(*req.Name)
	}

	if req.Description != nil {
		update.SetDescription(*req.Description)
	}

	newExpression := job.Expression
	if req.Expression != nil {
		parser := robfigcron.NewParser(robfigcron.Second | robfigcron.Minute | robfigcron.Hour | robfigcron.Dom | robfigcron.Month | robfigcron.Dow)
		if _, err := parser.Parse(*req.Expression); err != nil {
			return ErrCronJobInvalidExpr
		}
		update.SetExpression(*req.Expression)
		newExpression = *req.Expression
	}

	newType := string(job.Type)
	if req.Type != nil {
		update.SetType(cronjob.Type(*req.Type))
		newType = *req.Type
	}

	newCommand := job.Command
	if req.Command != nil {
		update.SetCommand(*req.Command)
		newCommand = *req.Command
	}

	// 如果 type=task，验证 task 名称已注册
	if newType == "task" {
		if _, ok := tasks.Get(newCommand); !ok {
			return ErrCronJobTaskNotFound
		}
	}

	if _, err := update.Save(ctx); err != nil {
		return fmt.Errorf("更新定时任务失败: %w", err)
	}

	// 如果任务是启用的，需要重新注册到调度器
	if job.Enabled {
		s.manager.RemoveJob(id)
		// 重新查询更新后的 job
		updatedJob, err := s.Client.CronJob.Query().
			Where(cronjob.IDEQ(id)).
			Only(ctx)
		if err == nil {
			_ = s.manager.AddJob(updatedJob)
		}
	}

	_ = newExpression // 已在 update builder 中使用

	return nil
}

// Delete 删除定时任务
func (s *CronJobService) Delete(ctx context.Context, id int) error {
	s.manager.RemoveJob(id)

	return DeleteByID(ctx,
		func(ctx context.Context) (*ent.CronJob, error) {
			return s.Client.CronJob.Query().Where(cronjob.IDEQ(id)).Only(ctx)
		},
		func(e *ent.CronJob) error {
			return s.Client.CronJob.DeleteOne(e).Exec(ctx)
		},
		ErrCronJobNotFound, "定时任务")
}

// Get 获取定时任务详情
func (s *CronJobService) Get(ctx context.Context, id int) (*CronJobResponse, error) {
	return GetByID(ctx,
		func(ctx context.Context) (*ent.CronJob, error) {
			return s.Client.CronJob.Query().Where(cronjob.IDEQ(id)).Only(ctx)
		}, s.toCronJobResponse, ErrCronJobNotFound, "定时任务")
}

// List 获取定时任务列表
func (s *CronJobService) List(ctx context.Context, req *ListCronJobsRequest) (*ListCronJobsResponse, error) {
	req.Page, req.PageSize = utils.NormalizePagination(req.Page, req.PageSize)

	query := s.Client.CronJob.Query()

	// 关键字搜索
	if req.Keyword != "" {
		query = query.Where(
			cronjob.Or(
				cronjob.NameContains(req.Keyword),
				cronjob.DescriptionContains(req.Keyword),
				cronjob.CommandContains(req.Keyword),
			),
		)
	}

	// 类型筛选
	if req.Type != "" {
		query = query.Where(cronjob.TypeEQ(cronjob.Type(req.Type)))
	}

	// 启用状态筛选
	if req.Enabled != nil {
		query = query.Where(cronjob.EnabledEQ(*req.Enabled))
	}

	// 获取总数
	total, err := query.Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取定时任务总数失败: %w", err)
	}

	// 分页查询
	query = query.Order(ent.Desc(cronjob.FieldCreatedAt))
	if req.PageSize > 0 {
		offset := (req.Page - 1) * req.PageSize
		query = query.Offset(offset).Limit(req.PageSize)
	}
	jobs, err := query.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取定时任务列表失败: %w", err)
	}

	return BuildList(jobs, total, req.Page, s.toCronJobResponse), nil
}

// Enable 启用定时任务
func (s *CronJobService) Enable(ctx context.Context, id int) error {
	job, err := s.Client.CronJob.Query().
		Where(cronjob.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrCronJobNotFound
		}
		return fmt.Errorf("查询定时任务失败: %w", err)
	}

	if job.Enabled {
		return nil
	}

	if _, err := s.Client.CronJob.UpdateOne(job).
		SetEnabled(true).
		Save(ctx); err != nil {
		return fmt.Errorf("启用定时任务失败: %w", err)
	}

	// 重新查询并注册到调度器
	updatedJob, err := s.Client.CronJob.Query().
		Where(cronjob.IDEQ(id)).
		Only(ctx)
	if err == nil {
		_ = s.manager.AddJob(updatedJob)
	}

	return nil
}

// Disable 禁用定时任务
func (s *CronJobService) Disable(ctx context.Context, id int) error {
	job, err := s.Client.CronJob.Query().
		Where(cronjob.IDEQ(id)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return ErrCronJobNotFound
		}
		return fmt.Errorf("查询定时任务失败: %w", err)
	}

	if !job.Enabled {
		return nil
	}

	if _, err := s.Client.CronJob.UpdateOne(job).
		SetEnabled(false).
		ClearNextRunAt().
		Save(ctx); err != nil {
		return fmt.Errorf("禁用定时任务失败: %w", err)
	}

	// 从调度器移除
	s.manager.RemoveJob(id)

	return nil
}

// RunNow 立即执行定时任务
func (s *CronJobService) RunNow(ctx context.Context, id int) error {
	// 验证任务存在
	exists, err := s.Client.CronJob.Query().
		Where(cronjob.IDEQ(id)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("查询定时任务失败: %w", err)
	}
	if !exists {
		return ErrCronJobNotFound
	}

	return s.manager.RunNow(id)
}

// GetLogs 获取定时任务日志列表
func (s *CronJobService) GetLogs(ctx context.Context, id int) ([]cron.LogFileInfo, error) {
	// 验证任务存在
	exists, err := s.Client.CronJob.Query().
		Where(cronjob.IDEQ(id)).
		Exist(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询定时任务失败: %w", err)
	}
	if !exists {
		return nil, ErrCronJobNotFound
	}

	return s.manager.GetJobLogger().ListLogs(id)
}

// GetLogContent 获取指定日志文件内容
func (s *CronJobService) GetLogContent(ctx context.Context, id int, filename string) (string, error) {
	// 验证任务存在
	exists, err := s.Client.CronJob.Query().
		Where(cronjob.IDEQ(id)).
		Exist(ctx)
	if err != nil {
		return "", fmt.Errorf("查询定时任务失败: %w", err)
	}
	if !exists {
		return "", ErrCronJobNotFound
	}

	return s.manager.GetJobLogger().ReadLog(id, filename)
}

// TailLog 读取最新日志的最后 N 行
func (s *CronJobService) TailLog(ctx context.Context, id int, lines int) (string, error) {
	// 验证任务存在
	exists, err := s.Client.CronJob.Query().
		Where(cronjob.IDEQ(id)).
		Exist(ctx)
	if err != nil {
		return "", fmt.Errorf("查询定时任务失败: %w", err)
	}
	if !exists {
		return "", ErrCronJobNotFound
	}

	return s.manager.GetJobLogger().TailLog(id, lines)
}

// toCronJobResponse 转换为响应格式
func (s *CronJobService) toCronJobResponse(job *ent.CronJob) *CronJobResponse {
	return &CronJobResponse{
		ID:          job.ID,
		Name:        job.Name,
		Description: job.Description,
		Type:        string(job.Type),
		Expression:  job.Expression,
		Command:     job.Command,
		Enabled:     job.Enabled,
		LastRunAt:   job.LastRunAt,
		NextRunAt:   job.NextRunAt,
		CreatedAt:   job.CreatedAt,
		UpdatedAt:   job.UpdatedAt,
	}
}
