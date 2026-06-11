# 数据权限第四轮加固：Role 写入防御 + 跨 scope 有效集合包含 + DI 修正

- 日期：2026-06-11
- 范围：go-admin 后端 service / handler / global 三层
- 关联前序：
  - `docs/superpowers/specs/2026-06-10-data-permission-closure-design.md`
  - `docs/superpowers/specs/2026-06-11-data-permission-create-update-hardening-design.md`
  - `docs/superpowers/specs/2026-06-11-data-permission-role-escalation-and-department-existence-design.md`

## 1. 背景

前三轮加固已修复 11 个 P 级数据权限漏洞。新一轮 review 发现 3 个新问题：

### P1-A：Role CRUD 未接入 op，绕过数据权限
- 现状：`handler/role.go` 的 CreateRole（L42）/ UpdateRole（L210）不构造 operator scope，也不传给 service；`service/role.go`（L313/L323）只校验 custom 部门存在，然后直接写入 `data_scope`。
- 攻击路径：拥有 `role:update` 的部门管理员把自己已有角色 data_scope 改为 `all` → 下次 `ResolveAdminScope` 获得全量数据权限。

### P1-B：`ensureRolesNotEscalating` 仅 rank 比较，未做有效集合包含
- 现状：`scopeRank` 把 Custom(4) 排在 DepartmentAndChild(3) 之上，非 Custom 角色只比 rank（`data_permission.go:281`），不验证实际部门集合。
- 攻击路径：op.Custom=[父部门 P] 操作者，给目标管理员（dept=P）分配 `DepartmentAndChild` 角色 → 目标登录后展开 P + 所有子部门 → 超出操作者 Custom 范围。
- 根因：rank 只表达"范围抽象等级"，不等于"实际覆盖部门集"。

### P2：ListAdmins 中现场 new dataPermSvc，未走 DI
- 现状：`super_admin_service.go:249` 现场 `NewDataPermissionService(NewDepartmentService())`，两者构造均依赖 `global.ORM`。
- 风险：测试、并行实例、未来多连接场景下 client 不一致，易用错 DB。

## 2. 目标

构建第四层数据权限闭环：
1. **角色字段写入防御**：Role 的 `data_scope` / `data_scope_department_ids` 不得被非超管用户提升至超过自身 op 范围。
2. **跨 scope 有效集合包含**：分配给目标管理员的角色，其"目标登录后实际可见的部门集合"必须 ⊆ op.DepartmentIDs。
3. **DI 一致性**：SuperAdminService 持有 dataPermSvc 字段，消除现场 new。

## 3. 设计决策

| 决策项 | 选定方案 |
|---|---|
| P1-A 防御策略 | Role.Create/Update 注入 op；非超管的 `data_scope` 与 `data_scope_department_ids` 必须不超过自身 op；超管/op.All 跳过 |
| P1-B 集合算法 | 计算 role 在目标管理员部门下的"有效部门集"，要求 ⊆ op.DepartmentIDs；不再仅依赖 rank |
| 错误码复用 | 全部复用 `ErrRoleScopeEscalation` / `ResponseRoleScopeEscalation=9010`，不新增 |
| DI 改造 | `SuperAdminService` 新增 `dataPermSvc *DataPermissionService` 字段，由 `NewSuperAdminService` 构造时注入或 setter；router 注入 |
| 兼容旧测试 | nil op 仍表示"内部调用绕过校验"语义保持不变 |
| 实施模式 | 包级守卫函数 + 接口签名变更（Role.Create/Update 加 op）|

## 4. 模块/文件分解

| 文件 | 改动 | 说明 |
|---|---|---|
| `internal/service/data_permission.go` | 新增 | `resolveRoleEffectiveDepartments` / `ensureRolesNotEscalatingForTarget` / `ensureRoleScopeWritable` |
| `internal/service/role.go` | 修改 | `RoleServiceInterface.Create` / `Update` 加 `op *EffectiveDataScope`；实现内调用 `ensureRoleScopeWritable` |
| `internal/service/super_admin_service.go` | 修改 | `SuperAdminService` 新增 `dataPermSvc` 字段；`ListAdmins` 使用 `s.dataPermSvc`；`CreateAdmin` / `AssignAdminRoles` 调用方改用 `ensureRolesNotEscalatingForTarget` |
| `internal/service/role_assignment.go` | 修改 | `AssignAdminRoles` 改用 `ensureRolesNotEscalatingForTarget`（带 target.DepartmentID）|
| `internal/handler/role.go` | 修改 | CreateRole/UpdateRole 注入 op，调用新签名 |
| `internal/router.go` | 修改 | NewSuperAdminService 注入 dataPermSvc（若已构造则改装） |
| 测试文件 | 修改 | 补现有 Role.Create/Update 调用的 op=nil；新增 6 个 E2E |

