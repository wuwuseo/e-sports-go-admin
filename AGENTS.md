# AGENTS.md

## Overview

This document describes the agent-like runtime components implemented in Go-Admin. In this project, an "agent" means a self-contained component that performs work on behalf of the application, handles delegated operations, or coordinates another subsystem. The project does not currently include LLM-based conversational agents; instead, it implements operational agents such as plugin agents, scheduled task agents, message delivery agents, permission enforcement agents, and initialization agents.

The document is intended for both technical and non-technical stakeholders. Technical readers can use it to understand implementation details, extension points, APIs, and troubleshooting steps. Non-technical readers can use it to understand each agent's role in the system.

## Agent Inventory

| Agent | Primary Purpose | Main Files | Runtime Scope |
| --- | --- | --- | --- |
| Plugin Agent | Install, enable, disable, uninstall, restore, and proxy plugin processes | `internal/plugin/manager.go`, `internal/service/plugin.go`, `internal/handler/plugin.go` | Plugin lifecycle and extension execution |
| Scheduled Task Agent | Run shell, HTTP, and Go task jobs on cron schedules | `internal/cron/manager.go`, `internal/cron/executor.go`, `internal/service/cron_job.go` | Background job scheduling and execution |
| Message Delivery Agent | Store, broadcast, and push in-app messages through SSE | `internal/service/message_service.go`, `internal/service/message_broadcast_service.go`, `internal/handler/sse.go` | Real-time notifications and message inbox |
| Permission Enforcement Agent | Enforce RBAC access control using JWT domain data and Casbin policies | `internal/middleware/permission.go`, `internal/service/permission.go` | API authorization |
| System Permission Seed Agent | Seed and maintain canonical system permission definitions | `internal/service/system_permission.go`, `internal/init.go` | Boot-time permission initialization |
| Bootstrap Initialization Agent | Initialize application infrastructure and seed baseline data | `internal/init.go`, `internal/provider.go`, `internal/global/global.go` | Application startup |
| Operation Logging Agent | Record auditable operation data for administrative activities | `internal/service/operation_log.go`, `internal/handler/operation_log.go` | Audit and observability |
| File Attachment Agent | Manage uploads, storage, preview, movement, deletion, and grouped assets | `internal/service/attachment.go`, `internal/service/attachment_chunk.go`, `internal/handler/attachment.go` | File storage and media management |

## Architecture

### High-Level Runtime Model

Go-Admin uses a layered architecture:

```text
HTTP Request
  -> Fiber Router
  -> Middleware Agents
  -> Handler Layer
  -> Service Agents
  -> Ent ORM / Cache / Casbin / Filesystem / SSE / Plugin Runtime
  -> MySQL / Redis / Local Disk / S3 / Plugin Processes
```

Agents are not registered through a central `AgentRegistry`. Instead, each agent is instantiated by the router, bootstrap layer, or global initialization flow and injected into request context using Fiber `Locals` or stored in global runtime handles where appropriate.

### Core Interaction Patterns

#### 1. Request-Scoped Service Injection

Handlers receive agent services through Fiber locals:

```go
adminAuthBase := admin.Group("/auth", jwt.NewJWTMiddleware(config.App.AdminSecret), middleware.DomainMiddleware(), func(c fiber.Ctx) error {
    c.Locals("service.PermissionService", permissionService)
    c.Locals("service.MessageService", messageService)
    c.Locals("service.PluginService", pluginService)
    return c.Next()
})
```

This keeps handlers thin and allows services to be mocked in tests.

#### 2. Middleware-Orchestrated Authorization

The permission middleware reads JWT claims, resolves the current domain, synchronizes role links, and asks Casbin whether the user can access the requested resource.

```text
JWT Claims -> DomainMiddleware -> PermissionMiddleware -> Casbin Enforcer -> Handler
```

#### 3. Background Execution

The scheduled task agent loads enabled jobs from the database and registers them with the cron scheduler.

```text
CronJob DB Record -> CronManager -> Executor -> Log File -> DB last_run_at / next_run_at update
```

#### 4. Plugin Proxying

