# 数据权限纵深防御加固实施计划：角色提权防御 + 部门存在性

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 go-admin 后端补齐数据权限纵深防御第三层：阻止操作者通过分配高范围角色或写入幽灵部门 ID 绕过自身数据权限。

**Architecture:** 沿用包级守卫函数模式（与 ensureAdminInScope / ensureDepartmentInScope 一致），在 internal/service/data_permission.go 新增 `ensureRolesNotEscalating` 与 `ensureDepartmentExists`，由 super_admin_service / role_assignment / role 三处 service 调用；handler 层做错误码映射；测试包补 6 个 E2E。

**Tech Stack:** Go 1.25.1 / Fiber v3 / Ent ORM / testify / SQLite enttest / PowerShell

关联 spec：`docs/superpowers/specs/2026-06-11-data-permission-role-escalation-and-department-existence-design.md`

---

## Task 1: 新增 ensureRolesNotEscalating + ensureDepartmentExists 守卫与单元测试

**Files:**
- Modify: `d:/www/admin/internal/service/data_permission.go`
- Modify: `d:/www/admin/internal/service/data_permission_test.go`

- [ ] **Step 1：先在测试文件加 14 个单元测试，确认失败**

在 `data_permission_test.go` 末尾追加：

```go
// 注意：本文件采用纯 helper 单元测试风格，部分测试需要 client，使用 setupSuperAdminTest helper
func TestEnsureRolesNotEscalating_NilOp(t *testing.T) {
    if err := ensureRolesNotEscalating(context.Background(), nil, nil, []int{1}); err != nil {
        t.Fatalf("nil op 应放行, got %v", err)
    }
}

func TestEnsureRolesNotEscalating_SuperAdmin(t *testing.T) {
    op := &EffectiveDataScope{IsSuperAdmin: true}
    if err := ensureRolesNotEscalating(context.Background(), nil, op, []int{1}); err != nil {
        t.Fatalf("超管应放行, got %v", err)
    }
}

func TestEnsureRolesNotEscalating_OpScopeAll(t *testing.T) {
    op := &EffectiveDataScope{Scope: DataScopeAll}
    if err := ensureRolesNotEscalating(context.Background(), nil, op, []int{1}); err != nil {
        t.Fatalf("op.Scope=All 应放行, got %v", err)
    }
}

func TestEnsureRolesNotEscalating_EmptyRoleIDs(t *testing.T) {
    op := &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1}}
    if err := ensureRolesNotEscalating(context.Background(), nil, op, nil); err != nil {
        t.Fatalf("空 roleIDs 应放行, got %v", err)
    }
}

func TestEnsureRolesNotEscalating_UnknownOpScope(t *testing.T) {
    op := &EffectiveDataScope{Scope: DataScope("invalid")}
    err := ensureRolesNotEscalating(context.Background(), nil, op, []int{1})
    if !errors.Is(err, ErrRoleScopeEscalation) {
        t.Fatalf("期望 ErrRoleScopeEscalation, got %v", err)
    }
}

// 下方 6 个测试需要 client，使用 setupSuperAdminTest helper
func TestEnsureRolesNotEscalating_RoleAllRejected_OpDept(t *testing.T) {
    ctx, client, _, cleanup := setupSuperAdminTest(t)
    defer cleanup()
    r := client.Role.Create().SetName("r_all").SetCode("r_all").
        SetDataScope(role.DataScopeAll).SetStatus(role.StatusActive).SaveX(ctx)
    op := &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1}}
    if err := ensureRolesNotEscalating(ctx, client, op, []int{r.ID}); !errors.Is(err, ErrRoleScopeEscalation) {
        t.Fatalf("期望 ErrRoleScopeEscalation, got %v", err)
    }
}

func TestEnsureRolesNotEscalating_RoleDept_OpDept(t *testing.T) {
    ctx, client, _, cleanup := setupSuperAdminTest(t)
    defer cleanup()
    r := client.Role.Create().SetName("r_dept").SetCode("r_dept").
        SetDataScope(role.DataScopeDepartment).SetStatus(role.StatusActive).SaveX(ctx)
    op := &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1}}
    if err := ensureRolesNotEscalating(ctx, client, op, []int{r.ID}); err != nil {
        t.Fatalf("同级应放行, got %v", err)
    }
}

func TestEnsureRolesNotEscalating_RoleSelf_OpCustom(t *testing.T) {
    ctx, client, _, cleanup := setupSuperAdminTest(t)
    defer cleanup()
    r := client.Role.Create().SetName("r_self").SetCode("r_self").
        SetDataScope(role.DataScopeSelf).SetStatus(role.StatusActive).SaveX(ctx)
    op := &EffectiveDataScope{Scope: DataScopeCustom, DepartmentIDs: []int{1, 2}}
    if err := ensureRolesNotEscalating(ctx, client, op, []int{r.ID}); err != nil {
        t.Fatalf("更低范围应放行, got %v", err)
    }
}

func TestEnsureRolesNotEscalating_RoleCustom_OpDept_Rejected(t *testing.T) {
    ctx, client, _, cleanup := setupSuperAdminTest(t)
    defer cleanup()
    r := client.Role.Create().SetName("r_custom").SetCode("r_custom").
        SetDataScope(role.DataScopeCustom).SetDataScopeDepartmentIds([]int{1}).
        SetStatus(role.StatusActive).SaveX(ctx)
    op := &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1}}
    if err := ensureRolesNotEscalating(ctx, client, op, []int{r.ID}); !errors.Is(err, ErrRoleScopeEscalation) {
        t.Fatalf("Custom rank 高于 Dept 应拒绝, got %v", err)
    }
}

func TestEnsureRolesNotEscalating_CustomSubset(t *testing.T) {
    ctx, client, _, cleanup := setupSuperAdminTest(t)
    defer cleanup()
    r := client.Role.Create().SetName("r_cs").SetCode("r_cs").
        SetDataScope(role.DataScopeCustom).SetDataScopeDepartmentIds([]int{1}).
        SetStatus(role.StatusActive).SaveX(ctx)
    op := &EffectiveDataScope{Scope: DataScopeCustom, DepartmentIDs: []int{1, 2, 3}}
    if err := ensureRolesNotEscalating(ctx, client, op, []int{r.ID}); err != nil {
        t.Fatalf("子集应放行, got %v", err)
    }
}

func TestEnsureRolesNotEscalating_CustomNotSubset(t *testing.T) {
    ctx, client, _, cleanup := setupSuperAdminTest(t)
    defer cleanup()
    r := client.Role.Create().SetName("r_ns").SetCode("r_ns").
        SetDataScope(role.DataScopeCustom).SetDataScopeDepartmentIds([]int{1, 99}).
        SetStatus(role.StatusActive).SaveX(ctx)
    op := &EffectiveDataScope{Scope: DataScopeCustom, DepartmentIDs: []int{1, 2}}
    if err := ensureRolesNotEscalating(ctx, client, op, []int{r.ID}); !errors.Is(err, ErrRoleScopeEscalation) {
        t.Fatalf("超出子集应拒绝, got %v", err)
    }
}

func TestEnsureDepartmentExists_ZeroAllowed(t *testing.T) {
    if err := ensureDepartmentExists(context.Background(), nil, 0); err != nil {
        t.Fatalf("departmentID=0 应放行, got %v", err)
    }
}

func TestEnsureDepartmentExists_ExistingID(t *testing.T) {
    ctx, client, _, cleanup := setupSuperAdminTest(t)
    defer cleanup()
    d := client.Department.Create().SetName("d1").SetCode("d1").SaveX(ctx)
    if err := ensureDepartmentExists(ctx, client, d.ID); err != nil {
        t.Fatalf("存在的 dept 应放行, got %v", err)
    }
}

func TestEnsureDepartmentExists_MissingID(t *testing.T) {
    ctx, client, _, cleanup := setupSuperAdminTest(t)
    defer cleanup()
    if err := ensureDepartmentExists(ctx, client, 999999); !errors.Is(err, ErrDepartmentNotFound) {
        t.Fatalf("期望 ErrDepartmentNotFound, got %v", err)
    }
}
```

