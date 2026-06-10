# 数据权限闭环设计稿

- 日期：2026-06-10
- 主题：data-permission-closure
- 状态：待审核
- 作者：brainstorming 协作产出

---

## 1. 背景

`go-admin` 已落地角色级数据权限的"骨架"：

- 部门表与 CRUD 接口已就绪
- 角色实体新增 `data_scope` 与 `data_scope_department_ids`
- `DataPermissionService.ResolveAdminScope` 可计算管理员的有效数据范围
- 管理员列表 `ListAdmins` 已按 `CurrentUID` 过滤

但通过 brainstorming 复盘，链路上存在 6 个真实问题，导致功能未闭环：

| 编号 | 问题摘要 | 严重度 |
| --- | --- | --- |
| P1-A | `admin.department_id` 创建/更新链路未写入，新建管理员永远是 0 | 高 |
| P1-B | 详情、更新、启停、角色分配等按 ID 操作的接口未做数据范围校验，可绕过列表过滤 | 高 |
| P2-C | `/admins/:uid` 路由参数名与 handler 实际行为不一致（按 int 解析） | 中 |
| P2-D | 系统权限 seed 缺失 `/admins` 系列，普通角色无法被授权管理员管理接口 | 中 |
| P2-E | 系统权限 `Update` 允许把 `parent_id` 设为自身或后代，级联递归会无限循环 | 中 |
| 测试 F | `TestInitSystemPermissions_CreatesNinetyFiveDefaultSeeds` 期望值 95 已过期，应为补完后的新数字 | 低 |

本设计稿是这 6 个问题的统一修复方案。

## 2. 目标与非目标

### 2.1 目标

- 让数据权限作用到所有按 ID 操作管理员的接口（详情/更新/启停/角色分配）
- 让管理员创建/更新流程能正确写入 `department_id`，使部门维度的数据过滤产生意义
- 修复路由参数命名不一致导致的 400 错误
- 补全系统权限 seed，让 RBAC 能管理新引入的接口
- 给系统权限树增加自引用/环检测，防止级联递归翻车
- 同步测试断言，保证 `go test ./...` 全绿

### 2.2 非目标（YAGNI）

- 不引入 `DataScopeGuard` 中间件（守卫放在 Service 层）
- 不修改 `admin` / `user` 表的 schema（`DepartmentID` 字段已存在）
- 不改造 Casbin 模型
- 不实现老数据自动迁移（`department_id=0` 的存量账号由超管手动指派）
- 不引入 LLM 风格的 AgentRegistry / 中介者
- 不动前端代码（前端在独立仓库 `admin-web/web`，本次只动后端）

## 3. 关键决策（brainstorming 12 题结论）

| Q | 决策 |
| --- | --- |
| Q1 | 数据范围校验放在 **Service 层**（不在中间件、不在 Casbin） |
| Q2 | 操作者身份用 **OperatorContext 对象** 显式传入 Service |
| Q3 | 守卫规则：超管跳过、访问自己跳过、禁止启停自己、禁止修改自己角色 |
| Q4 | OperatorContext **复用 `EffectiveDataScope` 结构体**，仅增加 `IsSuperAdmin` 字段 |
| Q5 | `CreateAdminRequest.DepartmentID` 可选，默认 0 |
| Q6 | `department_id=0` 视为"未分配"，仅超管可见（保持过滤 `containsInt` 行为） |
| Q7 | 路由 `/admins/:uid*` 统一改为 `/admins/:id*`，handler 同步按 int 查询 |
| Q8 | 新增 seed 的 `Path` 字段使用 `{id}` 模板语法 |
| Q9 | 系统权限父子环错误返回独立错误码 `PERMISSION_PARENT_CYCLE` |
| Q10 | 数据范围越权返回 HTTP 403，复用 `ResponseForbidden`（不暴露 ID 是否存在） |
| Q11 | 测试 `CreatesNinetyFiveDefaultSeeds` 改为新精确数字（实施时按实际跑出 count 写） |
| Q12 | 执行节奏：spec 提交 → writing-plans → 并发子代理实施 |

## 4. 架构总览

```
HTTP Request
  ├─ Fiber Router            （路由参数统一为 :id）
  ├─ JWT Middleware          （注入 uid 到 Locals）
  ├─ Permission Middleware   （Casbin 检查路由权限）
  └─ Handler
       ├─ NewOperatorContext(currentUID)   ← 构造一次操作者上下文
       └─ Service.Method(ctx, op, targetID, req)
            ├─ ensureAdminInScope(op, targetID)   ← 数据范围守卫
            ├─ 自我保护检查                          ← 启停/改自己角色
            └─ 业务逻辑
```