The plugin agent resolves an enabled plugin endpoint and proxies requests to the plugin runtime.

```text
/api/v1/plugin/:plugin_id/*
  -> ProxyPlugin Handler
  -> PluginService.Proxy
  -> Plugin Manager ResolveEndpoint
  -> HTTP/TCP or Unix socket endpoint
```

#### 5. Real-Time Message Push

The message delivery agent persists messages and optionally pushes events over SSE.

```text
MessageService.Create / Broadcast
  -> Database Insert
  -> Preference Filter
  -> SSE Hub Event
  -> Browser EventSource Client
```

## Agent Details

## 1. Plugin Agent

### Purpose

The Plugin Agent allows Go-Admin to be extended at runtime by installing and managing external plugin packages. It supports lifecycle operations and request proxying to plugin processes.

### Functionality

The Plugin Agent can:

- Install a plugin package from a local package path.
- Validate and safely extract plugin packages.
- Persist plugin metadata in the database.
- Enable a plugin and start its runtime process.
- Register plugin extension points.
- Restore previously enabled plugins during application startup.
- Disable or uninstall plugins.
- Resolve plugin endpoints and proxy HTTP traffic.

### Key Features

- Safe unzip and root-bound path validation.
- Process runtime integration through `github.com/wuwuseo/cmf/plugin`.
- Plugin status tracking: `installed`, `enabled`, `disabled`, `error`.
- Protected proxy route handling for authenticated admin paths.
- Header filtering during proxy requests to avoid forwarding sensitive headers such as `Authorization`, `Cookie`, and `Set-Cookie`.

### Main Components

- `internal/plugin/manager.go`: plugin lifecycle manager.
- `internal/service/plugin.go`: service wrapper used by handlers.
- `internal/handler/plugin.go`: HTTP API handlers and proxy handler.
- `internal/ent/schema/plugin.go`: plugin database schema.

### API Usage

#### Install a plugin

```http
POST /api/v1/admin/auth/plugins/install
Authorization: Bearer <admin_token>
Content-Type: application/json

{
  "package_path": "./data/plugin-packages/example-plugin.zip"
}
```

#### Enable a plugin

```http
POST /api/v1/admin/auth/plugins/example-plugin/enable
Authorization: Bearer <admin_token>
```

#### Disable a plugin

```http
POST /api/v1/admin/auth/plugins/example-plugin/disable
Authorization: Bearer <admin_token>
```

#### Proxy a plugin request

```http
GET /api/v1/plugin/example-plugin/public/status
```

Authenticated plugin admin routes use the protected proxy path:

```http
GET /api/v1/plugin/example-plugin/admin/auth/dashboard
Authorization: Bearer <admin_token>
```

### Configuration

The plugin manager is initialized with these defaults:

```go
pluginManager := adminplugin.NewManager(global.ORM, adminplugin.Options{
    PluginRoot: "./data/plugins",
    TempRoot:   "./data/plugin-tmp",
    Registry:   cmfplugin.NewMemoryExtensionRegistry(),
    Runtime:    cmfplugin.NewProcessRuntime(),
})
```

| Option | Description | Default |
| --- | --- | --- |
| `PluginRoot` | Directory where installed plugins are stored | `./data/plugins` |
| `TempRoot` | Temporary extraction directory | `./data/plugin-tmp` or OS temp fallback |
| `Registry` | In-memory extension registry | `cmfplugin.NewMemoryExtensionRegistry()` |
| `Runtime` | Runtime used to start plugin processes | `cmfplugin.NewProcessRuntime()` |

### Troubleshooting

| Symptom | Likely Cause | Resolution |
| --- | --- | --- |
| `插件包无效` | Package missing manifest or invalid zip structure | Verify plugin archive layout and manifest fields |
| `插件已安装` | Same `plugin_id` and version already exist | Uninstall existing version or install a new version |
| `插件路径非法` | Extracted path escapes plugin root | Rebuild package without unsafe paths |
| Plugin enable fails | Runtime process cannot start or health check fails | Inspect plugin executable, manifest engine config, and application logs |
| Proxy request fails | Plugin is not enabled or endpoint cannot be resolved | Enable plugin and verify health status |

