// Package queue 将 cmf/queue 任务队列接入本应用：
// 依据配置构建 Client/Server/Inspector 三件套，桥接 internal/tasks 注册表中的
// Go 任务为队列处理器，并提供死信清理协程。
package queue

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"sync"
	"time"

	"github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/log"
	cmfqueue "github.com/wuwuseo/cmf/queue"
	"go.uber.org/zap"

	"go-admin/internal/global"
	"go-admin/internal/tasks"
)

// maxTaskLogBytes 桥接任务日志缓冲上限（字节），超出部分丢弃并计数
const maxTaskLogBytes = 64 * 1024

// janitorInterval 死信清理协程的执行间隔
const janitorInterval = 24 * time.Hour

// janitorGrace 死信保留宽限期：仅清理归档时间早于该期限的死信
const janitorGrace = 7 * 24 * time.Hour

// queueNamePattern 队列名白名单：字母、数字、下划线、中划线，长度 1-64。
// 队列名会拼入 asynq 的 Redis 键（asynq:{queue}:...），
// 收紧字符集可避免构造出的名字落键到非预期前缀。
var queueNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// IsValidQueueName 判断队列名是否在白名单内。
func IsValidQueueName(name string) bool {
	return queueNamePattern.MatchString(name)
}

var lifecycleMu sync.Mutex
var cancelJanitor context.CancelFunc
var janitorDone chan struct{}

// configuredQueues Setup 时记录的已配置队列名，供入队校验使用
var configuredQueues []string

// ConfiguredQueues 返回配置中声明的队列名列表。
func ConfiguredQueues() []string {
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	out := make([]string, len(configuredQueues))
	copy(out, configuredQueues)
	return out
}

// engineConfig Setup 时构建的队列配置，供监控页挂载复用（与 worker 指向同一 Redis）
var engineConfig cmfqueue.Config

// EngineConfig 返回 Setup 构建的队列配置；未初始化时第二个返回值为 false。
func EngineConfig() (cmfqueue.Config, bool) {
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	if global.QueueServer.Load() == nil {
		return cmfqueue.Config{}, false
	}
	cfg := engineConfig
	cfg.Queues = maps.Clone(cfg.Queues)
	return cfg, true
}

// Setup 构建并启动任务队列：创建三件套、注册 tasks.Registry 中的全部任务、
// 启动 worker 与死信清理协程，并将句柄挂载到 global。
func Setup(cfg *config.Config) error {
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	if global.QueueServer.Load() != nil {
		return fmt.Errorf("任务队列已启动")
	}
	// 启动期校验队列名（在触碰 Redis 前快速失败）；缺省的 default 队列始终合法
	for name := range cfg.Queue.Queues {
		if !IsValidQueueName(name) {
			return fmt.Errorf("队列名 %q 非法：仅允许字母、数字、下划线、中划线，长度 1-64", name)
		}
	}
	qcfg, err := cmfqueue.NewConfigFromApp(cfg, cfg.Queue.RedisConnection)
	if err != nil {
		return err
	}

	// 启动期连通性检查：Redis 不可达/认证失败时快速失败，避免运行时才发现配置错误
	pingCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := qcfg.Ping(pingCtx); err != nil {
		return fmt.Errorf("队列 Redis 连接失败（检查 queue.redis_connection 与 redis.connections 配置）: %w", err)
	}

	// 配置文件中的运行参数覆盖默认值（零值/缺省时保留 cmf 默认）
	if cfg.Queue.Concurrency > 0 {
		qcfg.Concurrency = cfg.Queue.Concurrency
	}
	if len(cfg.Queue.Queues) > 0 {
		qcfg.Queues = maps.Clone(cfg.Queue.Queues)
	}
	qcfg.StrictPriority = cfg.Queue.StrictPriority
	if cfg.Queue.MaxRetry > 0 {
		qcfg.MaxRetry = cfg.Queue.MaxRetry
	}
	if cfg.Queue.Timeout > 0 {
		qcfg.Timeout = time.Duration(cfg.Queue.Timeout) * time.Second
	}
	if cfg.Queue.Retention > 0 {
		qcfg.Retention = time.Duration(cfg.Queue.Retention) * time.Second
	}
	if cfg.Queue.MaxQueueSize > 0 {
		qcfg.MaxQueueSize = cfg.Queue.MaxQueueSize
	}
	if cfg.Queue.MaxPayloadBytes > 0 {
		qcfg.MaxPayloadBytes = cfg.Queue.MaxPayloadBytes
	}

	client := cmfqueue.NewClient(qcfg)
	server := cmfqueue.NewServer(qcfg)
	inspector := cmfqueue.NewInspector(qcfg)
	engineConfig = qcfg

	// 记录已配置队列名，供入队校验（未配置的队列无人消费）
	configuredQueues = make([]string, 0, len(qcfg.Queues))
	for name := range qcfg.Queues {
		configuredQueues = append(configuredQueues, name)
	}

	registerTaskHandlers(server)

	if err := server.Start(); err != nil {
		client.Close()
		inspector.Close()
		// 回滚本函数已写入的包级状态，避免半初始化残留
		engineConfig = cmfqueue.Config{}
		configuredQueues = nil
		return err
	}

	global.QueueClient.Store(client)
	global.QueueInspector.Store(inspector)
	global.QueueServer.Store(server)

	// 启动死信清理协程：立即清理，并定期清理归档时间超过宽限期的任务
	janitorCtx, cancel := context.WithCancel(context.Background())
	cancelJanitor = cancel
	janitorDone = make(chan struct{})
	go func(done chan struct{}) {
		defer close(done)
		runArchiveJanitor(janitorCtx, qcfg)
	}(janitorDone)

	log.Info("任务队列已启动",
		zap.String("addr", qcfg.Addr),
		zap.Int("db", qcfg.DB),
		zap.Int("concurrency", qcfg.Concurrency),
		zap.Any("queues", qcfg.Queues),
	)
	return nil
}

