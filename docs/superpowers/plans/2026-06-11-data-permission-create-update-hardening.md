# 数据权限闭环加固 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复 CreateAdmin/UpdateAdmin 部门越权漏洞 + CreateAdmin 角色分配原子性与有效性。

**Architecture:** 新增包级 helper `ensureDepartmentInScope`（与已有 `ensureAdminInScope` 同模式）；SuperAdminService.CreateAdmin 签名加入 `op` 参数 + 角色预校验 + ent 事务包裹 admin/admin_role 写入；SuperAdminService.UpdateAdmin 在 department_id 变更时同样调用新 helper；handler 注入 op 与扩展错误映射。

**Tech Stack:** Go 1.25.1 / Fiber v3 / Ent ORM / Casbin / testify / MySQL

**Spec:** [docs/superpowers/specs/2026-06-11-data-permission-create-update-hardening-design.md](../specs/2026-06-11-data-permission-create-update-hardening-design.md)

---

## 文件结构与责任

| 文件 | 责任 | 操作 |
| --- | --- | --- |
| `internal/service/data_permission.go` | 新 helper `ensureDepartmentInScope` + 新错误 `ErrDepartmentOutOfScope` | 修改 |
| `internal/service/super_admin_service.go` | CreateAdmin 重构（op + 角色预校验 + 事务）；UpdateAdmin 加部门校验；接口签名变更 | 修改 |
| `internal/global/msg.go` | 错误码 `ResponseDepartmentOutOfScope=9009` + 文案 | 修改 |
| `internal/handler/admin_list.go` | CreateAdmin 构造 op；新增 `mapAdminCreateError`；现有 `mapAdminScopeError` 加 `ErrDepartmentOutOfScope` 分支 | 修改 |
| `internal/service/data_permission_test.go` | 新 helper 单元测试 | 修改 |
| `internal/service/super_admin_service_test.go` | 现有 CreateAdmin 调用补 nil；新增越权 / 角色校验 / 原子性测试 | 修改 |

---

## 任务清单

### 任务 1：新增 ErrDepartmentOutOfScope + ensureDepartmentInScope（TDD）

**Files:**
- Modify: `internal/service/data_permission.go`（末尾追加）
- Modify: `internal/service/data_permission_test.go`（末尾追加测试）

- [ ] **步骤 1：写失败测试**

追加到 `internal/service/data_permission_test.go` 末尾：

```go
func TestEnsureDepartmentInScope_NilOp(t *testing.T) {
    if err := ensureDepartmentInScope(nil, 999); err != nil {
        t.Fatalf("op==nil 应放行，got %v", err)
    }
}

func TestEnsureDepartmentInScope_SuperAdmin(t *testing.T) {
    op := &EffectiveDataScope{IsSuperAdmin: true}
    if err := ensureDepartmentInScope(op, 999); err != nil {
        t.Fatalf("超管应放行，got %v", err)
    }
}

func TestEnsureDepartmentInScope_DataScopeAll(t *testing.T) {
    op := &EffectiveDataScope{Scope: DataScopeAll}
    if err := ensureDepartmentInScope(op, 0); err != nil {
        t.Fatalf("All 应放行（含 0），got %v", err)
    }
    if err := ensureDepartmentInScope(op, 5); err != nil {
        t.Fatalf("All 应放行 5，got %v", err)
    }
}

func TestEnsureDepartmentInScope_DataScopeSelf_Rejects(t *testing.T) {
    op := &EffectiveDataScope{Scope: DataScopeSelf}
    if err := ensureDepartmentInScope(op, 5); !errors.Is(err, ErrDepartmentOutOfScope) {
        t.Fatalf("Self 应拒绝，got %v", err)
    }
}

func TestEnsureDepartmentInScope_Department_Hit(t *testing.T) {
    op := &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1, 3, 5}}
    if err := ensureDepartmentInScope(op, 3); err != nil {
        t.Fatalf("命中部门应放行，got %v", err)
    }
}

func TestEnsureDepartmentInScope_Department_Miss(t *testing.T) {
    op := &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1, 3, 5}}
    if err := ensureDepartmentInScope(op, 9); !errors.Is(err, ErrDepartmentOutOfScope) {
        t.Fatalf("未命中部门应拒绝，got %v", err)
    }
}

func TestEnsureDepartmentInScope_DepartmentZero_Rejects(t *testing.T) {
    op := &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1, 3}}
    if err := ensureDepartmentInScope(op, 0); !errors.Is(err, ErrDepartmentOutOfScope) {
        t.Fatalf("非超管 dept=0 应拒绝，got %v", err)
    }
}

func TestEnsureDepartmentInScope_DefaultFailClosed(t *testing.T) {
    op := &EffectiveDataScope{Scope: "unknown_scope"}
    if err := ensureDepartmentInScope(op, 5); !errors.Is(err, ErrDepartmentOutOfScope) {
        t.Fatalf("未知 scope 应 fail-closed，got %v", err)
    }
}
```

