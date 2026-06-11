# 数据权限纵深防御加固：角色提权防御 + 部门存在性校验

- 日期：2026-06-11
- 范围：go-admin 后端 service / handler / global 三层
- 关联前序：
  - `docs/superpowers/specs/2026-06-10-data-permission-closure-design.md`
  - `docs/superpowers/specs/2026-06-11-data-permission-create-update-hardening-design.md`

## 1. 背景

前两轮加固已修复 9 个 P 级数据权限漏洞。新一轮 review 发现以下 2 个仍可绕过的攻击面：

### P1：通过"分配更高数据范围的角色"绕过数据权限

- 现状：`CreateAdmin`、`AssignAdminRoles` 只通过 `rejectSuperAdminRoleIDs` 拒绝 `admin` 内置角色，并通过 `Count(ctx)` 校验角色存在/启用，但**未校验角色自身的 `data_scope`**（位于 `internal/ent/schema/role.go` line 42-48）。
- 攻击路径：部门范围管理员 A（op.Scope=Department）拥有"管理员管理"权限，可执行：
  1. 在自己部门内创建账号 B
  2. 为 B 分配某个 `data_scope=all` 的非 `admin` 角色
  3. 以自己设置的密码登录 B，从而获得 All 范围的数据权限
- AssignAdminRoles 同样可达。

### P2：`department_id` 只做范围判断，无存在性校验

- 现状：`ensureDepartmentInScope` 仅校验 ID 是否在 `op.DepartmentIDs` 列表中，未查询 department 表确认真实存在。
- 后果：传入不存在的 ID（如 99999）会落库 `admin.department_id = 99999`，产生"幽灵部门"管理员，影响部门树渲染、数据筛选、二次 `ResolveAdminScope` 推导。
- 同样问题存在于 `role.data_scope_department_ids` 的写入路径。

## 2. 目标

构建纵深防御的第三层（前两层为：`ensureAdminInScope`、`ensureDepartmentInScope`）：

1. **角色提权防御**：操作者分配/创建管理员时，所有候选角色的 `data_scope` 不得"宽于"操作者当前的 `op.Scope`；Custom 角色还需 `DepartmentIDs ⊆ op.DepartmentIDs`。
2. **部门存在性闭环**：所有写入 `admin.department_id` 与 `role.data_scope_department_ids` 的路径都必须确认部门真实存在。

## 3. 设计决策（用户确认）

| 决策项 | 选定方案 |
|---|---|
| P1 防御策略 | **严格不下降**：禁止分配 `rank(role.data_scope) > rank(op.Scope)` 的角色；Custom 需子集校验 |
| 超管/All 例外 | `op.IsSuperAdmin=true` 或 `op.Scope=All` 跳过提权校验（保留 `rejectSuperAdminRoleIDs` 不变） |
| 错误粒度 | 统一错误码，不暴露具体越界角色 ID |
| P2 校验范围 | `admin.department_id` + `role.data_scope_department_ids` 两处都校（不要求 department.status=active） |
| 空值语义 | `department_id == 0` 允许（仅超管/All 可设由 `ensureDepartmentInScope` 保证）；自定义范围空列表允许 |
| 实施模式 | 包级守卫函数（与现有 `ensureAdminInScope` / `ensureDepartmentInScope` 风格一致），不引入 `PermissionGuard` 抽象 |

## 4. 模块/文件分解

| 文件 | 改动 | 说明 |
|---|---|---|
| `internal/service/data_permission.go` | 新增 | `ErrRoleScopeEscalation`、`ErrDepartmentNotFound`、`scopeRank`、`ensureRolesNotEscalating`、`ensureDepartmentExists` |
| `internal/service/super_admin_service.go` | 修改 | `CreateAdmin`：增 `ensureDepartmentExists` + `ensureRolesNotEscalating`；`UpdateAdmin`：增 `ensureDepartmentExists` |
| `internal/service/role_assignment.go` | 修改 | `AssignAdminRoles`：增 `ensureRolesNotEscalating` |
| `internal/service/role.go` | 修改 | `CreateRole` / `UpdateRole`：`DataScope=Custom` 时逐个调用 `ensureDepartmentExists` |
| `internal/global/msg.go` | 新增 | `ResponseRoleScopeEscalation = 9010`、`ResponseDepartmentNotFound = 9011` 与中文文案 |
| `internal/handler/admin_list.go` | 修改 | `mapAdminCreateError` / `mapAdminScopeError` 追加 `ErrRoleScopeEscalation` 与 `ErrDepartmentNotFound` |
| `internal/handler/role_assignment.go` | 修改 | `AssignAdminRoles` 错误映射追加 `ErrRoleScopeEscalation` |
| `internal/handler/role.go` | 修改 | Create/Update Role 错误映射追加 `ErrDepartmentNotFound` |

## 5. 详细设计

### 5.1 `scopeRank` 与 `ensureRolesNotEscalating`

