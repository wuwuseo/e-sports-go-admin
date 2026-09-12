package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/loginlog"
	"go-admin/internal/ent/predicate"
	"go-admin/internal/global"
	"go-admin/internal/utils"

	"github.com/wuwuseo/cmf/log"
	"go.uber.org/zap"
)

var (
	ErrLoginLogNotFound  = errors.New("登录日志不存在")
	ErrLoginLogStopped   = errors.New("登录日志服务已停止")
	ErrLoginLogQueueFull = errors.New("登录日志队列已满")
)

const (
	defaultLoginLogQueueSize    = 1000
	defaultLoginLogWorkers      = 3
	defaultLoginLogMaxRetries   = 3
	defaultLoginLogWriteTimeout = 5 * time.Second
	defaultLoginLogEnqueueTTL   = 100 * time.Millisecond
)

var defaultLoginLogService atomic.Pointer[LoginLogService]

// LoginLogServiceInterface 登录日志服务接口
type LoginLogServiceInterface interface {
	Record(ctx context.Context, entry *LoginLogEntry) error
	List(ctx context.Context, op *EffectiveDataScope, req *ListLoginLogsRequest) (*ListLoginLogsResponse, error)
	Get(ctx context.Context, op *EffectiveDataScope, id int) (*LoginLogResponse, error)
}

// LoginLogOptions 登录日志服务配置
type LoginLogOptions struct {
	QueueSize      int
	Workers        int
	MaxRetries     int
	WriteTimeout   time.Duration
	EnqueueTimeout time.Duration
}

// LoginLogEntry 登录日志条目
type LoginLogEntry struct {
	Username  string
	UserID    int
	LoginType string // user / admin
	Status    string // success / fail
	Message   string
	IP        string
	UserAgent string
	Browser   string
	Os        string
}

// ListLoginLogsRequest 登录日志列表请求
type ListLoginLogsRequest struct {
	PaginationRequest
	Username  string `query:"username"`
	LoginType string `query:"login_type"`
	Status    string `query:"status"`
	IP        string `query:"ip"`
	StartTime string `query:"start_time"`
	EndTime   string `query:"end_time"`
}

// LoginLogResponse 登录日志响应
type LoginLogResponse struct {
	ID        int       `json:"id"`
	UserID    int       `json:"user_id"`
	Username  string    `json:"username"`
	LoginType string    `json:"login_type"`
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
	Browser   string    `json:"browser"`
	Os        string    `json:"os"`
	CreatedAt time.Time `json:"created_at"`
}

// ListLoginLogsResponse 登录日志列表响应
type ListLoginLogsResponse = ListResponse[*LoginLogResponse]

// LoginLogService 登录日志服务（异步写入，结构与 OperationLogService 一致）
type LoginLogService struct {
	*CRUDBase
	queue          chan *LoginLogEntry
	workers        int
	maxRetries     int
	writeTimeout   time.Duration
	enqueueTimeout time.Duration
	enqueueMu      sync.RWMutex
	wg             sync.WaitGroup
	closeOnce      sync.Once
	stopped        atomic.Bool
}

// NewLoginLogService 创建登录日志服务实例并注册为默认实例
func NewLoginLogService() *LoginLogService {
	return NewLoginLogServiceWithOptions(LoginLogOptions{})
}

// NewLoginLogServiceWithOptions 按配置创建登录日志服务实例并注册为默认实例
func NewLoginLogServiceWithOptions(opts LoginLogOptions) *LoginLogService {
	if opts.QueueSize <= 0 {
		opts.QueueSize = defaultLoginLogQueueSize
	}
	if opts.Workers <= 0 {
		opts.Workers = defaultLoginLogWorkers
	}
	if opts.MaxRetries <= 0 {
		opts.MaxRetries = defaultLoginLogMaxRetries
	}
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = defaultLoginLogWriteTimeout
	}
	if opts.EnqueueTimeout <= 0 {
		opts.EnqueueTimeout = defaultLoginLogEnqueueTTL
	}

	s := &LoginLogService{
		CRUDBase:       &CRUDBase{Client: global.ORM},
		queue:          make(chan *LoginLogEntry, opts.QueueSize),
		workers:        opts.Workers,
		maxRetries:     opts.MaxRetries,
		writeTimeout:   opts.WriteTimeout,
		enqueueTimeout: opts.EnqueueTimeout,
	}
	for range s.workers {
		s.wg.Add(1)
		go s.worker()
	}
	defaultLoginLogService.Store(s)
	return s
}

