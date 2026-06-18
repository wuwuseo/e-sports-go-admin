# RBAC 自有角色与敏感权限防护计划

## 摘要

为权限系统增加两层防护：
1. **写保护**：非超级管理员不能修改自己所属角色的权限（防止提权/降权），超级管理员角色 `admin` 继续受保护。
2. **列表过滤**：非超级管理员查看权限列表/权限树时，隐藏高风险权限 API 节点，减少暴露面。

## 现状分析

### 当前保护机制
- `permission.go` 已有 `ErrSuperAdminProtected` 错误和 `isSuperAdminRole()` 函数，在以下方法中检查目标角色是否为 `admin`：
  - `CreatePermission` (L368)
  - `UpdatePermission` (L423)
  - `DeletePermission` (L486)
  - `AssignPermissionsToRole` (L646)
  - `RemovePermissionsFromRole` (L701)
  - `SaveRolePermissions` (L814)
- **缺失**：没有检查"目标角色是否属于当前操作者"，即 `normal_admin` 用户可以修改自己 `normal_admin` 角色的权限。

### 当前列表接口
- `GET /api/v1/admin/auth/system-permissions` → `SystemPermissionService.List()` → 返回全量
- `GET /api/v1/admin/auth/system-permissions/tree` → `SystemPermissionService.GetTree()` → 返回全量树
- `GET /api/v1/admin/auth/permissions/all` → `PermissionService.ListAllPermissions()` → 返回全量 Casbin 规则
- `GET /api/v1/admin/auth/permissions` → `PermissionService.ListPermissions()` → 返回分页 Casbin 规则
- 以上接口均无调用者身份过滤。

### UID 获取方式
- handler 通过 `c.Locals("uid").(string)` 获取当前操作者 UID（如 `GetUserMenuPermissions` L480）。
- `PermissionService.getUserRoleNames(ctx, userUID)` 已能查询用户的所有 active 角色名。

## 变更方案

### 1. 新增错误定义

**文件**: `internal/service/permission.go`

在 L25-31 的 `var` 块中新增：
```go
ErrSelfRolePermissionMutationForbidden = errors.New("不允许修改自己所属角色的权限")
```

### 2. 新增错误码和消息

**文件**: `internal/global/msg.go`

在错误码区域新增：
```go
ResponseSelfRolePermissionForbidden = 9012
```

在 `ResponseMsg` 映射中新增：
```go
ResponseSelfRolePermissionForbidden: "不允许修改自己所属角色的权限",
```

### 3. 新增辅助方法 `isSelfRole`

**文件**: `internal/service/permission.go`

在 `PermissionService` 上新增方法，判断目标角色名是否属于当前操作者：

```go
// isSelfRole 检查目标角色是否属于当前操作者
func (s *PermissionService) isSelfRole(ctx context.Context, operatorUID string, targetRoleName string) (bool, error) {
    roleNames, err := s.getUserRoleNames(ctx, operatorUID)
    if err != nil {
        return false, fmt.Errorf("获取操作者角色失败: %w", err)
    }
    for _, name := range roleNames {
        if name == targetRoleName {
            return true, nil
        }
    }
    return false, nil
}
```

### 4. 修改写保护方法签名，增加 `operatorUID` 参数

以下 6 个方法需要增加 `operatorUID string` 参数，并在超级管理员检查之后增加自有角色检查：

#### 4.1 `CreatePermission`

**文件**: `internal/service/permission.go` L361

- 签名从 `CreatePermission(ctx context.Context, req *CreatePermissionRequest) error` 改为 `CreatePermission(ctx context.Context, operatorUID string, req *CreatePermissionRequest) error`
- 在 `isSuperAdminRole(req.RoleName)` 检查之后增加：
```go
isSelf, err := s.isSelfRole(ctx, operatorUID, req.RoleName)
if err != nil {
    return fmt.Errorf("检查角色归属失败: %w", err)
}
if isSelf {
    return ErrSelfRolePermissionMutationForbidden
}
```

#### 4.2 `UpdatePermission`

- 签名从 `UpdatePermission(ctx context.Context, req *UpdatePermissionRequest) error` 改为 `UpdatePermission(ctx context.Context, operatorUID string, req *UpdatePermissionRequest) error`
- 同上增加自有角色检查

#### 4.3 `DeletePermission`

