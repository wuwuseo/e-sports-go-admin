# 任务队列设计（2026-08-30，v2：Redis + asynq）

> v1（自研 DB 队列）已被否决，方向改为：**成熟 Redis 队列库 asynq，通用封装层进 cmf/queue，admin 做集成**。

## 背景与目标

admin 已有 `internal/cron`（robfig/cron 定时调度）与 `internal/tasks`（内存任务注册表），缺"队列"能力：异步投递、延迟执行、失败自动重试、任务状态管理、历史可观测。

选型结论：采用 **github.com/hibiken/asynq**（Redis 支撑、Go 原生、重试/延迟/优先级队列/去重完善、自带 asynqmon 监控 UI）。Redis 已是项目基础设施（config.yml 有连接配置），admin 的执行节点与 Redis 常驻。

## 总体架构

```
业务代码 ── global.QueueClient.Enqueue(name, payload, opts) ──▶ Redis
                                     ▲
global.QueueServer（常驻 worker）────┘ 轮询认领 → 执行注册的 Handler
                                     │
        成功 → completed(按 Retention 保留)；失败 → 自动指数退避重试 → 超限进 archived
                                     │
管理 API（/queue-jobs）─ Inspector ──┘ 列表/详情/重试/删除/取消/统计
```

- **cmf/queue（通用层，本次新增）**：对 asynq 的框架级封装——统一 Config、Client/Server/Inspector 三件套、自绘 Task/Handler 类型（隔离 asynq 类型）、Handler 注册表、asynqmon 监控页挂载助手。admin 及未来 cmf 应用直接复用。
- **admin（集成层）**：config.yml `queue:` 段（复用 `redis.connections` 命名连接，同 captcha 模式）；`internal/queue` 桥接（把 `tasks.Registry` 的 TaskFunc 适配为队列 Handler）；`global.Queue*` 句柄；`Init()` 启动 / `Cleanup()` 优雅停止；管理 API 挂 `/api/v1/admin/auth/queue-jobs`；asynqmon 监控页（默认关闭）挂 admin 认证之后。

cron 与 queue 保持独立：cron 管"何时触发"，queue 管"如何可靠执行"。

## cmf/queue 详细设计

### 类型与 API

```go
type Task struct { Name string; Payload []byte }          // 队列任务（Name=处理器名）
type Handler func(ctx context.Context, t *Task) error

type Config struct {                                      // Redis 连参 + 运行参数
    Addr, Username, Password string; DB, PoolSize int
    Concurrency int; Queues map[string]int; StrictPriority bool
    MaxRetry int; Timeout, Retention time.Duration
}
func DefaultConfig() Config
func NewConfigFromApp(cfg *config.Config, conn ...string) Config   // 从 cmf 全局配置构建

type Client struct                                        // 投递端
    Enqueue(ctx, name, payload []byte, opts ...EnqueueOption) (taskID string, err error)
    Close()
// EnqueueOption: WithQueue / WithDelay / WithMaxRetry / WithTimeout / WithRetention / WithUnique / WithTaskID

type Server struct                                        // 执行端
    Handle(name string, h Handler); RegisteredTasks() []string
    Start() error; Shutdown(ctx) error                    // 非阻塞 Start，优雅关闭
    // ctx 注入：queue.GetTaskID(ctx)、queue.GetAttempt(ctx) 取任务ID与第几次尝试

type Inspector struct                                     // 管理/观测端（驱动管理 API）
    Stats() / ListTasks(filter) / GetTask / Retry / Delete / Cancel / PruneArchived(olderThan)

func MonitorHandler(cfg, rootPath) (http.Handler, error)  // asynqmon UI（可选启用）
```

管理端返回自绘 `TaskInfo/QueueStats`，admin 不直接 import asynq。

### 关键语义

