// Package queue 将 cmf/queue 任务队列接入本应用：
// 依据配置构建 Runtime，桥接 internal/tasks 注册表中的
// Go 任务为队列处理器，并为 Asynq 提供死信清理协程。
package queue

import (
	"context"
	"fmt"
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
// 队列名会用作 broker 资源名；收紧字符集可避免落到非预期资源。
var queueNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// IsValidQueueName 判断队列名是否在白名单内。
func IsValidQueueName(name string) bool {
	return queueNamePattern.MatchString(name)
}

var lifecycleMu sync.Mutex
var cancelJanitor context.CancelFunc
var janitorDone chan struct{}

// ConfiguredQueues 返回配置中声明的队列名列表。
func ConfiguredQueues() []string {
	r := global.QueueRuntime.Load()
	if r == nil {
		return nil
	}
	return r.Queues()
}

// EngineConfig 返回 Asynq 配置供监控页复用；其他后端或未初始化时返回 false。
func EngineConfig() (cmfqueue.Config, bool) {
	r := global.QueueRuntime.Load()
	if r == nil {
		return cmfqueue.Config{}, false
	}
	return r.LegacyConfig()
}

// Setup 构建并启动所选后端，注册 tasks.Registry 中的全部任务，
// 启动 worker，并将统一句柄挂载到 global。
func Setup(cfg *config.Config) error {
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	if global.QueueRuntime.Load() != nil {
		return fmt.Errorf("任务队列已启动")
	}
	// 启动期校验队列名（在连接 broker 前快速失败）。
	for name := range cfg.Queue.Queues {
		if !IsValidQueueName(name) {
			return fmt.Errorf("队列名 %q 非法：仅允许字母、数字、下划线、中划线，长度 1-64", name)
		}
	}
	rcfg, err := cmfqueue.RuntimeConfigFromApp(cfg)
	if err != nil {
		return err
	}
	openCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runtime, err := cmfqueue.OpenRuntime(openCtx, rcfg)
	if err != nil {
		return fmt.Errorf("队列 %s 连接失败: %w", rcfg.Driver, err)
	}
	registerTaskHandlers(runtime)
	if err := runtime.Start(); err != nil {
		runtime.Shutdown(context.Background())
		return err
	}
	global.QueueRuntime.Store(runtime)

	if legacy, ok := runtime.LegacyConfig(); ok {
		janitorCtx, stop := context.WithCancel(context.Background())
		cancelJanitor = stop
		janitorDone = make(chan struct{})
		go func(done chan struct{}) {
			defer close(done)
			runArchiveJanitor(janitorCtx, legacy)
		}(janitorDone)
	}
	log.Info("任务队列已启动", zap.String("driver", runtime.Driver()), zap.Any("queues", runtime.Queues()))
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

	if runtime := global.QueueRuntime.Swap(nil); runtime != nil {
		runtime.Shutdown(ctx)
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
func registerTaskHandlers(server *cmfqueue.Runtime) {
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