## 5. 详细设计

### 5.1 `resolveRoleEffectiveDepartments`

```go
// resolveRoleEffectiveDepartments 计算指定角色被分配给目标管理员后，
// 该管理员登录时实际可见的部门集合
//   返回 scopeIsAll=true 表示该角色让目标可看全部数据
//   targetAdminDepartmentID 为目标管理员当前部门（Role/Dept-and-Child 需用）
func resolveRoleEffectiveDepartments(
    ctx context.Context,
    deptSvc DepartmentServiceInterface,
    r *ent.Role,
    targetAdminDepartmentID int,
) (effective []int, scopeIsAll bool, err error) {
    switch DataScope(r.DataScope) {
    case DataScopeAll:
        return nil, true, nil
    case DataScopeSelf:
        return nil, false, nil // 目标仅看自己，无部门展开
    case DataScopeDepartment:
        if targetAdminDepartmentID == 0 {
            return nil, false, nil
        }
        return []int{targetAdminDepartmentID}, false, nil
    case DataScopeDepartmentAndChild:
        if targetAdminDepartmentID == 0 {
            return nil, false, nil
        }
        ids, err := deptSvc.ChildIDs(ctx, targetAdminDepartmentID, true)
        if err != nil {
            return nil, false, err
        }
        return ids, false, nil
    case DataScopeCustom:
        return r.DataScopeDepartmentIds, false, nil
    default:
        return nil, false, ErrRoleScopeEscalation // 未知 fail-closed
    }
}
```

### 5.2 `ensureRolesNotEscalatingForTarget`

```go
// ensureRolesNotEscalatingForTarget 校验候选角色分配给目标管理员后，
// 其"有效部门集合"是否 ⊆ op.DepartmentIDs
//   op == nil               → 放行
//   op.IsSuperAdmin || All  → 放行
//   非 All：每个 role 的 effective 必须 ⊆ op.DepartmentIDs；任一 scopeIsAll → 拒绝
func ensureRolesNotEscalatingForTarget(
    ctx context.Context,
    client *ent.Client,
    deptSvc DepartmentServiceInterface,
    op *EffectiveDataScope,
    roleIDs []int,
    targetAdminDepartmentID int,
) error {
    if op == nil || op.IsSuperAdmin || op.Scope == DataScopeAll {
        return nil
    }
    if len(roleIDs) == 0 {
        return nil
    }
    roles, err := client.Role.Query().Where(role.IDIn(roleIDs...)).All(ctx)
    if err != nil {
        return err
    }
    opSet := make(map[int]struct{}, len(op.DepartmentIDs))
    for _, id := range op.DepartmentIDs {
        opSet[id] = struct{}{}
    }
    for _, r := range roles {
        effective, scopeIsAll, err := resolveRoleEffectiveDepartments(ctx, deptSvc, r, targetAdminDepartmentID)
        if err != nil {
            return err
        }
        if scopeIsAll {
            return ErrRoleScopeEscalation
        }
        for _, deptID := range effective {
            if _, ok := opSet[deptID]; !ok {
                return ErrRoleScopeEscalation
            }
        }
    }
    return nil
}
```

### 5.3 `ensureRoleScopeWritable`（Role 字段写入）

