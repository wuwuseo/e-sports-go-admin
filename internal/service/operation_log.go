package service

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/operationlog"
	"go-admin/internal/ent/predicate"
	"go-admin/internal/global"
	"go-admin/internal/utils"

	"github.com/wuwuseo/cmf/log"
	"go.uber.org/zap"
)

var (
	ErrOperationLogNotFound  = errors.New("操作日志不存在")
	ErrOperationLogStopped   = errors.New("操作日志服务已停止")
	ErrOperationLogQueueFull = errors.New("操作日志队列已满")
)

const (
	defaultOperationLogQueueSize            = 1000
	defaultOperationLogWorkers              = 5
	defaultOperationLogMaxRetries           = 3
	defaultOperationLogWriteTimeout         = 5 * time.Second
	defaultOperationLogEnqueueTimeout       = 100 * time.Millisecond
	defaultOperationLogMonitorInterval      = 30 * time.Second
	maxOperationLogRequestBodyLen           = 65535
	compressedOperationLogRequestBodyPrefix = "gzip:v1:"
)

var defaultOperationLogService atomic.Pointer[OperationLogService]

// OperationLogServiceInterface 操作日志服务接口
type OperationLogServiceInterface interface {
	Log(ctx context.Context, entry *LogEntry) error
	List(ctx context.Context, op *EffectiveDataScope, req *ListOperationLogsRequest) (*ListOperationLogsResponse, error)
	Get(ctx context.Context, op *EffectiveDataScope, id int) (*OperationLogResponse, error)
}

// OperationLogOptions 操作日志服务配置
type OperationLogOptions struct {
	QueueSize       int
	Workers         int
	MaxRetries      int
	WriteTimeout    time.Duration
	EnqueueTimeout  time.Duration
	MonitorInterval time.Duration
}

// LogMetrics 操作日志指标
type LogMetrics struct {
	totalLogs      atomic.Int64
	successLogs    atomic.Int64
	failedLogs     atomic.Int64
	retriedLogs    atomic.Int64
	droppedLogs    atomic.Int64
	queueSize      atomic.Int64
	processingTime atomic.Int64
}

// OperationLogService 操作日志服务
type OperationLogService struct {
	*CRUDBase
	logQueue       chan *LogEntry
	workers        int
	maxRetries     int
	writeTimeout   time.Duration
	enqueueTimeout time.Duration
	monitorEvery   time.Duration
	metrics        *LogMetrics
	enqueueMu      sync.RWMutex
	wg             sync.WaitGroup
	closeOnce      sync.Once
	stopMonitor    chan struct{}
	stopped        atomic.Bool
}

// NewOperationLogService 创建操作日志服务实例
func NewOperationLogService() *OperationLogService {
	return NewOperationLogServiceWithOptions(OperationLogOptions{
		QueueSize:       defaultOperationLogQueueSize,
		Workers:         defaultOperationLogWorkers,
		MaxRetries:      defaultOperationLogMaxRetries,
		WriteTimeout:    defaultOperationLogWriteTimeout,
		EnqueueTimeout:  defaultOperationLogEnqueueTimeout,
		MonitorInterval: defaultOperationLogMonitorInterval,
	})
}

// NewOperationLogServiceWithOptions 按配置创建操作日志服务实例
func NewOperationLogServiceWithOptions(opts OperationLogOptions) *OperationLogService {
	opts = normalizeOperationLogOptions(opts)
	s := &OperationLogService{
		CRUDBase:       &CRUDBase{Client: global.ORM},
		logQueue:       make(chan *LogEntry, opts.QueueSize),
		workers:        opts.Workers,
		maxRetries:     opts.MaxRetries,
		writeTimeout:   opts.WriteTimeout,
		enqueueTimeout: opts.EnqueueTimeout,
		monitorEvery:   opts.MonitorInterval,
		metrics:        &LogMetrics{},
		stopMonitor:    make(chan struct{}),
	}
	s.startWorkers()
	if s.monitorEvery > 0 {
		s.wg.Add(1)
		go s.monitorQueue()
	}
	defaultOperationLogService.Store(s)
	return s
}

func normalizeOperationLogOptions(opts OperationLogOptions) OperationLogOptions {
	if opts.QueueSize <= 0 {
		opts.QueueSize = defaultOperationLogQueueSize
	}
	if opts.Workers < 0 {
		opts.Workers = 0
	}
	if opts.MaxRetries <= 0 {
		opts.MaxRetries = defaultOperationLogMaxRetries
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = defaultOperationLogWriteTimeout
	}
	if opts.EnqueueTimeout <= 0 {
		opts.EnqueueTimeout = defaultOperationLogEnqueueTimeout
	}
	if opts.MonitorInterval < 0 {
		opts.MonitorInterval = 0
	}
	if opts.MonitorInterval == 0 {
		return opts
	}
	return opts
}