- **Redis 版本要求：≥ 6.2**（asynq v0.26 的归档脚本使用 `ZRANGE ... BYSCORE` 语法；本机开发 Redis 为 5.0.14 时归档路径不可用，cmf/queue 测试会自动探测并跳过归档相关用例）。
- 重试：asynq 内建指数退避；超过 `MaxRetry` 的任务进 archived（死信），可经管理 API 手动 Retry 或删除。
- completed 保留：入队 `Retention` > 0 时完成后保留相应时长（Redis TTL 自动过期），支撑"任务历史"展示。
- archived 清理：cmf/queue 提供 `PruneArchived`；admin 侧用现有 cron 每日调用。
- Handler panic 由 asynq recover 并按失败重试；未注册 Name 的任务执行期报"无处理器"，同时 admin 入队前预校验注册表。
- 优先级：多队列权重 `Queues map[name]priority`，`StrictPriority` 可选严格模式。

### cmf/config 新增 `queue` 段

`redis_connection`（引用 redis.connections 连接名，默认 redis）、`concurrency`（默认 10）、`queues`（默认 default:10）、`strict_priority`、`max_retry`（默认 3）、`timeout`（秒，默认 600）、`retention`（秒，默认 0）、`enable_monitor`（默认 false）、`monitor_path`（默认 /api/v1/admin/auth/queue-monitor）。

## admin 集成设计

1. **config.yml**：新增 `queue:` 段；监控页默认关闭（安全基线，与 swagger 同策略）。
2. **internal/queue/queue.go**：从 `config.Queue` + `redis.connections[queue.redis_connection]` 构建；Server 注册全部 `tasks.Registry` 任务（TaskFunc 的 io.Writer 接有界内存 buffer，日志走 zap 输出）；启动 PruneArchived 日清理协程；暴露 `global.QueueClient/QueueServer/QueueInspector`。
3. **internal/init.go / cleanup.go**：CronManager 之后启动；DB 关闭之前优雅停止。
4. **internal/service/queue_job.go**：列表（按 state/queue 分页筛选）、详情、重试、删除、取消（活动任务）、手动入队（校验 handler 已注册 + payload 合法 JSON）、统计、已注册 handler 列表。
5. **internal/handler/queue_job.go + router.go**：`/api/v1/admin/auth/queue-jobs`（GET 列表、GET /stats、GET /handlers、POST 入队、GET/:queue/:id、POST/:queue/:id/retry、POST/:queue/:id/cancel、DELETE/:queue/:id）；asynqmon 页经 adaptor 挂 adminAuth 下 `queue-monitor`（enable_monitor 时）。
6. **权限基线**：queue-jobs 与 queue-monitor 全部路径加入 `SensitivePermissionObjects`（与 cron-jobs 同理：队列管理可触发任意已注册任务，禁止普通管理员自动持有，仅超管）。
7. **global/msg.go**：新增 4101-4105 队列错误码；字典 `sys_oper_log_module` 增 `{"任务队列","queue",8}`。
8. **docs/task-queue.md**：API 示例与配置说明。

## 测试策略

- **cmf/queue**：TestMain 按 优先读环境变量 `CMF_QUEUE_TEST_REDIS_ADDR/DB/PASSWORD`（本机 Redis，独立 DB 15 隔离 + 测试前 FLUSHDB）→ 失败则 testcontainers（无 docker 跳过，与 cmf/redis 测试同模式）。覆盖：入队-消费往返、失败重试与退避、超限进 archived、延迟执行、Inspector 列表/统计/重试/删除、未注册任务报错。
- **admin**：`go test ./...` 保持零外部依赖——internal/queue 桥接与 service 层单测不连 Redis（校验注册表映射、参数校验、错误映射）；引擎行为由 cmf/queue 测试兜底。
- 验证命令：cmf `go test ./queue/...`（本机 Redis 环境）；admin `go build -o go-admin.exe main.go` + `go test ./...`。

## 明确不做（YAGNI）

- 不做 driver 抽象层/其他后端（选型已定 asynq，抽象是伪需求）。
- 不改 cron/tasks 既有语义；不做分布式部署编排。
- 监控页默认关闭，不替换管理端 API（两者互补：API 供前端集成，asynqmon 供运维）。