如已 import role/department schema 包不全，需在文件头加：
```go
import (
    "go-admin/internal/ent/role"
    "go-admin/internal/ent/department"  // 仅当 Department.Create 未导入时
)
```

- [ ] **Step 2：跑测试确认 FAIL（编译失败也算）**

```powershell
cd d:\www\admin ; go test ./internal/service/ -run "EnsureRolesNotEscalating|EnsureDepartmentExists" -v
```
预期：编译失败 `undefined: ensureRolesNotEscalating` / `ensureDepartmentExists` / `ErrRoleScopeEscalation` / `ErrDepartmentNotFound`。

- [ ] **Step 3：在 data_permission.go 实现守卫**

在 `data_permission.go` 错误声明区（第 27 行下方）追加：
```go
// ErrRoleScopeEscalation 目标角色的数据范围超出当前操作者
var ErrRoleScopeEscalation = errors.New("role data scope escalation")

// ErrDepartmentNotFound 目标部门不存在
var ErrDepartmentNotFound = errors.New("department not found")
```

在 `mergeScope` 函数下方追加 `scopeRank`：
```go
// scopeRank 返回数据权限范围的"宽度"等级，与 mergeScope 共用同一张表
func scopeRank(s DataScope) int {
    switch s {
    case DataScopeSelf:
        return 1
    case DataScopeDepartment:
        return 2
    case DataScopeDepartmentAndChild:
        return 3
    case DataScopeCustom:
        return 4
    case DataScopeAll:
        return 5
    default:
        return 0 // 未知范围 fail-closed
    }
}
```

