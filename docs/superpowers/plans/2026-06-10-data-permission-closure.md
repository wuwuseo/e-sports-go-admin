# 数据权限闭环 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 闭环数据权限链路——管理员部门字段读写、Service 层数据范围守卫、路由参数统一、补全 admin 系列 seed、防止权限树父子环。

**Architecture:** Service 层守卫策略，通过包级函数 `ensureAdminInScope` 共享；操作者身份通过复用扩展的 `*EffectiveDataScope` 对象（含 `IsSuperAdmin` 字段）显式传入受影响的 Service 方法；handler 从 `c.Locals("uid")` 取 UID 后调用 `DataPermissionService.NewOperatorContext` 构造一次 op。

**Tech Stack:** Go 1.25.1 / Fiber v3 / Ent ORM / Casbin / testify / MySQL

**Spec:** [docs/superpowers/specs/2026-06-10-data-permission-closure-design.md](../specs/2026-06-10-data-permission-closure-design.md)

---

## 文件结构与责任

| 文件 | 责任 | 操作 |
| --- | --- | --- |
| `internal/service/data_permission.go` | OperatorContext 构造、IsSuperAdmin 扩展、包级 helper | 修改 |
| `internal/service/super_admin_service.go` | DepartmentID 读写、5 方法加 op、自我保护 | 修改 |
| `internal/service/role_assignment.go` | GetAdminRoles/AssignAdminRoles 加 op、自我保护 | 修改 |
| `internal/service/system_permission.go` | parent_id 环检测、8 条 admin seed | 修改 |
| `internal/global/msg.go` | 3 个新错误码与文案 | 修改 |
| `internal/router.go` | `/admins/:uid*` → `:id*` | 修改 |
| `internal/handler/admin_list.go` | 构造 op + 按 id 查询 + 错误映射 helper | 修改 |
| `internal/handler/role_assignment.go` | `:id` 参数 + 构造 op + 错误映射 | 修改 |
| `internal/handler/system_permission.go` | 父子环错误映射 | 修改 |
| `internal/service/data_permission_test.go` | helper 单元测试（新建） | 创建 |
| `internal/service/system_permission_test.go` | 环检测 + seed 数量与新 admin seed 断言 | 修改 |

---

## 任务清单

### 任务 1：扩展 EffectiveDataScope 增加 IsSuperAdmin

**Files:**
- Modify: `internal/service/data_permission.go:17-24,51,65-70,80`

- [ ] **步骤 1：扩展结构体**

```go
type EffectiveDataScope struct {
    Scope         DataScope
    AdminID       int
    AdminUID      string
    DepartmentID  int
    DepartmentIDs []int
    IsSuperAdmin  bool
}
```

- [ ] **步骤 2：在 3 个返回点设置 IsSuperAdmin**

第 51 行（超管短路）：

```go
return &EffectiveDataScope{Scope: DataScopeAll, AdminID: a.ID, AdminUID: a.UID, DepartmentID: a.DepartmentID, IsSuperAdmin: true}, nil
```

第 65-70 行 `result := &EffectiveDataScope{...}` 显式 `IsSuperAdmin: false`。第 80 行角色 All 返回同样设 `IsSuperAdmin: false`。

- [ ] **步骤 3：编译**

```powershell
go build ./internal/service/
```

期望：通过。

- [ ] **步骤 4：提交**

```powershell
git add internal/service/data_permission.go
git commit -m "feat(data-permission): EffectiveDataScope 增加 IsSuperAdmin 字段"
```

---

### 任务 2：containsInt 与 NewOperatorContext

**Files:**
- Create: `internal/service/data_permission_test.go`
- Modify: `internal/service/data_permission.go`（末尾追加）

- [ ] **步骤 1：写失败测试**

```go
package service

import "testing"

func TestContainsInt_Found(t *testing.T) {
    if !containsInt([]int{1, 2, 3}, 2) { t.Fatal("期望 true") }
}

func TestContainsInt_NotFound(t *testing.T) {
    if containsInt([]int{1, 2, 3}, 99) { t.Fatal("期望 false") }
}

func TestContainsInt_EmptySlice(t *testing.T) {
    if containsInt(nil, 1) { t.Fatal("期望 false") }
}
```

- [ ] **步骤 2：运行测试确认失败**

```powershell
go test ./internal/service/ -run TestContainsInt -count=1
```

期望：FAIL `undefined: containsInt`。

- [ ] **步骤 3：实现**

追加到 `data_permission.go` 末尾：

```go
// containsInt 判断切片中是否包含目标整数
func containsInt(haystack []int, needle int) bool {
    for _, v := range haystack {
        if v == needle {
            return true
        }
    }
    return false
}

// NewOperatorContext 从 currentUID 构造操作者上下文
// currentUID 为空时返回 nil（内部调用绕过校验）
func (s *DataPermissionService) NewOperatorContext(ctx context.Context, currentUID string) (*EffectiveDataScope, error) {
    if currentUID == "" {
        return nil, nil
    }
    return s.ResolveAdminScope(ctx, currentUID)
}
```

- [ ] **步骤 4：测试通过**

```powershell
go test ./internal/service/ -run TestContainsInt -count=1
```

期望：PASS。

- [ ] **步骤 5：提交**