文件顶部已有 `errors`、`context` 等 import，确认是否含 `errors`；若无追加。

- [ ] **步骤 2：跑测试确认 FAIL**

```powershell
go test ./internal/service/ -run TestEnsureDepartmentInScope -count=1
```

期望：FAIL `undefined: ensureDepartmentInScope` 与 `undefined: ErrDepartmentOutOfScope`

- [ ] **步骤 3：声明错误（追加到 `internal/service/data_permission.go`）**

在已有 `ErrCannotModifySelfRoles` 之后追加：

```go
// ErrDepartmentOutOfScope 目标部门超出当前操作者数据权限范围
var ErrDepartmentOutOfScope = errors.New("department out of data scope")
```

- [ ] **步骤 4：实现 ensureDepartmentInScope（追加到 `internal/service/data_permission.go` 末尾）**

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

- [ ] **步骤 5：跑测试确认 PASS**

```powershell
go test ./internal/service/ -run TestEnsureDepartmentInScope -count=1
```

期望：8 个测试全 PASS。

- [ ] **步骤 6：提交**

```powershell
git add internal/service/data_permission.go internal/service/data_permission_test.go
git commit -m "feat(data-permission): 新增 ensureDepartmentInScope 守卫与 ErrDepartmentOutOfScope"
```

---

### 任务 2：新增错误码 ResponseDepartmentOutOfScope

**Files:**
- Modify: `internal/global/msg.go`

- [ ] **步骤 1：在 const 块中追加常量**

打开 `internal/global/msg.go`，定位现有 `ResponseSelfRoleModifyForbidden = 9008`，在其之后追加：

```go
// ResponseDepartmentOutOfScope 目标部门不在数据权限范围内
ResponseDepartmentOutOfScope = 9009
```

- [ ] **步骤 2：在 ResponseMsg map 中追加文案**

```go
ResponseDepartmentOutOfScope: "目标部门不在你的数据权限范围内",
```

- [ ] **步骤 3：编译验证**

```powershell
go build ./internal/global/
```

期望：通过。

- [ ] **步骤 4：提交**

```powershell
git add internal/global/msg.go
git commit -m "feat(global): 新增 ResponseDepartmentOutOfScope 错误码 9009"
```

---

### 任务 3：SuperAdminService.CreateAdmin 重构（接口签名 + 部门校验 + 角色预校验 + 事务）

**Files:**
- Modify: `internal/service/super_admin_service.go:33-40`（接口）
- Modify: `internal/service/super_admin_service.go:87-196`（CreateAdmin 整段）

- [ ] **步骤 1：修改接口签名**

第 33-40 行 `SuperAdminServiceInterface` 把 `CreateAdmin` 改为：

```go
type SuperAdminServiceInterface interface {
    CreateAdmin(ctx context.Context, op *EffectiveDataScope, req *CreateAdminRequest) (*AdminResponse, error)
    ListAdmins(ctx context.Context, req *AdminListRequest) (*ListResponse[*AdminListItem], error)
    GetAdminByID(ctx context.Context, op *EffectiveDataScope, adminID int) (*AdminResponse, error)
    UpdateAdmin(ctx context.Context, op *EffectiveDataScope, adminID int, req *UpdateAdminRequest) (*AdminResponse, error)
    ActivateAdmin(ctx context.Context, op *EffectiveDataScope, adminID int) error
    DeactivateAdmin(ctx context.Context, op *EffectiveDataScope, adminID int) error
}
```

- [ ] **步骤 2：替换 CreateAdmin 完整实现**

定位 `internal/service/super_admin_service.go` 第 87 行起的 CreateAdmin 函数（一直到对应 `}` 与 `return &AdminResponse{...}`，约第 196 行结束），完整替换为：