设计原则：
- **守卫职责单一**：`ensureAdminInScope` 只做"目标 admin 是否在 op 范围内"，不混入业务逻辑
- **op 可空**：`op == nil` 表示内部调用（cron、seed、单元测试），直接放行
- **OperatorContext 一次构造、多处复用**：减少 `ResolveAdminScope` 的重复查询

## 5. 模块分解

| # | 模块 | 受影响文件 | 依赖 |
| --- | --- | --- | --- |
| **A** | admin DepartmentID 字段读写 | `internal/service/super_admin_service.go`、对应 handler | 无 |
| **B** | OperatorContext + 守卫 | `internal/service/data_permission.go`、`super_admin_service.go`、`role_assignment.go`、对应 handler | 依赖 A、C |
| **C** | 路由 `:uid` → `:id` 统一 | `internal/router.go`、`internal/handler/role_assignment.go`、admin 详情/启停 handler | 无 |
| **D** | seed 补 `/admins` 8 条 | `internal/service/system_permission.go` | 依赖 C（路径定型） |
| **E** | parent_id 环检测 | `internal/service/system_permission.go` Update | 无 |
| **F** | 测试同步与新增 | `internal/service/system_permission_test.go`、`internal/service/data_permission_test.go`（新增） | 依赖 A/B/D/E |

### 5.1 并发执行波次

- **波次 1（可并发）**：A、C、E
- **波次 2（可并发）**：D（依赖 C 的路径定型）、B（依赖 A 的字段、C 的路由定型）
- **波次 3（串行）**：F + 最终编译验证

## 6. 详细设计

### 6.1 模块 A：admin DepartmentID 字段读写

**类型变更**：

```go
// CreateAdminRequest 增加
DepartmentID int `json:"department_id"`

// UpdateAdminRequest 增加（指针，区分"未传"和"清空"）
DepartmentID *int `json:"department_id"`

// AdminResponse / AdminListItem 增加
DepartmentID int `json:"department_id"`
```

**写入点**：

```go
// CreateAdmin 的 .Save 链增加
.SetDepartmentID(req.DepartmentID)

// UpdateAdmin 中
if req.DepartmentID != nil {
    update.SetDepartmentID(*req.DepartmentID)
}
```

**读取点**：`GetAdminByID`、`ListAdmins` 返回结构填充 `DepartmentID`。

**老数据策略**：不动 seed，department_id=0 的账号在 Q6 决策下"仅超管可见"，由超管手动重新分派部门。

### 6.2 模块 B：OperatorContext + 守卫

**`EffectiveDataScope` 扩展**：

```go
type EffectiveDataScope struct {
    Scope         DataScope
    AdminID       int
    AdminUID      string
    DepartmentID  int
    DepartmentIDs []int
    IsSuperAdmin  bool   // 新增
}
```

`ResolveAdminScope` 内部的两个返回点（超管返回、普通返回末尾）都需要设置 `IsSuperAdmin`。

**新错误**：

```go
var (
    ErrAdminOutOfScope            = errors.New("admin out of data scope")
    ErrCannotToggleSelfStatus     = errors.New("cannot activate or deactivate yourself")
    ErrCannotModifySelfRoles      = errors.New("cannot modify your own roles")
)
```

**构造函数**（`DataPermissionService` 上）：

```go
// NewOperatorContext 从 currentUID 构造操作者上下文
// currentUID 为空时返回 nil（内部调用绕过校验）
func (s *DataPermissionService) NewOperatorContext(ctx context.Context, currentUID string) (*EffectiveDataScope, error) {
    if currentUID == "" {
        return nil, nil
    }
    return s.ResolveAdminScope(ctx, currentUID)
}
```

**守卫 helper**（包级函数，便于跨 service 复用，避免循环依赖）：