- 签名从 `DeletePermission(ctx context.Context, roleName, domain, object, action string) error` 改为 `DeletePermission(ctx context.Context, operatorUID string, roleName, domain, object, action string) error`
- 同上增加自有角色检查

#### 4.4 `AssignPermissionsToRole`

- 签名从 `AssignPermissionsToRole(ctx context.Context, roleName, domain string, permissions []PermissionRule) error` 改为 `AssignPermissionsToRole(ctx context.Context, operatorUID string, roleName, domain string, permissions []PermissionRule) error`
- 同上增加自有角色检查

#### 4.5 `RemovePermissionsFromRole`

- 签名从 `RemovePermissionsFromRole(ctx context.Context, roleName, domain string, permissions []PermissionRule) error` 改为 `RemovePermissionsFromRole(ctx context.Context, operatorUID string, roleName, domain string, permissions []PermissionRule) error`
- 同上增加自有角色检查

#### 4.6 `SaveRolePermissions`

- 签名从 `SaveRolePermissions(ctx context.Context, roleID int, req *SaveRolePermissionsRequest) error` 改为 `SaveRolePermissions(ctx context.Context, operatorUID string, roleID int, req *SaveRolePermissionsRequest) error`
- 在获取 `roleName` 后、超级管理员检查之后增加自有角色检查

### 5. 更新接口定义

**文件**: `internal/service/permission.go` L111-141 `PermissionServiceInterface`

更新所有 6 个方法签名，增加 `operatorUID string` 参数。

### 6. 修改 Handler 层传入 UID

**文件**: `internal/handler/permission.go`

以下 handler 需要从 `c.Locals("uid")` 获取操作者 UID 并传给 service 方法：

| Handler | 行号 | 修改 |
|---------|------|------|
| `CreatePermission` | L31-63 | 获取 `uid`，传入 `permissionService.CreatePermission(ctx, uid, req)` |
| `UpdatePermission` | L80-112 | 获取 `uid`，传入 `permissionService.UpdatePermission(ctx, uid, req)` |
| `DeletePermission` | L114-166 | 获取 `uid`，传入 `permissionService.DeletePermission(ctx, uid, ...)` |
| `AssignPermissionsToRole` | L328-362 | 获取 `uid`，传入 `permissionService.AssignPermissionsToRole(ctx, uid, ...)` |
| `RemovePermissionsFromRole` | L379-413 | 获取 `uid`，传入 `permissionService.RemovePermissionsFromRole(ctx, uid, ...)` |
| `SaveRolePermissions` | L653-687 | 获取 `uid`，传入 `permissionService.SaveRolePermissions(ctx, uid, id, req)` |

每个 handler 中增加 `ErrSelfRolePermissionMutationForbidden` 的错误处理：
```go
if err == service.ErrSelfRolePermissionMutationForbidden {
    return resp.Fail(global.ResponseSelfRolePermissionForbidden, global.ResponseMsg[global.ResponseSelfRolePermissionForbidden]).ToJSON(c)
}
```

### 7. 非超级管理员列表过滤

#### 7.1 定义高风险权限 API 路径集合

**文件**: `internal/service/permission.go`

新增常量/变量，定义需要隐藏的高风险 API 权限对象路径：

```go
// SensitivePermissionObjects 高风险权限 API 对象路径，非超级管理员不可见
var SensitivePermissionObjects = map[string]struct{}{
    // 权限定义 CRUD
    "/api/v1/admin/auth/system-permissions":          {},
    "/api/v1/admin/auth/system-permissions/{id}":     {},
    "/api/v1/admin/auth/system-permissions/tree":     {},
    // 角色权限保存/分配/移除
    "/api/v1/admin/auth/roles/{id}/permissions":      {},
    "/api/v1/admin/auth/roles/permissions/assign":    {},
    "/api/v1/admin/auth/roles/permissions/remove":    {},
    // 权限 CRUD（Casbin 规则）
    "/api/v1/admin/auth/permissions":                 {},
    "/api/v1/admin/auth/permissions/detail":          {},
    "/api/v1/admin/auth/permissions/all":             {},
}
```

#### 7.2 `SystemPermissionService` 列表和树过滤

**文件**: `internal/service/system_permission.go`