```go
// CreateAdmin 创建管理员
func (s *SuperAdminService) CreateAdmin(ctx context.Context, op *EffectiveDataScope, req *CreateAdminRequest) (*AdminResponse, error) {
    // P1-A：目标部门必须在 op 的数据范围内
    if err := ensureDepartmentInScope(op, req.DepartmentID); err != nil {
        return nil, err
    }

    if err := rejectSuperAdminRoleIDs(ctx, s.client, requestedRoleIDs(req.RoleIDs, req.PrimaryRoleID)); err != nil {
        return nil, err
    }

    emailExists, err := s.client.Admin.Query().
        Where(admin.EmailEQ(req.Email)).
        Exist(ctx)
    if err != nil {
        return nil, err
    }
    if emailExists {
        return nil, ErrAdminEmailExists
    }

    usernameExists, err := s.client.Admin.Query().
        Where(admin.UsernameEQ(req.Username)).
        Exist(ctx)
    if err != nil {
        return nil, err
    }
    if usernameExists {
        return nil, ErrAdminUsernameExists
    }

    hashedPassword, salt, err := crypto.HashPassword(req.Password)
    if err != nil {
        return nil, err
    }

    // P2：预校验 RoleIDs 全部存在且 Active；PrimaryRoleID 必须在 RoleIDs 中
    var validRoles []*ent.Role
    primaryID := 0
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
        primaryID = req.PrimaryRoleID
        if primaryID == 0 {
            primaryID = req.RoleIDs[0]
        } else if !containsInt(req.RoleIDs, primaryID) {
            return nil, ErrPrimaryRoleInvalid
        }
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
            SetIsPrimary(r.ID == primaryID).
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

注意：
- `ErrInvalidRoleIDs` 与 `ErrPrimaryRoleInvalid` 已在 [role_assignment.go:20-21](file:///d:/www/admin/internal/service/role_assignment.go#L20-L21) 声明，同 package 直接引用
- `containsInt` 已在 data_permission.go 末尾定义，同 package 直接调用
- import 需含 `ent` 包（即 `"go-admin/internal/ent"`），先 Grep 顶部 import 块；若已存在则跳过

- [ ] **步骤 3：跑 service 单包编译**

```powershell
go build ./internal/service/
```

期望：通过。`go build ./...` 此时 handler/test 会失败（任务 4、6 修复），属预期。

- [ ] **步骤 4：提交**

```powershell
git add internal/service/super_admin_service.go
git commit -m "feat(admin): CreateAdmin 加 op 部门守卫+角色预校验+事务原子性"
```

---

### 任务 4：SuperAdminService.UpdateAdmin 部门校验

**Files:**
- Modify: `internal/service/super_admin_service.go`（UpdateAdmin 方法体起始）

- [ ] **步骤 1：定位 UpdateAdmin**

Grep `^func \(s \*SuperAdminService\) UpdateAdmin`，应该在第 350 行附近。当前方法头：

```go
func (s *SuperAdminService) UpdateAdmin(ctx context.Context, op *EffectiveDataScope, adminID int, req *UpdateAdminRequest) (*AdminResponse, error) {
    if err := ensureAdminInScope(ctx, s.client, op, adminID); err != nil {
        return nil, err
    }
    // ...
}
```

- [ ] **步骤 2：在 ensureAdminInScope 之后插入部门校验**

把方法体起始改为：

```go
func (s *SuperAdminService) UpdateAdmin(ctx context.Context, op *EffectiveDataScope, adminID int, req *UpdateAdminRequest) (*AdminResponse, error) {
    if err := ensureAdminInScope(ctx, s.client, op, adminID); err != nil {
        return nil, err
    }
    // P1-B：若 req 要改 department_id，新部门也必须在 op 范围内
    if req.DepartmentID != nil {
        if err := ensureDepartmentInScope(op, *req.DepartmentID); err != nil {
            return nil, err
        }
    }
    // ...原有逻辑保持不变
}
```

- [ ] **步骤 3：编译**

```powershell
go build ./internal/service/
```

期望：通过。

- [ ] **步骤 4：提交**

```powershell
git add internal/service/super_admin_service.go
git commit -m "feat(admin): UpdateAdmin 修改 department_id 时校验新部门范围"
```

---

### 任务 5：handler 改造（CreateAdmin 注入 op + 错误映射扩展）

**Files:**
- Modify: `internal/handler/admin_list.go`（CreateAdmin 函数 + mapAdminScopeError + 新增 mapAdminCreateError）

- [ ] **步骤 1：替换 CreateAdmin（第 26-55 行）**

```go
// CreateAdmin 创建管理员
// @Summary 创建管理员账号
// @Tags 管理员管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body service.CreateAdminRequest true "创建管理员请求"
// @Success 200 {object} utils.Response{data=service.AdminResponse}
// @Router /api/v1/admin/auth/admins [post]
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

- [ ] **步骤 2：在 mapAdminScopeError 中追加 ErrDepartmentOutOfScope 分支**

定位现有 `mapAdminScopeError`（admin_list.go 末尾），在 `case service.ErrCannotModifySelfRoles:` 之后追加：