在文件末尾追加：
```go
// ensureRolesNotEscalating 校验候选角色的 data_scope 不超过操作者
//   op == nil               → 放行（内部调用）
//   op.IsSuperAdmin || All  → 放行
//   非 All：要求 rank(role) ≤ rank(op)；Custom 角色还要求 role.DeptIDs ⊆ op.DeptIDs
//   未知 op.Scope           → 拒绝（fail-closed）
func ensureRolesNotEscalating(ctx context.Context, client *ent.Client, op *EffectiveDataScope, roleIDs []int) error {
    if op == nil || op.IsSuperAdmin || op.Scope == DataScopeAll {
        return nil
    }
    if len(roleIDs) == 0 {
        return nil
    }
    opRank := scopeRank(op.Scope)
    if opRank == 0 {
        return ErrRoleScopeEscalation
    }
    roles, err := client.Role.Query().Where(role.IDIn(roleIDs...)).All(ctx)
    if err != nil {
        return err
    }
    for _, r := range roles {
        rs := DataScope(r.DataScope)
        if scopeRank(rs) > opRank {
            return ErrRoleScopeEscalation
        }
        if rs == DataScopeCustom {
            for _, deptID := range r.DataScopeDepartmentIds {
                if !containsInt(op.DepartmentIDs, deptID) {
                    return ErrRoleScopeEscalation
                }
            }
        }
    }
    return nil
}

// ensureDepartmentExists 校验目标部门真实存在
//   departmentID == 0 → 放行（仅超管/All 可设由 ensureDepartmentInScope 保证）
func ensureDepartmentExists(ctx context.Context, client *ent.Client, departmentID int) error {
    if departmentID == 0 {
        return nil
    }
    exists, err := client.Department.Query().Where(department.IDEQ(departmentID)).Exist(ctx)
    if err != nil {
        return err
    }
    if !exists {
        return ErrDepartmentNotFound
    }
    return nil
}
```

确认 `import` 块包含 `"go-admin/internal/ent/department"`。

- [ ] **Step 4：跑测试确认 PASS**

```powershell
cd d:\www\admin ; go test ./internal/service/ -run "EnsureRolesNotEscalating|EnsureDepartmentExists" -v -count=1
```
预期：所有 14 个测试 PASS。

- [ ] **Step 5：commit**

```powershell
cd d:\www\admin ; git add internal/service/data_permission.go internal/service/data_permission_test.go ; git commit -m "feat(data-permission): 新增 ensureRolesNotEscalating + ensureDepartmentExists 守卫"
```

---

## Task 2: 新增错误码 9010/9011 与文案

**Files:**
- Modify: `d:/www/admin/internal/global/msg.go`

- [ ] **Step 1：找到 ResponseDepartmentOutOfScope = 9009 行位置**