```go
// ensureAdminInScope 校验 targetAdminID 是否在 op 的数据范围内
//   op == nil          → 放行（内部调用）
//   op.IsSuperAdmin    → 放行
//   target.UID == op.AdminUID → 放行（访问自己）
//   target.DepartmentID == 0  → 拒绝（Q6: 未分配只对超管可见）
//   其他               → 按 op.Scope 校验
func ensureAdminInScope(ctx context.Context, client *ent.Client, op *EffectiveDataScope, targetAdminID int) error {
    if op == nil || op.IsSuperAdmin {
        return nil
    }
    target, err := client.Admin.Query().Where(admin.IDEQ(targetAdminID)).Only(ctx)
    if err != nil {
        if ent.IsNotFound(err) {
            return ErrAdminNotFound
        }
        return err
    }
    if target.UID == op.AdminUID {
        return nil
    }
    switch op.Scope {
    case DataScopeAll:
        return nil
    case DataScopeSelf:
        return ErrAdminOutOfScope
    case DataScopeDepartment, DataScopeDepartmentAndChild, DataScopeCustom:
        if target.DepartmentID == 0 {
            return ErrAdminOutOfScope
        }
        if !containsInt(op.DepartmentIDs, target.DepartmentID) {
            return ErrAdminOutOfScope
        }
    }
    return nil
}

// containsInt 若包内已有则复用，否则就近新增
```

**自我保护**（在具体方法内）：

```go
func (s *SuperAdminService) ActivateAdmin(ctx context.Context, op *EffectiveDataScope, adminID int) error {
    if op != nil && op.AdminID == adminID {
        return ErrCannotToggleSelfStatus
    }
    if err := ensureAdminInScope(ctx, s.client, op, adminID); err != nil {
        return err
    }
    // 原有逻辑
}

func (s *SuperAdminService) DeactivateAdmin(ctx context.Context, op *EffectiveDataScope, adminID int) error {
    if op != nil && op.AdminID == adminID {
        return ErrCannotToggleSelfStatus
    }
    if err := ensureAdminInScope(ctx, s.client, op, adminID); err != nil {
        return err
    }
    // 原有逻辑
}

// RoleAssignmentService.AssignAdminRoles
func (s *RoleAssignmentService) AssignAdminRoles(ctx context.Context, op *EffectiveDataScope, adminID int, req *AssignRolesRequest) error {
    if op != nil && op.AdminID == adminID {
        return ErrCannotModifySelfRoles
    }
    if err := ensureAdminInScope(ctx, s.client, op, adminID); err != nil {
        return err
    }
    // 原有逻辑
}
```

去掉了"内部方法/包级函数二选一"的歧义，统一用包级函数实现。

**接口签名变更总表**：

| 方法 | 旧签名 | 新签名 |
| --- | --- | --- |
| `SuperAdminService.GetAdminByID` | `(ctx, adminID)` | `(ctx, op *EffectiveDataScope, adminID)` |
| `SuperAdminService.UpdateAdmin` | `(ctx, adminID, req)` | `(ctx, op, adminID, req)` |
| `SuperAdminService.ActivateAdmin` | `(ctx, adminID)` | `(ctx, op, adminID)` |
| `SuperAdminService.DeactivateAdmin` | `(ctx, adminID)` | `(ctx, op, adminID)` |
| `RoleAssignmentService.GetAdminRoles` | `(ctx, adminID)` | `(ctx, op, adminID)` |
| `RoleAssignmentService.AssignAdminRoles` | `(ctx, adminID, req)` | `(ctx, op, adminID, req)` |

`ListAdmins` 已通过 `req.CurrentUID` 实现过滤，本次不动。

`CreateAdmin` 暂不加 op 参数（创建时尚未有"目标管理员"概念，权限由 Casbin 控制）。

**Handler 改造模板**：

```go
func GetAdminDetail(c fiber.Ctx) error {
    response := c.Locals("utils.Response").(*utils.Response)
    svc := c.Locals("service.SuperAdminService").(service.SuperAdminServiceInterface)
    dataPermSvc := c.Locals("service.DataPermissionService").(*service.DataPermissionService)

    id, err := strconv.Atoi(c.Params("id"))
    if err != nil {
        return response.Fail(global.ResponseErrorCode, "无效的管理员ID").ToJSON(c)
    }

    op, err := dataPermSvc.NewOperatorContext(c.Context(), getCurrentUID(c))
    if err != nil {
        return response.Fail(global.ResponseInternalServerError, err.Error()).ToJSON(c)
    }

    result, err := svc.GetAdminByID(c.Context(), op, id)
    if err != nil {
        return mapAdminError(response, c, err)
    }
    return response.Success(result).ToJSON(c)
}

// 复用的错误映射
func mapAdminError(response *utils.Response, c fiber.Ctx, err error) error {
    switch err {
    case service.ErrAdminOutOfScope:
        return response.Fail(global.ResponseForbidden, "没有权限访问该资源").ToJSON(c)
    case service.ErrCannotToggleSelfStatus:
        return response.Fail(global.ResponseSelfToggleForbidden, "不能对自己执行启用/禁用").ToJSON(c)
    case service.ErrCannotModifySelfRoles:
        return response.Fail(global.ResponseSelfRoleModifyForbidden, "不能修改自己的角色").ToJSON(c)
    case service.ErrAdminNotFound:
        return response.Fail(global.ResponseUserNotFound, "管理员不存在").ToJSON(c)
    default:
        return response.Fail(global.ResponseInternalServerError, err.Error()).ToJSON(c)
    }
}
```