```go
// scopeRank 数据范围"宽度"等级，与 mergeScope 同一张表
func scopeRank(s DataScope) int {
    switch s {
    case DataScopeSelf:               return 1
    case DataScopeDepartment:         return 2
    case DataScopeDepartmentAndChild: return 3
    case DataScopeCustom:             return 4
    case DataScopeAll:                return 5
    default:                          return 0 // 未知 → fail-closed
    }
}

// ensureRolesNotEscalating 校验候选角色的 data_scope 不超过操作者
//   op == nil               → 放行（内部调用）
//   op.IsSuperAdmin || All  → 放行
//   非 All：rank(role) ≤ rank(op)；Custom 还要求 role.DeptIDs ⊆ op.DeptIDs
//   未知 op.Scope           → 拒绝
func ensureRolesNotEscalating(ctx context.Context, client *ent.Client,
    op *EffectiveDataScope, roleIDs []int) error {
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
```

### 5.2 `ensureDepartmentExists`

```go
func ensureDepartmentExists(ctx context.Context, client *ent.Client,
    departmentID int) error {
    if departmentID == 0 {
        return nil
    }
    exists, err := client.Department.Query().
        Where(department.IDEQ(departmentID)).Exist(ctx)
    if err != nil {
        return err
    }
    if !exists {
        return ErrDepartmentNotFound
    }
    return nil
}
```

### 5.3 `CreateAdmin` 调用顺序

```go
// P1-A：目标部门必须在 op 的数据范围内
if err := ensureDepartmentInScope(op, req.DepartmentID); err != nil { ... }
// P2（新）：目标部门必须真实存在
if err := ensureDepartmentExists(ctx, s.client, req.DepartmentID); err != nil { ... }
// 拒绝 admin 内置角色
if err := rejectSuperAdminRoleIDs(...); err != nil { ... }
// P1（新）：禁止分配高于操作者范围的角色
if err := ensureRolesNotEscalating(ctx, s.client, op, requestedRoleIDs(req.RoleIDs, req.PrimaryRoleID)); err != nil { ... }
// ... 后续 email/username/事务 ...
```

### 5.4 `UpdateAdmin`

```go
if req.DepartmentID != nil {
    if err := ensureDepartmentInScope(op, *req.DepartmentID); err != nil { ... }
    if err := ensureDepartmentExists(ctx, s.client, *req.DepartmentID); err != nil { ... }
}
```

### 5.5 `AssignAdminRoles`

在 `rejectSuperAdminRoleIDs` 之后插入：

```go
if err := ensureRolesNotEscalating(ctx, s.client, op, requestedRoleIDs(req.RoleIDs, req.PrimaryRoleID)); err != nil {
    return err
}
```

### 5.6 `RoleService.CreateRole` / `UpdateRole`

`DataScope=Custom` 时遍历 `req.DataScopeDepartmentIDs` 逐个 `ensureDepartmentExists`。

## 6. 错误码

```go
ResponseRoleScopeEscalation = 9010 // 目标角色的数据范围超出你的权限
ResponseDepartmentNotFound  = 9011 // 目标部门不存在
```

## 7. 测试矩阵

### 7.1 单元（`data_permission_test.go`）

| 函数 | 场景 | 期望 |
|---|---|---|
| ensureRolesNotEscalating | NilOp | nil |
|  | SuperAdmin | nil |
|  | OpScopeAll | nil |
|  | EmptyRoleIDs | nil |
|  | RoleAll vs OpDept | ErrRoleScopeEscalation |
|  | RoleDept vs OpDept | nil |
|  | RoleSelf vs OpCustom | nil |
|  | RoleCustom vs OpDept | ErrRoleScopeEscalation |
|  | CustomSubset | nil |
|  | CustomNotSubset | ErrRoleScopeEscalation |
|  | UnknownOpScope | ErrRoleScopeEscalation |
| ensureDepartmentExists | ZeroAllowed | nil |
|  | ExistingID | nil |
|  | MissingID | ErrDepartmentNotFound |

### 7.2 E2E（service 层）

- `CreateAdmin_RejectsRoleScopeEscalation`
- `CreateAdmin_RejectsNonExistentDepartment`
- `UpdateAdmin_RejectsNonExistentDepartmentMigration`
- `AssignAdminRoles_RejectsRoleScopeEscalation`
- `CreateRole_RejectsNonExistentCustomDepartmentID`
- `UpdateRole_RejectsNonExistentCustomDepartmentID`

### 7.3 测试注意事项

`setupSuperAdminTest` 已关闭外键约束，因此"幽灵部门 ID"在 DB 层不会被拦截，能精确驱动到业务校验分支。Custom 子集测试需用 helper 构造 role + 角色 admin_role 数据。

## 8. 失败模式与回退

- **fail-closed**：未知 op.Scope、未知 role.data_scope → ErrRoleScopeEscalation
- **DB 透传**：role / department 查询失败直接返回原错误，不吞
- **守卫均在事务前**：失败无副作用
- **测试包补 nil 兼容**：现有测试中 svc.CreateAdmin / AssignAdminRoles 不传 op 时为 nil（绕过校验语义不变）

## 9. 与历史 spec 关系

本 spec 是 `2026-06-11-data-permission-create-update-hardening-design.md` 的纵深防御扩展，不替换原 spec；原 spec 中的 `ensureDepartmentInScope`、`ErrDepartmentOutOfScope`、`ResponseDepartmentOutOfScope=9009` 全部保留并复用。