```powershell
cd d:\www\admin ; Select-String -Path internal/global/msg.go -Pattern "ResponseDepartmentOutOfScope"
```

- [ ] **Step 2：在 9009 之后追加 9010/9011**

在常量声明块中 `ResponseDepartmentOutOfScope = 9009` 下方追加：
```go
    ResponseRoleScopeEscalation = 9010
    ResponseDepartmentNotFound  = 9011
```

在 `ResponseMsg` map 中 `ResponseDepartmentOutOfScope: "..."` 下方追加：
```go
    ResponseRoleScopeEscalation: "目标角色的数据范围超出你的权限",
    ResponseDepartmentNotFound:  "目标部门不存在",
```

- [ ] **Step 3：编译验证**

```powershell
cd d:\www\admin ; go build ./...
```
预期：exit 0。

- [ ] **Step 4：commit**

```powershell
cd d:\www\admin ; git add internal/global/msg.go ; git commit -m "feat(global): 新增 ResponseRoleScopeEscalation=9010 与 ResponseDepartmentNotFound=9011"
```

---

## Task 3: super_admin_service CreateAdmin/UpdateAdmin 接入守卫

**Files:**
- Modify: `d:/www/admin/internal/service/super_admin_service.go`

- [ ] **Step 1：CreateAdmin 在 ensureDepartmentInScope 之后插入 ensureDepartmentExists；在 rejectSuperAdminRoleIDs 之后插入 ensureRolesNotEscalating**

定位（约 line 88-96 区段），改为：
```go
func (s *SuperAdminService) CreateAdmin(ctx context.Context, op *EffectiveDataScope, req *CreateAdminRequest) (*AdminResponse, error) {
    // P1-A：目标部门必须在 op 的数据范围内
    if err := ensureDepartmentInScope(op, req.DepartmentID); err != nil {
        return nil, err
    }
    // P2（新）：目标部门必须真实存在
    if err := ensureDepartmentExists(ctx, s.client, req.DepartmentID); err != nil {
        return nil, err
    }

    if err := rejectSuperAdminRoleIDs(ctx, s.client, requestedRoleIDs(req.RoleIDs, req.PrimaryRoleID)); err != nil {
        return nil, err
    }
    // P1（新）：禁止分配数据范围高于操作者的角色
    if err := ensureRolesNotEscalating(ctx, s.client, op, requestedRoleIDs(req.RoleIDs, req.PrimaryRoleID)); err != nil {
        return nil, err
    }
    // ... 后续 email/username 检查保持不变
```

- [ ] **Step 2：UpdateAdmin 在 ensureDepartmentInScope 之后追加 ensureDepartmentExists**

定位 UpdateAdmin 中 `if req.DepartmentID != nil` 区块（约 line 371-376），改为：
```go
if req.DepartmentID != nil {
    if err := ensureDepartmentInScope(op, *req.DepartmentID); err != nil {
        return nil, err
    }
    if err := ensureDepartmentExists(ctx, s.client, *req.DepartmentID); err != nil {
        return nil, err
    }
}
```

- [ ] **Step 3：编译验证**

```powershell
cd d:\www\admin ; go build ./...
```
预期：exit 0。

- [ ] **Step 4：commit**

```powershell
cd d:\www\admin ; git add internal/service/super_admin_service.go ; git commit -m "feat(admin): CreateAdmin/UpdateAdmin 接入 ensureRolesNotEscalating + ensureDepartmentExists"
```

---

## Task 4: AssignAdminRoles 接入 ensureRolesNotEscalating

**Files:**
- Modify: `d:/www/admin/internal/service/role_assignment.go`

- [ ] **Step 1：在 AssignAdminRoles 中 rejectSuperAdminRoleIDs 之后插入守卫**

定位约 line 298-300 区段：
```go
if err := rejectSuperAdminRoleIDs(ctx, s.client, requestedRoleIDs(req.RoleIDs, req.PrimaryRoleID)); err != nil {
    return err
}
// P1（新）：禁止分配数据范围高于操作者的角色
if err := ensureRolesNotEscalating(ctx, s.client, op, requestedRoleIDs(req.RoleIDs, req.PrimaryRoleID)); err != nil {
    return err
}
```

- [ ] **Step 2：编译验证**

