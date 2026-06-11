# 数据权限闭环加固设计稿（CreateAdmin 部门校验 + UpdateAdmin 部门迁移校验 + 创建原子性）

- 日期：2026-06-11
- 主题：data-permission-create-update-hardening
- 状态：待审核
- 上游：[2026-06-10-data-permission-closure-design.md](2026-06-10-data-permission-closure-design.md)

---

## 1. 背景

上一轮（2026-06-10）实施关闭了 admin 详情/列表/启停/角色分配的数据权限闭环，但 **CreateAdmin 与 UpdateAdmin 的部门字段写入**仍然没有受到守卫，加上 CreateAdmin 的角色分配存在 3 个潜在缺陷。

本次需要修复的 3 个真实问题：

| 编号 | 问题 | 严重度 |
| --- | --- | --- |
| P1-A | CreateAdmin 直接 `SetDepartmentID(req.DepartmentID)`，部门管理员可创建账号到任意部门 | 高 |
| P1-B | UpdateAdmin 仅校验"当前目标在范围内"，未校验"新 department_id 是否在范围内"，可越权迁移账号 | 高 |
| P2 | CreateAdmin 中 admin 先入库，再异步分配角色；无效 RoleIDs 静默忽略；PrimaryRoleID 可能不在 RoleIDs 中 | 中 |

## 2. 目标与非目标

### 2.1 目标

- 让 CreateAdmin 的 department_id 写入受到与查询过滤同一套规则的守卫
- 让 UpdateAdmin 改动 department_id 时同样受到守卫
- CreateAdmin 的 admin 记录 + admin_role 记录在同一个 ent 事务内插入；任一失败整体回滚
- 预校验 RoleIDs 全部存在且 Active；PrimaryRoleID 必须在 RoleIDs 中

### 2.2 非目标（YAGNI）

- 不把 Casbin 同步纳入 ent 事务（与现有 AssignAdminRoles 行为保持一致，失败仅记日志）
- 不引入 saga / outbox 等补偿机制
- 不抽 `requireWritableAdmin` 等更高层 helper（上一轮 review 建议但 spec 已 punt）
- 不动 ent schema、前端、Casbin 模型

## 3. 关键决策

| Q | 决策 |
| --- | --- |
| Q1 | CreateAdmin 的部门校验与查询过滤**完全同规则**：超管/op==nil 放行；DataScopeAll 放行；DataScopeSelf 拒绝；部门模式必须命中 `op.DepartmentIDs`；`department_id=0` 仅超管可设 |
| Q2 | UpdateAdmin 修改 department_id 时执行**与 Create 完全相同**的部门校验函数 |
| Q3 | CreateAdmin 采用**完整事务 + 严格预校验**：admin Create 与 admin_role Insert 同事务；预校验 RoleIDs 全有效；PrimaryRoleID 必须在 RoleIDs |

## 4. 架构

```
HTTP Request
  → Handler
       ├─ NewOperatorContext(currentUID)
       └─ Service.CreateAdmin(ctx, op, req)
            ├─ ensureDepartmentInScope(op, req.DepartmentID)      ← 新守卫
            ├─ 邮箱/用户名唯一性检查（已有）
            ├─ 预校验 RoleIDs 全有效（count == len） + PrimaryRoleID ∈ RoleIDs
            ├─ tx := client.Tx
            │    ├─ tx.Admin.Create()...
            │    └─ for r in validRoles: tx.AdminRole.Create()
            ├─ tx.Commit
            └─ Casbin 同步（事务外，失败仅记日志）

  → Service.UpdateAdmin(ctx, op, adminID, req)
       ├─ ensureAdminInScope(op, adminID)                          ← 已有
       ├─ ensureDepartmentInScope(op, *req.DepartmentID) if !nil   ← 新增
       └─ 原有 update.Save
```

## 5. 详细设计

### 5.1 新 helper：ensureDepartmentInScope