func (s *OperationLogService) startWorkers() {
	for range s.workers {
		s.wg.Add(1)
		go s.worker()
	}
}

func (s *OperationLogService) worker() {
	defer s.wg.Done()
	for entry := range s.logQueue {
		s.processLog(entry)
	}
}

func (s *OperationLogService) monitorQueue() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.monitorEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.printMetrics()
		case <-s.stopMonitor:
			return
		}
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
	s.enqueueMu.RLock()
	defer s.enqueueMu.RUnlock()
	if s.stopped.Load() {
		return ErrOperationLogStopped
	}
	s.metrics.totalLogs.Add(1)
	timer := time.NewTimer(s.enqueueTimeout)
	defer timer.Stop()

	select {
	case s.logQueue <- entry:
		s.metrics.queueSize.Add(1)
		return nil
	case <-ctx.Done():
		return fmt.Errorf("日志写入超时: %w", ctx.Err())
	case <-timer.C:
		s.metrics.droppedLogs.Add(1)
		log.Error("OperationLog 日志队列已满，丢弃日志", zap.Any("entry", entry))
		return ErrOperationLogQueueFull
	}
}

func (s *OperationLogService) processLog(entry *LogEntry) {
	startTime := time.Now()
	s.metrics.queueSize.Add(-1)
	var lastErr error
	for attempt := 0; attempt < s.maxRetries; attempt++ {
		if attempt > 0 {
			s.metrics.retriedLogs.Add(1)
			time.Sleep(time.Duration(attempt*attempt) * 100 * time.Millisecond)
		}
		if err := s.writeLog(entry); err == nil {
			s.metrics.successLogs.Add(1)
			s.metrics.processingTime.Add(time.Since(startTime).Milliseconds())
			return
		} else {
			lastErr = err
			log.Error("OperationLog 写入失败", zap.Int("attempt", attempt+1), zap.Int("max_retries", s.maxRetries), zap.Error(err))
		}
	}
	s.metrics.failedLogs.Add(1)
	log.Error("OperationLog 写入最终失败", zap.Error(lastErr), zap.Any("entry", entry))
}

func (s *OperationLogService) writeLog(entry *LogEntry) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.writeTimeout)
	defer cancel()
	// UID 为纯数字时落库为 user_id（历史版本恒写 0，导致无法按操作者过滤）
	userID := 0
	if n, err := strconv.Atoi(entry.UserUID); err == nil && n > 0 {
		userID = n
	}
	builder := s.Client.OperationLog.Create().
		SetUserID(userID).
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
		builder.SetRequestBody(normalizeOperationLogRequestBody(entry.RequestBody))
	}

	_, err := builder.Save(ctx)
	return err
}

func normalizeOperationLogRequestBody(body string) string {
	normalized := compressOperationLogRequestBody(body)
	if len(normalized) <= maxOperationLogRequestBodyLen {
		return normalized
	}
	return normalized[:maxOperationLogRequestBodyLen]
}

func compressOperationLogRequestBody(body string) string {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(body)); err != nil {
		return body
	}
	if err := zw.Close(); err != nil {
		return body
	}

	compressed := compressedOperationLogRequestBodyPrefix + base64.StdEncoding.EncodeToString(buf.Bytes())
	if len(compressed) >= len(body) {
		return body
	}
	return compressed
}

func decodeOperationLogRequestBody(body string) string {
	if len(body) < len(compressedOperationLogRequestBodyPrefix) || body[:len(compressedOperationLogRequestBodyPrefix)] != compressedOperationLogRequestBodyPrefix {
		return body
	}

	payload := body[len(compressedOperationLogRequestBodyPrefix):]
	compressed, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return body
	}
	zr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return body
	}
	defer zr.Close()

	decoded, err := io.ReadAll(zr)
	if err != nil {
		return body
	}
	return string(decoded)
}

// Shutdown 优雅关闭操作日志服务
func (s *OperationLogService) Shutdown(ctx context.Context) error {
	s.closeOnce.Do(func() {
		s.enqueueMu.Lock()
		defer s.enqueueMu.Unlock()
		s.stopped.Store(true)
		close(s.stopMonitor)
		close(s.logQueue)
	})
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("日志服务关闭超时: %w", ctx.Err())
	}
}

// ShutdownDefaultOperationLogService 关闭默认操作日志服务
func ShutdownDefaultOperationLogService(ctx context.Context) error {
	svc := defaultOperationLogService.Load()
	if svc == nil {
		return nil
	}
	return svc.Shutdown(ctx)
}