`getCurrentUID(c)` 是已有的 Locals 读取 helper（若无则就近新增）。

### 6.3 模块 C：路由参数命名统一

**`internal/router.go` 改动**：

```go
adminAuth.Get("/admins/:id", handler.GetAdminDetail)               // 原 :uid
adminAuth.Put("/admins/:id", handler.UpdateAdminByID)              // 不变
adminAuth.Get("/admins/:id/roles", handler.GetAdminRoles)          // 原 :uid
adminAuth.Put("/admins/:id/roles", handler.AssignAdminRoles)       // 原 :uid
adminAuth.Put("/admins/:id/activate", handler.ActivateAdmin)       // 不变
adminAuth.Put("/admins/:id/deactivate", handler.DeactivateAdmin)   // 不变
```

**handler 改动**：

- `GetAdminDetail`：`c.Params("uid")` → `c.Params("id")`，按 int 查询
- `GetAdminRoles`：`c.Params("uid")` → `c.Params("id")`
- `AssignAdminRoles`：`c.Params("uid")` → `c.Params("id")`

**Swagger 注释**：`@Param uid` → `@Param id`，`Param description` 同步改为"管理员ID"。

**重要变更**：`GetAdminDetail` 当前是按 UID 字符串查询的，需要调整为按 int ID 查询。前端 admin-web 调用此接口时需要传 int ID，但前端调用代码本就传 ID（前端工程未在本仓库，由独立提示词指导前端同步）。

### 6.4 模块 D：seed 补 `/admins` 系列

在 `system_permission.go` 的 seed 列表中追加 8 条（ID 从 110 起，避开现有占用）：

```go
{ID: 110, Domain: "default", Type: SystemPermissionTypeAPI,
  Code: "admin:list", Name: "管理员列表",
  Path: "/api/v1/admin/auth/admins", Method: "GET"},
{ID: 111, Domain: "default", Type: SystemPermissionTypeAPI,
  Code: "admin:create", Name: "创建管理员",
  Path: "/api/v1/admin/auth/admins", Method: "POST"},
{ID: 112, Domain: "default", Type: SystemPermissionTypeAPI,
  Code: "admin:detail", Name: "管理员详情",
  Path: "/api/v1/admin/auth/admins/{id}", Method: "GET"},
{ID: 113, Domain: "default", Type: SystemPermissionTypeAPI,
  Code: "admin:update", Name: "更新管理员",
  Path: "/api/v1/admin/auth/admins/{id}", Method: "PUT"},
{ID: 114, Domain: "default", Type: SystemPermissionTypeAPI,
  Code: "admin:roles:get", Name: "查看管理员角色",
  Path: "/api/v1/admin/auth/admins/{id}/roles", Method: "GET"},
{ID: 115, Domain: "default", Type: SystemPermissionTypeAPI,
  Code: "admin:roles:put", Name: "分配管理员角色",
  Path: "/api/v1/admin/auth/admins/{id}/roles", Method: "PUT"},
{ID: 116, Domain: "default", Type: SystemPermissionTypeAPI,
  Code: "admin:activate", Name: "启用管理员",
  Path: "/api/v1/admin/auth/admins/{id}/activate", Method: "PUT"},
{ID: 117, Domain: "default", Type: SystemPermissionTypeAPI,
  Code: "admin:deactivate", Name: "禁用管理员",
  Path: "/api/v1/admin/auth/admins/{id}/deactivate", Method: "PUT"},
```

ID 段位规划：现有 seed 占用 ≤ 104；本次 110-117 预留空隙避免冲突。`SortOrder` 字段按 seed 现有惯例填写或省略（由默认值兜底）。

### 6.5 模块 E：parent_id 环检测

在 `SystemPermissionService.Update` 的 `SetParentID` 之前增加：