// Shutdown 优雅停止任务队列：等待在途任务完成（受 timeout 预算约束），
// 随后停掉清理协程并释放连接。
func Shutdown(timeout time.Duration) {
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if cancelJanitor != nil {
		cancelJanitor()
		cancelJanitor = nil
	}

	server := global.QueueServer.Swap(nil)
	client := global.QueueClient.Swap(nil)
	inspector := global.QueueInspector.Swap(nil)
	if server != nil {
		done := make(chan struct{})
		go func() {
			server.Shutdown()
			close(done)
		}()
		select {
		case <-done:
			log.Info("任务队列 worker 已停止")
		case <-ctx.Done():
			log.Warn("任务队列 worker 关闭超时，放弃等待（在途任务将由下次启动恢复）")
		}
	}
	if client != nil {
		if err := client.Close(); err != nil {
			log.Error("关闭队列投递端失败", zap.Error(err))
		}
	}
	if inspector != nil {
		if err := inspector.Close(); err != nil {
			log.Error("关闭队列管理端失败", zap.Error(err))
		}
	}
	if janitorDone != nil {
		select {
		case <-janitorDone:
		case <-ctx.Done():
			log.Warn("死信清理协程关闭超时")
		}
		janitorDone = nil
	}
}

// registerTaskHandlers 将 tasks.Registry 中的 Go 任务全部注册为队列处理器。
func registerTaskHandlers(server *cmfqueue.Server) {
	for name, fn := range tasks.Registry {
		server.Handle(name, adaptTaskFunc(name, fn))
	}
	log.Info("队列任务处理器注册完成", zap.Any("handlers", server.RegisteredTasks()))
}

// adaptTaskFunc 将 tasks.Registry 的 TaskFunc 适配为队列处理器：
// TaskFunc 的 io.Writer 接有界内存缓冲，执行结果（含日志尾部）经 zap 输出。
func adaptTaskFunc(name string, fn tasks.TaskFunc) cmfqueue.Handler {
	return func(ctx context.Context, t *cmfqueue.Task) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		buf := newBoundedBuffer(maxTaskLogBytes)
		start := time.Now()
		err := fn(ctx, buf)
		fields := []zap.Field{
			zap.String("task_id", cmfqueue.GetTaskID(ctx)),
			zap.String("task_name", name),
			zap.Int("attempt", cmfqueue.GetAttempt(ctx)),
			zap.Duration("duration", time.Since(start)),
			zap.String("log_tail", buf.Tail()),
		}
		if err != nil {
			fields = append(fields, zap.Error(err))
			log.Error("队列任务执行失败", fields...)
		} else {
			log.Info("队列任务执行成功", fields...)
		}
		return err
	}
}

// runArchiveJanitor 死信清理协程：每个清理周期遍历配置内的队列，
// 删除归档时间早于宽限期的 archived 任务。
func runArchiveJanitor(ctx context.Context, qcfg cmfqueue.Config) {
	inspector := cmfqueue.NewInspector(qcfg)
	defer inspector.Close()

	queues := make([]string, 0, len(qcfg.Queues))
	for name := range qcfg.Queues {
		queues = append(queues, name)
	}

	ticker := time.NewTicker(janitorInterval)
	defer ticker.Stop()
	for {
		cutoff := time.Now().Add(-janitorGrace)
		for _, q := range queues {
			removed, err := inspector.PruneArchivedContext(ctx, q, cutoff)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Error("清理死信任务失败", zap.String("queue", q), zap.Error(err))
				continue
			}
			if removed > 0 {
				log.Info("死信任务清理完成", zap.String("queue", q), zap.Int("removed", removed))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// boundedBuffer 有界内存缓冲：作为桥接任务的 io.Writer，超过上限后丢弃后续写入。
type boundedBuffer struct {
	buf       []byte
	max       int
	discarded int64
}

func newBoundedBuffer(max int) *boundedBuffer {
	return &boundedBuffer{buf: make([]byte, 0, 1024), max: max}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(b.buf) < b.max {
		room := b.max - len(b.buf)
		if room >= len(p) {
			b.buf = append(b.buf, p...)
			return len(p), nil
		}
		b.buf = append(b.buf, p[:room]...)
		b.discarded += int64(len(p) - room)
		return len(p), nil
	}
	b.discarded += int64(len(p))
	return len(p), nil
}

// Tail 返回缓冲中的日志尾部内容；有丢弃时附注说明。
func (b *boundedBuffer) Tail() string {
	if b.discarded > 0 {
		return string(b.buf) + fmt.Sprintf("\n... (已截断 %dKB 日志)", b.discarded/1024)
	}
	return string(b.buf)
}