## 2. Scheduled Task Agent

### Purpose

The Scheduled Task Agent runs background jobs using cron expressions. It supports three execution modes: shell commands, HTTP requests, and internal Go tasks.

### Functionality

The Scheduled Task Agent can:

- Load enabled jobs from the database at startup.
- Register jobs with a cron scheduler that supports seconds.
- Execute jobs immediately through API calls.
- Enable, disable, create, update, and delete jobs.
- Write execution logs for each run.
- Update `last_run_at` and `next_run_at` timestamps.
- Clean old job logs using an internal daily cleanup task.

### Key Features

- Uses `github.com/robfig/cron/v3` with seconds support.
- Supports `shell`, `curl`, and `task` job types.
- Shell commands must be registered in a whitelist registry before use.
- HTTP jobs include SSRF protections for protocol, host, IP, port, DNS rebinding, and redirects.
- Go tasks use a typed registry for safe in-process execution.

### Execution Types

#### Shell Executor

Shell jobs use registered commands only. Commands are executed directly through `exec.CommandContext`, not through a shell interpreter.

```text
command_name arg1 arg2
```

#### Curl Executor

Curl jobs use JSON command payloads:

```json
{
  "method": "POST",
  "url": "https://api.example.com/tasks/rebuild-index",
  "headers": {
    "Content-Type": "application/json"
  },
  "body": "{\"force\":true}",
  "timeout": 30
}
```

#### Go Task Executor

Go tasks are registered in code:

```go
func init() {
    Register("cleanup_expired_logs", CleanupExpiredLogs)
    Register("health_check", HealthCheck)
}
```

Example task signature:

```go
func HealthCheck(ctx context.Context, logger io.Writer) error {
    fmt.Fprintf(logger, "[%s] 执行健康检查...\n", time.Now().Format(time.DateTime))
    fmt.Fprintf(logger, "[%s] 系统运行正常\n", time.Now().Format(time.DateTime))
    return nil
}
```

### API Usage

#### Create a scheduled Go task

```http
POST /api/v1/admin/auth/cron-jobs
Authorization: Bearer <admin_token>
Content-Type: application/json

{
  "name": "Health Check",
  "description": "Run internal health check every minute",
  "type": "task",
  "expression": "0 */1 * * * *",
  "command": "health_check",
  "enabled": true
}
```

#### Create a scheduled HTTP task

```http
POST /api/v1/admin/auth/cron-jobs
Authorization: Bearer <admin_token>
Content-Type: application/json

{
  "name": "Notify external service",
  "type": "curl",
  "expression": "0 0/10 * * * *",
  "command": "{\"method\":\"POST\",\"url\":\"https://api.example.com/hooks/job\",\"timeout\":30}",
  "enabled": true
}
```

#### Run a job immediately

```http
POST /api/v1/admin/auth/cron-jobs/1/run
Authorization: Bearer <admin_token>
```

### Configuration

The CronManager is stored in `global.CronManager` and initialized with:

| Option | Description |
| --- | --- |
| Ent client | Reads and updates `CronJob` records |
| Log directory | Stores task run logs |
| Retention duration | Controls old log cleanup |
| Executor map | Maps `shell`, `curl`, and `task` to executor implementations |

### Troubleshooting

| Symptom | Likely Cause | Resolution |
| --- | --- | --- |
| `无效的cron表达式` | Expression does not match seconds-enabled cron parser | Use six-field cron syntax such as `0 */5 * * * *` |
| `Go task未注册` | `type=task` command does not exist in task registry | Register the task with `tasks.Register` |
| `Shell命令未注册` | Shell command not whitelisted | Register command in cron command registry before use |
| HTTP job rejected | URL blocked by SSRF safeguards | Use public `http` or `https` endpoint on ports `80` or `443` |
| Job created but not running | Job disabled or scheduler failed to register | Check `enabled`, expression, and application logs |

## 3. Message Delivery Agent

### Purpose

The Message Delivery Agent provides in-app notification capabilities. It stores messages in the database and can push real-time updates to connected clients through Server-Sent Events (SSE).

### Functionality