```powershell
cd d:\www\admin ; go build ./...
```
预期：exit 0。

- [ ] **Step 3：commit**

```powershell
cd d:\www\admin ; git add internal/service/role_assignment.go ; git commit -m "feat(role-assign): AssignAdminRoles 接入 ensureRolesNotEscalating"
```

---

## Task 5: RoleService CreateRole/UpdateRole 部门存在性校验

**Files:**
- Modify: `d:/www/admin/internal/service/role.go`

- [ ] **Step 1：找到 CreateRole 中 DataScope=Custom 处理位置**

```powershell
cd d:\www\admin ; Select-String -Path internal/service/role.go -Pattern "DataScopeDepartmentIds|data_scope_department_ids|DataScope.*Custom"
```

- [ ] **Step 2：在 CreateRole 与 UpdateRole 写入 DataScopeDepartmentIDs 之前插入循环校验**

形如（按 role.go 实际写入路径就近插入；常见位置在 SetDataScopeDepartmentIds 调用前）：
```go
if req.DataScope == string(DataScopeCustom) {
    for _, deptID := range req.DataScopeDepartmentIDs {
        if err := ensureDepartmentExists(ctx, s.client, deptID); err != nil {
            return nil, err
        }
    }
}
```
UpdateRole 同样追加。**注意字段命名以 role.go 实际为准**（若字段叫 DataScopeDepartmentIds 则用该名）。

- [ ] **Step 3：编译验证**

```powershell
cd d:\www\admin ; go build ./...
```
预期：exit 0。如出现 import 缺失，加 `"go-admin/internal/ent/department"`（多数情况已通过 data_permission.go 同包间接覆盖，但 role.go 自身 import 仍需）。

- [ ] **Step 4：commit**

```powershell
cd d:\www\admin ; git add internal/service/role.go ; git commit -m "feat(role): CreateRole/UpdateRole 自定义范围部门 ID 存在性校验"
```

---

## Task 6: handler 错误映射追加新错误码

**Files:**
- Modify: `d:/www/admin/internal/handler/admin_list.go`
- Modify: `d:/www/admin/internal/handler/role_assignment.go`
- Modify: `d:/www/admin/internal/handler/role.go`

- [ ] **Step 1：admin_list.go 的 mapAdminCreateError 与 mapAdminScopeError 追加 case**

定位 `mapAdminCreateError`，在现有 `case service.ErrDepartmentOutOfScope:` 之后追加：
```go
case service.ErrRoleScopeEscalation:
    return response.Fail(global.ResponseRoleScopeEscalation, global.ResponseMsg[global.ResponseRoleScopeEscalation]).ToJSON(c)
case service.ErrDepartmentNotFound:
    return response.Fail(global.ResponseDepartmentNotFound, global.ResponseMsg[global.ResponseDepartmentNotFound]).ToJSON(c)
```
`mapAdminScopeError`（UpdateAdmin 用）同样在 ErrDepartmentOutOfScope 后追加 ErrDepartmentNotFound 分支（角色提权一般不会从 update 走，但保持容错可双加 ErrRoleScopeEscalation）。

- [ ] **Step 2：role_assignment.go 的错误映射追加**

```powershell
cd d:\www\admin ; Select-String -Path internal/handler/role_assignment.go -Pattern "ErrPrimaryRoleInvalid|ErrInvalidRoleIDs"
```
在错误映射 switch 中追加：
```go
case service.ErrRoleScopeEscalation:
    return response.Fail(global.ResponseRoleScopeEscalation, global.ResponseMsg[global.ResponseRoleScopeEscalation]).ToJSON(c)
```

- [ ] **Step 3：role.go 的 Create/Update Role 错误映射追加**

定位 handler/role.go 的 Create/Update 处理函数错误映射：
```go
case service.ErrDepartmentNotFound:
    return response.Fail(global.ResponseDepartmentNotFound, global.ResponseMsg[global.ResponseDepartmentNotFound]).ToJSON(c)
```

- [ ] **Step 4：编译验证**

```powershell
cd d:\www\admin ; go build ./...
```
预期：exit 0。

- [ ] **Step 5：commit**

