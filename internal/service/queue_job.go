package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	cmfqueue "github.com/wuwuseo/cmf/queue"

	"go-admin/internal/global"
	adminqueue "go-admin/internal/queue"
)

// 服务层队列错误，handler 侧据此映射响应错误码
var (
	// ErrQueueUnavailable 队列组件未就绪（未初始化或已停止）
	ErrQueueUnavailable = errors.New("任务队列不可用")
	// ErrQueueUnknownHandler 任务处理器未注册
	ErrQueueUnknownHandler = errors.New("队列任务未注册")
	// ErrQueueInvalidPayload 载荷不是合法 JSON
	ErrQueueInvalidPayload = errors.New("任务载荷必须是合法 JSON")
	// ErrQueueUnknownQueue 目标队列未在配置中声明（无人消费）
	ErrQueueUnknownQueue = errors.New("目标队列未配置")
	// ErrQueueInvalidQueueName 队列名包含非法字符（会拼入 Redis 键，必须走白名单）
	ErrQueueInvalidQueueName = errors.New("队列名包含非法字符")
)

// QueueJobService 任务队列管理服务：封装 Inspector 的观测/管理操作与 Client 的手动入队
type QueueJobService struct{}

// NewQueueJobService 创建任务队列管理服务
func NewQueueJobService() *QueueJobService {
	return &QueueJobService{}
}

// QueueTaskResponse 队列任务响应
type QueueTaskResponse struct {
	ID             string     `json:"id"`              // 任务 ID
	Queue          string     `json:"queue"`           // 所属队列
	Name           string     `json:"name"`            // 处理器名
	Payload        string     `json:"payload"`         // 载荷（JSON 文本）
	State          string     `json:"state"`           // 状态
	MaxRetry       int        `json:"max_retry"`       // 最大重试次数
	Attempt        int        `json:"attempt"`         // 已重试次数
	LastError      string     `json:"last_error"`      // 最近失败原因
	LastFailedAt   *time.Time `json:"last_failed_at"`  // 最近失败时间
	NextProcessAt  *time.Time `json:"next_process_at"` // 下次执行时间
	CompletedAt    *time.Time `json:"completed_at"`    // 完成时间
	TimeoutSeconds float64    `json:"timeout_seconds"` // 单任务超时（秒）
	IsOrphaned     bool       `json:"is_orphaned"`     // 是否孤儿任务
}

// QueueStatsResponse 队列统计响应
type QueueStatsResponse struct {
	Queue       string `json:"queue"`
	Size        int    `json:"size"`
	Pending     int    `json:"pending"`
	Active      int    `json:"active"`
	Scheduled   int    `json:"scheduled"`
	Retry       int    `json:"retry"`
	Archived    int    `json:"archived"`
	Completed   int    `json:"completed"`
	MemoryUsage int64  `json:"memory_usage"`
	Processed   int    `json:"processed"`
	Failed      int    `json:"failed"`
}

// ListQueueTasksRequest 任务列表查询参数
type ListQueueTasksRequest struct {
	ListRequest
	State string `query:"state" validate:"required,oneof=pending active scheduled retry archived completed"`
	Queue string `query:"queue" validate:"omitempty,max=64"`
}

// EnqueueTaskRequest 手动入队请求
type EnqueueTaskRequest struct {
	Name         string `json:"name" validate:"required,max=191"`
	Payload      string `json:"payload" validate:"omitempty,max=10240"`
	Queue        string `json:"queue" validate:"omitempty,max=64"`
	DelaySeconds int    `json:"delay_seconds" validate:"omitempty,min=0,max=86400"`
	MaxRetry     *int   `json:"max_retry" validate:"omitempty,min=0,max=100"`
}

// EnqueueTaskResponse 手动入队响应
type EnqueueTaskResponse struct {
	TaskID string `json:"task_id"`
}

// inspector 返回队列管理端句柄；未就绪时报错
func (s *QueueJobService) inspector() (*cmfqueue.Inspector, error) {
	inspector := global.QueueInspector.Load()
	if inspector == nil {
		return nil, ErrQueueUnavailable
	}
	return inspector, nil
}

// normalizeQueueName 规范化并校验队列名：去首尾空白、空值回退 default，
// 再按字符集白名单校验（队列名会拼入 asynq 的 Redis 键）。
func normalizeQueueName(name string) (string, error) {
	queueName := strings.TrimSpace(name)
	if queueName == "" {
		queueName = "default"
	}
	if !adminqueue.IsValidQueueName(queueName) {
		return "", ErrQueueInvalidQueueName
	}
	return queueName, nil
}

// ListTasks 按状态分页列出任务
func (s *QueueJobService) ListTasks(ctx context.Context, req *ListQueueTasksRequest) (*ListResponse[*QueueTaskResponse], error) {
	queueName, err := normalizeQueueName(req.Queue)
	if err != nil {
		return nil, err
	}
	insp, err := s.inspector()
	if err != nil {
		return nil, err
	}
	items, total, err := insp.ListTasks(req.State, queueName, req.Page, req.PageSize)
	if err != nil {
		return nil, err
	}
	data := make([]*QueueTaskResponse, 0, len(items))
	for i := range items {
		data = append(data, newQueueTaskResponse(&items[i]))
	}
	return &ListResponse[*QueueTaskResponse]{
		Total: total,
		Page:  req.Page,
		Data:  data,
	}, nil
}

