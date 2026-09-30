# 任务队列后端与管理能力

`queue.driver` 默认为 `asynq`。队列运行时只连接所选 broker，不使用 MySQL 或额外 Redis 连接。管理页先请求 `/queue-jobs/capabilities`，再展示 `/queue-jobs/overview`；不存在的 broker 指标为 `null`。不支持的逐任务 API 返回业务错误码 **4112**。JetStream 流序号不存在或消息已被确认移除时返回 **4113**。

| driver | 原生概览 | 额外管理能力 |
| --- | --- | --- |
| `asynq` | 待处理、执行中 | 现有列表、详情、状态统计、重试、删除、取消、延时和重试选项 |
| `rabbitmq` | Management API 的 ready、unacked、消费者及队列/连接健康 | 逐任务列表、详情、状态统计、人工重试、删除、取消、延时及最大重试次数；死信队列 `<namespace>.<queue>.dead` |
| `nats`（JetStream） | durable consumer 的 pending、ack pending | 同上；死信 subject `<namespace>.dead.<queue>`；可按流序号读取仍保留的原始消息 |
| `nsq` | nsqd 的 channel depth、in-flight、客户端数和暂停状态 | 延时投递、最大重试次数、持久死信 topic `<namespace>_<queue>_dead`；暂停、恢复 channel（超管接口，记录操作日志）。无逐任务查询、删除或取消 |
| `kafka` | 消费组已提交 offset、末端 offset、lag 和分区数 | 逐任务列表、详情、状态统计、人工重试、删除、取消、延时及最大重试次数；死信 topic `<namespace>.<queue>.dead` |
| `amqp10` | AMQP 1.0 连接及当前进程消费者健康 | 无 broker 专属管理操作 |

RabbitMQ、NATS 和 Kafka 的逐任务状态分别保存在 **RabbitMQ stream 队列** `<namespace>.task-state`、**JetStream 文件流** `<NAMESPACE>_TASK_STATE` 和 **Kafka 单分区 topic** `<namespace>.task-state`。入队、worker 认领、完成、失败及管理操作写入同一 broker 的有序持久日志；各进程按 offset/序号增量同步。升级前已经排队、尚无状态记录的旧消息会在首次消费时登记。删除写入任务墓碑：仍在工作队列中的消息到达 worker 后被跳过并确认，不会被旧消息重新创建。取消写入跨进程信号，运行中的处理器通过 `ctx` 感知；取消后的失败仍按最大重试次数流转。计划任务到期后由运行中的 worker 投递，调度精度约为 1 秒。状态已写入但消息投递中断时，worker 会从日志恢复投递。人工重试使归档、待重试或计划任务立即重新投递。任务状态统计来自持久日志，broker 概览仍来自 broker 原生指标。

RabbitMQ 处理失败时重新发布带重试次数的持久消息，发布确认后才确认原消息；JetStream 和 Kafka 同样确认替代记录后才确认或提交原记录。NSQ 使用 broker 重投，超限后先发布到预先创建 channel 的死信 topic，再确认原消息。发布成功而确认或提交原消息之前崩溃时仍可能重复投递；处理器应保证幂等。AMQP 1.0 仍使用 broker 重投。JetStream 工作流使用 work-queue retention，已确认消息可能不再保留，按原始流序号查询此时会失败。

状态日志持久保存历史事件。Kafka 状态 topic 固定为 1 个分区并设置无限保留，RabbitMQ stream 与 JetStream 状态流也不设置过期时间；上线时要监控 broker 磁盘容量。已删除任务在任务列表中立即消失，但 broker 的物理日志及死信记录依各 broker 的保留策略回收。`retention`、`unique`、`strict_priority` 和 `max_queue_size` 仍只由 Asynq 提供；NSQ 与通用 AMQP 1.0 保留驱动，但受各自 broker 能力限制，不提供逐任务查询、删除或取消。

## 配置与切换

保留现有 `queue` 配置，只需设置对应驱动的连接参数。例如：