- `List` 方法签名增加 `operatorUID string` 参数
- `GetTree` 方法签名增加 `operatorUID string` 参数
- 在返回结果前，检查操作者是否为超级管理员：
  - 是超级管理员 → 返回全量
  - 非超级管理员 → 过滤掉 `type=api` 且 `path` 在 `SensitivePermissionObjects` 中的节点；对于树结构，过滤后如果父节点无子节点且父节点本身也是 api 类型的高风险节点，则移除该父节点

**过滤逻辑**：
```go
func isSensitiveAPIPermission(perm *ent.SystemPermission) bool {
    if perm.Type != "api" {
        return false
    }
    _, ok := permission.SensitivePermissionObjects[perm.Path]
    return ok
}
```

对于 `List`：在构建 `data` 切片时跳过 `isSensitiveAPIPermission` 为 true 的条目。

对于 `GetTree`：在构建 `nodeMap` 前过滤掉敏感节点，然后正常构建树。需要同时处理：如果过滤后某个非 API 父节点的所有子节点都被移除，保留该父节点（因为菜单节点仍可能有用）；如果父节点本身也是被过滤的 API 节点，则移除。

#### 7.3 `PermissionService.ListAllPermissions` 过滤

**文件**: `internal/service/permission.go`

- `ListAllPermissions` 方法签名增加 `operatorUID string` 参数
- 在返回结果前，检查操作者是否为超级管理员：
  - 是 → 返回全量
  - 否 → 过滤掉 `object` 在 `SensitivePermissionObjects` 中的 Casbin 规则

#### 7.4 `ListPermissions` 过滤

**文件**: `internal/service/permission.go`

- `ListPermissions` 方法签名增加 `operatorUID string` 参数
- 同上逻辑过滤

#### 7.5 更新接口定义

**文件**: `internal/service/permission.go` `PermissionServiceInterface` 和 `internal/service/system_permission.go` `SystemPermissionServiceInterface`

更新 `List`、`GetTree`、`ListAllPermissions`、`ListPermissions` 的签名。

#### 7.6 更新 Handler 传入 UID

**文件**: `internal/handler/permission.go` 和 `internal/handler/system_permission.go`

- `ListSystemPermissions` → 获取 `uid`，传入 `svc.List(ctx, uid, req)`
- `GetSystemPermissionTree` → 获取 `uid`，传入 `svc.GetTree(ctx, uid, domain)`
- `ListAllPermissions` → 获取 `uid`，传入 `permissionService.ListAllPermissions(ctx, uid, ...)`
- `ListPermissions` → 获取 `uid`，传入 `permissionService.ListPermissions(ctx, uid, req)`

### 8. 内部调用点适配

需要检查所有内部调用这些方法的地方，传入空字符串 `""` 作为 `operatorUID`（表示系统内部调用，不受自有角色限制）：

- `InitSystemPermissions` 等初始化流程调用 `CreatePermission` 等方法时，传入 `""`
- 其他 service 内部调用（如果有）

**注意**：`isSelfRole` 在 `operatorUID` 为空时直接返回 `false`，跳过检查。

## 假设与决策

1. **"自己所属角色"** 按当前登录用户拥有的所有 active 角色判断，不只判断主角色。
2. `admin` 是超级管理员角色名，任何非受控接口都不能改它的权限（已有保护，不变）。
3. 超级管理员（拥有 `admin` 角色的用户）不受自有角色限制，可以修改任何角色权限。
4. 列表过滤只是 UI 辅助，后端写接口是最终安全边界。
5. 高风险权限 API 列表为硬编码常量，后续新增敏感路由需手动维护。
6. 系统内部调用（如种子初始化）传入空 `operatorUID`，跳过自有角色检查。

## 验证步骤

1. **编译检查**：`go build ./...`
2. **单元测试**：`go test ./internal/service ./internal/handler`
3. **手动验证**：
   - 启动服务 `go run main.go`
   - 使用 `normal_admin` 角色用户调用 `POST /api/v1/admin/auth/roles/{normal_admin_id}/permissions` → 应返回 403（错误码 9012）
   - 使用 `normal_admin` 角色用户修改不属于自己的角色权限 → 应成功
   - 使用超级管理员修改任何角色权限 → 应成功
   - 非超级管理员调用 `GET /api/v1/admin/auth/system-permissions/tree` → 不包含高风险 API 节点
   - 超级管理员调用同一接口 → 包含完整权限树