The Message Delivery Agent can:

- Create user-specific messages.
- Broadcast messages to all users or selected users.
- Store message metadata and optional HTML content.
- Track read and unread states.
- Return unread counts grouped by message type.
- Delete messages individually or in batches.
- Push real-time events through SSE.
- Replay missed messages when an SSE client reconnects.

### Key Features

- Message types: `system`, `business`, `task`.
- Message levels: `info`, `warning`, `error`.
- Event names: `message`, `unread_count`, `message_deleted`.
- User preference filtering through `MessagePreferenceService`.
- One-time SSE ticket issuance for secure connection setup.

### API Usage

#### Create or broadcast a message

```http
POST /api/v1/admin/auth/messages/broadcast
Authorization: Bearer <admin_token>
Content-Type: application/json

{
  "type": "system",
  "level": "info",
  "title": "System maintenance",
  "content": "The system will be upgraded tonight.",
  "extra": {
    "source": "ops"
  }
}
```

#### Get SSE ticket

```http
POST /api/v1/admin/auth/sse/ticket
Authorization: Bearer <admin_token>
```

Response example:

```json
{
  "code": 200,
  "message": "success",
  "data": {
    "ticket": "one-time-ticket",
    "expire_at": "2026-06-03T12:00:00+08:00"
  }
}
```

#### Connect with EventSource

```javascript
const ticket = "one-time-ticket";
const source = new EventSource(`/api/v1/admin/sse/connect?ticket=${encodeURIComponent(ticket)}`);

source.addEventListener("message", (event) => {
  const payload = JSON.parse(event.data);
  console.log("New message", payload);
});

source.addEventListener("unread_count", (event) => {
  const payload = JSON.parse(event.data);
  console.log("Unread count", payload);
});
```

### Configuration

The message agent depends on:

| Dependency | Purpose |
| --- | --- |
| `global.ORM` | Message persistence |
| `appInstance.Hub` | SSE connection hub |
| `appInstance.TicketStore` | One-time SSE ticket storage |
| `MessagePreferenceService` | Per-user push preference filtering |

### Troubleshooting

| Symptom | Likely Cause | Resolution |
| --- | --- | --- |
| `SSE 凭证服务未初始化` | TicketStore was not injected | Check bootstrap initialization and router locals |
| `sse hub not configured` | SSE hub missing from context | Verify `appInstance.Hub` is initialized |
| Browser receives no messages | User preference disabled or no active EventSource | Check message preferences and client connection |
| Missed events after reconnect | `lastEventId` not sent or old messages deleted | Pass `lastEventId` and verify message retention |

## 4. Permission Enforcement Agent

### Purpose

The Permission Enforcement Agent protects admin APIs using JWT identity, domain-aware RBAC, and Casbin policy rules.

### Functionality

The Permission Enforcement Agent can:

- Read authenticated user identity from JWT claims.
- Read authorization domain from JWT claims.
- Resolve the correct Casbin enforcer for the current domain.
- Synchronize user-role links with Casbin.
- Match both actual request paths and Fiber route templates.
- Allow access when policy rules permit the request.
- Deny unauthorized or forbidden requests with structured responses.

### Key Features

- Domain-aware RBAC model: `sub`, `dom`, `obj`, `act`.
- Super admin role detection through the `admin` role name.
- Route template normalization such as `/:id` to `/{id}`.
- Cache-aware permission service for role and user permission lookups.

### Casbin Model Example

```ini
[request_definition]
r = sub, dom, obj, act

[policy_definition]
p = sub, dom, obj, act

[role_definition]
g = _, _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = g(r.sub, p.sub, r.dom) && r.dom == p.dom && (r.obj == p.obj || p.obj == "*") && (r.act == p.act || p.act == "*")
```

### Permission Example

A role can be granted access to a route using object and action rules:

```json
{
  "role_name": "normal_admin",
  "domain": "default",
  "type": "api",
  "object": "/api/v1/admin/auth/users/{id}",
  "action": "GET",
  "description": "View user detail"
}
```

### Configuration

Permission behavior is controlled by the `casbin` section in `config/config.yml`:

```yaml
casbin:
  domains_default: "default"
  domains:
    - name: "default"
      auto_load: true
      model_text: |
        [request_definition]
        r = sub, dom, obj, act
        [policy_definition]
        p = sub, dom, obj, act
        [role_definition]
        g = _, _, _
        [policy_effect]
        e = some(where (p.eft == allow))
        [matchers]
        m = g(r.sub, p.sub, r.dom) && r.dom == p.dom && (r.obj == p.obj || p.obj == "*") && (r.act == p.act || p.act == "*")
```

### Troubleshooting

| Symptom | Likely Cause | Resolution |
| --- | --- | --- |
| `JWT中缺少域信息` | Token does not contain `domain` claim | Re-login or update token generation flow |
| `获取权限执行器失败` | Domain not configured or enforcer not loaded | Check `casbin.domains` configuration |
| `没有权限访问该资源` | No matching policy for route and method | Add or seed the required system permission and assign it to a role |
| Dynamic route cannot match | Policy uses actual ID path instead of route template or vice versa | Prefer route template format such as `/users/{id}` |

## 5. System Permission Seed Agent

### Purpose

The System Permission Seed Agent keeps the canonical permission definition table synchronized with the system's known API, menu, and button permissions.

### Functionality

The agent runs during seed initialization and calls `InitSystemPermissions(ctx)`. It is responsible for populating the `system_permissions` table so roles can later be assigned permissions from a consistent source of truth.

### Key Features

- Runs during application seed initialization.
- Supports API, menu, and button permission definitions.
- Works with role assignment and Casbin policy generation flows.
- Helps prevent missing permissions for newly added routes.

### Usage Pattern

The initialization flow calls the agent after baseline admin and Casbin rules are prepared:

```go
if err := service.InitSystemPermissions(ctx); err != nil {
    appInstance.Logger.Error("failed to initialize system permissions", zap.Error(err))
    return fmt.Errorf("failed to initialize system permissions: %w", err)
}
```

### Troubleshooting

| Symptom | Likely Cause | Resolution |
| --- | --- | --- |
| New API does not appear in permission list | Seed definitions were not updated or initialization failed | Update permission seed definitions and rerun initialization |
| Role has menu but API denied | API permission missing from assigned role | Assign matching API permission to the role |
| Initialization fails | Database unavailable or schema mismatch | Check DB connectivity and Ent migration status |

## 6. Bootstrap Initialization Agent

### Purpose

The Bootstrap Initialization Agent prepares the application runtime before serving requests. It wires global infrastructure and seeds required baseline data.

### Functionality

The initialization process can:

- Initialize ORM access.
- Initialize Casbin enforcer manager.
- Initialize cache, filesystem, logger, SSE, and other framework services through bootstrap configuration.
- Seed admin roles and default permissions.
- Reload Casbin policies after seed changes.
- Initialize regular admin role permissions.
- Initialize dictionary seed data.

### Key Features

- Centralizes startup behavior.
- Ensures baseline administrator access exists.
- Reloads policies after role and permission changes.
- Stores shared handles in `internal/global/global.go`.

### Global Runtime Handles

```go
var (
    ORM             *ent.Client
    Bootstrap       *bootstrap.Bootstrap
    EnforcerManager *cmfCasbin.EnforcerManager
    CronManager     *cron.CronManager
)
```

### Troubleshooting

| Symptom | Likely Cause | Resolution |
| --- | --- | --- |
| Application starts but admin cannot access routes | Seed data or Casbin reload failed | Check startup logs around seed initialization and policy reload |
| ORM is nil | Database initialization failed | Check `database.connections.default` configuration |
| Cron routes panic or fail | `global.CronManager` not initialized | Verify cron initialization during bootstrap |

## 7. Operation Logging Agent

### Purpose

The Operation Logging Agent provides audit visibility for administrative operations.

### Functionality

The agent stores operation log records including module, action, description, method, path, IP, user agent, request body, and user identity fields.

### Key Features

- Queryable operation logs through admin APIs.
- Structured operation metadata.
- Useful for audit, compliance, and debugging administrative behavior.

### API Usage