// GetMetrics 获取操作日志指标
func (s *OperationLogService) GetMetrics() map[string]int64 {
	success := s.metrics.successLogs.Load()
	avgTime := int64(0)
	if success > 0 {
		avgTime = s.metrics.processingTime.Load() / success
	}
	return map[string]int64{
		"total_logs":       s.metrics.totalLogs.Load(),
		"success_logs":     success,
		"failed_logs":      s.metrics.failedLogs.Load(),
		"retried_logs":     s.metrics.retriedLogs.Load(),
		"dropped_logs":     s.metrics.droppedLogs.Load(),
		"queue_size":       s.metrics.queueSize.Load(),
		"avg_process_time": avgTime,
	}
}

func (s *OperationLogService) printMetrics() {
	metrics := s.GetMetrics()
	log.Info("OperationLog 指标",
		zap.Int64("total_logs", metrics["total_logs"]),
		zap.Int64("success_logs", metrics["success_logs"]),
		zap.Int64("failed_logs", metrics["failed_logs"]),
		zap.Int64("retried_logs", metrics["retried_logs"]),
		zap.Int64("dropped_logs", metrics["dropped_logs"]),
		zap.Int64("queue_size", metrics["queue_size"]),
		zap.Int64("avg_process_time", metrics["avg_process_time"]),
	)
}

// List 获取操作日志列表（按操作者数据范围过滤）
func (s *OperationLogService) List(ctx context.Context, op *EffectiveDataScope, req *ListOperationLogsRequest) (*ListOperationLogsResponse, error) {
	req.Page, req.PageSize = utils.NormalizePagination(req.Page, req.PageSize)

	query := s.Client.OperationLog.Query()

	// 数据范围过滤
	if scopeFilter, err := s.buildScopeFilter(ctx, op); err != nil {
		return nil, err
	} else if scopeFilter != nil {
		query = query.Where(scopeFilter...)
	}

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

// Get 获取操作日志详情（超出数据范围视为不存在，避免存在性泄露）
func (s *OperationLogService) Get(ctx context.Context, op *EffectiveDataScope, id int) (*OperationLogResponse, error) {
	opLog, err := s.Client.OperationLog.Query().Where(operationlog.IDEQ(id)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrOperationLogNotFound
		}
		return nil, fmt.Errorf("查询操作日志失败: %w", err)
	}
	if !s.isLogInScope(ctx, op, opLog.Username) {
		return nil, ErrOperationLogNotFound
	}
	return toOperationLogResponse(opLog), nil
}

// buildScopeFilter 构造数据范围过滤条件；返回 nil 表示不过滤（超管/All/内部调用）
// 注：日志表 user_id 历史上恒为 0，范围过滤基于 username 实现
func (s *OperationLogService) buildScopeFilter(ctx context.Context, op *EffectiveDataScope) ([]predicate.OperationLog, error) {
	if shouldBypassCheck(op, false) || op.Scope == DataScopeAll {
		return nil, nil
	}
	names, err := s.scopeAdminUsernames(ctx, op)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		// 空集合时用不可能命中的条件，避免 In() 空切片退化
		return []predicate.OperationLog{operationlog.UsernameEQ("__scope_empty__")}, nil
	}
	return []predicate.OperationLog{operationlog.UsernameIn(names...)}, nil
}

// scopeAdminUsernames 返回数据范围内可见的管理员用户名集合（始终包含操作者本人）
func (s *OperationLogService) scopeAdminUsernames(ctx context.Context, op *EffectiveDataScope) ([]string, error) {
	names := make([]string, 0, 4)
	if op.Username != "" {
		names = append(names, op.Username)
	}
	switch op.Scope {
	case DataScopeSelf:
		return names, nil
	case DataScopeDepartment, DataScopeDepartmentAndChild, DataScopeCustom:
		if len(op.DepartmentIDs) == 0 {
			return names, nil
		}
		admins, err := s.Client.Admin.Query().
			Where(admin.DepartmentIDIn(op.DepartmentIDs...)).
			All(ctx)
		if err != nil {
			return nil, fmt.Errorf("查询范围内管理员失败: %w", err)
		}
		for _, a := range admins {
			if a.Username != "" {
				names = append(names, a.Username)
			}
		}
		return names, nil
	default:
		return names, nil
	}
}

// isLogInScope 判断日志是否在操作者数据范围内（基于 username）
func (s *OperationLogService) isLogInScope(ctx context.Context, op *EffectiveDataScope, logUsername string) bool {
	if shouldBypassCheck(op, false) || op.Scope == DataScopeAll {
		return true
	}
	names, err := s.scopeAdminUsernames(ctx, op)
	if err != nil {
		return false
	}
	for _, name := range names {
		if name == logUsername {
			return true
		}
	}
	return false
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
		RequestBody: decodeOperationLogRequestBody(l.RequestBody),
		StatusCode:  l.StatusCode,
		DurationMs:  l.DurationMs,
		CreatedAt:   l.CreatedAt,
	}
}