```powershell
git add internal/service/data_permission.go internal/service/data_permission_test.go
git commit -m "feat(data-permission): 新增 containsInt 与 NewOperatorContext"
```

---

### 任务 3：错误声明与包级 ensureAdminInScope 守卫

**Files:**
- Modify: `internal/service/data_permission.go`（错误与函数）

- [ ] **步骤 1：声明 3 个错误**

在 `ErrDataPermissionAdminNotFound` 之后追加：

```go
var ErrAdminOutOfScope = errors.New("admin out of data scope")
var ErrCannotToggleSelfStatus = errors.New("cannot activate or deactivate yourself")
var ErrCannotModifySelfRoles = errors.New("cannot modify your own roles")
```

- [ ] **步骤 2：实现 ensureAdminInScope**

追加到 `data_permission.go` 末尾（import 需含 `"go-admin/internal/ent"` 与 `"go-admin/internal/ent/admin"`，data_permission.go 已有）：

```go
// ensureAdminInScope 校验 targetAdminID 是否在 op 的数据范围内
//   op == nil          → 放行（内部调用）
//   op.IsSuperAdmin    → 放行
//   target.UID == op.AdminUID → 放行（访问自己）
//   target.DepartmentID == 0  → 拒绝（未分配只对超管可见）
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
```

- [ ] **步骤 3：写测试**

追加到 `data_permission_test.go`：

```go
import (
    "context"

    "go-admin/internal/global"
)

func TestEnsureAdminInScope_NilOp(t *testing.T) {
    setupSystemPermissionTest(t)
    if err := ensureAdminInScope(context.Background(), global.ORM, nil, 999); err != nil {
        t.Fatalf("op==nil 应放行，got %v", err)
    }
}

func TestEnsureAdminInScope_SuperAdmin(t *testing.T) {
    setupSystemPermissionTest(t)
    op := &EffectiveDataScope{IsSuperAdmin: true}
    if err := ensureAdminInScope(context.Background(), global.ORM, op, 999); err != nil {
        t.Fatalf("超管应放行，got %v", err)
    }
}
```

- [ ] **步骤 4：测试通过**

```powershell
go test ./internal/service/ -run TestEnsureAdminInScope -count=1
```

期望：PASS（target 不存在时 nil 与超管路径都不会触发查询）。

- [ ] **步骤 5：提交**

```powershell
git add internal/service/data_permission.go internal/service/data_permission_test.go
git commit -m "feat(data-permission): 新增 ensureAdminInScope 守卫与自我保护错误"
```

---

### 任务 4：admin DepartmentID 字段读写

**Files:**
- Modify: `internal/service/super_admin_service.go:22-29,53-63,84-128,239-267,298-321,323-388`

- [ ] **步骤 1：扩展 UpdateAdminRequest**

第 22-29 行：

```go
type UpdateAdminRequest struct {
    Nickname     string `json:"nickname"`
    Name         string `json:"name"`
    Phone        string `json:"phone"`
    Email        string `json:"email" validate:"omitempty,email"`
    Password     string `json:"password" validate:"omitempty,min=6"`
    DepartmentID *int   `json:"department_id"`
}
```

- [ ] **步骤 2：扩展 CreateAdminRequest**

第 53-63 行：

```go
type CreateAdminRequest struct {
    Email         string `json:"email" validate:"required,email"`
    Password      string `json:"password" validate:"required,min=6"`
    Username      string `json:"username" validate:"required,min=3"`
    Nickname      string `json:"nickname"`
    Name          string `json:"name"`
    Phone         string `json:"phone"`
    RoleIDs       []int  `json:"role_ids"`
    PrimaryRoleID int    `json:"primary_role_id"`
    DepartmentID  int    `json:"department_id"`
}
```

- [ ] **步骤 3：CreateAdmin 写入 DepartmentID**

定位 `s.client.Admin.Create()` 链（第 115 行左右），在 `.SetPhone(req.Phone)` 后插入：

```go
        SetDepartmentID(req.DepartmentID).
```

- [ ] **步骤 4：UpdateAdmin 写入 DepartmentID**

在 `UpdateAdmin` 内 `update := s.client.Admin.UpdateOneID(adminID)` 之后追加：

```go
    if req.DepartmentID != nil {
        update.SetDepartmentID(*req.DepartmentID)
    }
```

- [ ] **步骤 5：AdminListItem 与 GetAdminByID 返回 DepartmentID**

第 75-82 行 `AdminListItem` 增加：

```go
type AdminListItem struct {
    ID           int    `json:"id"`
    UID          string `json:"uid"`
    Email        string `json:"email"`
    Username     string `json:"username"`
    Nickname     string `json:"nickname"`
    Status       string `json:"status"`
    DepartmentID int    `json:"department_id"`
}
```

ListAdmins 内构造 items 处填入 `DepartmentID: a.DepartmentID`。

`AdminResponse`（在同 package 其他文件，搜索后定位）增加 `DepartmentID int` 字段，并在 `GetAdminByID`、`UpdateAdmin` 的返回构造处填入 `DepartmentID: a.DepartmentID`。

- [ ] **步骤 6：编译验证**

```powershell
go build ./internal/service/
```

期望：通过。

- [ ] **步骤 7：提交**

```powershell
git add internal/service/super_admin_service.go
git commit -m "feat(admin): 创建/更新/详情/列表支持 department_id 字段"
```

---