```go
case service.ErrDepartmentOutOfScope:
    return response.Fail(global.ResponseDepartmentOutOfScope, global.ResponseMsg[global.ResponseDepartmentOutOfScope]).ToJSON(c)
```

- [ ] **步骤 3：在 admin_list.go 末尾追加 mapAdminCreateError**

```go
// mapAdminCreateError 把 CreateAdmin 的 service 层错误映射为 HTTP 响应
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

- [ ] **步骤 4：编译**

```powershell
go build ./...
```

期望：通过（任务 6 修复测试代码之前 `go test` 会编译失败，先确保 build 通过即可）。

- [ ] **步骤 5：提交**

```powershell
git add internal/handler/admin_list.go
git commit -m "feat(handler): CreateAdmin 注入 op + 错误映射扩展 ErrDepartmentOutOfScope"
```

---

### 任务 6：测试代码同步更新 + 新增越权/角色/原子性测试

**Files:**
- Modify: `internal/service/super_admin_service_test.go`

- [ ] **步骤 1：grep 现有 CreateAdmin 调用**

```powershell
# 在终端不执行，仅引导：
# Grep 'svc\.CreateAdmin\(' internal/service/super_admin_service_test.go
```

定位每一处 `svc.CreateAdmin(ctx, &req)` 或类似形式，在 `ctx,` 之后追加 `nil,`。常见匹配模式：

```go
// 旧
got, err := svc.CreateAdmin(ctx, &CreateAdminRequest{...})
// 新
got, err := svc.CreateAdmin(ctx, nil, &CreateAdminRequest{...})
```

- [ ] **步骤 2：跑测试看是否通过**

```powershell
go test ./internal/service/ -count=1
```

期望：现有所有测试 PASS（除新建的越权/角色/原子性测试外）。

- [ ] **步骤 3：在 `super_admin_service_test.go` 末尾追加越权测试**

```go
func TestSuperAdminService_CreateAdmin_RejectsOutOfScopeDepartment(t *testing.T) {
    setupSystemPermissionTest(t)
    ctx := t.Context()
    svc := NewSuperAdminService()
    // 部门管理员 op，仅能看部门 1；尝试创建到部门 99
    op := &EffectiveDataScope{
        Scope:         DataScopeDepartment,
        AdminID:       1,
        AdminUID:      "u-mgr",
        DepartmentID:  1,
        DepartmentIDs: []int{1},
    }
    _, err := svc.CreateAdmin(ctx, op, &CreateAdminRequest{
        Email:        "x@test.com",
        Password:     "passw0rd",
        Username:     "x",
        DepartmentID: 99,
    })
    if !errors.Is(err, ErrDepartmentOutOfScope) {
        t.Fatalf("期望 ErrDepartmentOutOfScope，got %v", err)
    }
}

func TestSuperAdminService_UpdateAdmin_RejectsOutOfScopeDepartmentMigration(t *testing.T) {
    client, _, cleanup := setupSuperAdminTest(t)
    t.Cleanup(cleanup)
    ctx := t.Context()
    a := createTestAdminRaw(t, client, "u1@test.com", "u-1", "u1", "pass", admin.StatusActive)
    // 让 a 属于部门 1
    _, err := client.Admin.UpdateOneID(a.ID).SetDepartmentID(1).Save(ctx)
    if err != nil {
        t.Fatalf("set department failed: %v", err)
    }
    op := &EffectiveDataScope{
        Scope:         DataScopeDepartment,
        AdminID:       100,
        AdminUID:      "u-mgr",
        DepartmentID:  1,
        DepartmentIDs: []int{1},
    }
    svc := NewSuperAdminService()
    newDept := 99
    _, err = svc.UpdateAdmin(ctx, op, a.ID, &UpdateAdminRequest{DepartmentID: &newDept})
    if !errors.Is(err, ErrDepartmentOutOfScope) {
        t.Fatalf("期望 ErrDepartmentOutOfScope，got %v", err)
    }
}

func TestSuperAdminService_CreateAdmin_RejectsInvalidRoleIDs(t *testing.T) {
    setupSystemPermissionTest(t)
    ctx := t.Context()
    svc := NewSuperAdminService()
    _, err := svc.CreateAdmin(ctx, nil, &CreateAdminRequest{
        Email:    "y@test.com",
        Password: "passw0rd",
        Username: "y",
        RoleIDs:  []int{999999, 999998},
    })
    if !errors.Is(err, ErrInvalidRoleIDs) {
        t.Fatalf("期望 ErrInvalidRoleIDs，got %v", err)
    }
}