```http
GET /api/v1/admin/auth/operation-logs?page=1&page_size=20
Authorization: Bearer <admin_token>
```

```http
GET /api/v1/admin/auth/operation-logs/1
Authorization: Bearer <admin_token>
```

### Troubleshooting

| Symptom | Likely Cause | Resolution |
| --- | --- | --- |
| Operation logs missing | Logging middleware or service not called for that route | Verify route integration and service injection |
| User agent too long | User-Agent exceeds schema validator limit | Trim or normalize oversized client headers |
| Request body unavailable | Handler intentionally omitted sensitive body logging | Confirm logging policy for sensitive endpoints |

## 8. File Attachment Agent

### Purpose

The File Attachment Agent manages uploaded files and their metadata. It supports local and S3-compatible storage backends.

### Functionality

The agent can:

- Upload files.
- Track attachment metadata.
- Preview files.
- Move files between groups.
- Delete files individually or in batches.
- Manage attachment groups.
- Query chunk upload status and cancel chunk uploads.

### Key Features

- Uses configured filesystem disk.
- Supports local storage and S3-compatible storage.
- Provides grouped asset management.
- Separates file metadata from physical storage.

### API Usage

```http
POST /api/v1/admin/auth/attachments/upload
Authorization: Bearer <admin_token>
Content-Type: multipart/form-data

file=@avatar.png
```

```http
GET /api/v1/admin/auth/attachments/1/preview
Authorization: Bearer <admin_token>
```

### Configuration

Storage is configured in `config/config.yml`:

```yaml
filesystem:
  default: local
  disks:
    local:
      driver: local
      options:
        root: ./data/storage
    s3:
      driver: s3
      options:
        access_key: ""
        secret_key: ""
        region: ""
        bucket: ""
        endpoint: ""
```

### Troubleshooting

| Symptom | Likely Cause | Resolution |
| --- | --- | --- |
| Upload fails | Filesystem not initialized or body limit too small | Check `filesystem` and `app.body_limit` configuration |
| Preview fails | File missing from storage backend | Verify physical file path or S3 object exists |
| S3 upload fails | Invalid credentials, bucket, region, or endpoint | Check S3 disk options |

## Configuration Reference

### Application Configuration

```yaml
app:
  name: admin-api
  port: 9501
  debug: false
  secret: secret
  admin_secret: admin-secret-key
  admin_login_expires: 7200
  admin_refresh_expires: 86400
  body_limit: 10485760
```

Relevant agent impact:

- `admin_secret`: used by authenticated admin routes and agent APIs.
- `body_limit`: affects plugin package installation requests and file uploads.
- `debug`: affects diagnostics and runtime behavior through the framework.

### Cache Configuration

```yaml
cache:
  default: memory
  stores:
    memory:
      driver: memory
      default_ttl: 3600
    redis:
      driver: redis
      default_ttl: 3600
```

Relevant agent impact:

- Permission, role, blacklist, and configuration services can use cache when available.
- Cache invalidation is important after permission and role updates.

### Redis Configuration

```yaml
redis:
  default: redis
  connections:
    redis:
      addr: localhost:6379
      username: ""
      password: ""
      db: 0
```

Relevant agent impact:

- Used when Redis-backed cache is selected.
- Can affect authorization and admin operations if cache lookups fail.

### Casbin Configuration

```yaml
casbin:
  domains_default: "default"
  domains:
    - name: "default"
      auto_load: true
      model_text: |
        [request_definition]
        r = sub, dom, obj, act
```

Relevant agent impact:

- Permission Enforcement Agent depends on domain configuration.
- Multi-domain deployments should define one domain entry per tenant or business domain.

### Filesystem Configuration

```yaml
filesystem:
  default: local
  disks:
    local:
      driver: local
      options:
        root: ./data/storage
```

Relevant agent impact:

- File Attachment Agent uses this configuration for storage.
- Plugin packages are stored separately under `./data/plugins` by plugin manager options.

## Development Guidelines

### Adding a New Service Agent