// GetTask 查询单个任务详情
func (s *QueueJobService) GetTask(ctx context.Context, queue, taskID string) (*QueueTaskResponse, error) {
	queueName, err := normalizeQueueName(queue)
	if err != nil {
		return nil, err
	}
	insp, err := s.inspector()
	if err != nil {
		return nil, err
	}
	info, err := insp.GetTask(queueName, taskID)
	if err != nil {
		return nil, err
	}
	return newQueueTaskResponse(&info), nil
}

// RetryTask 立即执行一个 scheduled/retry/archived 状态的任务
func (s *QueueJobService) RetryTask(ctx context.Context, queue, taskID string) error {
	queueName, err := normalizeQueueName(queue)
	if err != nil {
		return err
	}
	insp, err := s.inspector()
	if err != nil {
		return err
	}
	return insp.Retry(queueName, taskID)
}

// CancelTask 取消执行中的任务
func (s *QueueJobService) CancelTask(ctx context.Context, taskID string) error {
	insp, err := s.inspector()
	if err != nil {
		return err
	}
	return insp.Cancel(taskID)
}

// DeleteTask 删除任务
func (s *QueueJobService) DeleteTask(ctx context.Context, queue, taskID string) error {
	queueName, err := normalizeQueueName(queue)
	if err != nil {
		return err
	}
	insp, err := s.inspector()
	if err != nil {
		return err
	}
	return insp.Delete(queueName, taskID)
}

// Enqueue 手动入队一个任务：校验处理器已注册、载荷为合法 JSON、目标队列已配置
func (s *QueueJobService) Enqueue(ctx context.Context, req *EnqueueTaskRequest) (*EnqueueTaskResponse, error) {
	client, server := global.QueueClient.Load(), global.QueueServer.Load()
	if client == nil || server == nil {
		return nil, ErrQueueUnavailable
	}
	if !server.Has(req.Name) {
		return nil, ErrQueueUnknownHandler
	}
	payload := []byte(req.Payload)
	if len(payload) > 0 && !json.Valid(payload) {
		return nil, ErrQueueInvalidPayload
	}
	queueName, err := normalizeQueueName(req.Queue)
	if err != nil {
		return nil, err
	}
	known := false
	for _, q := range adminqueue.ConfiguredQueues() {
		if q == queueName {
			known = true
			break
		}
	}
	if !known {
		return nil, ErrQueueUnknownQueue
	}

	opts := []cmfqueue.EnqueueOption{cmfqueue.WithQueue(queueName)}
	if req.DelaySeconds > 0 {
		opts = append(opts, cmfqueue.WithDelay(time.Duration(req.DelaySeconds)*time.Second))
	}
	if req.MaxRetry != nil {
		opts = append(opts, cmfqueue.WithMaxRetry(*req.MaxRetry))
	}

	taskID, err := client.Enqueue(ctx, req.Name, payload, opts...)
	if err != nil {
		return nil, err
	}
	return &EnqueueTaskResponse{TaskID: taskID}, nil
}

// Stats 返回所有队列的统计快照
func (s *QueueJobService) Stats(ctx context.Context) ([]*QueueStatsResponse, error) {
	insp, err := s.inspector()
	if err != nil {
		return nil, err
	}
	stats, err := insp.AllQueueStats(ctx)
	if err != nil {
		return nil, err
	}
	data := make([]*QueueStatsResponse, 0, len(stats))
	for i := range stats {
		si := stats[i]
		data = append(data, &QueueStatsResponse{
			Queue:       si.Queue,
			Size:        si.Size,
			Pending:     si.Pending,
			Active:      si.Active,
			Scheduled:   si.Scheduled,
			Retry:       si.Retry,
			Archived:    si.Archived,
			Completed:   si.Completed,
			MemoryUsage: si.MemoryUsage,
			Processed:   si.Processed,
			Failed:      si.Failed,
		})
	}
	return data, nil
}

// Handlers 返回已注册的队列任务处理器名列表
func (s *QueueJobService) Handlers(ctx context.Context) ([]string, error) {
	server := global.QueueServer.Load()
	if server == nil {
		return nil, ErrQueueUnavailable
	}
	return server.RegisteredTasks(), nil
}

// newQueueTaskResponse 将 cmf/queue TaskInfo 转换为响应结构
func newQueueTaskResponse(info *cmfqueue.TaskInfo) *QueueTaskResponse {
	resp := &QueueTaskResponse{
		ID:             info.ID,
		Queue:          info.Queue,
		Name:           info.Name,
		Payload:        string(info.Payload),
		State:          info.State,
		MaxRetry:       info.MaxRetry,
		Attempt:        info.Attempt,
		LastError:      info.LastError,
		TimeoutSeconds: info.Timeout.Seconds(),
		IsOrphaned:     info.IsOrphaned,
	}
	resp.LastFailedAt = timePtr(info.LastFailedAt)
	resp.NextProcessAt = timePtr(info.NextProcessAt)
	resp.CompletedAt = timePtr(info.CompletedAt)
	return resp
}

// timePtr 零值时间转 nil 指针
func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