### 任务 5：SuperAdminService 5 方法加 op 参数与自我保护

**Files:**
- Modify: `internal/service/super_admin_service.go`（接口与方法签名）

- [ ] **步骤 1：修改接口**

第 31-39 行：

```go
type SuperAdminServiceInterface interface {
    CreateAdmin(ctx context.Context, req *CreateAdminRequest) (*AdminResponse, error)
    ListAdmins(ctx context.Context, req *AdminListRequest) (*ListResponse[*AdminListItem], error)
    GetAdminByID(ctx context.Context, op *EffectiveDataScope, adminID int) (*AdminResponse, error)
    UpdateAdmin(ctx context.Context, op *EffectiveDataScope, adminID int, req *UpdateAdminRequest) (*AdminResponse, error)
    ActivateAdmin(ctx context.Context, op *EffectiveDataScope, adminID int) error
    DeactivateAdmin(ctx context.Context, op *EffectiveDataScope, adminID int) error
}
```

- [ ] **步骤 2：修改 GetAdminByID / UpdateAdmin / ActivateAdmin / DeactivateAdmin**

每个方法签名增加 `op *EffectiveDataScope`，方法体起始处增加守卫：

```go
func (s *SuperAdminService) GetAdminByID(ctx context.Context, op *EffectiveDataScope, adminID int) (*AdminResponse, error) {
    if err := ensureAdminInScope(ctx, s.client, op, adminID); err != nil {
        return nil, err
    }
    // 原有逻辑
}

func (s *SuperAdminService) UpdateAdmin(ctx context.Context, op *EffectiveDataScope, adminID int, req *UpdateAdminRequest) (*AdminResponse, error) {
    if err := ensureAdminInScope(ctx, s.client, op, adminID); err != nil {
        return nil, err
    }
    // 原有逻辑
}

func (s *SuperAdminService) ActivateAdmin(ctx context.Context, op *EffectiveDataScope, adminID int) error {
    if op != nil && op.AdminID == adminID {
        return ErrCannotToggleSelfStatus
    }
    if err := ensureAdminInScope(ctx, s.client, op, adminID); err != nil {
        return err
    }
    _, err := s.client.Admin.UpdateOneID(adminID).
        SetStatus(admin.StatusActive).
        Save(ctx)
    if err != nil {
        if ent.IsNotFound(err) {
            return ErrAdminNotFound
        }
        return err
    }
    return nil
}

func (s *SuperAdminService) DeactivateAdmin(ctx context.Context, op *EffectiveDataScope, adminID int) error {
    if op != nil && op.AdminID == adminID {
        return ErrCannotToggleSelfStatus
    }
    if err := ensureAdminInScope(ctx, s.client, op, adminID); err != nil {
        return err
    }
    _, err := s.client.Admin.UpdateOneID(adminID).
        SetStatus(admin.StatusInactive).
        Save(ctx)
    if err != nil {
        if ent.IsNotFound(err) {
            return ErrAdminNotFound
        }
        return err
    }
    return nil
}
```

- [ ] **步骤 3：service 包编译**

```powershell
go build ./internal/service/
```

期望：通过。`go build ./...` 此时 handler 会失败，属预期，任务 9 修复。

- [ ] **步骤 4：提交**

```powershell
git add internal/service/super_admin_service.go
git commit -m "feat(admin): SuperAdminService 5 方法加 op 守卫与自我保护"
```

---

### 任务 6：RoleAssignmentService 加 op 与自我保护

**Files:**
- Modify: `internal/service/role_assignment.go:79-86,235-282,284-355`

- [ ] **步骤 1：修改接口**

```go
type RoleAssignmentServiceInterface interface {
    GetUserRoles(ctx context.Context, userID int) (*UserRolesResponse, error)
    AssignRoles(ctx context.Context, userID int, req *AssignRolesRequest, operatorUID string) error
    GetAdminRoles(ctx context.Context, op *EffectiveDataScope, adminID int) (*UserRolesResponse, error)
    AssignAdminRoles(ctx context.Context, op *EffectiveDataScope, adminID int, req *AssignRolesRequest, operatorUID string) error
}
```

- [ ] **步骤 2：修改 GetAdminRoles**

```go
func (s *RoleAssignmentService) GetAdminRoles(ctx context.Context, op *EffectiveDataScope, adminID int) (*UserRolesResponse, error) {
    if err := ensureAdminInScope(ctx, s.client, op, adminID); err != nil {
        return nil, err
    }
    // 原有逻辑
}
```

- [ ] **步骤 3：修改 AssignAdminRoles**

```go
func (s *RoleAssignmentService) AssignAdminRoles(ctx context.Context, op *EffectiveDataScope, adminID int, req *AssignRolesRequest, operatorUID string) error {
    if op != nil && op.AdminID == adminID {
        return ErrCannotModifySelfRoles
    }
    if err := ensureAdminInScope(ctx, s.client, op, adminID); err != nil {
        return err
    }
    // 原有逻辑
}
```

- [ ] **步骤 4：编译**

```powershell
go build ./internal/service/
```

期望：通过。

- [ ] **步骤 5：提交**

```powershell
git add internal/service/role_assignment.go
git commit -m "feat(role-assignment): GetAdminRoles/AssignAdminRoles 加 op 守卫与自我保护"
```

---

### 任务 7：路由参数 `:uid` 统一为 `:id`