```go
if req.ParentID != nil && *req.ParentID != 0 {
    if *req.ParentID == id {
        return ErrPermissionParentCycle
    }
    isDescendant, err := s.isDescendantOf(ctx, *req.ParentID, id)
    if err != nil {
        return fmt.Errorf("检查父级环依赖失败: %w", err)
    }
    if isDescendant {
        return ErrPermissionParentCycle
    }
}
```

**新增方法**：

```go
// isDescendantOf 判断 candidateID 是否是 ancestorID 的后代
// 通过自下而上遍历 parent_id 链实现，带深度上限防御
func (s *SystemPermissionService) isDescendantOf(ctx context.Context, candidateID, ancestorID int) (bool, error) {
    const maxDepth = 100
    current := candidateID
    visited := make(map[int]struct{}, maxDepth)
    for i := 0; i < maxDepth; i++ {
        if current == 0 {
            return false, nil
        }
        if current == ancestorID {
            return true, nil
        }
        if _, seen := visited[current]; seen {
            // 数据已损坏存在环，直接返回 true 防止下一步操作放大问题
            return true, nil
        }
        visited[current] = struct{}{}
        node, err := s.client.SystemPermission.Query().
            Where(systempermission.IDEQ(current)).
            Only(ctx)
        if err != nil {
            if ent.IsNotFound(err) {
                return false, nil
            }
            return false, err
        }
        current = node.ParentID
    }
    return false, fmt.Errorf("权限层级超过最大深度 %d", maxDepth)
}
```

**新错误**：

```go
var ErrPermissionParentCycle = errors.New("permission parent cycle detected")
```

handler 把 `ErrPermissionParentCycle` 映射为 HTTP 400 + 独立错误码 `ResponsePermissionParentCycle`。

### 6.6 错误码与响应映射

在 `internal/global/msg.go` 增加：

```go
const (
    ResponsePermissionParentCycle   = 9005
    ResponseSelfToggleForbidden     = 9007
    ResponseSelfRoleModifyForbidden = 9008
)

// 同步往 ResponseMsg map 中添加对应中文文案
```

**错误码总表**：

| 错误 | HTTP | code | 文案 |
| --- | --- | --- | --- |
| `ErrAdminOutOfScope` | 403 | `ResponseForbidden` | 没有权限访问该资源 |
| `ErrCannotToggleSelfStatus` | 400 | `ResponseSelfToggleForbidden` | 不能对自己执行启用/禁用 |
| `ErrCannotModifySelfRoles` | 400 | `ResponseSelfRoleModifyForbidden` | 不能修改自己的角色 |
| `ErrPermissionParentCycle` | 400 | `ResponsePermissionParentCycle` | 不能将权限的父级设为自身或其后代 |

### 6.7 模块 F：测试同步与新增

**修改已有测试**：

```go
// system_permission_test.go
// TestInitSystemPermissions_CreatesNinetyFiveDefaultSeeds
//   → 重命名为 TestInitSystemPermissions_CreatesAllDefaultSeeds
//   → 期望 count 改为实施完成后的实际数字（预期 ≥ 112）
```

**新增测试**：

| 测试 | 覆盖点 |
| --- | --- |
| `TestDataPermissionService_NewOperatorContext_Nil` | currentUID="" 时返回 nil |
| `TestDataPermissionService_NewOperatorContext_SuperAdmin` | 超管返回 `IsSuperAdmin=true` |
| `TestSuperAdminService_EnsureAdminInScope_SuperAdmin` | 超管直接放行 |
| `TestSuperAdminService_EnsureAdminInScope_Self` | UID 相同直接放行 |
| `TestSuperAdminService_EnsureAdminInScope_Department_Hit` | 目标部门命中 → 放行 |
| `TestSuperAdminService_EnsureAdminInScope_Department_Miss` | 目标部门未命中 → 返回 `ErrAdminOutOfScope` |
| `TestSuperAdminService_EnsureAdminInScope_TargetDeptZero` | 目标 `department_id=0` 非超管 → 拒绝 |
| `TestSuperAdminService_ActivateSelf_Forbidden` | op.AdminID==target → 返回 `ErrCannotToggleSelfStatus` |
| `TestSuperAdminService_DeactivateSelf_Forbidden` | 同上 |
| `TestRoleAssignmentService_AssignSelfRoles_Forbidden` | 返回 `ErrCannotModifySelfRoles` |
| `TestSystemPermission_Update_RejectsSelfParent` | parent_id == id → `ErrPermissionParentCycle` |
| `TestSystemPermission_Update_RejectsDescendantParent` | parent 为后代 → `ErrPermissionParentCycle` |
| `TestSystemPermission_IsDescendantOf_BrokenCycle` | 数据已损坏含环时返回 true 而非死循环 |
| `TestInitSystemPermissions_IncludesAdminManagementSeeds` | 断言 8 条 admin 权限存在 |