// Record 异步记录登录日志；Browser/Os 缺省时从 User-Agent 解析补全
func (s *LoginLogService) Record(ctx context.Context, entry *LoginLogEntry) error {
	if entry == nil {
		return nil
	}
	s.enqueueMu.RLock()
	defer s.enqueueMu.RUnlock()
	if s.stopped.Load() {
		return ErrLoginLogStopped
	}

	e := *entry
	e.Username = loginLogText(e.Username, 255)
	e.IP = loginLogText(e.IP, 50)
	e.UserAgent = loginLogText(e.UserAgent, 1024)
	if e.LoginType == "" {
		e.LoginType = "user"
	}
	if e.Status == "" {
		e.Status = "fail"
	}
	if e.Browser == "" || e.Os == "" {
		browser, osName := utils.ParseUserAgent(e.UserAgent)
		if e.Browser == "" {
			e.Browser = browser
		}
		if e.Os == "" {
			e.Os = osName
		}
	}
	e.Message = loginLogText(e.Message, 255)
	e.Browser = loginLogText(e.Browser, 100)
	e.Os = loginLogText(e.Os, 100)

	timer := time.NewTimer(s.enqueueTimeout)
	defer timer.Stop()
	select {
	case s.queue <- &e:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("登录日志写入超时: %w", ctx.Err())
	case <-timer.C:
		log.Error("LoginLog 日志队列已满，丢弃日志", zap.Any("entry", e))
		return ErrLoginLogQueueFull
	}
}

// loginLogText 按字节限制保留完整 UTF-8 字符，并脱离 HTTP 请求复用的缓冲区。
func loginLogText(value string, limit int) string {
	value = strings.ToValidUTF8(value, "")
	if len(value) > limit {
		for !utf8.RuneStart(value[limit]) {
			limit--
		}
		value = value[:limit]
	}
	return strings.Clone(value)
}

func (s *LoginLogService) worker() {
	defer s.wg.Done()
	for entry := range s.queue {
		s.processLog(entry)
	}
}

func (s *LoginLogService) processLog(entry *LoginLogEntry) {
	var lastErr error
	for attempt := 0; attempt < s.maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*attempt) * 100 * time.Millisecond)
		}
		if err := s.writeLog(entry); err == nil {
			return
		} else {
			lastErr = err
			log.Error("LoginLog 写入失败", zap.Int("attempt", attempt+1), zap.Error(err))
		}
	}
	log.Error("LoginLog 写入最终失败", zap.Error(lastErr), zap.Any("entry", entry))
}

func (s *LoginLogService) writeLog(entry *LoginLogEntry) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.writeTimeout)
	defer cancel()
	builder := s.Client.LoginLog.Create().
		SetUsername(entry.Username).
		SetUserID(entry.UserID).
		SetLoginType(loginlog.LoginType(entry.LoginType)).
		SetStatus(loginlog.Status(entry.Status)).
		SetIP(entry.IP)

	if entry.Message != "" {
		builder.SetMessage(entry.Message)
	}
	if entry.UserAgent != "" {
		builder.SetUserAgent(entry.UserAgent)
	}
	if entry.Browser != "" {
		builder.SetBrowser(entry.Browser)
	}
	if entry.Os != "" {
		builder.SetOs(entry.Os)
	}

	_, err := builder.Save(ctx)
	return err
}

// List 获取登录日志列表（按操作者数据范围过滤）
func (s *LoginLogService) List(ctx context.Context, op *EffectiveDataScope, req *ListLoginLogsRequest) (*ListLoginLogsResponse, error) {
	req.Page, req.PageSize = utils.NormalizePagination(req.Page, req.PageSize)

	query := s.Client.LoginLog.Query()

	if scopeFilter, err := s.buildScopeFilter(ctx, op); err != nil {
		return nil, err
	} else if scopeFilter != nil {
		query = query.Where(scopeFilter...)
	}

	if req.Username != "" {
		query = query.Where(loginlog.UsernameContains(req.Username))
	}
	if req.LoginType != "" {
		query = query.Where(loginlog.LoginTypeEQ(loginlog.LoginType(req.LoginType)))
	}
	if req.Status != "" {
		query = query.Where(loginlog.StatusEQ(loginlog.Status(req.Status)))
	}
	if req.IP != "" {
		query = query.Where(loginlog.IPEQ(req.IP))
	}
	if req.StartTime != "" {
		if t, err := time.Parse(time.RFC3339, req.StartTime); err == nil {
			query = query.Where(loginlog.CreatedAtGTE(t))
		}
	}
	if req.EndTime != "" {
		if t, err := time.Parse(time.RFC3339, req.EndTime); err == nil {
			query = query.Where(loginlog.CreatedAtLTE(t))
		}
	}

	total, err := query.Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取登录日志总数失败: %w", err)
	}

	query = query.Order(ent.Desc(loginlog.FieldCreatedAt))
	if req.PageSize > 0 {
		offset := (req.Page - 1) * req.PageSize
		query = query.Offset(offset).Limit(req.PageSize)
	}
	logs, err := query.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取登录日志列表失败: %w", err)
	}

	return BuildList(logs, total, req.Page, toLoginLogResponse), nil
}

// Get 获取登录日志详情（超出数据范围视为不存在，避免存在性泄露）
func (s *LoginLogService) Get(ctx context.Context, op *EffectiveDataScope, id int) (*LoginLogResponse, error) {
	row, err := s.Client.LoginLog.Query().Where(loginlog.IDEQ(id)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrLoginLogNotFound
		}
		return nil, fmt.Errorf("查询登录日志失败: %w", err)
	}
	if !s.isLogInScope(ctx, op, row) {
		return nil, ErrLoginLogNotFound
	}
	return toLoginLogResponse(row), nil
}