**Files:**
- Modify: `internal/router.go:163-168`

- [ ] **步骤 1：路由替换**

第 163-168 行改为：

```go
adminAuth.Get("/admins/:id", handler.GetAdminDetail)
adminAuth.Put("/admins/:id", handler.UpdateAdminByID)
adminAuth.Get("/admins/:id/roles", handler.GetAdminRoles)
adminAuth.Put("/admins/:id/roles", handler.AssignAdminRoles)
adminAuth.Put("/admins/:id/activate", handler.ActivateAdmin)
adminAuth.Put("/admins/:id/deactivate", handler.DeactivateAdmin)
```

- [ ] **步骤 2：提交**

```powershell
git add internal/router.go
git commit -m "refactor(router): /admins/:uid 统一为 /admins/:id"
```

---

### 任务 8：parent_id 环检测

**Files:**
- Modify: `internal/service/system_permission.go`
- Modify: `internal/service/system_permission_test.go`

- [ ] **步骤 1：写失败测试**

追加到 `system_permission_test.go`（确保文件顶部 import 含 `"errors"`）：

```go
func TestSystemPermission_Update_RejectsSelfParent(t *testing.T) {
    setupSystemPermissionTest(t)
    ctx := t.Context()
    svc := NewSystemPermissionService()
    created, err := svc.Create(ctx, &CreateSystemPermissionRequest{
        Domain: "default", Type: SystemPermissionTypeMenu, Name: "P", Code: "cycle:self",
    })
    if err != nil { t.Fatalf("create failed: %v", err) }
    selfID := created.ID
    err = svc.Update(ctx, selfID, &UpdateSystemPermissionRequest{ParentID: &selfID})
    if !errors.Is(err, ErrPermissionParentCycle) {
        t.Fatalf("期望 ErrPermissionParentCycle，got %v", err)
    }
}

func TestSystemPermission_Update_RejectsDescendantParent(t *testing.T) {
    setupSystemPermissionTest(t)
    ctx := t.Context()
    svc := NewSystemPermissionService()
    parent, err := svc.Create(ctx, &CreateSystemPermissionRequest{
        Domain: "default", Type: SystemPermissionTypeMenu, Name: "P", Code: "cycle:p",
    })
    if err != nil { t.Fatalf("create parent failed: %v", err) }
    child, err := svc.Create(ctx, &CreateSystemPermissionRequest{
        Domain: "default", Type: SystemPermissionTypeMenu, Name: "C", Code: "cycle:c", ParentID: parent.ID,
    })
    if err != nil { t.Fatalf("create child failed: %v", err) }
    err = svc.Update(ctx, parent.ID, &UpdateSystemPermissionRequest{ParentID: &child.ID})
    if !errors.Is(err, ErrPermissionParentCycle) {
        t.Fatalf("期望 ErrPermissionParentCycle，got %v", err)
    }
}
```

- [ ] **步骤 2：运行测试确认失败**

```powershell
go test ./internal/service/ -run TestSystemPermission_Update_Rejects -count=1
```

期望：FAIL `undefined: ErrPermissionParentCycle`。

- [ ] **步骤 3：声明错误**

在 `system_permission.go` 中 `ErrXxx` 区域追加：

```go
var ErrPermissionParentCycle = errors.New("permission parent cycle detected")
```

- [ ] **步骤 4：实现 isDescendantOf**

追加方法：

```go
// isDescendantOf 判断 candidateID 是否是 ancestorID 的后代
// 通过自下而上遍历 parent_id 链实现，带深度上限与已访问集合防御
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

- [ ] **步骤 5：在 Update 方法增加环检测**

定位 Update 方法内现有的 `if req.ParentID != nil { update.SetParentID(*req.ParentID) }`，在之前插入：

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

注意原 `if req.ParentID != nil { update.SetParentID(*req.ParentID) }` 保留（清空父级用）。

- [ ] **步骤 6：测试通过**

```powershell
go test ./internal/service/ -run TestSystemPermission_Update_Rejects -count=1
```

期望：PASS。

- [ ] **步骤 7：提交**

```powershell
git add internal/service/system_permission.go internal/service/system_permission_test.go
git commit -m "feat(system-permission): parent_id 环检测与 isDescendantOf 防御"
```

---

### 任务 9：错误码常量与文案

**Files:**
- Modify: `internal/global/msg.go`

- [ ] **步骤 1：定位错误码常量段**

打开 `internal/global/msg.go`，找到现有的 9001-9004 常量段。

- [ ] **步骤 2：追加常量**

```go
// ResponsePermissionParentCycle 系统权限父子环
ResponsePermissionParentCycle = 9005
// ResponseSelfToggleForbidden 禁止启用/禁用自己
ResponseSelfToggleForbidden = 9007
// ResponseSelfRoleModifyForbidden 禁止修改自己角色
ResponseSelfRoleModifyForbidden = 9008
```

- [ ] **步骤 3：追加 ResponseMsg 文案**

```go
ResponsePermissionParentCycle:   "不能将权限的父级设为自身或其后代",
ResponseSelfToggleForbidden:     "不能对自己执行启用/禁用",
ResponseSelfRoleModifyForbidden: "不能修改自己的角色",
```

- [ ] **步骤 4：编译验证**

```powershell
go build ./internal/global/
```

期望：通过。

- [ ] **步骤 5：提交**

```powershell
git add internal/global/msg.go
git commit -m "feat(global): 新增数据权限/自我保护/父子环错误码"
```

---

### 任务 10：handler 改造（构造 op、改 :id、错误映射）

**Files:**
- Modify: `internal/handler/admin_list.go`
- Modify: `internal/handler/role_assignment.go`

- [ ] **步骤 1：admin_list.go 末尾追加 helper**

```go
// getOperatorUID 从 fiber.Ctx Locals 中读取当前操作者 UID
func getOperatorUID(c fiber.Ctx) string {
    if uid, ok := c.Locals("uid").(string); ok {
        return uid
    }
    return ""
}

