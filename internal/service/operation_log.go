package service

import (
	"context"
	"errors"
	"fmt"
	"go-admin/internal/ent"
	"go-admin/internal/ent/operationlog"
	"go-admin/internal/global"
	"go-admin/internal/utils"
	"log"
	"time"
)

var (
	ErrOperationLogNotFound = errors.New("操作日志不存在")
)

// OperationLogServiceInterface 操作日志服务接口
type OperationLogServiceInterface interface {
	Log(ctx context.Context, entry *LogEntry) error
	List(ctx context.Context, req *ListOperationLogsRequest) (*ListOperationLogsResponse, error)
	Get(ctx context.Context, id int) (*OperationLogResponse, error)
}

// OperationLogService 操作日志服务
type OperationLogService struct {
	*CRUDBase
}

// NewOperationLogService 创建操作日志服务实例
func NewOperationLogService() *OperationLogService {
	return &OperationLogService{
		CRUDBase: &CRUDBase{Client: global.ORM},
	}
}

// LogEntry 日志记录条目
type LogEntry struct {
	UserUID     string
	Username    string
	Module      string
	Action      string // "创建", "更新", "删除", "上传", "启用", "禁用" 等
	Description string
	Method      string
	Path        string
	IP          string
	UserAgent   string
	RequestBody string
	StatusCode  int
	DurationMs  int64
}

// ListOperationLogsRequest 操作日志列表请求
type ListOperationLogsRequest struct {
	PaginationRequest
	Username  string `query:"username"`
	Module    string `query:"module"`
	Action    string `query:"action"`
	StartTime string `query:"start_time"` // RFC3339 格式
	EndTime   string `query:"end_time"`   // RFC3339 格式
}

// OperationLogResponse 操作日志响应
type OperationLogResponse struct {
	ID          int       `json:"id"`
	UserID      int       `json:"user_id"`
	Username    string    `json:"username"`
	Module      string    `json:"module"`
	Action      string    `json:"action"`
	Description string    `json:"description"`
	Method      string    `json:"method"`
	Path        string    `json:"path"`
	IP          string    `json:"ip"`
	UserAgent   string    `json:"user_agent"`
	RequestBody string    `json:"request_body"`
	StatusCode  int       `json:"status_code"`
	DurationMs  int64     `json:"duration_ms"`
	CreatedAt   time.Time `json:"created_at"`
}

// ListOperationLogsResponse 操作日志列表响应
type ListOperationLogsResponse = ListResponse[*OperationLogResponse]

// Log 异步记录操作日志
func (s *OperationLogService) Log(ctx context.Context, entry *LogEntry) error {
	go func() {
		bgCtx := context.Background()
		builder := s.Client.OperationLog.Create().
			SetUserID(0).
			SetUsername(entry.Username).
			SetModule(entry.Module).
			SetAction(entry.Action).
			SetMethod(entry.Method).
			SetPath(entry.Path).
			SetIP(entry.IP).
			SetStatusCode(entry.StatusCode).
			SetDurationMs(entry.DurationMs)

		if entry.Description != "" {
			builder.SetDescription(entry.Description)
		}
		if entry.UserAgent != "" {
			builder.SetUserAgent(entry.UserAgent)
		}
		if entry.RequestBody != "" {
			builder.SetRequestBody(entry.RequestBody)
		}

		if _, err := builder.Save(bgCtx); err != nil {
			log.Printf("[OperationLog] 写入操作日志失败: %v", err)
		}
	}()
	return nil
}

// List 获取操作日志列表
func (s *OperationLogService) List(ctx context.Context, req *ListOperationLogsRequest) (*ListOperationLogsResponse, error) {
	req.Page, req.PageSize = utils.NormalizePagination(req.Page, req.PageSize)

	query := s.Client.OperationLog.Query()

	// 筛选条件
	if req.Username != "" {
		query = query.Where(operationlog.UsernameContains(req.Username))
	}
	if req.Module != "" {
		query = query.Where(operationlog.ModuleEQ(req.Module))
	}
	if req.Action != "" {
		query = query.Where(operationlog.ActionEQ(req.Action))
	}
	if req.StartTime != "" {
		t, err := time.Parse(time.RFC3339, req.StartTime)
		if err == nil {
			query = query.Where(operationlog.CreatedAtGTE(t))
		}
	}
	if req.EndTime != "" {
		t, err := time.Parse(time.RFC3339, req.EndTime)
		if err == nil {
			query = query.Where(operationlog.CreatedAtLTE(t))
		}
	}

	// 获取总数
	total, err := query.Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取操作日志总数失败: %w", err)
	}

	// 分页查询，按创建时间倒序
	query = query.Order(ent.Desc(operationlog.FieldCreatedAt))
	if req.PageSize > 0 {
		offset := (req.Page - 1) * req.PageSize
		query = query.Offset(offset).Limit(req.PageSize)
	}
	logs, err := query.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取操作日志列表失败: %w", err)
	}

	return BuildList(logs, total, req.Page, toOperationLogResponse), nil
}

// Get 获取操作日志详情
func (s *OperationLogService) Get(ctx context.Context, id int) (*OperationLogResponse, error) {
	return GetByID(ctx,
		func(ctx context.Context) (*ent.OperationLog, error) {
			return s.Client.OperationLog.Query().Where(operationlog.IDEQ(id)).Only(ctx)
		}, toOperationLogResponse, ErrOperationLogNotFound, "操作日志")
}

// toOperationLogResponse 将Ent实体转换为响应格式
func toOperationLogResponse(l *ent.OperationLog) *OperationLogResponse {
	return &OperationLogResponse{
		ID:          l.ID,
		UserID:      l.UserID,
		Username:    l.Username,
		Module:      l.Module,
		Action:      l.Action,
		Description: l.Description,
		Method:      l.Method,
		Path:        l.Path,
		IP:          l.IP,
		UserAgent:   l.UserAgent,
		RequestBody: l.RequestBody,
		StatusCode:  l.StatusCode,
		DurationMs:  l.DurationMs,
		CreatedAt:   l.CreatedAt,
	}
}