```go
// ensureRoleScopeWritable 校验非超管设置 role.data_scope 时不得提升到超过 op 自身
//   op == nil / IsSuperAdmin / op.Scope=All → 放行
//   非 All：rank(newScope) ≤ rank(op.Scope)
//   Custom newScope → newDeptIDs ⊆ op.DepartmentIDs
//   未知 newScope → 拒绝
func ensureRoleScopeWritable(op *EffectiveDataScope, newScope DataScope, newDeptIDs []int) error {
    if op == nil || op.IsSuperAdmin || op.Scope == DataScopeAll {
        return nil
    }
    opRank := scopeRank(op.Scope)
    if opRank == 0 {
        return ErrRoleScopeEscalation
    }
    newRank := scopeRank(newScope)
    if newRank == 0 || newRank > opRank {
        return ErrRoleScopeEscalation
    }
    if newScope == DataScopeCustom {
        for _, id := range newDeptIDs {
            if !containsInt(op.DepartmentIDs, id) {
                return ErrRoleScopeEscalation
            }
        }
    }
    return nil
}
```

注：此处仍用 rank 是因为 Role 字段写入阶段不存在"目标管理员"上下文，无法展开 Dept/Child 集合；rank 是该场景下能做的最严防御。Custom 场景配合子集校验封住主要绕过路径。

### 5.4 `RoleService` 接口签名变更

```go
type RoleServiceInterface interface {
    Create(ctx context.Context, op *EffectiveDataScope, req *CreateRoleRequest) (*RoleResponse, error)
    Update(ctx context.Context, op *EffectiveDataScope, id int, req *UpdateRoleRequest) error
    // 其他方法签名不变
}
```

实现内：
```go
func (s *RoleService) Create(ctx context.Context, op *EffectiveDataScope, req *CreateRoleRequest) (*RoleResponse, error) {
    dataScope := req.DataScope
    if dataScope == "" {
        dataScope = string(DataScopeSelf)
    }
    // P1-A 防御：非超管不得创建超过自身范围的角色
    if err := ensureRoleScopeWritable(op, DataScope(dataScope), req.DataScopeDepartmentIDs); err != nil {
        return nil, err
    }
    // P2 防御（已有）：Custom 部门必须存在
    if dataScope == string(DataScopeCustom) && len(req.DataScopeDepartmentIDs) > 0 {
        for _, deptID := range req.DataScopeDepartmentIDs {
            if err := ensureDepartmentExists(ctx, s.Client, deptID); err != nil {
                return nil, err
            }
        }
    }
    // ... 既有写入逻辑
}
```

Update 同样模式：当 `req.DataScope != ""` 时调用 `ensureRoleScopeWritable`。

### 5.5 handler/role.go 改造

```go
func CreateRole(c fiber.Ctx) error {
    // ...
    roleService := c.Locals("service.RoleService").(service.RoleServiceInterface)
    dataPermSvc := c.Locals("service.DataPermissionService").(*service.DataPermissionService)
    op, err := dataPermSvc.NewOperatorContext(c.Context(), getCurrentUID(c))
    if err != nil {
        return resp.Fail(...)
    }
    role, err := roleService.Create(c.Context(), op, req)
    if err != nil {
        switch err {
        case service.ErrRoleScopeEscalation:
            return resp.Fail(global.ResponseRoleScopeEscalation, global.ResponseMsg[global.ResponseRoleScopeEscalation]).ToJSON(c)
        // ... 既有 case
        }
    }
    // ...
}
```

UpdateRole 同样改造。

### 5.6 SuperAdminService DI 修正

```go
type SuperAdminService struct {
    client       *ent.Client
    dataPermSvc  *DataPermissionService // 新增字段
    // ...
}

func NewSuperAdminService(client *ent.Client, dataPermSvc *DataPermissionService) *SuperAdminService {
    return &SuperAdminService{client: client, dataPermSvc: dataPermSvc}
}

// ListAdmins 中
if req.CurrentUID != "" {
    scope, err := s.dataPermSvc.ResolveAdminScope(ctx, req.CurrentUID)
    // ...
}
```

`internal/router.go` 中调整构造顺序：先构造 dataPermSvc，再注入 super_admin_service。

### 5.7 CreateAdmin / AssignAdminRoles 切换到 ForTarget 版本

- `CreateAdmin`：`ensureRolesNotEscalatingForTarget(ctx, s.client, deptSvc, op, requestedRoleIDs(req), req.DepartmentID)`
- `AssignAdminRoles`：先查目标 admin 的 DepartmentID，再调 `ensureRolesNotEscalatingForTarget(..., target.DepartmentID)`