## 7. 实施顺序

```
波次 1（并发 3 个子代理）
  ├─ Sub-A: 模块 A admin DepartmentID 字段读写
  ├─ Sub-C: 模块 C 路由 :uid → :id
  └─ Sub-E: 模块 E parent_id 环检测

波次 2（并发 2 个子代理，依赖波次 1）
  ├─ Sub-B: 模块 B OperatorContext + 守卫
  └─ Sub-D: 模块 D seed 补 /admins 系列

波次 3（串行）
  └─ Sub-F: 模块 F 测试同步与新增 + 最终编译/测试
```

每个子代理任务完成后：
- 提交一个独立 commit
- 通过 `go build` 验证
- 通过相关单元测试

最终在 master 上合并所有变更，跑一遍 `go test ./...` 全绿后视为完成。

## 8. 验证清单

```powershell
# 编译
go build -o tmp/go-admin.exe main.go

# Service 层测试
go test ./internal/service/... -count=1

# 全量测试
go test ./... -count=1

# 代码质量
golangci-lint run
```

**功能联调矩阵**：

| 场景 | 操作者角色 | 目标 admin | 预期 |
| --- | --- | --- | --- |
| 详情读取 | 超管 | 任意 | 200 |
| 详情读取 | DataScopeAll | 任意 | 200 |
| 详情读取 | DataScopeSelf | 别人 | 403 |
| 详情读取 | DataScopeSelf | 自己 | 200 |
| 详情读取 | DataScopeDepartment | 同部门别人 | 200 |
| 详情读取 | DataScopeDepartment | 跨部门别人 | 403 |
| 详情读取 | DataScopeDepartmentAndChild | 子部门别人 | 200 |
| 详情读取 | DataScopeCustom（含部门 X） | 部门 X 别人 | 200 |
| 详情读取 | DataScopeCustom | 部门 X 之外别人 | 403 |
| 详情读取 | 任意非超管 | department_id=0 的账号 | 403 |
| 启用/禁用 | 任意 | 自己 | 400 + `SelfToggleForbidden` |
| 分配角色 | 任意 | 自己 | 400 + `SelfRoleModifyForbidden` |
| 系统权限 | 任意管理员 | 把节点 parent 设为自身 | 400 + `PermissionParentCycle` |
| 系统权限 | 任意管理员 | 把节点 parent 设为其子孙 | 400 + `PermissionParentCycle` |

## 9. 风险与缓解

| 风险 | 缓解 |
| --- | --- |
| Service 接口签名变更，所有 mock/测试都要改 | 用并发子代理一次性改全；编译失败即定位 |
| `:uid` 改 `:id` 是破坏性变更，前端可能 404 | 在最终回报中提示前端同步修改；本 spec 不包含前端改动 |
| seed 新增 8 条后总数变化未知 | 测试断言用"实施后实际跑出 count"写死，不预设 |
| `department_id=0` 老账号在 Q6 决策下不可被普通管理员看到 | 留下运维提示：超管登录后给所有老账号补部门归属 |
| `RoleAssignmentService` 引用 `SuperAdminService` 产生循环依赖 | 用包级函数 `ensureAdminInScope` 而非方法 |
| `isDescendantOf` 遇上脏数据环 | 用 `visited` map 短路，返回 true 拒绝操作而不是死循环 |

## 10. 不在本次范围的工作

- 前端 `admin-web/web` 表单加 `department_id` 选项（独立任务，由前端提示词指导）
- 数据迁移脚本（手动操作即可）
- DataScope 继承功能（之前用户单独询问，未列入本次范围）
- 操作日志增强（记录"被谁越权尝试"）
- 部门删除时的级联清理（已有部门服务实现，不在本次范围）

## 11. 完成定义

满足以下全部条件视为完成：

1. 模块 A-F 全部 commit 落到 master
2. `go build` 编译通过
3. `go test ./...` 全绿
4. 手工验证联调矩阵的 14 个场景
5. spec 与最终代码一致，无 TODO/占位符残留