追加到 [data_permission.go](file:///d:/www/admin/internal/service/data_permission.go) 末尾：

```go
// ensureDepartmentInScope 校验目标部门是否在 op 的数据范围内
//   op == nil          → 放行（内部调用）
//   op.IsSuperAdmin    → 放行
//   DataScopeAll       → 放行
//   DataScopeSelf      → 拒绝
//   DataScopeDepartment/DepartmentAndChild/Custom：
//                        targetDepartmentID == 0 → 拒绝（未分配仅超管可设）
//                        其他要求命中 op.DepartmentIDs
//   未知 scope         → 拒绝（fail-closed）
func ensureDepartmentInScope(op *EffectiveDataScope, targetDepartmentID int) error {
    if op == nil || op.IsSuperAdmin {
        return nil
    }
    switch op.Scope {
    case DataScopeAll:
        return nil
    case DataScopeSelf:
        return ErrDepartmentOutOfScope
    case DataScopeDepartment, DataScopeDepartmentAndChild, DataScopeCustom:
        if targetDepartmentID == 0 {
            return ErrDepartmentOutOfScope
        }
        if !containsInt(op.DepartmentIDs, targetDepartmentID) {
            return ErrDepartmentOutOfScope
        }
        return nil
    default:
        return ErrDepartmentOutOfScope
    }
}
```

### 5.2 新错误（追加到 data_permission.go）

```go
// ErrDepartmentOutOfScope 目标部门超出当前操作者数据权限范围
var ErrDepartmentOutOfScope = errors.New("department out of data scope")
```

`ErrInvalidRoleIDs` / `ErrPrimaryRoleInvalid` **复用** [role_assignment.go](file:///d:/www/admin/internal/service/role_assignment.go) 已有的同名错误（实施时先 grep 确认，若不存在则按 plan 新增于 role_assignment.go 中以保持就近原则）。

### 5.3 错误码与文案

追加到 [internal/global/msg.go](file:///d:/www/admin/internal/global/msg.go)：

```go
const (
    // ...已有
    ResponseDepartmentOutOfScope = 9009
)

// ResponseMsg
ResponseDepartmentOutOfScope: "目标部门不在你的数据权限范围内",
```

### 5.4 SuperAdminServiceInterface 签名变更

```go
type SuperAdminServiceInterface interface {
    CreateAdmin(ctx context.Context, op *EffectiveDataScope, req *CreateAdminRequest) (*AdminResponse, error)  // 新增 op
    ListAdmins(ctx context.Context, req *AdminListRequest) (*ListResponse[*AdminListItem], error)
    GetAdminByID(ctx context.Context, op *EffectiveDataScope, adminID int) (*AdminResponse, error)
    UpdateAdmin(ctx context.Context, op *EffectiveDataScope, adminID int, req *UpdateAdminRequest) (*AdminResponse, error)
    ActivateAdmin(ctx context.Context, op *EffectiveDataScope, adminID int) error
    DeactivateAdmin(ctx context.Context, op *EffectiveDataScope, adminID int) error
}
```

### 5.5 CreateAdmin 重构

完整新实现：

```go
func (s *SuperAdminService) CreateAdmin(ctx context.Context, op *EffectiveDataScope, req *CreateAdminRequest) (*AdminResponse, error) {
    // P1-A：部门必须在 op 范围内
    if err := ensureDepartmentInScope(op, req.DepartmentID); err != nil {
        return nil, err
    }

    // 已有：邮箱 / 用户名唯一性
    if err := s.checkAdminUniqueness(ctx, req.Email, req.Username); err != nil {
        return nil, err
    }

    hashedPassword, salt, err := crypto.HashPassword(req.Password)
    if err != nil {
        return nil, err
    }

    // P2：预校验 RoleIDs + PrimaryRoleID
    var validRoles []*ent.Role
    if len(req.RoleIDs) > 0 {
        count, err := s.client.Role.Query().
            Where(role.IDIn(req.RoleIDs...), role.StatusEQ(role.StatusActive)).
            Count(ctx)
        if err != nil {
            return nil, fmt.Errorf("查询角色失败: %w", err)
        }
        if count != len(req.RoleIDs) {
            return nil, ErrInvalidRoleIDs
        }
        validRoles, err = s.client.Role.Query().
            Where(role.IDIn(req.RoleIDs...)).
            All(ctx)
        if err != nil {
            return nil, err
        }
        primaryID := req.PrimaryRoleID
        if primaryID == 0 {
            primaryID = req.RoleIDs[0]
        } else if !containsInt(req.RoleIDs, primaryID) {
            return nil, ErrPrimaryRoleInvalid
        }
        req.PrimaryRoleID = primaryID
    }

    // P2：admin Create + admin_role Insert 同事务
    tx, err := s.client.Tx(ctx)
    if err != nil {
        return nil, fmt.Errorf("开启事务失败: %w", err)
    }

    a, err := tx.Admin.Create().
        SetEmail(req.Email).
        SetPassword(hashedPassword).
        SetPasswordSalt(salt).
        SetUID(utils.GenerateUID()).
        SetUsername(req.Username).
        SetNickname(req.Nickname).
        SetName(req.Name).
        SetPhone(req.Phone).
        SetDepartmentID(req.DepartmentID).
        SetStatus(admin.StatusInactive).
        Save(ctx)
    if err != nil {
        _ = tx.Rollback()
        return nil, err
    }

    for _, r := range validRoles {
        _, err = tx.AdminRole.Create().
            SetAdminID(a.ID).
            SetRoleID(r.ID).
            SetIsPrimary(r.ID == req.PrimaryRoleID).
            Save(ctx)
        if err != nil {
            _ = tx.Rollback()
            return nil, fmt.Errorf("分配角色失败: %w", err)
        }
    }

    if err := tx.Commit(); err != nil {
        return nil, fmt.Errorf("提交事务失败: %w", err)
    }

    // Casbin 同步（事务外，与 AssignAdminRoles 行为一致）
    if len(validRoles) > 0 && global.EnforcerManager != nil {
        enforcer, enfErr := global.EnforcerManager.GetEnforcer("default")
        if enfErr == nil {
            for _, r := range validRoles {
                _, _ = enforcer.AddRoleForUserInDomain(a.UID, r.Name, "default")
            }
        }
    }

    return &AdminResponse{
        ID:           a.UID,
        Email:        a.Email,
        Username:     a.Username,
        Nickname:     a.Nickname,
        Name:         a.Name,
        Phone:        a.Phone,
        Avatar:       a.Avatar,
        Department:   a.Department,
        DepartmentID: a.DepartmentID,
        Position:     a.Position,
        Status:       string(a.Status),
    }, nil
}
```

说明：
- `checkAdminUniqueness` 是把现有 email/username 唯一性逻辑抽出的私有方法，实施时按当前代码结构决定是否实抽；若不抽则保留原内联检查
- 复用包级 `containsInt`（上一轮已加）

### 5.6 UpdateAdmin 增加部门校验

```go
func (s *SuperAdminService) UpdateAdmin(ctx context.Context, op *EffectiveDataScope, adminID int, req *UpdateAdminRequest) (*AdminResponse, error) {
    if err := ensureAdminInScope(ctx, s.client, op, adminID); err != nil {
        return nil, err
    }
    // P1-B：若改 department_id，新部门也必须在 op 范围内
    if req.DepartmentID != nil {
        if err := ensureDepartmentInScope(op, *req.DepartmentID); err != nil {
            return nil, err
        }
    }
    // ...原有逻辑保持
}
```

### 5.7 handler 改造

[admin_list.go](file:///d:/www/admin/internal/handler/admin_list.go) 中 `CreateAdmin`：

```go
func CreateAdmin(c fiber.Ctx) error {
    response := c.Locals("utils.Response").(*utils.Response)
    svc := c.Locals("service.SuperAdminService").(service.SuperAdminServiceInterface)
    dataPermSvc := c.Locals("service.DataPermissionService").(*service.DataPermissionService)

    var req service.CreateAdminRequest
    if err := c.Bind().Body(&req); err != nil {
        return response.Fail(global.ResponseErrorCode, "Invalid request body").ToJSON(c)
    }
    if req.Email == "" || req.Password == "" || req.Username == "" {
        return response.Fail(global.ResponseErrorCode, "邮箱、密码、用户名不能为空").ToJSON(c)
    }

    op, err := dataPermSvc.NewOperatorContext(c.Context(), getOperatorUID(c))
    if err != nil {
        return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
    }

    result, err := svc.CreateAdmin(c.Context(), op, &req)
    if err != nil {
        return mapAdminCreateError(c, response, err)
    }
    return response.Success(result).ToJSON(c)
}
```

新增 helper `mapAdminCreateError`（或扩展现有 `mapAdminScopeError` 增加 3 个 case），放在 admin_list.go：

```go
func mapAdminCreateError(c fiber.Ctx, response *utils.Response, err error) error {
    switch err {
    case service.ErrAdminEmailExists:
        return response.Fail(global.ResponseErrorCode, "邮箱已存在").ToJSON(c)
    case service.ErrAdminUsernameExists:
        return response.Fail(global.ResponseErrorCode, "用户名已存在").ToJSON(c)
    case service.ErrSuperAdminRoleAssignmentForbidden:
        return response.Fail(global.ResponseForbidden, err.Error()).ToJSON(c)
    case service.ErrDepartmentOutOfScope:
        return response.Fail(global.ResponseDepartmentOutOfScope, global.ResponseMsg[global.ResponseDepartmentOutOfScope]).ToJSON(c)
    case service.ErrInvalidRoleIDs:
        return response.Fail(global.ResponseErrorCode, "存在无效的角色 ID").ToJSON(c)
    case service.ErrPrimaryRoleInvalid:
        return response.Fail(global.ResponseErrorCode, "主角色必须在 role_ids 列表中").ToJSON(c)
    default:
        return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
    }
}
```

同时 `mapAdminScopeError`（UpdateAdmin 错误映射）追加 `ErrDepartmentOutOfScope` case。

### 5.8 测试

| 测试 | 文件 | 覆盖 |
| --- | --- | --- |
| `TestEnsureDepartmentInScope_SuperAdmin` | data_permission_test.go | 超管放行 |
| `TestEnsureDepartmentInScope_NilOp` | 同 | 内部调用放行 |
| `TestEnsureDepartmentInScope_All` | 同 | DataScopeAll 放行（含 dept=0）|
| `TestEnsureDepartmentInScope_Self` | 同 | DataScopeSelf 拒绝 |
| `TestEnsureDepartmentInScope_Department_Hit` | 同 | 命中 |
| `TestEnsureDepartmentInScope_Department_Miss` | 同 | 不命中 |
| `TestEnsureDepartmentInScope_DepartmentZero` | 同 | 非超管 dept=0 拒绝 |
| `TestEnsureDepartmentInScope_DefaultFailClosed` | 同 | 未知 scope 拒绝 |
| `TestSuperAdminService_CreateAdmin_RejectsOutOfScopeDepartment` | super_admin_service_test.go | E2E：部门管理员越权创建被拒 |
| `TestSuperAdminService_UpdateAdmin_RejectsOutOfScopeDepartmentMigration` | 同 | E2E：越权迁移被拒 |
| `TestSuperAdminService_CreateAdmin_RejectsInvalidRoleIDs` | 同 | 无效 RoleIDs 整体拒绝 |
| `TestSuperAdminService_CreateAdmin_RejectsPrimaryRoleNotInRoleIDs` | 同 | Primary 不在 RoleIDs 拒绝 |
| `TestSuperAdminService_CreateAdmin_AtomicRollback` | 同 | 模拟 admin_role 插入失败，admin 表无残留 |

### 5.9 测试代码同步更新

- `super_admin_service_test.go` 中所有现有 `svc.CreateAdmin(ctx, &req)` 调用追加 `nil` 参数：`svc.CreateAdmin(ctx, nil, &req)`
- 任何 mock（grep 确认）

## 6. 模块分解与并发

| # | 模块 | 文件 | 依赖 |
| --- | --- | --- | --- |
| **A** | ensureDepartmentInScope + ErrDepartmentOutOfScope | data_permission.go | 无 |
| **B** | ResponseDepartmentOutOfScope 错误码 | global/msg.go | 无 |
| **C** | SuperAdminService.CreateAdmin 重构 | super_admin_service.go | A |
| **D** | SuperAdminService.UpdateAdmin 部门校验 | super_admin_service.go | A |
| **E** | admin_list.go CreateAdmin + 错误映射 | handler/admin_list.go | B、C |
| **F** | 现有测试调用补 nil + 新增 11 个测试 | service 测试 | A-E |

并发波次：
- 波次 1：A、B（独立）
- 波次 2：C、D 并发（依赖 A）
- 波次 3：E（依赖 B、C）
- 波次 4：F（依赖全部）

## 7. 风险与缓解

| 风险 | 缓解 |
| --- | --- |
| CreateAdmin 签名变更，handler/test/mock 全部要改 | 一次性 grep 出全部调用点；编译失败即定位 |
| Casbin 写入失败导致 admin/role 关系建立但 enforcer 不知 | 与现有 AssignAdminRoles 行为一致；后续可加 reconciliation 任务（不在本次范围） |
| `ensureDepartmentInScope` 与 `ensureAdminInScope` 行为分歧 | 二者共享相同的 op short-circuit 与 scope 分类；测试矩阵显式覆盖 |
| 预查询 + 二次查询性能 | 单次 count + 单次 list 都是主键 IN 查询，影响极小 |

## 8. 验证

```powershell
go build -o tmp/go-admin.exe main.go
go test ./internal/service/... -count=1
go test ./... -count=1
```

联调矩阵新增 5 行：

| 操作者 | 操作 | 期望 |
| --- | --- | --- |
| Department(A) 管理员 | CreateAdmin{dept=B} | 403 + ResponseDepartmentOutOfScope |
| Department(A) 管理员 | CreateAdmin{dept=0} | 403 + ResponseDepartmentOutOfScope |
| Department(A) 管理员 | UpdateAdmin admin@A {dept=B} | 403 + ResponseDepartmentOutOfScope |
| 超管 | CreateAdmin{roles=[1,9999]} | 400 + "存在无效的角色 ID" |
| 超管 | CreateAdmin{roles=[1,2], primary=3} | 400 + "主角色必须在 role_ids 列表中" |

## 9. 完成定义

- 6 个模块全部 commit
- `go build` 通过
- `go test ./...` 全绿
- 联调矩阵 5 个新场景手工验证
