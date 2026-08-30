# 任务队列（Task Queue）

基于 [asynq](https://github.com/hibiken/asynq)（Redis 支撑）的任务队列，通用封装层在 `cmf/queue`，本仓库在 `internal/queue` 做集成。与定时任务（`internal/cron`）互补：**cron 管"何时触发"，队列管"如何可靠执行"**（异步、延迟、自动重试、死信）。

> **部署要求：Redis ≥ 6.2**（归档/死信能力依赖 `ZRANGE ... BYSCORE`）。Redis 是启动硬依赖：不可达时应用启动即失败（fail-fast）。

## 配置（config/config.yml）

```yaml
queue:
  redis_connection: queue_redis   # 复用 redis.connections 中的命名连接（专用 queue_redis 连接，db 1）
  concurrency: 10           # 并发 worker 数
  queues:
    default: 10             # 消费的队列及优先级权重
  strict_priority: false    # 严格优先级模式
  max_retry: 3              # 默认最大重试次数，超限进入死信（archived）
  timeout: 600              # 单任务默认处理超时（秒）
  retention: 3600           # 成功任务保留时长（秒），供管理端查看历史
  enable_monitor: false     # asynqmon 只读监控页（挂载在管理端认证之后）
  monitor_path: /api/v1/admin/auth/queue-monitor
```

## 业务接入

**投递任务**（通过 `global.QueueClient`）：

```go
payload, _ := json.Marshal(MyJobParam{OrderID: 42})
taskID, err := global.QueueClient.Enqueue(ctx, "my_job", payload,
    cmfqueue.WithQueue("default"),
    cmfqueue.WithDelay(5*time.Minute), // 可选：延迟执行
)
```

**注册处理器**：在 `internal/tasks` 用 `tasks.Register("my_job", fn)` 注册的 Go 任务会被自动桥接为队列处理器（`TaskFunc` 的 `io.Writer` 日志经 zap 输出）；或在 `internal/queue` 的 `registerTaskHandlers` 中追加专用 `cmfqueue.Handler`（可用 `cmfqueue.GetTaskID(ctx)` / `GetAttempt(ctx)`）。

## 管理 API（均挂载在管理端认证之后，仅超管，全部记录操作日志）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/v1/admin/auth/queue-jobs?state=pending&queue=default&page=1&page_size=10` | 按状态分页列出任务 |
| GET | `/api/v1/admin/auth/queue-jobs/stats` | 各队列状态统计 |
| GET | `/api/v1/admin/auth/queue-jobs/handlers` | 已注册处理器名（入队 `name` 候选） |
| POST | `/api/v1/admin/auth/queue-jobs` | 手动入队（见下） |
| GET | `/api/v1/admin/auth/queue-jobs/{queue}/{id}` | 任务详情 |
| POST | `/api/v1/admin/auth/queue-jobs/{queue}/{id}/retry` | 立即执行 scheduled/retry/archived 任务 |
| POST | `/api/v1/admin/auth/queue-jobs/{queue}/{id}/cancel` | 取消执行中的任务 |
| DELETE | `/api/v1/admin/auth/queue-jobs/{queue}/{id}` | 删除任务（执行中的不可删） |

手动入队示例：

```json
POST /api/v1/admin/auth/queue-jobs
{
  "name": "cleanup_expired_logs",
  "payload": {},
  "queue": "default",
  "delay_seconds": 0,
  "max_retry": 3
}
```

`name` 必须是已注册处理器，`payload` 必须是合法 JSON，`queue` 必须在 `queue.queues` 中声明，否则返回 4102/4103/4107。

## 任务状态流转

```
pending ──▶ active ──▶ completed（retention 到期自动删除）
               │ ▲
               ▼ │ 失败（指数退避，attempts < max_retry）
             retry
               │ attempts >= max_retry
               ▼
           archived（死信，可手动 retry 或删除；每日协程清理 7 天前的死信）
```

运维备注：asynqmon 监控页（`enable_monitor: true` 时）为只读，所有变更操作走上述 API 以保留审计记录；队列管理权限与 cron-jobs 同基线（`SensitivePermissionObjects`，不自动授予 normal_admin）。
