# 任务队列后端与管理能力

`queue.driver` 默认为 `asynq`。队列运行时只连接所选 broker，不使用 MySQL，也不建立统一任务表。管理页先请求 `/queue-jobs/capabilities`，再展示 `/queue-jobs/overview`；不存在的 broker 指标为 `null`。不支持的逐任务 API 返回业务错误码 **4112**，不返回空列表或假零。JetStream 流序号不存在或消息已被确认移除时返回 **4113**。

| driver | 原生概览 | 额外管理能力 |
| --- | --- | --- |
| `asynq` | 待处理、执行中 | 现有列表、详情、状态统计、重试、删除、取消、延时和重试选项 |
| `rabbitmq` | Management API 的 ready、unacked、消费者及队列/连接健康 | 无逐任务操作 |
| `nats`（JetStream） | durable consumer 的 pending、ack pending | 按流序号查看仍保留的原始消息 |
| `nsq` | nsqd 的 channel depth、in-flight、客户端数和暂停状态 | 暂停、恢复 channel（超管接口，记录操作日志） |
| `kafka` | 消费组已提交 offset、末端 offset、lag 和分区数 | 无逐任务操作 |
| `amqp10` | AMQP 1.0 连接及当前进程消费者健康 | 无 broker 专属管理操作 |

新后端的任务 ID 仅作消息关联标识。broker 确认发布后才返回 ID；处理失败时 RabbitMQ、JetStream、NSQ、AMQP 1.0 使用 broker 重投，Kafka 在当前分区重试失败记录且不提交其 offset。JetStream 流使用 work-queue retention，已确认消息可能不再保留，按序号查询此时会失败。管理页不会把 broker 指标解释成 Asynq 任务状态。

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
  max_retry: 0
  retention: 0
  max_queue_size: 0
```

其他连接项：`nats.url`、`nsq.nsqd`/`nsq.http`（可选 `nsq.lookupd`）、`kafka.brokers`/`kafka.group`/`kafka.partitions`/`kafka.replication_factor`、`amqp10.url`。`amqp10` 的目标地址需由 broker 预先配置或支持自动创建。非 Asynq 驱动要求 `strict_priority: false`、`max_retry: 0`、`retention: 0`、`max_queue_size: 0`、`enable_monitor: false`；手动入队不能指定延时或最大重试。切换驱动前先排空旧 broker；历史消息不自动迁移。各驱动的资源按 `namespace` 隔离。新后端只使用 `queues` 的队列名，数值权重仅 Asynq 生效。Kafka 启动时创建缺失的 topic；分区数和副本数默认均为 1，生产环境应按集群规模设置。

## 固定测试版本

Go 客户端版本记录在 `cmf/go.mod`：RabbitMQ `amqp091-go v1.15.0`、NATS `nats.go v1.53.1`、NSQ `go-nsq v1.1.0`、Kafka `franz-go v1.21.7`/`kadm v1.17.2`、AMQP 1.0 `go-amqp v1.7.0`。本地 broker 测试镜像固定为 `redis:7.4.6-alpine`、`rabbitmq:4.1.4-management`、`nats:2.12.4`、`nsqio/nsq:v1.3.0`、`apache/kafka:4.0.1`。AMQP 1.0 测试复用 RabbitMQ 4.1.4 的 `rabbitmq_amqp1_0` 插件（预先声明测试队列）。不要使用浮动 `latest` 标签。

Asynq 完整队列测试设置 `CMF_QUEUE_TEST_REDIS_ADDR` 指向 Redis 7.4.6 后运行 `go test ./queue -count=1`。`cmf/queue/native_integration_test.go` 通过 `QUEUE_TEST_RABBIT_URL`/`QUEUE_TEST_RABBIT_MANAGEMENT_URL`、`QUEUE_TEST_NATS_URL`、`QUEUE_TEST_NSQD`/`QUEUE_TEST_NSQ_HTTP`、`QUEUE_TEST_KAFKA_BROKER` 或 `QUEUE_TEST_AMQP10_URL`/`QUEUE_TEST_AMQP10_DECLARE_RABBIT` 指向真实 broker。设置相应环境变量后运行 `go test ./queue -run TestNativeBrokers -count=1 -v`。测试会为每次运行创建独立 namespace，验证发布确认、失败重投、概览，以及 JetStream 按流序号读取和 NSQ 暂停/恢复。

断线重连测试另设 `QUEUE_TEST_RECONNECT_DRIVER`（`rabbitmq`/`nats`/`nsq`/`kafka`/`amqp10`）和 `QUEUE_TEST_RESTART_CONTAINER`（可重启的临时 Docker 容器名），保留对应连接环境变量，运行 `go test ./queue -run TestNativeBrokerReconnect -count=1 -v`。此测试会实际重启指定容器，随后在同一 Runtime 中再次投递和消费。