旧 `ensureRolesNotEscalating` 保留供 Role.Create/Update 等"无目标"场景；或删除（若所有调用点均已切换）——本 spec 选择**保留**作为通用 rank 守卫（无 target 时降级使用）。

## 6. 错误码

复用：
- `ErrRoleScopeEscalation` → `ResponseRoleScopeEscalation = 9010`

## 7. 测试矩阵

### 7.1 单元 `data_permission_test.go`（新增）

| 函数 | 场景 | 期望 |
|---|---|---|
| resolveRoleEffectiveDepartments | All | scopeIsAll=true |
|  | Self | effective=nil, scopeIsAll=false |
|  | Department + targetDept=5 | effective=[5] |
|  | DepartmentAndChild + targetDept=5（mock deptSvc 返回 [5,6,7]）| effective=[5,6,7] |
|  | Custom（role.IDs=[3,4]）| effective=[3,4] |
|  | Unknown scope | err=ErrRoleScopeEscalation |
| ensureRolesNotEscalatingForTarget | NilOp / SuperAdmin / OpAll / EmptyRoleIDs | nil |
|  | RoleAll → 拒 | ErrRoleScopeEscalation |
|  | RoleSelf → 放行（effective 空集 ⊆ 任何集合）| nil |
|  | RoleDept 命中 / 未命中 op.DeptIDs | nil / ErrRoleScopeEscalation |
|  | RoleDeptAndChild 子集命中 / 未命中 | nil / ErrRoleScopeEscalation |
|  | RoleCustom 子集命中 / 未命中 | nil / ErrRoleScopeEscalation |
| ensureRoleScopeWritable | NilOp / Super / OpAll | nil |
|  | NewScope=All, OpDept → 拒 | ErrRoleScopeEscalation |
|  | NewScope=Self, OpDept → 放行 | nil |
|  | NewScope=Custom 子集 | nil |
|  | NewScope=Custom 非子集 | ErrRoleScopeEscalation |
|  | UnknownNewScope | ErrRoleScopeEscalation |

### 7.2 E2E

- `TestRoleService_Create_RejectsOpDeptToAll`：op=Dept，req.DataScope=all → ErrRoleScopeEscalation
- `TestRoleService_Update_RejectsOpDeptToAll`：同上 update 路径
- `TestSuperAdminService_AssignAdminRoles_RejectsDeptAndChildOverflow`：op.Custom=[P]，target.dept=P，分配 Dept-and-Child 角色，mock 返回 [P, C1, C2] → ErrRoleScopeEscalation
- `TestSuperAdminService_CreateAdmin_RejectsDeptAndChildOverflow`：同上 create 路径
- `TestSuperAdminService_ListAdmins_UsesInjectedDataPermSvc`：验证 ListAdmins 通过注入的 dataPermSvc 走通过滤逻辑（间接证 DI 注入生效）

### 7.3 测试 helper

测试需 mock DepartmentServiceInterface.ChildIDs。可在 service 包内新建简单 fake：
```go
type fakeDeptSvc struct{ childIDs map[int][]int }
func (f *fakeDeptSvc) ChildIDs(ctx context.Context, id int, includeSelf bool) ([]int, error) {
    return f.childIDs[id], nil
}
// 其他方法返回零值即可
```

## 8. 失败模式

- **fail-closed**：未知 op.Scope / 未知 role.data_scope → 拒绝
- **DB / deptSvc 错误透传**：不吞
- **守卫均在事务前**：失败无副作用
- **接口签名变更影响**：所有现有 Role.Create/Update 测试需补 `nil` op；handler 已统一从 c.Locals 取 op

## 9. 与历史 spec 关系

本 spec 是前两轮 P1 修复的**深化**：
- 第二轮新增 `ensureDepartmentInScope`
- 第三轮新增 `ensureRolesNotEscalating`（rank 版本）
- 本轮新增 `ensureRolesNotEscalatingForTarget`（集合包含版本）+ `ensureRoleScopeWritable`（Role 字段写入版本）+ DI 修正

旧 `ensureRolesNotEscalating` 作为通用 rank 守卫保留；新 ForTarget 版本在 CreateAdmin/AssignAdminRoles 路径覆盖。