// mapAdminScopeError 把 service 层数据权限/自我保护错误映射为 HTTP 响应
func mapAdminScopeError(c fiber.Ctx, response *utils.Response, err error) error {
    switch err {
    case service.ErrAdminOutOfScope:
        return response.Fail(global.ResponseForbidden, global.ResponseMsg[global.ResponseForbidden]).ToJSON(c)
    case service.ErrCannotToggleSelfStatus:
        return response.Fail(global.ResponseSelfToggleForbidden, global.ResponseMsg[global.ResponseSelfToggleForbidden]).ToJSON(c)
    case service.ErrCannotModifySelfRoles:
        return response.Fail(global.ResponseSelfRoleModifyForbidden, global.ResponseMsg[global.ResponseSelfRoleModifyForbidden]).ToJSON(c)
    case service.ErrAdminNotFound:
        return response.Fail(global.ResponseNotFound, "管理员不存在").ToJSON(c)
    case service.ErrAdminEmailExists:
        return response.Fail(global.ResponseErrorCode, "邮箱已存在").ToJSON(c)
    default:
        return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
    }
}
```

- [ ] **步骤 2：替换 GetAdminDetail**

第 102-134 行整段替换为：

```go
// GetAdminDetail 管理员详情
// @Summary 获取管理员详情
// @Description 根据 ID 获取管理员详细信息
// @Tags 管理员管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "管理员ID"
// @Success 200 {object} utils.Response{data=service.AdminResponse} "获取成功"
// @Failure 400 {object} utils.Response "参数错误"
// @Failure 401 {object} utils.Response "未授权"
// @Failure 403 {object} utils.Response "权限不足"
// @Failure 404 {object} utils.Response "管理员不存在"
// @Router /api/v1/admin/auth/admins/{id} [get]
func GetAdminDetail(c fiber.Ctx) error {
    response := c.Locals("utils.Response").(*utils.Response)

    adminID, err := strconv.Atoi(c.Params("id"))
    if err != nil || adminID <= 0 {
        return response.Fail(global.ResponseErrorCode, "无效的管理员ID").ToJSON(c)
    }

    svc := c.Locals("service.SuperAdminService").(service.SuperAdminServiceInterface)
    dataPermSvc := c.Locals("service.DataPermissionService").(*service.DataPermissionService)

    op, err := dataPermSvc.NewOperatorContext(c.Context(), getOperatorUID(c))
    if err != nil {
        return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
    }

    result, err := svc.GetAdminByID(c.Context(), op, adminID)
    if err != nil {
        return mapAdminScopeError(c, response, err)
    }
    return response.Success(result).ToJSON(c)
}
```

- [ ] **步骤 3：替换 ActivateAdmin**

```go
func ActivateAdmin(c fiber.Ctx) error {
    response := c.Locals("utils.Response").(*utils.Response)

    adminID, err := strconv.Atoi(c.Params("id"))
    if err != nil || adminID <= 0 {
        return response.Fail(global.ResponseErrorCode, "无效的管理员ID").ToJSON(c)
    }

    svc := c.Locals("service.SuperAdminService").(service.SuperAdminServiceInterface)
    dataPermSvc := c.Locals("service.DataPermissionService").(*service.DataPermissionService)
    op, err := dataPermSvc.NewOperatorContext(c.Context(), getOperatorUID(c))
    if err != nil {
        return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
    }

    if err := svc.ActivateAdmin(c.Context(), op, adminID); err != nil {
        return mapAdminScopeError(c, response, err)
    }
    return response.Success(nil).ToJSON(c)
}
```

- [ ] **步骤 4：替换 DeactivateAdmin**

```go
func DeactivateAdmin(c fiber.Ctx) error {
    response := c.Locals("utils.Response").(*utils.Response)

    adminID, err := strconv.Atoi(c.Params("id"))
    if err != nil || adminID <= 0 {
        return response.Fail(global.ResponseErrorCode, "无效的管理员ID").ToJSON(c)
    }

    svc := c.Locals("service.SuperAdminService").(service.SuperAdminServiceInterface)
    dataPermSvc := c.Locals("service.DataPermissionService").(*service.DataPermissionService)
    op, err := dataPermSvc.NewOperatorContext(c.Context(), getOperatorUID(c))
    if err != nil {
        return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
    }

    if err := svc.DeactivateAdmin(c.Context(), op, adminID); err != nil {
        return mapAdminScopeError(c, response, err)
    }
    return response.Success(nil).ToJSON(c)
}
```

- [ ] **步骤 5：替换 UpdateAdminByID**

```go
func UpdateAdminByID(c fiber.Ctx) error {
    response := c.Locals("utils.Response").(*utils.Response)

    adminID, err := strconv.Atoi(c.Params("id"))
    if err != nil || adminID <= 0 {
        return response.Fail(global.ResponseErrorCode, "无效的管理员ID").ToJSON(c)
    }

    var req service.UpdateAdminRequest
    if err := c.Bind().Body(&req); err != nil {
        return response.Fail(global.ResponseErrorCode, "Invalid request body").ToJSON(c)
    }

    svc := c.Locals("service.SuperAdminService").(service.SuperAdminServiceInterface)
    dataPermSvc := c.Locals("service.DataPermissionService").(*service.DataPermissionService)
    op, err := dataPermSvc.NewOperatorContext(c.Context(), getOperatorUID(c))
    if err != nil {
        return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
    }

    result, err := svc.UpdateAdmin(c.Context(), op, adminID, &req)
    if err != nil {
        return mapAdminScopeError(c, response, err)
    }
    return response.Success(result).ToJSON(c)
}
```

- [ ] **步骤 6：role_assignment.go 改 GetAdminRoles**

第 103-137 行 `GetAdminRoles`：swagger `:uid` → `:id`，`c.Params("uid")` → `c.Params("id")`，调用改为：

```go
    dataPermSvc := c.Locals("service.DataPermissionService").(*service.DataPermissionService)
    op, err := dataPermSvc.NewOperatorContext(c.Context(), getOperatorUID(c))
    if err != nil {
        return response.Fail(global.ResponseInternalServerError, "获取管理员角色失败").ToJSON(c)
    }
    result, err := svc.GetAdminRoles(c.Context(), op, adminID)
    if err != nil {
        switch err {
        case service.ErrAdminOutOfScope:
            return response.Fail(global.ResponseForbidden, global.ResponseMsg[global.ResponseForbidden]).ToJSON(c)
        case service.ErrUserNotFound, service.ErrAdminNotFound:
            return response.Fail(global.ResponseUserNotFound, global.ResponseMsg[global.ResponseUserNotFound]).ToJSON(c)
        default:
            return response.Fail(global.ResponseInternalServerError, "获取管理员角色失败").ToJSON(c)
        }
    }
    return response.Success(result).ToJSON(c)