```powershell
cd d:\www\admin ; git add internal/handler/admin_list.go internal/handler/role_assignment.go internal/handler/role.go ; git commit -m "feat(handler): 追加 ErrRoleScopeEscalation/ErrDepartmentNotFound 错误映射"
```

---

## Task 7: service 层 E2E 测试（6 个）

**Files:**
- Modify: `d:/www/admin/internal/service/super_admin_service_test.go`
- Modify: `d:/www/admin/internal/service/role_assignment_test.go`
- Modify: `d:/www/admin/internal/service/role_test.go`（若不存在则用 super_admin_service_test.go）

- [ ] **Step 1：super_admin_service_test.go 追加 3 个 E2E**

```go
func TestSuperAdminService_CreateAdmin_RejectsRoleScopeEscalation(t *testing.T) {
    ctx, client, svc, cleanup := setupSuperAdminTest(t)
    defer cleanup()
    rAll := client.Role.Create().SetName("r_esc").SetCode("r_esc").
        SetDataScope(role.DataScopeAll).SetStatus(role.StatusActive).SaveX(ctx)
    op := &EffectiveDataScope{
        Scope: DataScopeDepartment, AdminID: 1, AdminUID: "u-mgr",
        DepartmentID: 1, DepartmentIDs: []int{1},
    }
    _, err := svc.CreateAdmin(ctx, op, &CreateAdminRequest{
        Email: "esc@example.com", Username: "esc", Password: "secret123!",
        DepartmentID: 1, RoleIDs: []int{rAll.ID}, PrimaryRoleID: rAll.ID,
    })
    if !errors.Is(err, ErrRoleScopeEscalation) {
        t.Fatalf("期望 ErrRoleScopeEscalation, got %v", err)
    }
    _ = client
}

func TestSuperAdminService_CreateAdmin_RejectsNonExistentDepartment(t *testing.T) {
    ctx, client, svc, cleanup := setupSuperAdminTest(t)
    defer cleanup()
    r := client.Role.Create().SetName("r_ok").SetCode("r_ok").
        SetDataScope(role.DataScopeSelf).SetStatus(role.StatusActive).SaveX(ctx)
    op := &EffectiveDataScope{IsSuperAdmin: true, AdminID: 1, AdminUID: "u-sa"}
    _, err := svc.CreateAdmin(ctx, op, &CreateAdminRequest{
        Email: "ne@example.com", Username: "ne", Password: "secret123!",
        DepartmentID: 999999, RoleIDs: []int{r.ID}, PrimaryRoleID: r.ID,
    })
    if !errors.Is(err, ErrDepartmentNotFound) {
        t.Fatalf("期望 ErrDepartmentNotFound, got %v", err)
    }
}

func TestSuperAdminService_UpdateAdmin_RejectsNonExistentDepartmentMigration(t *testing.T) {
    ctx, client, svc, cleanup := setupSuperAdminTest(t)
    defer cleanup()
    a := client.Admin.Create().SetEmail("u@e.com").SetUsername("u").
        SetPassword("x").SetPasswordSalt("s").SetUID("uid-u").
        SetNickname("n").SetName("nm").SetDepartmentID(1).SaveX(ctx)
    op := &EffectiveDataScope{IsSuperAdmin: true, AdminID: 100, AdminUID: "u-sa"}
    newDept := 999999
    _, err := svc.UpdateAdmin(ctx, op, a.ID, &UpdateAdminRequest{DepartmentID: &newDept})
    if !errors.Is(err, ErrDepartmentNotFound) {
        t.Fatalf("期望 ErrDepartmentNotFound, got %v", err)
    }
}
```

- [ ] **Step 2：role_assignment_test.go 追加 1 个 E2E**