```yaml
queue:
  driver: rabbitmq
  namespace: admin
  rabbitmq:
    url: amqp://user:password@localhost:5672/
    management_url: http://localhost:15672
  queues:
    default: 10
  concurrency: 10
  timeout: 600
  max_retry: 3
  retention: 0
  max_queue_size: 0
```

其他连接项：`nats.url`、`nsq.nsqd`/`nsq.http`（可选 `nsq.lookupd`）、`kafka.brokers`/`kafka.group`/`kafka.partitions`/`kafka.replication_factor`、`amqp10.url`。`amqp10` 的目标地址需由 broker 预先配置或支持自动创建。非 Asynq 驱动要求 `strict_priority: false`、`retention: 0`、`max_queue_size: 0`、`enable_monitor: false`；RabbitMQ、NATS、NSQ、Kafka 可以设置 `max_retry` 并在手动入队时覆盖，AMQP 1.0 仍要求 `max_retry: 0`。RabbitMQ、NATS、NSQ 和 Kafka 支持手动延时入队。切换驱动前先排空旧 broker；历史消息不自动迁移。各驱动的资源按 `namespace` 隔离。新后端只使用 `queues` 的队列名，数值权重仅 Asynq 生效。Kafka 启动时创建缺失的 topic；分区数和副本数默认均为 1，生产环境应按集群规模设置。RabbitMQ 需要允许创建 stream 队列，NATS 需要启用 JetStream，Kafka 需要允许创建及调整状态 topic 的保留配置。

## 固定测试版本

Go 客户端版本记录在 `cmf/go.mod`：RabbitMQ `amqp091-go v1.15.0`、NATS `nats.go v1.53.1`、NSQ `go-nsq v1.1.0`、Kafka `franz-go v1.21.7`/`kadm v1.17.2`、AMQP 1.0 `go-amqp v1.7.0`。本地 broker 测试镜像固定为 `redis:7.4.6-alpine`、`rabbitmq:4.1.4-management`、`nats:2.12.4`、`nsqio/nsq:v1.3.0`、`apache/kafka:4.0.1`。AMQP 1.0 测试复用 RabbitMQ 4.1.4 的 `rabbitmq_amqp1_0` 插件（预先声明测试队列）。不要使用浮动 `latest` 标签。

Asynq 完整队列测试设置 `CMF_QUEUE_TEST_REDIS_ADDR` 指向隔离的 Redis 7.4.6 后运行 `go test ./queue -count=1`。`cmf/queue/native_integration_test.go` 通过 `QUEUE_TEST_RABBIT_URL`/`QUEUE_TEST_RABBIT_MANAGEMENT_URL`、`QUEUE_TEST_NATS_URL`、`QUEUE_TEST_NSQD`/`QUEUE_TEST_NSQ_HTTP`、`QUEUE_TEST_KAFKA_BROKER` 或 `QUEUE_TEST_AMQP10_URL`/`QUEUE_TEST_AMQP10_DECLARE_RABBIT` 指向真实 broker。只运行原生 broker 测试时设置 `CMF_QUEUE_TEST_ONLY_NATIVE=1`，再运行 `go test ./queue -run 'TestNativeBrokers|TestNativeTask|TestNativeLegacy|TestNativeDistributed' -count=1 -v`，无需 Redis。测试会为每次运行创建独立 namespace，验证发布、重试、死信、逐任务管理、跨进程取消、删除、延时、恢复、旧消息迁移、双 worker 并行消费、重复投递去重和队列概览。

断线重连测试另设 `QUEUE_TEST_RECONNECT_DRIVER`（`rabbitmq`/`nats`/`nsq`/`kafka`/`amqp10`）和 `QUEUE_TEST_RESTART_CONTAINER`（可重启的临时 Docker 容器名），保留对应连接环境变量，运行 `go test ./queue -run TestNativeBrokerReconnect -count=1 -v`。此测试会实际重启指定容器，随后在同一 Runtime 中再次投递和消费。