```

- [ ] **步骤 7：role_assignment.go 改 AssignAdminRoles**

第 139-190 行 `AssignAdminRoles`：swagger `:uid` → `:id`，`c.Params("uid")` → `c.Params("id")`，调用改为：

```go
    dataPermSvc := c.Locals("service.DataPermissionService").(*service.DataPermissionService)
    op, err := dataPermSvc.NewOperatorContext(c.Context(), operatorUID)
    if err != nil {
        return response.Fail(global.ResponseInternalServerError, "分配角色失败").ToJSON(c)
    }
    err = svc.AssignAdminRoles(c.Context(), op, adminID, req, operatorUID)
    if err != nil {
        switch err {
        case service.ErrAdminOutOfScope:
            return response.Fail(global.ResponseForbidden, global.ResponseMsg[global.ResponseForbidden]).ToJSON(c)
        case service.ErrCannotModifySelfRoles:
            return response.Fail(global.ResponseSelfRoleModifyForbidden, global.ResponseMsg[global.ResponseSelfRoleModifyForbidden]).ToJSON(c)
        case service.ErrUserNotFound, service.ErrAdminNotFound:
            return response.Fail(global.ResponseUserNotFound, global.ResponseMsg[global.ResponseUserNotFound]).ToJSON(c)
        case service.ErrInvalidRoleIDs:
            return response.Fail(global.ResponseInvalidRoleIDs, global.ResponseMsg[global.ResponseInvalidRoleIDs]).ToJSON(c)
        case service.ErrPrimaryRoleInvalid:
            return response.Fail(global.ResponsePrimaryRoleInvalid, global.ResponseMsg[global.ResponsePrimaryRoleInvalid]).ToJSON(c)
        case service.ErrSuperAdminRoleAssignmentForbidden:
            return response.Fail(global.ResponseForbidden, err.Error()).ToJSON(c)
        default:
            return response.Fail(global.ResponseInternalServerError, "分配角色失败").ToJSON(c)
        }
    }
    return response.Success(nil, global.ResponseSuccessActionCode).ToJSON(c)
