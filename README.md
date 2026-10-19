# 陪玩报单系统 · 后端（go-admin）

基于 Go + Fiber v3 + Ent 的管理后台 API，承载 **陪玩报单系统**（业务代号 `esports`）的完整服务端能力：商品与报价、报单与派单、服务与结单、售后与退款、钱包与储值卡、分成结算与提现、会员与礼物、考核与报表，以及通用后台能力（双身份认证、RBAC、验证码、附件、SSE、插件、任务队列、定时任务、OAuth）。

## 仓库分工

本仓库是三套代码中的服务端，三者独立管理源码、共同组成一套系统：

| 仓库 | 角色 | 技术栈 | 身份 |
| --- | --- | --- | --- |
| **go-admin**（本仓库） | 后端 API | Go + Fiber v3 + Ent + Casbin | `admin` 管理端 / `user` 用户端 |
| **go-admin-web**（同级目录） | 管理端 / 客服端 | Vue 3 + TS + Vite + Arco Design | 客服、超级管理员 |
| **e-sports-uniapp**（同级目录） | 客户端 / 陪玩端 / 移动客服端 | Vue 3 + TS + uni-app | 客户、陪玩、客服 |

> 后端通过 `go:embed` 把管理端构建产物打进二进制；开发时管理端 Vite 将 `/api` 代理到 `127.0.0.1:9501`。

## 功能特性

### 平台能力

- **双身份认证**：管理端与用户端使用独立 JWT 密钥、独立过期时间，支持刷新令牌
- **权限控制**：Casbin v3 多域 RBAC（`sub, dom, obj, act`）+ 权限码菜单/按钮/API 三级校验
- **登录安全**：RSA 加密登录、验证码挑战票据、登录限流、黑名单、Nonce 防重放
- **文件存储**：本地磁盘与 S3 兼容存储、分片上传、附件权限校验
- **实时消息**：SSE 站内信（ticket 鉴权）+ 短信 / 邮件 / 极光推送可选通道
- **插件系统**：进程外插件（gRPC / HTTP / stdio）、Ed25519 包签名与信任公钥
- **任务队列**：默认 asynq（Redis），可切换 RabbitMQ / NATS / NSQ / Kafka / AMQP 1.0
- **定时任务**：Cron 管理端可视化 + Go 任务注册表，执行日志独立落盘
- **其他**：OAuth 登录与绑定、Swagger 注解生成、Ent 非破坏性自动迁移、结构化日志轮转

### 业务模块（`esports` 功能集）

| 模块 | 能力 |
| --- | --- |
| 商品与定价 | 商品父实体、规格组、SKU、产品分类、游戏分区、技能等级、停服申请与逐单处置 |
| 报单与订单 | 即时/预约报单、幂等创建、名额占用、报价版本、改价/改期/取消、存单、加钟、历史补录 |
| 排单与派单 | 席位看板、指派/撤回/认领/转单/催单、抢单大厅、优先派单权（独占优先通知）、固排成队、接单上限 |
| 陪玩管理 | 档案与资格、在线状态、心跳保活、自助接单/开工/完工、考核定级、责任信用 |
| 服务与结单 | 服务凭证、结单规则、结单表单设计器、提交规则、串行审批流、待复核 |
| 售后与退款 | 受理→方案→客户确认→审批→执行状态机、补履约任务、陪玩追偿、SLA 超时升级、终态复核 |
| 资金与账本 | 客户余额、陪玩猫粮、只增流水、批次分摊、储值卡（开卡/核销/退卡）、充值活动、在线收款与回调 |
| 结算与提现 | 陪玩分成规则与结算单、客服提成（含争议冻结、作废与重开）、提现申请与审核打款、退款冲回、渠道成本与工作室剩余 |
| 会员与礼物 | 会员等级与成长值、生日礼遇、优先预约、礼物目录、代客送礼、防刷榜、贡献榜 |
| 客服工作台 | 客户档案与跟进、企微自动建档、话术库、订单池与待办聚合、数据隔离 |
| 风控与报表 | 风险客户标记、五类经营报表（总览/客服业绩/派单效率/售后质量/财务）、下钻明细 |
| 页面装修 | uni-app 客户首页草稿、版本发布与回滚、定时发布、素材库 |