// buildScopeFilter 构造数据范围过滤条件；返回 nil 表示不过滤（超管/All/内部调用）。
// 前台用户（users 表）登录日志仅限 All/超管查看：users 与 admins 是两个自增序列，
// 按管理员范围过滤 user_id 时必须限定 login_type=admin，避免跨表 ID 误匹配。
func (s *LoginLogService) buildScopeFilter(ctx context.Context, op *EffectiveDataScope) ([]predicate.LoginLog, error) {
	if shouldBypassCheck(op, false) || op.Scope == DataScopeAll {
		return nil, nil
	}
	userIDs, err := adminIDsInScope(ctx, s.Client, op)
	if err != nil {
		return nil, err
	}
	if len(userIDs) == 0 {
		return []predicate.LoginLog{loginlog.LoginTypeEQ(loginlog.LoginTypeAdmin), loginlog.UserIDEQ(-1)}, nil
	}
	return []predicate.LoginLog{
		loginlog.LoginTypeEQ(loginlog.LoginTypeAdmin),
		loginlog.UserIDIn(userIDs...),
	}, nil
}

// isLogInScope 判断登录日志是否在操作者数据范围内（仅后台管理员登录记录参与范围过滤）。
func (s *LoginLogService) isLogInScope(ctx context.Context, op *EffectiveDataScope, row *ent.LoginLog) bool {
	if shouldBypassCheck(op, false) || op.Scope == DataScopeAll {
		return true
	}
	if row.LoginType != loginlog.LoginTypeAdmin {
		return false
	}
	userIDs, err := adminIDsInScope(ctx, s.Client, op)
	if err != nil {
		return false
	}
	for _, userID := range userIDs {
		if userID == row.UserID {
			return true
		}
	}
	return false
}

// Shutdown 优雅关闭登录日志服务，排空队列中剩余日志
func (s *LoginLogService) Shutdown(ctx context.Context) error {
	s.closeOnce.Do(func() {
		s.enqueueMu.Lock()
		defer s.enqueueMu.Unlock()
		s.stopped.Store(true)
		close(s.queue)
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
		return fmt.Errorf("登录日志服务关闭超时: %w", ctx.Err())
	}
}

// ShutdownDefaultLoginLogService 关闭默认登录日志服务
func ShutdownDefaultLoginLogService(ctx context.Context) error {
	svc := defaultLoginLogService.Load()
	if svc == nil {
		return nil
	}
	return svc.Shutdown(ctx)
}

func toLoginLogResponse(l *ent.LoginLog) *LoginLogResponse {
	return &LoginLogResponse{
		ID:        l.ID,
		UserID:    l.UserID,
		Username:  l.Username,
		LoginType: string(l.LoginType),
		Status:    string(l.Status),
		Message:   l.Message,
		IP:        l.IP,
		UserAgent: l.UserAgent,
		Browser:   l.Browser,
		Os:        l.Os,
		CreatedAt: l.CreatedAt,
	}
}

// ========== 登录元数据上下文 ==========

type loginMetaKeyType struct{}

// LoginMeta 登录请求的来源元数据，由 HTTP 层注入请求上下文
type LoginMeta struct {
	IP        string
	UserAgent string
}

// ContextWithLoginMeta 在请求上下文中注入登录来源元数据
func ContextWithLoginMeta(ctx context.Context, ip, userAgent string) context.Context {
	return context.WithValue(ctx, loginMetaKeyType{}, LoginMeta{IP: ip, UserAgent: userAgent})
}

func loginMetaFromContext(ctx context.Context) (LoginMeta, bool) {
	if ctx == nil {
		return LoginMeta{}, false
	}
	meta, ok := ctx.Value(loginMetaKeyType{}).(LoginMeta)
	return meta, ok
}

// ========== 共享的数据范围辅助 ==========

// adminIDsInScope 返回数据范围内可见的管理员数据库主键（始终包含操作者本人）。
func adminIDsInScope(ctx context.Context, client *ent.Client, op *EffectiveDataScope) ([]int, error) {
	userIDs := make([]int, 0, 4)
	if op.AdminID > 0 {
		userIDs = append(userIDs, op.AdminID)
	}
	switch op.Scope {
	case DataScopeSelf:
		return userIDs, nil
	case DataScopeDepartment, DataScopeDepartmentAndChild, DataScopeCustom:
		if len(op.DepartmentIDs) == 0 {
			return userIDs, nil
		}
		admins, err := client.Admin.Query().
			Where(admin.DepartmentIDIn(op.DepartmentIDs...)).
			All(ctx)
		if err != nil {
			return nil, fmt.Errorf("查询范围内管理员失败: %w", err)
		}
		for _, a := range admins {
			userIDs = append(userIDs, a.ID)
		}
		return userIDs, nil
	default:
		return userIDs, nil
	}
}