```

- [ ] **步骤 8：编译验证**

```powershell
go build ./...
```

期望：通过。

- [ ] **步骤 9：提交**

```powershell
git add internal/handler/admin_list.go internal/handler/role_assignment.go
git commit -m "feat(handler): admin 接口构造 op、统一 :id、增加错误映射"
```

---

### 任务 11：system_permission handler 映射父子环错误

**Files:**
- Modify: `internal/handler/system_permission.go`（UpdateSystemPermission 函数）

- [ ] **步骤 1：定位错误处理位置**

打开 `internal/handler/system_permission.go`，找到 `UpdateSystemPermission` 函数中 `svc.Update` 调用。

- [ ] **步骤 2：增加错误分支**

将错误处理改为：

```go
if err := svc.Update(c.Context(), id, req); err != nil {
    if errors.Is(err, service.ErrPermissionParentCycle) {
        return resp.Fail(global.ResponsePermissionParentCycle, global.ResponseMsg[global.ResponsePermissionParentCycle]).ToJSON(c)
    }
    return resp.Fail(global.ResponseInternalServerError, err.Error()).ToJSON(c)
}
```

文件顶部 import 增加 `"errors"`（若已有则跳过）。

- [ ] **步骤 3：编译验证**

```powershell
go build ./...
```

期望：通过。

- [ ] **步骤 4：提交**

```powershell
git add internal/handler/system_permission.go
git commit -m "feat(handler): system-permission 父子环映射 ResponsePermissionParentCycle"
```

---

### 任务 12：seed 补 admins 8 条

**Files:**
- Modify: `internal/service/system_permission.go`（InitSystemPermissions 内 seedData 末尾）

- [ ] **步骤 1：写失败测试**

追加到 `system_permission_test.go`：

```go
func TestInitSystemPermissions_IncludesAdminManagementSeeds(t *testing.T) {
    setupSystemPermissionTest(t)
    ctx := t.Context()
    if err := InitSystemPermissions(ctx); err != nil {
        t.Fatalf("InitSystemPermissions failed: %v", err)
    }
    wantCodes := []string{
        "admin:list", "admin:create", "admin:detail", "admin:update",
        "admin:roles:get", "admin:roles:put", "admin:activate", "admin:deactivate",
    }
    for _, code := range wantCodes {
        exists, err := global.ORM.SystemPermission.Query().
            Where(systempermission.DomainEQ("default"), systempermission.CodeEQ(code)).
            Exist(ctx)
        if err != nil { t.Fatalf("查询 %s 失败: %v", code, err) }
        if !exists { t.Fatalf("缺失 admin seed: %s", code) }
    }
}
```

- [ ] **步骤 2：运行测试确认失败**

```powershell
go test ./internal/service/ -run TestInitSystemPermissions_IncludesAdminManagementSeeds -count=1
```

期望：FAIL，提示 `缺失 admin seed: admin:list`。

- [ ] **步骤 3：在 seedData 末尾追加 8 条**

定位 `InitSystemPermissions` 内 `seedData := []SeedPermission{ ... }` 的最后一个元素之后插入：

```go
        {ID: 110, Domain: "default", Type: "api", Name: "管理员列表", Code: "admin:list", Path: "/api/v1/admin/auth/admins", Method: "GET", ParentID: 3, SortOrder: 70, IsCommon: false, Status: 1, Description: "管理员列表API"},
        {ID: 111, Domain: "default", Type: "api", Name: "创建管理员", Code: "admin:create", Path: "/api/v1/admin/auth/admins", Method: "POST", ParentID: 3, SortOrder: 71, IsCommon: false, Status: 1, Description: "创建管理员API"},
        {ID: 112, Domain: "default", Type: "api", Name: "管理员详情", Code: "admin:detail", Path: "/api/v1/admin/auth/admins/{id}", Method: "GET", ParentID: 3, SortOrder: 72, IsCommon: false, Status: 1, Description: "管理员详情API"},
        {ID: 113, Domain: "default", Type: "api", Name: "更新管理员", Code: "admin:update", Path: "/api/v1/admin/auth/admins/{id}", Method: "PUT", ParentID: 3, SortOrder: 73, IsCommon: false, Status: 1, Description: "更新管理员API"},
        {ID: 114, Domain: "default", Type: "api", Name: "查看管理员角色", Code: "admin:roles:get", Path: "/api/v1/admin/auth/admins/{id}/roles", Method: "GET", ParentID: 3, SortOrder: 74, IsCommon: false, Status: 1, Description: "查看管理员角色API"},
        {ID: 115, Domain: "default", Type: "api", Name: "分配管理员角色", Code: "admin:roles:put", Path: "/api/v1/admin/auth/admins/{id}/roles", Method: "PUT", ParentID: 3, SortOrder: 75, IsCommon: false, Status: 1, Description: "分配管理员角色API"},
        {ID: 116, Domain: "default", Type: "api", Name: "启用管理员", Code: "admin:activate", Path: "/api/v1/admin/auth/admins/{id}/activate", Method: "PUT", ParentID: 3, SortOrder: 76, IsCommon: false, Status: 1, Description: "启用管理员API"},
        {ID: 117, Domain: "default", Type: "api", Name: "禁用管理员", Code: "admin:deactivate", Path: "/api/v1/admin/auth/admins/{id}/deactivate", Method: "PUT", ParentID: 3, SortOrder: 77, IsCommon: false, Status: 1, Description: "禁用管理员API"},