1. Define a focused interface in `internal/service`.
2. Implement the service with explicit dependencies.
3. Inject the service in `internal/router.go` using Fiber locals.
4. Keep handlers thin and delegate business logic to the service.
5. Add tests for service behavior and handler integration.
6. If the agent needs startup behavior, wire it in `internal/init.go` or the provider/bootstrap flow.

Example interface pattern:

```go
type ExampleAgentInterface interface {
    Run(ctx context.Context, input ExampleInput) (*ExampleResult, error)
}

type ExampleAgent struct {
    client *ent.Client
}

func NewExampleAgent(client *ent.Client) *ExampleAgent {
    return &ExampleAgent{client: client}
}
```

### Adding a New Go Task Agent

Register an internal task:

```go
func init() {
    Register("daily_report", DailyReport)
}

func DailyReport(ctx context.Context, logger io.Writer) error {
    fmt.Fprintln(logger, "生成日报...")
    return nil
}
```

Then create a cron job with:

```json
{
  "name": "Daily Report",
  "type": "task",
  "expression": "0 0 8 * * *",
  "command": "daily_report",
  "enabled": true
}
```

### Adding a New Protected API Agent Route

1. Add a handler in `internal/handler`.
2. Add a service method in `internal/service`.
3. Register the route under `adminAuth` in `internal/router.go`.
4. Add a system permission definition for the route.
5. Assign the permission to the appropriate role.

Example permission object format:

```json
{
  "type": "api",
  "object": "/api/v1/admin/auth/example/{id}",
  "action": "GET"
}
```

## Security Notes

- Plugin packages must be trusted before installation. The manager performs path safety checks, but plugin code can still execute as a process.
- Plugin proxy requests intentionally filter sensitive headers.
- Shell jobs are only allowed through a command registry and are executed without shell interpolation.
- HTTP scheduled tasks include SSRF protections and should use public endpoints only.
- Permission checks are domain-aware; tokens must contain the correct domain claim.
- Super admin role name is `admin` and is protected from permission mutation.
- File upload limits should be configured according to deployment requirements.

## Operational Troubleshooting Checklist

Use this checklist when an agent does not behave as expected:

1. Check application logs in `data/logs`.
2. Confirm the route is registered in `internal/router.go`.
3. Confirm the service is injected into Fiber locals before the handler runs.
4. Confirm the authenticated token contains the expected user ID and domain.
5. Confirm Casbin policies exist for the route object and HTTP method.
6. Confirm database records exist and schema migrations are current.
7. Confirm cache is not serving stale role or permission data.
8. For cron jobs, inspect the job run log and `last_run_at` / `next_run_at` fields.
9. For plugin issues, inspect plugin status, manifest, install path, process health, and proxy endpoint.
10. For SSE issues, confirm ticket issuance, EventSource connection, and hub initialization.
11. For file issues, confirm storage backend credentials and physical file existence.

## Common Commands

Run the application:

```powershell
go run main.go
```

Run all tests:

```powershell
go test ./...
```

Run code quality checks:

```powershell
golangci-lint run
```

Regenerate Ent code after schema changes:

```powershell
go generate ./internal/ent
```

Build the application:

```powershell
go build -o go-admin.exe main.go
```

## Glossary

| Term | Meaning |
| --- | --- |
| Agent | A component that performs delegated work for the application |
| Handler | HTTP layer that parses requests and returns responses |
| Service | Business logic layer where most agents are implemented |
| Fiber Locals | Request-scoped dependency storage used by handlers and middleware |
| Casbin | RBAC authorization engine |
| Domain | Authorization namespace used for multi-tenant or multi-business separation |
| SSE | Server-Sent Events, used for real-time browser push |
| Plugin Runtime | Runtime responsible for starting and stopping plugin processes |
| Cron Expression | Schedule syntax used to run jobs periodically |

## Current Limitations

- There is no centralized `AgentRegistry`; agents are wired manually through router and bootstrap code.
- Plugin management paths are currently hard-coded during router initialization.
- Scheduled task HTTP executor intentionally blocks private networks and non-standard ports for security.
- SSE connections are one-way server-to-client streams; clients must use regular HTTP APIs for acknowledgements or actions.
- The project does not currently implement LLM or autonomous reasoning agents.