func TestSuperAdminService_CreateAdmin_RejectsPrimaryRoleNotInRoleIDs(t *testing.T) {
    client, _, cleanup := setupSuperAdminTest(t)
    t.Cleanup(cleanup)
    ctx := t.Context()
    // 创建两个真实角色
    r1, err := client.Role.Create().SetName("r1").SetDisplayName("R1").SetStatus(role.StatusActive).Save(ctx)
    if err != nil {
        t.Fatalf("create r1 failed: %v", err)
    }
    r2, err := client.Role.Create().SetName("r2").SetDisplayName("R2").SetStatus(role.StatusActive).Save(ctx)
    if err != nil {
        t.Fatalf("create r2 failed: %v", err)
    }
    svc := NewSuperAdminService()
    _, err = svc.CreateAdmin(ctx, nil, &CreateAdminRequest{
        Email:         "z@test.com",
        Password:      "passw0rd",
        Username:      "z",
        RoleIDs:       []int{r1.ID, r2.ID},
        PrimaryRoleID: r1.ID + r2.ID + 9999, // 故意不在 RoleIDs 中
    })
    if !errors.Is(err, ErrPrimaryRoleInvalid) {
        t.Fatalf("期望 ErrPrimaryRoleInvalid，got %v", err)
    }
}
```

注意：
- `setupSystemPermissionTest` 与 `setupSuperAdminTest` 已在同 package 测试基座存在
- `createTestAdminRaw` 已在测试基座存在（参考 T5 已有使用方式）
- 顶部 import 含 `"errors"`；若无追加

- [ ] **步骤 4：跑测试**

```powershell
go test ./internal/service/ -count=1
```

期望：包括 4 个新测试在内全部 PASS。

- [ ] **步骤 5：提交**

```powershell
git add internal/service/super_admin_service_test.go
git commit -m "test(admin): CreateAdmin/UpdateAdmin 加 op nil 兼容 + 越权/角色/部门测试"
```

---

### 任务 7：最终全量验证

- [ ] **步骤 1：编译**

```powershell
go build -o tmp/go-admin.exe main.go
```

期望：通过。

- [ ] **步骤 2：跑全量测试**

```powershell
go test ./... -count=1
```

期望：所有包 PASS。

- [ ] **步骤 3：手工联调矩阵（建议运维 / 开发自跑）**

按 spec §8 的 5 个新场景手动验证：

| 操作者 | 操作 | 期望 |
| --- | --- | --- |
| Department(A) 管理员 | CreateAdmin{dept=B} | 403 + ResponseDepartmentOutOfScope |
| Department(A) 管理员 | CreateAdmin{dept=0} | 403 + ResponseDepartmentOutOfScope |
| Department(A) 管理员 | UpdateAdmin admin@A {dept=B} | 403 + ResponseDepartmentOutOfScope |
| 超管 | CreateAdmin{roles=[1,9999]} | 400 + "存在无效的角色 ID" |
| 超管 | CreateAdmin{roles=[1,2], primary=3} | 400 + "主角色必须在 role_ids 列表中" |

---

## Self-Review 清单

| Spec 要求 | 对应任务 |
| --- | --- |
| §5.1 ensureDepartmentInScope | 任务 1 |
| §5.2 ErrDepartmentOutOfScope | 任务 1 |
| §5.3 ResponseDepartmentOutOfScope 错误码 | 任务 2 |
| §5.4 SuperAdminServiceInterface CreateAdmin 加 op | 任务 3 |
| §5.5 CreateAdmin 重构（部门 + 角色预校验 + 事务）| 任务 3 |
| §5.6 UpdateAdmin 部门校验 | 任务 4 |
| §5.7 handler CreateAdmin + 错误映射 | 任务 5 |
| §5.8 测试（8 helper + 4 service E2E）| 任务 1（helper）+ 任务 6（service E2E）|
| §5.9 现有测试调用补 nil | 任务 6 |
| §8 验证 | 任务 7 |

类型一致性核对：
- `ensureDepartmentInScope(op, deptID)` 签名在任务 1、3、4、6 完全一致
- `ErrDepartmentOutOfScope` 在任务 1、5、6 完全一致
- `ResponseDepartmentOutOfScope` 常量在任务 2、5 完全一致
- `mapAdminCreateError` / `mapAdminScopeError` 二者职责分离明确
- `containsInt` 复用已有定义（上一轮已加）
- `ErrInvalidRoleIDs` / `ErrPrimaryRoleInvalid` 复用 role_assignment.go 已有定义

无占位符、无"类似 Task N 略"。