详细实现状态、接口清单与人工验收步骤见 [internal/apps/esports/README.md](internal/apps/esports/README.md)。

## 技术栈

| 分类 | 组件 |
| --- | --- |
| 语言 | Go 1.25.1 |
| Web 框架 | Fiber v3.2.0 |
| ORM | Ent v0.14.5（MySQL；测试用 SQLite） |
| 基础框架 | [github.com/wuwuseo/cmf](https://github.com/wuwuseo/cmf) v1.1.0 |
| 认证 / 权限 | golang-jwt/jwt v5.3.1、Casbin v3.10.0、bcrypt |
| 缓存 | gocache v4（memory / Redis） |
| 任务队列 | hibiken/asynq v0.26.0，支持 rabbitmq / nats / nsq / kafka / amqp10 |
| 日志 | zap v1.28.0 + lumberjack 轮转 |
| 依赖注入 | google/wire v0.7.0（`internal/wire_gen.go`） |
| 文档 | swag v1.16.4（`docs/swagger.json`） |
| 数据库 | MySQL 5.7+ / 8.0+、Redis 6.2+ |

## 项目结构

```text
go-admin/
├── main.go                      # 入口：Wire 初始化 + 静态文件/SPA fallback
├── go.mod
├── AGENTS.md                    # 仓库协作规范与常用命令
├── config/
│   ├── config.yml               # 主配置（含逐项注释）
│   └── rbac_model.conf          # Casbin 模型
├── internal/
│   ├── app.go / provider.go / wire.go / wire_gen.go
│   ├── init.go                  # 初始化：迁移、种子数据、Casbin、队列、Cron、SSE
│   ├── router.go                # 路由注册与中间件装配
│   ├── cleanup.go
│   ├── apps/                    # 源码功能集
│   │   ├── apps.go / zz_generated.go
│   │   ├── cms/                 # 内容管理
│   │   └── esports/             # 陪玩报单业务（schema/ 与 README.md）
│   ├── handler/                 # HTTP 处理器
│   ├── service/                 # 业务逻辑
│   ├── middleware/              # JWT、权限、黑名单、数据域、审计等
│   ├── ent/                     # Ent 生成代码与通用 schema
│   ├── captcha/  casbin/  cron/  oauth/  plugin/  queue/  tasks/  utils/  global/
├── cmd/                         # captcha-reencrypt / cleanup-e2e / reset-admin-password
├── pluginapi/                   # 插件 gRPC 协议与运行时
├── web/dist/                    # 管理端构建产物（go:embed，需自行构建）
├── data/                        # 运行时：logs / storage / plugins / cron
├── docs/                        # Swagger 与专题文档
└── README.md
```

## 快速开始

### 环境要求

- Go 1.25.1 或更高版本
- MySQL 5.7+ / 8.0+
- Redis 6.2+（默认队列驱动 `asynq` 依赖；仅当显式切换其他 driver 时可不部署）
- Node.js 16+ 与 pnpm（构建管理端，用于生成 `web/dist`）

### 1. 初始化数据库

```sql
CREATE DATABASE go_admin CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
```

### 2. 调整配置

编辑 `config/config.yml`，至少确认数据库连接与队列 Redis：

```yaml
app:
  name: admin-api
  port: 9501
  secret: ""              # 留空启动自动生成随机密钥（重启后 token 失效）
  admin_secret: ""        # 同上，管理端专用
  admin_refresh_expires: 86400

database:
  connections:
    default:
      driver: mysql
      host: localhost
      port: 3306
      user: root
      password: "root"
      name: go_admin
      table_prefix: admin_

queue:
  driver: asynq
  redis_connection: queue_redis

redis:
  connections:
    queue_redis:
      addr: localhost:6379
      db: 1
```

生产环境不要在此文件写密钥，改用环境变量（见下文「环境变量」）。

### 3. 构建管理端并放入 `web/dist`

`main.go` 通过 `//go:embed web/dist` 内嵌前端，**缺少该目录时无法编译**：

```bash
cd ../go-admin-web
pnpm install
pnpm build                 # 产物在 go-admin-web/dist
```

将 `go-admin-web/dist` 的内容复制到 `go-admin/web/dist`（保留 `index.html` 与 `assets/`），再回到本仓库构建。

### 4. 构建并运行

```bash
go mod download
go build -o go-admin main.go
./go-admin
```

开发期可直接 `go run main.go`。

### 5. 首次登录

首次启动会创建超级管理员与 `admin` 角色：

- 账号：`admin`（或 `admin@example.com`）
- 密码：由 `ADMIN_INITIAL_PASSWORD` 环境变量指定；未设置时随机生成，**只在控制台输出一次**，请立即保存并登录后修改
- 管理端登录接口：`POST /api/v1/admin/public/login`

### 6. 启用 esports 业务

`esports` 功能集默认关闭（不播种权限、不挂载路由、不注册超时任务），需显式开启。**启用条件只看该变量，不再要求 `CMF_APP_ENV=development`**（运行环境只影响插件签名与调试开关）：

```bash
# Linux / macOS
export ESPORTS_EXPERIMENTAL=1

# PowerShell
$env:ESPORTS_EXPERIMENTAL="1"
```

启用后还需在系统配置中确认 `app.esports.payment_deadline_minutes` 为非零（默认 30），否则待付款订单无法支付。上线前请逐项完成 [esports README](internal/apps/esports/README.md) 中的验收清单。

访问地址：

- 管理端：<http://localhost:9501>
- API 基址：<http://localhost:9501/api/v1>
- 上传直链：`/uploads/*`（映射到 `./data/storage/public`）

## 配置说明

配置读取顺序为 `config/config.yml` 叠加环境变量（前缀 `CMF_`，层级用下划线连接，例如 `database.connections.default.password` → `CMF_DATABASE_CONNECTIONS_DEFAULT_PASSWORD`）。

### 主要配置段

| 配置段 | 说明 |
| --- | --- |
| `app` | 端口、调试模式、Swagger 开关、JWT 双密钥与过期时间、请求体上限 |
| `log` | 日志路径、控制台/文件输出、轮转大小与保留天数、操作日志保留天数 |
| `database` | 默认连接、驱动、表前缀、SSL |
| `redis` / `cache` | 命名连接（缓存与队列分离）、默认 TTL |
| `captcha` | 存储位置（`memory` / `redis`）、键前缀；多实例或 `prefork` 必须用 Redis |
| `queue` | 驱动、命名空间、并发、队列权重与优先级、重试/超时/保留期、载荷上限、监控页 |
| `filesystem` | `local` / `s3` 磁盘与根目录 |
| `plugin` | 是否强制签名、可信 Ed25519 公钥列表 |
| `notify` | 短信 / 邮件 / 推送渠道，未配置即自动禁用 |
| `casbin` | 默认域与多域模型（内联 `model_text` 或 `model_path`） |
| `attachment` | 是否启用 `X-Accel-Redirect`（生产建议经 Nginx 下发） |

### 环境变量

| 变量 | 用途 |
| --- | --- |
| `CMF_APP_ENV` | 运行环境；`production` 时强制要求插件签名 |
| `CMF_APP_SECRET` | 用户端 JWT 密钥（生产必填强密钥） |
| `CMF_APP_ADMIN_SECRET` | 管理端 JWT 密钥（生产必填强密钥） |
| `ADMIN_INITIAL_PASSWORD` | 初始管理员密码；未设置则随机生成并输出到控制台 |
| `CMF_DATABASE_CONNECTIONS_DEFAULT_PASSWORD` | 数据库密码 |
| `CMF_CONFIG_SECRET_ACTIVE_VERSION` / `CMF_CONFIG_SECRET_KEY_V1` | 系统配置密钥版本与 AES-256-GCM 主密钥（Base64） |
| `CMF_PLUGIN_REQUIRE_SIGNATURE` / `CMF_PLUGIN_TRUSTED_PUBLIC_KEYS` | 插件签名开关与可信公钥 |
| `ESPORTS_EXPERIMENTAL` | `1` 时启用陪玩报单业务功能集 |
| `CMF_NOTIFY_SMS_ACCESS_KEY` 等 | 通知渠道密钥，按 `config.yml` 键名推导 |

> 密钥轮换后需重跑 `go run ./cmd/captcha-reencrypt --dry-run=false` 重新加密存量配置。

## API 与认证

所有业务接口位于 `/api/v1` 下，源码功能集统一挂载在 `/api/v1/esports`（如 `/api/v1/esports/admin/*`、`/api/v1/esports/user/*`、公开接口 `/api/v1/esports/skus`、`/api/v1/esports/page`）。

### 登录与令牌

| 场景 | 接口 |
| --- | --- |
| 用户登录 / 刷新 | `POST /api/v1/public/login`、`POST /api/v1/public/refresh-token` |
| 管理端登录 / 刷新 | `POST /api/v1/admin/public/login`、`POST /api/v1/admin/public/refresh` |
| RSA 公钥 | `GET /api/v1/public/public-key` |
| 验证码挑战 / 换票 | `POST /api/v1/public/captcha/challenges`、`POST /api/v1/public/captcha/verifications` |

- **加密登录**：先取公钥，再以密文字段搭配 `timestamp` / `nonce` / `key_id` 提交（用户端为 `encrypted_email`，管理端为 `encrypted_username`，密码统一为 `encrypted_password`）。
- **验证码**：换取的一次性票据通过 `X-Captcha-Token` 头携带；登录场景是否强制由验证码场景配置决定。
- **受保护接口**：携带 `Authorization: Bearer <token>`；管理端接口同时校验 Casbin 权限码。

```http
GET /api/v1/admin/auth/user
Authorization: Bearer <jwt_token>
```

### 文档

```bash
swag init -g main.go -o docs
```

Swagger UI 默认关闭（安全基线：`/docs` 无需认证即可查看全部 API）。仅在隔离的本地环境将 `app.swagger: true`（或 `app.debug: true`）后访问 <http://localhost:9501/docs>。

### 实时消息

- 申请票据：`POST /api/v1/admin/auth/sse/ticket`
- 建立连接：`GET /api/v1/admin/sse/connect?ticket=...`

## 开发指南

### 常用命令

```bash
go run main.go                      # 本地启动
go build -o go-admin main.go        # 构建
go test ./...                       # 全量测试（需 web/dist 存在）
go test -cover ./...                # 覆盖率
golangci-lint run                   # 静态检查（--fix 可自动修复）
```

### 代码生成

Ent 与源码功能集代码均为生成产物，**不要手工编辑**：

```bash
go generate ./internal/apps        # 汇总 apps 的 schema 与注册表
go generate ./internal/ent         # 生成 Ent 客户端
```

新增业务模块时，在 `internal/apps/<code>/` 同时声明配置、路由、权限与 Ent Schema，参见 [docs/source-apps.md](docs/source-apps.md)。

### 添加一个通用接口

1. 在 `internal/ent/schema/` 定义模型（如需要）并重新生成
2. 在 `internal/service/` 实现业务逻辑
3. 在 `internal/handler/` 编写处理器
4. 在 `internal/router.go` 注册路由并配置权限
5. 补充单元测试与必要的 Casbin 权限种子

### 测试约定

- 使用标准库 `testing`，测试文件与源码同目录（`*_test.go`）
- 数据库测试使用 Ent + 内存 SQLite，文件测试使用 `t.TempDir()`
- Bug 修复需补回归测试，覆盖授权、上传、定时任务与插件边界

## 部署

### 构建生产二进制

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -a -installsuffix cgo -ldflags '-w -s' -o go-admin main.go
```

> 生产代码不使用 C 依赖，`CGO_ENABLED=0` 可静态编译；`mattn/go-sqlite3` 只被测试引用。

### systemd 示例

```ini
[Unit]
Description=Esports Partner API
After=network.target mysql.service redis.service

[Service]
Type=simple
WorkingDirectory=/opt/esports/go-admin
ExecStart=/opt/esports/go-admin/go-admin
Restart=always
RestartSec=5
Environment=CMF_APP_ENV=production
Environment=ESPORTS_EXPERIMENTAL=1
# 密钥通过 EnvironmentFile 注入，不要写进仓库
EnvironmentFile=/etc/esports/api.env

[Install]
WantedBy=multi-user.target
```

### 反向代理要点

- 静态资源与 API 同域转发到 `9501`
- SSE 路径关闭代理缓冲（`proxy_buffering off`），并放宽读超时
- `client_max_body_size` 不小于 `app.body_limit`（默认 10 MiB）
- 启用 `attachment.x_accel_enabled` 时由代理直接下发私有附件

## 运维

| 项目 | 说明 |
| --- | --- |
| 应用日志 | `./data/logs/admin-api.log`（按 `log.max_size` / `max_backups` 轮转） |
| 定时任务日志 | `./data/logs/cron/`，保留 7 天 |
| 运行时目录 | `./data/logs`、`./data/storage`、`./data/plugins`、`./data/plugin-packages` |
| 队列监控 | 开启 `queue.enable_monitor` 后访问 `/api/v1/admin/auth/queue-monitor`（只读，需管理端认证） |
| 上线自检 | `GET /api/v1/esports/admin/readiness`，逐项回报开关、支付时限、收款渠道、通知与企微配置状态，不回显密钥 |
| 日志清理 | 任务 `cleanup_expired_logs` 按 `log.retention_days` 清理操作/登录日志（负数表示不清理） |

### 注册的后台任务

`esports` 功能集在启用后注册 **37 个**任务（由 Cron 管理端或队列调度）：

`esports_activate_scheduled_commission_rules`、`esports_apply_due_sku_suspensions`、`esports_apply_member_downgrade`、`esports_auto_offline_players`、`esports_auto_pause_disconnected_players`、`esports_auto_transfer_backlog_orders`、`esports_auto_trigger_upgrade_assessments`、`esports_check_aftersale_sla`、`esports_check_close_approval_timeout`、`esports_check_service_overtime`、`esports_deliver_notifications`、`esports_deliver_outbox`、`esports_detect_review_brushing`、`esports_expire_appointment_confirmations`、`esports_expire_customer_stored_value_intents`、`esports_expire_dispatch_invites`、`esports_expire_dispatch_priority_notice`、`esports_expire_member_level_overrides`、`esports_expire_orders`、`esports_expire_priority_booking_intents`、`esports_expire_recharge_intents`、`esports_expire_reschedule_proposals`、`esports_expire_stored_value_cards`、`esports_expire_wallet_bonus`、`esports_flag_high_risk_customers`、`esports_generate_cs_settlements`、`esports_generate_player_settlements`、`esports_generate_spot_checks`、`esports_publish_scheduled_pages`、`esports_rebuild_dispatch_priority`、`esports_reconcile_ledgers`、`esports_remind_dispatch_ladder`、`esports_run_player_level_review`、`esports_scan_aftersale_rules`、`esports_scan_close_deadline`、`esports_scan_dispatch_escalations`、`esports_schedule_repurchase_follow_ups`。

> 该清单与 `internal/apps/esports/*.go` 中 `tasks.Register` 的注册项一一对应（37 注册 / 37 种子，种子见 `internal/apps/esports/cron_seeds.go`）。其中 `esports_expire_member_level_overrides`（会员手动调级覆盖到期回落）与 `esports_expire_priority_booking_intents`（会员优先预约意向过期消解）为 2026-10 新增；两者对应的回落 / 消解在读路径也有惰性判定，因此任务未注册时能力仍自愈。

## 安全基线

- JWT 密钥为空或为已知弱默认值时启动自动生成随机值；生产必须显式配置强密钥
- 生产环境（`CMF_APP_ENV=production`）强制插件签名，且必须配置至少一个可信公钥
- Swagger UI 默认关闭，生产必须保持关闭
- 管理端接口统一走 Casbin 权限校验；资金类操作强制「申请与审批分离、不得自审」
- 密码使用 bcrypt + 独立盐值；登录日志与操作日志按保留期自动清理
- 渠道密钥、商户号等敏感配置使用 AES-256-GCM 加密后入库，主密钥只来自环境变量

## 相关文档

- [文档导航](docs/README.md) — Swagger 与专题文档索引
- [陪玩报单功能集说明](internal/apps/esports/README.md) — 逐模块实现状态、接口清单与人工验收步骤
- [源码功能集开发](docs/source-apps.md) — 功能集注册、校验、路由挂载与代码生成
- [GitHub OAuth 接入](docs/oauth-github.md)
- [插件协议](docs/plugin-protocols.md)
- [CMS 接口](docs/cms.md)
- [任务队列后端](docs/task-queue-backends.md)
- [API 规范](docs/swagger.json) — Swag 生成的 OpenAPI 2.0
- [AGENTS.md](AGENTS.md) — 仓库协作规范与常用命令

## 许可证

本项目采用 [MIT License](LICENSE)。
