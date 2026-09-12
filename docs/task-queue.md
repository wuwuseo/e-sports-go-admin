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
  max_queue_size: 10000     # 每队列存量任务准入阈值，包含成功和死信历史
  max_payload_bytes: 65536  # 单任务载荷字节上限，投递端和执行端均检查
  enable_monitor: false     # asynqmon 只读监控页（挂载在管理端认证之后）
  monitor_path: /api/v1/admin/auth/queue-monitor
```

## 业务接入

**投递任务**（通过 `global.QueueClient.Load()` 获取一次句柄）：

```go
payload, _ := json.Marshal(MyJobParam{OrderID: 42})
client := global.QueueClient.Load()
if client == nil {
    return errors.New("任务队列不可用")
}
taskID, err := client.Enqueue(ctx, "my_job", payload,
    cmfqueue.WithQueue("default"),
    cmfqueue.WithDelay(5*time.Minute), // 可选：延迟执行
)
```

未指定目标队列时，优先使用 `default`；配置没有 `default` 时使用字典序首个队列。管理 API 始终显式投递到请求中指定的队列。

投递端按字节限制载荷，并串行检查当前 Client 的队列存量后再入队。`max_queue_size` 包含 pending、active、scheduled、retry、archived、completed，达到阈值返回 `ErrQueueFull`；`max_payload_bytes` 超限返回 `ErrPayloadTooLarge`。配置值缺省或非正数时使用上述默认值。入队等待和写入的上下文预算最多 5 秒。

多个 Client 或进程同时投递时，容量检查是软阈值，不能替代 Redis 的全局内存上限。生产环境应使用专用 Redis 实例，配置适合实例容量的 `maxmemory` 和 `maxmemory-policy noeviction`，并监控队列存量、Redis 内存和 4109 错误。仅使用不同 Redis DB 不能隔离内存。已存在的大载荷不会被自动改写；执行端会拒绝超过当前上限的任务并跳过重试。

**注册处理器**：在 `internal/tasks` 用 `tasks.Register("my_job", fn)` 注册的 Go 任务会被自动桥接为队列处理器（`TaskFunc` 的 `io.Writer` 日志经 zap 输出）；或在 `internal/queue` 的 `registerTaskHandlers` 中追加专用 `cmfqueue.Handler`（可用 `cmfqueue.GetTaskID(ctx)` / `GetAttempt(ctx)`）。

任务必须把 `ctx` 传给数据库、HTTP 等阻塞操作，并在长循环中检查取消信号。超时和取消不能强制终止忽略 `ctx` 的 Go 函数，处理器也应保持幂等以应对重试。

## Payload 安全约定

队列载荷是**数据而非指令**：手动入队（管理 API）的调用者是超管，新增任务处理器时必须把 payload 当作不可信输入——只用于携带参数，不得包含可执行语义。解析前校验 schema、字段枚举与大小上限；禁止把 payload 中的字符串直接拼进文件路径、命令行或 URL（路径穿越/SSRF 面与 HTTP handler 同等对待）。当前内置任务 `cleanup_expired_logs` 不读取 payload。

队列名在两处都做字符集白名单（字母、数字、下划线、中划线，1-64 字符）：`queue.queues` 配置声明时启动期校验（fail-fast），管理 API 传参时逐请求校验。因为队列名会拼入 asynq 的 Redis 键（`asynq:{queue}:...`），宽松字符集会允许构造出落键到非预期前缀的名字。

未来若允许插件向 `tasks.Registry` 注册任务，注册入口需重新过审：payload 解析面、处理器执行权限与队列资源占用都会随插件扩大。

## 管理 API（管理端认证与 RBAC 之后，再强制校验超管）

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
  "payload": "{}",
  "queue": "default",
  "delay_seconds": 0,
  "max_retry": 3
}
```

`name` 必须是已注册处理器，`payload` 是包含合法 JSON 的字符串，`queue` 必须在 `queue.queues` 中声明且通过字符集白名单，否则返回 4102/4103/4107/4111。达到存量阈值返回 4109，超过载荷字节上限返回 4110；管理 API 另保留原有 10240 字符限制。

成功的入队、重试、取消、删除操作记录审计日志。日志保留操作者、任务 ID 等元信息，不保存队列请求体，避免嵌套 JSON 字符串中的凭据进入日志。读取接口不新增操作日志。

## 任务状态流转

```
pending ──▶ active ──▶ completed（retention 到期自动删除）
               │ ▲
               ▼ │ 失败（指数退避，attempts < max_retry）
             retry
               │ attempts >= max_retry
               ▼
           archived（死信，可手动 retry 或删除；按归档时间保留 7 天）
```

死信清理在启动时立即执行，之后每 24 小时执行一次；每批最多 100 条，在 Redis 内原子选择并删除，不把载荷读入应用内存。归档时间采用当前 Asynq v0.26 的 archived zset 时间戳，包括手动归档任务；正在重试或刚重新归档的任务不会被当作旧死信删除。升级 Asynq 时需运行 `queue` 包集成测试验证存储格式。

asynqmon 监控页（`enable_monitor: true` 时）固定只读，并强制校验超管；`monitor_path` 必须是 `/api/v1/admin/auth/` 下的完整静态路径，根页面、静态资源和子 API 使用相同鉴权。队列资源在 HTTP 请求结束后关闭；全局句柄使用原子加载/交换，关闭后新调用应处理不可用错误。