若 role_assignment 已有 setup helper，复用；否则简化为：
```go
func TestRoleAssignmentService_AssignAdminRoles_RejectsRoleScopeEscalation(t *testing.T) {
    ctx, client, svc, cleanup := setupRoleAssignmentTest(t) // 或 setupSuperAdminTest 复用
    defer cleanup()
    a := client.Admin.Create().SetEmail("a@e.com").SetUsername("a").
        SetPassword("x").SetPasswordSalt("s").SetUID("uid-a").
        SetNickname("n").SetName("nm").SetDepartmentID(1).SaveX(ctx)
    rAll := client.Role.Create().SetName("r_esc2").SetCode("r_esc2").
        SetDataScope(role.DataScopeAll).SetStatus(role.StatusActive).SaveX(ctx)
    op := &EffectiveDataScope{
        Scope: DataScopeDepartment, AdminID: 99, AdminUID: "u-mgr",
        DepartmentID: 1, DepartmentIDs: []int{1},
    }
    err := svc.AssignAdminRoles(ctx, op, a.ID,
        &AssignRolesRequest{RoleIDs: []int{rAll.ID}, PrimaryRoleID: rAll.ID}, "u-mgr")
    if !errors.Is(err, ErrRoleScopeEscalation) {
        t.Fatalf("期望 ErrRoleScopeEscalation, got %v", err)
    }
}
```
**若 role_assignment_test.go 不存在或无 setup helper**：直接在 super_admin_service_test.go 实现（用 setupSuperAdminTest 取得 client，再 `NewRoleAssignmentService(client)` 构造）。

- [ ] **Step 3：role 测试（CreateRole/UpdateRole 部门存在性）**

```powershell
cd d:\www\admin ; Test-Path internal/service/role_test.go
```
若存在则追加；不存在直接放在 super_admin_service_test.go：
```go
func TestRoleService_CreateRole_RejectsNonExistentCustomDepartmentID(t *testing.T) {
    ctx, client, _, cleanup := setupSuperAdminTest(t)
    defer cleanup()
    svc := NewRoleService(client) // 以实际构造函数为准
    _, err := svc.CreateRole(ctx, &CreateRoleRequest{
        Name: "r_x", Code: "r_x",
        DataScope: string(DataScopeCustom),
        DataScopeDepartmentIDs: []int{999999},
    })
    if !errors.Is(err, ErrDepartmentNotFound) {
        t.Fatalf("期望 ErrDepartmentNotFound, got %v", err)
    }
}

func TestRoleService_UpdateRole_RejectsNonExistentCustomDepartmentID(t *testing.T) {
    ctx, client, _, cleanup := setupSuperAdminTest(t)
    defer cleanup()
    svc := NewRoleService(client)
    r, err := svc.CreateRole(ctx, &CreateRoleRequest{
        Name: "r_y", Code: "r_y", DataScope: string(DataScopeSelf),
    })
    if err != nil { t.Fatalf("setup: %v", err) }
    _, err = svc.UpdateRole(ctx, r.ID, &UpdateRoleRequest{
        DataScope: string(DataScopeCustom),
        DataScopeDepartmentIDs: []int{999999},
    })
    if !errors.Is(err, ErrDepartmentNotFound) {
        t.Fatalf("期望 ErrDepartmentNotFound, got %v", err)
    }
}
```
**字段命名与构造函数以 role.go 实际为准**；若签名不同请用 Read 工具读 role.go 取实际签名后调整。

- [ ] **Step 4：跑测试确认 PASS**

```powershell
cd d:\www\admin ; go test ./internal/service/ -count=1 -v -run "RoleScopeEscalation|NonExistentDepartment|NonExistentCustomDepartment"
```
预期：6 个新增 E2E 全 PASS。

- [ ] **Step 5：commit**

```powershell
cd d:\www\admin ; git add internal/service/super_admin_service_test.go internal/service/role_assignment_test.go internal/service/role_test.go ; git commit -m "test(data-permission): 角色提权+部门存在性 6 个 E2E 测试"
```

---

## Task 8: 最终全量验证

- [ ] **Step 1：全量 build**

```powershell
cd d:\www\admin ; go build ./...
```
预期：exit 0。

- [ ] **Step 2：全量 test**

```powershell
cd d:\www\admin ; go test ./... -count=1
```
预期：exit 0，所有包 `ok`。

- [ ] **Step 3：git log 检查**

```powershell
cd d:\www\admin ; git log --oneline -10
```
确认本轮 7 个 commit 都在。

---

## 收尾

完成后用户应能看到：
- 数据权限纵深防御第三层闭环
- 部门管理员无法通过分配 All/Custom 高范围角色或写入幽灵部门 ID 绕过自身限制
- 2 个新守卫 + 2 个新错误码 + 6 个 E2E + 14 个单元测试
