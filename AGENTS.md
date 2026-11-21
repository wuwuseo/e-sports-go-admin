# AGENTS.md · 仓库协作与开发规范

本文件面向在本仓库工作的开发者与 AI 助手，说明项目边界、常用命令、代码生成规则与业务硬约束。更完整的功能说明见 [README.md](README.md)，陪玩报单业务的逐模块状态见 [internal/apps/esports/README.md](internal/apps/esports/README.md)。

## 1. 仓库定位与边界

- EsportsPartner 的服务端是 **[e-sports-go-admin](https://gitee.com/wanglei199104/e-sports-go-admin)**（Go + Fiber v3 + Ent + Casbin），即本仓库，承载通用后台能力与 `esports` 陪玩报单业务功能集。
- EsportsPartner 的管理端仓库是 **[e-sports-go-admin-web](https://gitee.com/wanglei199104/e-sports-go-admin-web)**，本地目录 `go-admin-web`；客户端 / 陪玩端 / 移动客服端仓库是 **[e-sports-uniapp](https://gitee.com/wanglei199104/e-sports-uniapp)**，本地目录 `e-sports-uniapp`；本仓库不包含它们的源码。
- 管理端构建产物经 `//go:embed web/dist` 打进二进制；`web/` 已在 `.gitignore` 中，属于本地构建产物，不要提交。
- 三层职责不要混：`handler` 只做参数解析与响应，`service` 承载业务规则与事务，`middleware` 处理认证、权限、审计等横切逻辑。

## 2. 环境与本地启动

要求 Go 1.25.1（以 `go.mod` 为准）、MySQL 5.7+/8.0、Redis 6.2+（默认队列驱动 `asynq`）。

```bash
# 1) 准备依赖与配置
go mod download
# 编辑 config/config.yml 的数据库与 queue_redis 连接

# 2) 首次启动（会创建 admin 账号与 admin 角色）
ADMIN_INITIAL_PASSWORD='<强密码>' go run main.go

# 3) 启用陪玩报单业务
ESPORTS_EXPERIMENTAL=1 go run main.go
```

Windows PowerShell 等价写法：

```powershell
$env:ADMIN_INITIAL_PASSWORD = '<强密码>'
$env:ESPORTS_EXPERIMENTAL = '1'
go run main.go
```

关键环境变量：

| 变量 | 说明 |
| --- | --- |
| `CMF_APP_ENV` | 运行环境；设为 `production` 时强制插件签名 |
| `CMF_APP_SECRET` / `CMF_APP_ADMIN_SECRET` | 用户端 / 管理端 JWT 密钥，生产必须显式配置 |
| `ADMIN_INITIAL_PASSWORD` | 初始管理员密码；未设置时随机生成并仅在控制台输出一次 |
| `ESPORTS_EXPERIMENTAL` | `1` 时启用 `esports` 功能集（默认关闭，不播种权限、不挂路由） |

`config/config.yml` 为基线配置，环境变量以 `CMF_` 前缀覆盖（如 `CMF_DATABASE_CONNECTIONS_DEFAULT_PASSWORD`）。

## 3. 常用命令

| 目的 | 命令 |
| --- | --- |
| 本地运行 | `go run main.go` |
| 构建 | `go build -o go-admin.exe main.go`（Linux 见 README 部署章节） |
| 业务包测试 | `go test ./internal/apps/esports ./internal/apps` |
| 全量测试 | `go test ./...` |
| 覆盖率 | `go test -cover ./...` |
| 静态检查 | `golangci-lint run` |
| 生成功能集注册表 | `go generate ./internal/apps` |
| 生成 Ent 代码 | `go generate ./internal/ent` |
| 生成 API 文档 | `swag init -g main.go -o docs` |
| 重新生成依赖注入 | `go run github.com/google/wire/cmd/wire gen ./internal`（仅改动 provider 时） |

> `go build ./...` 与 `go test ./...` 都需要 `web/dist/index.html` 存在。若本地没有构建管理端，请先从 `go-admin-web` 构建并复制 `dist` 到 `web/dist`，否则会因 `embed` 找不到文件而失败。

## 4. 目录与代码组织

| 路径 | 说明 |
| --- | --- |
| `main.go` | 入口：Wire 初始化、静态文件与 SPA fallback |
| `internal/init.go` | 迁移、种子数据（admin/角色/字典/系统配置）、Casbin、队列、Cron、SSE |
| `internal/router.go` | 路由与中间件装配 |
| `internal/handler/` | HTTP 处理器（保持薄） |
| `internal/service/` | 业务逻辑、事务边界、账本与幂等 |
| `internal/middleware/` | JWT、权限、黑名单、数据域、审计、限流 |
| `internal/apps/<code>/` | 源码功能集：`app.go` 声明配置/路由/权限，`schema/` 放 Ent 模型 |
| `internal/ent/` | Ent 客户端与通用 schema（除 `schema/`、`entc.go` 外均为生成代码） |
| `internal/tasks/` | Go 后台任务注册表（`tasks.Register`） |
| `internal/cron/` `internal/queue/` | 定时任务管理与任务队列运行时 |
| `internal/plugin/` `pluginapi/` | 进程外插件管理、协议与运行时 |
| `cmd/` | 辅助命令（`captcha-reencrypt`、`reset-admin-password`、`cleanup-e2e`、`verify-role-access`、`esports-demo-seed`、`esports-data-repair`、`esports-stored-value-export`） |
| `config/` `docs/` `data/` | 配置、Swagger 与专题文档、运行时数据（勿提交） |

## 5. 生成代码：只改源，不手改产物

以下文件由工具生成，**禁止手工编辑**：

- `internal/ent/` 下除 `schema/` 与代码生成程序 `entc.go` 外的全部文件
- `internal/ent/zz_tableprefix_gen.go`、`internal/ent/schema/zz_apps_gen.go`
- `internal/apps/zz_generated.go`
- `internal/wire_gen.go`
- `docs/docs.go`、`docs/swagger.json`、`docs/swagger.yaml`

修改 schema 或功能集后的正确顺序：

```bash
go generate ./internal/apps
go generate ./internal/ent
go build ./...
go test ./internal/apps ./internal
```

新增功能集时：

1. 在 `internal/apps/<code>/app.go` 的 `init()` 中 `cmfapp.Register(...)`，`code` 只用小写字母/数字，且不得占用保留字（`admin`、`public`、`user`、`plugin`、`attachments`、`auth`）。
2. 路由写功能集内相对路径（`/items` → `/api/v1/<code>/items`），鉴权选 `AuthPublic` / `AuthUser` / `AuthAdmin`。
3. **每条管理端路由都必须有方法 + 完整路径一致的 `PermAPI` 权限声明**；权限路径参数写 `{id}`，Fiber 路由写 `:id`；权限码以 `<code>:` 开头，`Parent` 指向同功能集权限。
4. Ent 模型放 `internal/apps/<code>/schema/`，类型名以功能集代码首字母大写开头，关系只引用同功能集 schema。
5. 提交全部生成产物。

详见 [docs/source-apps.md](docs/source-apps.md)。

## 6. 编码约定

- 每个改动文件执行 `gofmt`；导入分组保持标准库 / 第三方 / 本仓库三段。
- 命名：包名短小写；导出 `PascalCase`；未导出 `camelCase`；测试 `TestXxx`。
- 错误使用 `fmt.Errorf("...: %w", err)` 包装并保留上下文；面向用户的业务错误使用**中文提示**（沿用现有写法）。
- 金额一律用「分」为单位的整数（字段名 `*_cents`），禁止浮点；比例用基点 `*_bps` / 万分比 `*_bp`。
- 优先显式依赖注入（Wire），不要在 service 里新增全局单例；确需跨层共享时通过 `internal/global` 已有入口。
- 日志用 `zap`，禁止打印密钥、token、完整手机号、支付凭证等敏感信息。
- JSON 字段使用 `snake_case`；请求体校验使用现有 validator 标签。
- 涉及业务日（生日、当日上限、结算周期）按 UTC+8 口径计算，沿用现有实现。

## 7. 业务硬约束（`esports` 功能集）

以下规则是既有设计，不要为了「跑通流程」而绕过：

- **功能开关**：`ESPORTS_EXPERIMENTAL=1` 才注册路由、权限与超时任务；上线还要求 `app.esports.payment_deadline_minutes` 非零。
- **幂等**：对外写接口须带幂等键（通常 8–128 字符），以唯一索引兜底；重复提交返回既有单据，同键不同体判冲突。
- **资金与账本**：钱包流水、分成明细、成长值流水等为**只增**数据，不得更新或删除；余额一律由流水求和派生，不落冗余余额字段。
- **事务**：状态流转、额度扣减、流水写入、订单事件与事务外盒必须在同一事务内完成；退款额度用条件表达式（`refunded_cents + refund_reserved_cents <= payable_cents`）防并发超退。
- **职责分离**：审批人不得是申请人，执行人不得是登记人；资金类复核不得自审。
- **快照**：报价、分成规则、结单表单模板、渠道成本等按业务时点快照，后续规则变更不得追溯改写历史单据。
- **不假成功**：未配置的支付渠道、推送通道、插件能力必须明确失败或降级，不能返回伪成功。
- **系统配置**：新增配置项通过功能集 `Config` 声明（键为 `app.esports.*`），不要写死在代码里。

## 8. 权限与安全

- 新增受保护路由时，同时补齐：路由、`PermAPI` 权限声明、必要的菜单/按钮权限、Casbin 策略与数据域校验。
- 不要绕开 `PermissionMiddleware` 或自建平行鉴权；公开接口必须显式使用 `AuthPublic`，并假定调用方无身份、只返回完成该操作所需的最小信息。
- 保留现有安全边界：上传路径校验、私有附件访问控制、Cron SSRF 限制、敏感请求头过滤、插件签名与信任公钥校验。
- 生产环境：Swagger 关闭、插件强制签名、密钥经环境变量注入、数据库密码不写入仓库。
- 禁止提交密钥、凭证、`data/` 运行时内容、二进制与 `web/dist`。

## 9. 测试

- 使用标准库 `testing`，测试与源码同目录（`*_test.go`）。
- 数据库测试统一用 Ent 内存 SQLite：

  ```go
  db := enttest.Open(t, "sqlite3", "file:esports_xxx?mode=memory&cache=shared&_fk=1",
      enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
  defer db.Close()
  ```

  每个测试使用唯一 DSN，避免共享内存库互相污染；需要表前缀时按现有 helper 处理。
- 文件相关测试使用 `t.TempDir()`，不要写入仓库目录。
- 表驱动优先；断言业务错误时同时校验错误分类与关键文案。
- Bug 修复、权限边界、资金计算、幂等与并发路径必须补回归测试。
- 改动业务包时先跑 `go test ./internal/apps/esports`，再跑 `go test ./...`；全量测试前确认 `web/dist` 存在。

## 10. 提交与 Pull Request

提交信息遵循 Conventional Commits，附带范围，例如：

```text
feat(esports): add priority dispatch window
fix(order): prevent duplicate refund reservation
docs(readme): refresh local setup
refactor(service): extract ledger allocation
```

提交前自检：

```bash
gofmt -l ./internal ./cmd
go build ./...
go test ./internal/apps/esports ./internal/apps
golangci-lint run
```

PR 描述应包含：行为变化、配置/迁移影响、接口变更（若有请重新生成 Swagger）、验证命令与结果；涉及前端可见行为时附截图或请求示例。

## 11. 文档同步

- 启动方式、依赖、环境变量、部署变化 → 更新 [README.md](README.md)。
- `esports` 功能实现状态、接口清单、人工验收步骤 → 更新 [internal/apps/esports/README.md](internal/apps/esports/README.md)。
- 功能集机制、插件协议、任务队列、OAuth 等专题 → 更新 [docs/](docs/README.md) 下对应文档。
- 修改 Swag 注解后必须重跑 `swag init -g main.go -o docs` 并一并提交生成文件。