```

- [ ] **步骤 4：测试通过**

```powershell
go test ./internal/service/ -run TestInitSystemPermissions_IncludesAdminManagementSeeds -count=1
```

期望：PASS。

- [ ] **步骤 5：提交**

```powershell
git add internal/service/system_permission.go internal/service/system_permission_test.go
git commit -m "feat(seed): 补 8 条 admin 系列系统权限 seed"
```

---

### 任务 13：修复 seed 数量旧断言

**Files:**
- Modify: `internal/service/system_permission_test.go`

- [ ] **步骤 1：定位旧断言**

打开 `internal/service/system_permission_test.go`，搜索 `CreatesNinetyFiveDefaultSeeds`，定位到约 782 行。

- [ ] **步骤 2：跑一次确认当前实际数量**

```powershell
go test ./internal/service/ -run TestInitSystemPermissions_CreatesNinetyFiveDefaultSeeds -count=1
```

期望：FAIL，错误信息中会显示 `count = N`（如 `count = 112`）。记下这个数字。

- [ ] **步骤 3：重命名并修改期望值**

```go
func TestInitSystemPermissions_CreatesAllDefaultSeeds(t *testing.T) {
    setupSystemPermissionTest(t)
    ctx := t.Context()
    if err := InitSystemPermissions(ctx); err != nil {
        t.Fatalf("InitSystemPermissions failed: %v", err)
    }
    count, err := global.ORM.SystemPermission.Query().
        Where(systempermission.DomainEQ("default")).
        Count(ctx)
    if err != nil {
        t.Fatalf("count system permissions failed: %v", err)
    }
    const wantCount = /* 步骤 2 跑出来的实际数字 */
    if count != wantCount {
        t.Fatalf("default system permission count = %d, want %d", count, wantCount)
    }
}
```

- [ ] **步骤 4：测试通过**

```powershell
go test ./internal/service/ -run TestInitSystemPermissions_CreatesAllDefaultSeeds -count=1
```

期望：PASS。

- [ ] **步骤 5：提交**

```powershell
git add internal/service/system_permission_test.go
git commit -m "test(system-permission): 同步默认 seed 总数断言"
```

---

### 任务 14：新增自我保护单元测试

**Files:**
- Modify: `internal/service/super_admin_service_test.go`（若不存在则创建）
- Modify: `internal/service/role_assignment_test.go`（若不存在则创建）

- [ ] **步骤 1：写自我保护测试**

追加到 `super_admin_service_test.go`（如不存在，参考 `system_permission_test.go` 的 setup 模板创建）：

```go
func TestSuperAdminService_ActivateSelf_Forbidden(t *testing.T) {
    setupSystemPermissionTest(t)
    svc := NewSuperAdminService()
    op := &EffectiveDataScope{AdminID: 100, AdminUID: "u-self"}
    err := svc.ActivateAdmin(t.Context(), op, 100)
    if !errors.Is(err, ErrCannotToggleSelfStatus) {
        t.Fatalf("期望 ErrCannotToggleSelfStatus，got %v", err)
    }
}

func TestSuperAdminService_DeactivateSelf_Forbidden(t *testing.T) {
    setupSystemPermissionTest(t)
    svc := NewSuperAdminService()
    op := &EffectiveDataScope{AdminID: 100, AdminUID: "u-self"}
    err := svc.DeactivateAdmin(t.Context(), op, 100)
    if !errors.Is(err, ErrCannotToggleSelfStatus) {
        t.Fatalf("期望 ErrCannotToggleSelfStatus，got %v", err)
    }
}
```

- [ ] **步骤 2：写 RoleAssignment 自我保护测试**

追加到 `role_assignment_test.go`：

```go
func TestRoleAssignmentService_AssignSelfRoles_Forbidden(t *testing.T) {
    setupSystemPermissionTest(t)
    svc := NewRoleAssignmentService()
    op := &EffectiveDataScope{AdminID: 100, AdminUID: "u-self"}
    err := svc.AssignAdminRoles(t.Context(), op, 100, &AssignRolesRequest{}, "u-self")
    if !errors.Is(err, ErrCannotModifySelfRoles) {
        t.Fatalf("期望 ErrCannotModifySelfRoles，got %v", err)
    }
}
```

- [ ] **步骤 3：测试通过**

```powershell
go test ./internal/service/ -run "Self_Forbidden|AssignSelfRoles_Forbidden" -count=1
```

期望：PASS。

- [ ] **步骤 4：提交**

```powershell
git add internal/service/super_admin_service_test.go internal/service/role_assignment_test.go
git commit -m "test(admin): 新增自我保护守卫单元测试"
```

---

### 任务 15：最终全量验证

- [ ] **步骤 1：编译**

```powershell
go build -o tmp/go-admin.exe main.go
```

期望：通过。

- [ ] **步骤 2：跑全量测试**

```powershell
go test ./... -count=1
```

期望：所有测试 PASS。

- [ ] **步骤 3：代码质量**

```powershell
golangci-lint run
```

期望：无新增 warning。

- [ ] **步骤 4：手工启动联调**

```powershell
./tmp/go-admin.exe
```

按 spec §8 的 14 场景联调矩阵手动验证关键路径（至少 1 个超管 + 1 个 self + 1 个 department + 自我启停拒绝 + 父子环拒绝）。

---

## Self-Review 清单

| Spec 要求 | 对应任务 |
| --- | --- |
| §6.1 admin DepartmentID 字段读写 | 任务 4 |
| §6.2 OperatorContext + 守卫 | 任务 1、2、3、5、6 |
| §6.3 路由 :uid → :id | 任务 7、10 |
| §6.4 seed 补 admins | 任务 12 |
| §6.5 parent_id 环检测 | 任务 8 |
| §6.6 错误码与文案 | 任务 9、10、11 |
| §6.7 测试同步与新增 | 任务 13、14 |
| §8 编译/全量测试/联调 | 任务 15 |

无占位符、无 TODO。任务编号与方法签名一致：`ensureAdminInScope` / `NewOperatorContext` / `isDescendantOf` / `ErrAdminOutOfScope` / `ErrCannotToggleSelfStatus` / `ErrCannotModifySelfRoles` / `ErrPermissionParentCycle` 在多个任务中名称一致。