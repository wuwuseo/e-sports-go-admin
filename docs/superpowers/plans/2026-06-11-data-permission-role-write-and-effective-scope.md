# 数据权限第四轮加固实施计划：Role 写入防御 + 跨 scope 有效集合包含 + DI 修正

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 闭合 go-admin 数据权限三类残余漏洞：Role.data_scope 字段写入未防护、跨 scope 角色分配未按实际部门集合校验、ListAdmins 现场 new 依赖未走 DI。

**Architecture:** 新增 3 个包级守卫（resolveRoleEffectiveDepartments / ensureRolesNotEscalatingForTarget / ensureRoleScopeWritable）；SuperAdminService 与 RoleAssignmentService 持有注入的 dataPermSvc + deptSvc；RoleService 接口扩签名 op；handler/role.go 注入 op。

**Tech Stack:** Go 1.25.1 / Fiber v3 / Ent ORM / testify / SQLite enttest / PowerShell

关联 spec：`docs/superpowers/specs/2026-06-11-data-permission-role-write-and-effective-scope-design.md`

---

## Task 1: 新增 3 个守卫函数 + 单元测试

**Files:**
- Modify: `d:/www/admin/internal/service/data_permission.go`
- Modify: `d:/www/admin/internal/service/data_permission_test.go`

- [ ] **Step 1：在 data_permission_test.go 末尾追加单元测试**

```go
// fakeDeptSvc 用于守卫层注入，避开真实 DB 查询
type fakeDeptSvc struct{ childIDs map[int][]int }

func (f *fakeDeptSvc) ChildIDs(ctx context.Context, id int, includeSelf bool) ([]int, error) {
	if f.childIDs == nil {
		return nil, nil
	}
	return f.childIDs[id], nil
}
// 其余 DepartmentServiceInterface 方法返回零值即可（按实际接口补齐）

func TestResolveRoleEffectiveDepartments_AllAndSelf(t *testing.T) {
	ctx := context.Background()
	fd := &fakeDeptSvc{}
	eff, isAll, err := resolveRoleEffectiveDepartments(ctx, fd, &ent.Role{DataScope: "all"}, 5)
	if err != nil || !isAll || eff != nil {
		t.Fatalf("All 期望 isAll=true，got eff=%v isAll=%v err=%v", eff, isAll, err)
	}
	eff, isAll, err = resolveRoleEffectiveDepartments(ctx, fd, &ent.Role{DataScope: "self"}, 5)
	if err != nil || isAll || eff != nil {
		t.Fatalf("Self 期望 nil/false，got eff=%v isAll=%v err=%v", eff, isAll, err)
	}
}

func TestResolveRoleEffectiveDepartments_DepartmentAndChild(t *testing.T) {
	ctx := context.Background()
	fd := &fakeDeptSvc{childIDs: map[int][]int{5: {5, 6, 7}}}
	eff, _, _ := resolveRoleEffectiveDepartments(ctx, fd, &ent.Role{DataScope: "department"}, 5)
	if len(eff) != 1 || eff[0] != 5 {
		t.Fatalf("Department 期望 [5], got %v", eff)
	}
	eff, _, _ = resolveRoleEffectiveDepartments(ctx, fd, &ent.Role{DataScope: "department_and_child"}, 5)
	if len(eff) != 3 {
		t.Fatalf("DeptAndChild 期望 [5,6,7], got %v", eff)
	}
	eff, _, _ = resolveRoleEffectiveDepartments(ctx, fd, &ent.Role{DataScope: "department"}, 0)
	if eff != nil {
		t.Fatalf("targetDept=0 期望 nil, got %v", eff)
	}
}

func TestResolveRoleEffectiveDepartments_Custom(t *testing.T) {
	ctx := context.Background()
	fd := &fakeDeptSvc{}
	eff, _, _ := resolveRoleEffectiveDepartments(ctx, fd, &ent.Role{DataScope: "custom", DataScopeDepartmentIds: []int{3, 4}}, 99)
	if len(eff) != 2 || eff[0] != 3 || eff[1] != 4 {
		t.Fatalf("Custom 期望 [3,4], got %v", eff)
	}
}

func TestResolveRoleEffectiveDepartments_UnknownRejected(t *testing.T) {
	ctx := context.Background()
	_, _, err := resolveRoleEffectiveDepartments(ctx, &fakeDeptSvc{}, &ent.Role{DataScope: "weird"}, 5)
	if !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("未知 scope 期望 ErrRoleScopeEscalation, got %v", err)
	}
}

func TestEnsureRolesNotEscalatingForTarget_SkipPaths(t *testing.T) {
	ctx := context.Background()
	if err := ensureRolesNotEscalatingForTarget(ctx, nil, nil, nil, []int{1}, 5); err != nil {
		t.Fatalf("nil op 放行, got %v", err)
	}
	op := &EffectiveDataScope{IsSuperAdmin: true}
	if err := ensureRolesNotEscalatingForTarget(ctx, nil, nil, op, []int{1}, 5); err != nil {
		t.Fatalf("super 放行, got %v", err)
	}
	op = &EffectiveDataScope{Scope: DataScopeAll}
	if err := ensureRolesNotEscalatingForTarget(ctx, nil, nil, op, []int{1}, 5); err != nil {
		t.Fatalf("op All 放行, got %v", err)
	}
	op = &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1}}
	if err := ensureRolesNotEscalatingForTarget(ctx, nil, nil, op, nil, 5); err != nil {
		t.Fatalf("空 roleIDs 放行, got %v", err)
	}
}

func TestEnsureRolesNotEscalatingForTarget_Coverage(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	defer cleanup()
	ctx := t.Context()
	fd := &fakeDeptSvc{childIDs: map[int][]int{5: {5, 6, 7}}}

	rAll := client.Role.Create().SetName("rt_all").SetDisplayName("rt_all").SetDataScope(role.DataScopeAll).SetStatus(role.StatusActive).SaveX(ctx)
	rSelf := client.Role.Create().SetName("rt_self").SetDisplayName("rt_self").SetDataScope(role.DataScopeSelf).SetStatus(role.StatusActive).SaveX(ctx)
	rDept := client.Role.Create().SetName("rt_dept").SetDisplayName("rt_dept").SetDataScope(role.DataScopeDepartment).SetStatus(role.StatusActive).SaveX(ctx)
	rDc := client.Role.Create().SetName("rt_dc").SetDisplayName("rt_dc").SetDataScope(role.DataScopeDepartmentAndChild).SetStatus(role.StatusActive).SaveX(ctx)
	rCustomOK := client.Role.Create().SetName("rt_cok").SetDisplayName("rt_cok").SetDataScope(role.DataScopeCustom).SetDataScopeDepartmentIds([]int{5, 6}).SetStatus(role.StatusActive).SaveX(ctx)
	rCustomBad := client.Role.Create().SetName("rt_cbad").SetDisplayName("rt_cbad").SetDataScope(role.DataScopeCustom).SetDataScopeDepartmentIds([]int{5, 99}).SetStatus(role.StatusActive).SaveX(ctx)

	op := &EffectiveDataScope{Scope: DataScopeCustom, DepartmentIDs: []int{5, 6, 7}}

	if err := ensureRolesNotEscalatingForTarget(ctx, client, fd, op, []int{rAll.ID}, 5); !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("RoleAll 期望 Esc, got %v", err)
	}
	if err := ensureRolesNotEscalatingForTarget(ctx, client, fd, op, []int{rSelf.ID}, 5); err != nil {
		t.Fatalf("RoleSelf 放行, got %v", err)
	}
	if err := ensureRolesNotEscalatingForTarget(ctx, client, fd, op, []int{rDept.ID}, 5); err != nil {
		t.Fatalf("RoleDept 命中放行, got %v", err)
	}
	if err := ensureRolesNotEscalatingForTarget(ctx, client, fd, op, []int{rDept.ID}, 99); !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("RoleDept 不命中期望 Esc, got %v", err)
	}
	if err := ensureRolesNotEscalatingForTarget(ctx, client, fd, op, []int{rDc.ID}, 5); err != nil {
		t.Fatalf("RoleDC 子集放行, got %v", err)
	}
	op2 := &EffectiveDataScope{Scope: DataScopeCustom, DepartmentIDs: []int{5}}
	if err := ensureRolesNotEscalatingForTarget(ctx, client, fd, op2, []int{rDc.ID}, 5); !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("RoleDC 超集期望 Esc, got %v", err)
	}
	if err := ensureRolesNotEscalatingForTarget(ctx, client, fd, op, []int{rCustomOK.ID}, 5); err != nil {
		t.Fatalf("RoleCustom 子集放行, got %v", err)
	}
	if err := ensureRolesNotEscalatingForTarget(ctx, client, fd, op, []int{rCustomBad.ID}, 5); !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("RoleCustom 非子集期望 Esc, got %v", err)
	}
}

func TestEnsureRoleScopeWritable(t *testing.T) {
	if err := ensureRoleScopeWritable(nil, DataScopeAll, nil); err != nil {
		t.Fatalf("nil 放行, got %v", err)
	}
	if err := ensureRoleScopeWritable(&EffectiveDataScope{IsSuperAdmin: true}, DataScopeAll, nil); err != nil {
		t.Fatalf("super 放行, got %v", err)
	}
	if err := ensureRoleScopeWritable(&EffectiveDataScope{Scope: DataScopeAll}, DataScopeAll, nil); err != nil {
		t.Fatalf("opAll 放行, got %v", err)
	}
	op := &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1}}
	if err := ensureRoleScopeWritable(op, DataScopeAll, nil); !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("Dept→All 期望 Esc, got %v", err)
	}
	if err := ensureRoleScopeWritable(op, DataScopeSelf, nil); err != nil {
		t.Fatalf("Dept→Self 放行, got %v", err)
	}
	op = &EffectiveDataScope{Scope: DataScopeCustom, DepartmentIDs: []int{1, 2}}
	if err := ensureRoleScopeWritable(op, DataScopeCustom, []int{1}); err != nil {
		t.Fatalf("Custom 子集放行, got %v", err)
	}
	if err := ensureRoleScopeWritable(op, DataScopeCustom, []int{1, 99}); !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("Custom 非子集期望 Esc, got %v", err)
	}
	if err := ensureRoleScopeWritable(op, DataScope("weird"), nil); !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("未知 newScope 期望 Esc, got %v", err)
	}
}
```

**fakeDeptSvc 补全**：用 Read 读 `internal/service/department.go` L20-40 取 DepartmentServiceInterface 全部方法，按签名为 fakeDeptSvc 补齐所有方法（返回零值/nil）。

- [ ] **Step 2：跑测试确认 FAIL（编译错误）**

```powershell
cd d:\www\admin ; go test ./internal/service/ -run "ResolveRoleEffectiveDepartments|EnsureRolesNotEscalatingForTarget|EnsureRoleScopeWritable" -v
```

- [ ] **Step 3：在 data_permission.go 末尾追加 3 个函数**

```go
// resolveRoleEffectiveDepartments 计算指定角色分配给目标管理员后目标登录时实际可见的部门集合
//   scopeIsAll=true 表示该角色让目标可看全部数据
//   targetAdminDepartmentID==0 时 Department/DepartmentAndChild 退化为空集
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
		return nil, false, nil
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
		return nil, false, ErrRoleScopeEscalation
	}
}

// ensureRolesNotEscalatingForTarget 校验候选角色分配给目标管理员后"有效部门集合" ⊆ op.DepartmentIDs
//   op == nil / IsSuperAdmin / op.Scope=All → 放行
//   scopeIsAll → 拒绝；effective 任一元素不在 op.DepartmentIDs → 拒绝
//   未知 op.Scope → 拒绝
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
	if scopeRank(op.Scope) == 0 {
		return ErrRoleScopeEscalation
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

// ensureRoleScopeWritable 校验非超管修改 role.data_scope 时不得超过自身 op 范围
//   op == nil / IsSuperAdmin / op.Scope=All → 放行
//   非 All：rank(newScope) ≤ rank(op.Scope)；Custom 还要求 newDeptIDs ⊆ op.DepartmentIDs
//   未知 newScope → 拒绝
// 说明：Role 字段写入阶段无"目标管理员"，Dept/DeptAndChild 用 rank 防御，
// 实际分配阶段由 ensureRolesNotEscalatingForTarget 兜底。
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

- [ ] **Step 4：跑测试确认 PASS**

```powershell
cd d:\www\admin ; go test ./internal/service/ -run "ResolveRoleEffectiveDepartments|EnsureRolesNotEscalatingForTarget|EnsureRoleScopeWritable" -v -count=1
```

- [ ] **Step 5：commit**

```powershell
cd d:\www\admin ; git add internal/service/data_permission.go internal/service/data_permission_test.go ; git commit -m "feat(data-permission): 新增 resolveRoleEffectiveDepartments+ensureRolesNotEscalatingForTarget+ensureRoleScopeWritable"
```

---

## Task 2: SuperAdminService 注入 dataPermSvc + deptSvc 字段

**Files:**
- Modify: `d:/www/admin/internal/service/super_admin_service.go`
- Modify: `d:/www/admin/internal/router.go`

- [ ] **Step 1：修改 SuperAdminService 结构 + 构造**

把 super_admin_service.go L43-50 的：
```go
type SuperAdminService struct {
	client *ent.Client
}
func NewSuperAdminService() *SuperAdminService {
	return &SuperAdminService{client: global.ORM}
}
```
改为：
```go
type SuperAdminService struct {
	client      *ent.Client
	dataPermSvc *DataPermissionService
	deptSvc     DepartmentServiceInterface
}

// NewSuperAdminService 路由层注入 dataPermSvc 与 deptSvc，避免现场 new 导致依赖不一致
func NewSuperAdminService(dataPermSvc *DataPermissionService, deptSvc DepartmentServiceInterface) *SuperAdminService {
	return &SuperAdminService{client: global.ORM, dataPermSvc: dataPermSvc, deptSvc: deptSvc}
}
```

- [ ] **Step 2：修改 router.go L130 调用**

把：
```go
c.Locals("service.SuperAdminService", service.NewSuperAdminService())
```
改为：
```go
c.Locals("service.SuperAdminService", service.NewSuperAdminService(dataPermissionService, departmentService))
```

- [ ] **Step 3：main build 验证（test 包预计因签名变更失败，由 T7 修）**

```powershell
cd d:\www\admin ; go build ./internal/handler ./internal/service ./internal
```
预期：exit 0。

- [ ] **Step 4：commit**

```powershell
cd d:\www\admin ; git add internal/service/super_admin_service.go internal/router.go ; git commit -m "refactor(admin): SuperAdminService 注入 dataPermSvc/deptSvc 字段"
```

---

## Task 3: RoleService 接口签名变更 + Create/Update 接入 ensureRoleScopeWritable

**Files:**
- Modify: `d:/www/admin/internal/service/role.go`

- [ ] **Step 1：读 role.go 找定位**

```powershell
cd d:\www\admin ; Select-String -Path internal/service/role.go -Pattern "RoleServiceInterface|func \(s \*RoleService\) Create|func \(s \*RoleService\) Update" -Context 0,3
```

- [ ] **Step 2：修改接口签名**

在 `type RoleServiceInterface interface { ... }` 内：
```go
Create(ctx context.Context, op *EffectiveDataScope, req *CreateRoleRequest) (*RoleResponse, error)
Update(ctx context.Context, op *EffectiveDataScope, id int, req *UpdateRoleRequest) error
```

- [ ] **Step 3：修改 RoleService.Create**

函数签名加 `op *EffectiveDataScope`；在 `dataScope := req.DataScope; if dataScope == "" { dataScope = string(DataScopeSelf) }` 之后立即插入：
```go
// P1-A 防御：非超管不得创建超过自身范围的角色
if err := ensureRoleScopeWritable(op, DataScope(dataScope), req.DataScopeDepartmentIDs); err != nil {
	return nil, err
}
```
（既有的 ensureDepartmentExists 循环保留）

- [ ] **Step 4：修改 RoleService.Update**

函数签名加 `op *EffectiveDataScope`；在 `if req.DataScope != ""` 块入口插入：
```go
// P1-A 防御：非超管不得修改 role.data_scope 至超过自身范围
if err := ensureRoleScopeWritable(op, DataScope(req.DataScope), req.DataScopeDepartmentIDs); err != nil {
	return err
}
```
（既有的 ensureDepartmentExists 循环保留）

- [ ] **Step 5：build 验证**

```powershell
cd d:\www\admin ; go build ./internal/service
```
预期：service 包通过；handler 会因签名变更失败，T6 修。

- [ ] **Step 6：commit**

```powershell
cd d:\www\admin ; git add internal/service/role.go ; git commit -m "feat(role): Create/Update 接口注入 op + ensureRoleScopeWritable 防御"
```

---

## Task 4: CreateAdmin / AssignAdminRoles 切换 ensureRolesNotEscalatingForTarget

**Files:**
- Modify: `d:/www/admin/internal/service/super_admin_service.go`
- Modify: `d:/www/admin/internal/service/role_assignment.go`
- Modify: `d:/www/admin/internal/router.go`

- [ ] **Step 1：CreateAdmin 替换守卫调用**

把 CreateAdmin 内：
```go
if err := ensureRolesNotEscalating(ctx, s.client, op, requestedRoleIDs(req.RoleIDs, req.PrimaryRoleID)); err != nil {
	return nil, err
}
```
改为：
```go
// P1-B 防御：按目标管理员部门展开角色有效集，要求 ⊆ op.DepartmentIDs
if err := ensureRolesNotEscalatingForTarget(ctx, s.client, s.deptSvc, op, requestedRoleIDs(req.RoleIDs, req.PrimaryRoleID), req.DepartmentID); err != nil {
	return nil, err
}
```

- [ ] **Step 2：RoleAssignmentService 加 deptSvc 字段**

读 role_assignment.go 结构与构造（约 L30-50），把：
```go
type RoleAssignmentService struct {
	client *ent.Client
	cache  cache.CacheInterface
}
func NewRoleAssignmentService(c cache.CacheInterface) *RoleAssignmentService {
	return &RoleAssignmentService{client: global.ORM, cache: c}
}
```
改为（保留实际现有字段不要遗漏）：
```go
type RoleAssignmentService struct {
	client  *ent.Client
	cache   cache.CacheInterface
	deptSvc DepartmentServiceInterface
}
func NewRoleAssignmentService(c cache.CacheInterface, deptSvc DepartmentServiceInterface) *RoleAssignmentService {
	return &RoleAssignmentService{client: global.ORM, cache: c, deptSvc: deptSvc}
}
```
**字段名以实际为准**，用 Read 工具读 role_assignment.go L1-60 后再改。

- [ ] **Step 3：AssignAdminRoles 替换守卫调用**

把 AssignAdminRoles 内：
```go
if err := ensureRolesNotEscalating(ctx, s.client, op, requestedRoleIDs(req.RoleIDs, req.PrimaryRoleID)); err != nil {
	return err
}
```
改为：
```go
// 查目标管理员部门（用于按有效集合校验角色）
target, err := s.client.Admin.Query().Where(admin.IDEQ(adminID)).Only(ctx)
if err != nil {
	if ent.IsNotFound(err) {
		return ErrUserNotFound
	}
	return err
}
// P1-B 防御：按目标管理员部门展开角色有效集
if err := ensureRolesNotEscalatingForTarget(ctx, s.client, s.deptSvc, op, requestedRoleIDs(req.RoleIDs, req.PrimaryRoleID), target.DepartmentID); err != nil {
	return err
}
```
若函数后续已有 target/a 变量复用，删除重复查询（避免 N+1）。

- [ ] **Step 4：router.go 更新 NewRoleAssignmentService 调用**

```powershell
cd d:\www\admin ; Select-String -Path internal/router.go -Pattern "NewRoleAssignmentService"
```
所有 `NewRoleAssignmentService(xxx)` → `NewRoleAssignmentService(xxx, departmentService)`。

- [ ] **Step 5：build 验证**

```powershell
cd d:\www\admin ; go build ./internal/service ./internal
```
预期：exit 0。

- [ ] **Step 6：commit**

```powershell
cd d:\www\admin ; git add internal/service/super_admin_service.go internal/service/role_assignment.go internal/router.go ; git commit -m "feat(admin): CreateAdmin/AssignAdminRoles 切换 ensureRolesNotEscalatingForTarget"
```

---

## Task 5: ListAdmins 使用注入的 dataPermSvc

**Files:**
- Modify: `d:/www/admin/internal/service/super_admin_service.go`

- [ ] **Step 1：替换 ListAdmins 中现场 new**

把 super_admin_service.go L249 区段：
```go
if req.CurrentUID != "" {
	dataPermSvc := NewDataPermissionService(NewDepartmentService())
	scope, err := dataPermSvc.ResolveAdminScope(ctx, req.CurrentUID)
```
改为：
```go
if req.CurrentUID != "" {
	scope, err := s.dataPermSvc.ResolveAdminScope(ctx, req.CurrentUID)
```

- [ ] **Step 2：搜索同文件其他现场 new 一并替换**

```powershell
cd d:\www\admin ; Select-String -Path internal/service/super_admin_service.go -Pattern "NewDataPermissionService|NewDepartmentService"
```
若有其他出现按相同模式改用 `s.dataPermSvc` / `s.deptSvc`。

- [ ] **Step 3：build 验证**

```powershell
cd d:\www\admin ; go build ./internal/service
```
预期：exit 0。

- [ ] **Step 4：commit**

```powershell
cd d:\www\admin ; git add internal/service/super_admin_service.go ; git commit -m "refactor(admin): ListAdmins 使用注入的 dataPermSvc 不再现场 new"
```

---

## Task 6: handler/role.go 注入 op + 错误映射

**Files:**
- Modify: `d:/www/admin/internal/handler/role.go`
- Modify: `d:/www/admin/internal/router.go`

- [ ] **Step 1：确认 c.Locals 中 DataPermissionService 注入键**

```powershell
cd d:\www\admin ; Select-String -Path internal/router.go -Pattern "service.DataPermissionService"
```
若未注入，在 router.go 的 adminAuthBase 中间件中（与其他 Locals 相邻位置）追加：
```go
c.Locals("service.DataPermissionService", dataPermissionService)
```

读 handler/admin_list.go 中 CreateAdmin 取 op 的代码片段，作为 role.go 改造参考。

- [ ] **Step 2：CreateRole 注入 op**

定位 `role, err := roleService.Create(c.Context(), req)`（约 L42），改为：
```go
dataPermSvc, ok := c.Locals("service.DataPermissionService").(*service.DataPermissionService)
if !ok || dataPermSvc == nil {
	return resp.Fail(global.ResponseInternalServerError, "数据权限服务未注入").ToJSON(c)
}
op, err := dataPermSvc.NewOperatorContext(c.Context(), getCurrentUID(c))
if err != nil {
	return resp.Fail(global.ResponseInternalServerError, "解析操作者权限失败").ToJSON(c)
}
role, err := roleService.Create(c.Context(), op, req)
```
**getCurrentUID** 替换为 handler 包内实际 helper（admin_list.go 等已用 → grep 找）。

在错误 switch 内追加：
```go
case service.ErrRoleScopeEscalation:
	return resp.Fail(global.ResponseRoleScopeEscalation, global.ResponseMsg[global.ResponseRoleScopeEscalation]).ToJSON(c)
```

- [ ] **Step 3：UpdateRole 注入 op**

定位 `if err := roleService.Update(c.Context(), id, req); err != nil`（约 L210），改为相同模式，并在 switch 中追加 ErrRoleScopeEscalation 分支。

- [ ] **Step 4：build 验证**

```powershell
cd d:\www\admin ; go build ./...
```
预期：exit 0（除测试包外全绿）。

- [ ] **Step 5：commit**

```powershell
cd d:\www\admin ; git add internal/handler/role.go internal/router.go ; git commit -m "feat(handler): CreateRole/UpdateRole 注入 op + ErrRoleScopeEscalation 映射"
```

---

## Task 7: 测试包同步：补 nil op + 5 个 E2E

**Files:**
- Modify: `d:/www/admin/internal/service/super_admin_service_test.go`
- Modify: `d:/www/admin/internal/service/data_permission_test.go`
- Modify: 任何调用 `NewSuperAdminService` / `NewRoleAssignmentService` / `roleSvc.Create` / `roleSvc.Update` 的 *_test.go

- [ ] **Step 1：把所有 roleSvc.Create/Update 调用补 nil op**

```powershell
cd d:\www\admin ; Select-String -Path internal/service/*_test.go -Pattern "RoleService.*Create\(|RoleService.*Update\(|\.Create\(ctx, &CreateRoleRequest|\.Update\(ctx, .*&UpdateRoleRequest"
```
- `svc.Create(ctx, &req)` → `svc.Create(ctx, nil, &req)`
- `svc.Update(ctx, id, &req)` → `svc.Update(ctx, nil, id, &req)`

- [ ] **Step 2：更新 NewSuperAdminService 调用**

```powershell
cd d:\www\admin ; Select-String -Path internal/service/*_test.go -Pattern "NewSuperAdminService"
```
所有 `NewSuperAdminService()` → `NewSuperAdminService(NewDataPermissionService(NewDepartmentService()), NewDepartmentService())`。
若 setupSuperAdminTest 内部调用，也同步。

- [ ] **Step 3：更新 NewRoleAssignmentService 调用**

```powershell
cd d:\www\admin ; Select-String -Path internal/service/*_test.go -Pattern "NewRoleAssignmentService"
```
所有 `NewRoleAssignmentService(cache)` → `NewRoleAssignmentService(cache, NewDepartmentService())`。

- [ ] **Step 4：在 super_admin_service_test.go 末尾追加 5 个 E2E**

```go
func TestRoleService_Create_RejectsOpDeptToAll(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	defer cleanup()
	ctx := t.Context()
	svc := NewRoleService(nil)
	op := &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1}}
	_, err := svc.Create(ctx, op, &CreateRoleRequest{
		Name: "rw_all", DisplayName: "rw_all", DataScope: string(DataScopeAll),
	})
	if !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("Dept→All 期望 Esc, got %v", err)
	}
	_ = client
}

func TestRoleService_Update_RejectsOpDeptToAll(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	defer cleanup()
	ctx := t.Context()
	svc := NewRoleService(nil)
	r, err := svc.Create(ctx, nil, &CreateRoleRequest{Name: "rw_u", DisplayName: "rw_u", DataScope: string(DataScopeSelf)})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	op := &EffectiveDataScope{Scope: DataScopeDepartment, DepartmentIDs: []int{1}}
	err = svc.Update(ctx, op, r.ID, &UpdateRoleRequest{DataScope: string(DataScopeAll)})
	if !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("Update Dept→All 期望 Esc, got %v", err)
	}
	_ = client
}

func TestSuperAdminService_CreateAdmin_RejectsDeptAndChildOverflow(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	defer cleanup()
	ctx := t.Context()
	d := client.Department.Create().SetName("P-dc").SaveX(ctx)
	rDc := client.Role.Create().SetName("rdc").SetDisplayName("rdc").
		SetDataScope(role.DataScopeDepartmentAndChild).SetStatus(role.StatusActive).SaveX(ctx)
	fd := &fakeDeptSvc{childIDs: map[int][]int{d.ID: {d.ID, 99, 100}}}
	svc := &SuperAdminService{client: client, deptSvc: fd, dataPermSvc: NewDataPermissionService(fd)}
	op := &EffectiveDataScope{Scope: DataScopeCustom, DepartmentIDs: []int{d.ID}}
	_, err := svc.CreateAdmin(ctx, op, &CreateAdminRequest{
		Email: "dc@e.com", Username: "dc", Password: "secret123!",
		DepartmentID: d.ID, RoleIDs: []int{rDc.ID}, PrimaryRoleID: rDc.ID,
	})
	if !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("DC overflow 期望 Esc, got %v", err)
	}
}

func TestSuperAdminService_AssignAdminRoles_RejectsDeptAndChildOverflow(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	defer cleanup()
	ctx := t.Context()
	d := client.Department.Create().SetName("P-dc2").SaveX(ctx)
	a := client.Admin.Create().SetEmail("dc2@e.com").SetUsername("dc2").
		SetPassword("x").SetPasswordSalt("s").SetUID("uid-dc2").
		SetNickname("n").SetName("nm").SetDepartmentID(d.ID).SaveX(ctx)
	rDc := client.Role.Create().SetName("rdc2").SetDisplayName("rdc2").
		SetDataScope(role.DataScopeDepartmentAndChild).SetStatus(role.StatusActive).SaveX(ctx)
	fd := &fakeDeptSvc{childIDs: map[int][]int{d.ID: {d.ID, 199, 200}}}
	svc := &RoleAssignmentService{client: client, deptSvc: fd}
	op := &EffectiveDataScope{Scope: DataScopeCustom, AdminID: 99, AdminUID: "u-mgr", DepartmentIDs: []int{d.ID}}
	err := svc.AssignAdminRoles(ctx, op, a.ID,
		&AssignRolesRequest{RoleIDs: []int{rDc.ID}, PrimaryRoleID: rDc.ID}, "u-mgr")
	if !errors.Is(err, ErrRoleScopeEscalation) {
		t.Fatalf("Assign DC overflow 期望 Esc, got %v", err)
	}
}

func TestSuperAdminService_ListAdmins_UsesInjectedDataPermSvc(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	defer cleanup()
	ctx := t.Context()
	a := client.Admin.Create().SetEmail("la@e.com").SetUsername("la").
		SetPassword("x").SetPasswordSalt("s").SetUID("uid-la").
		SetNickname("n").SetName("nm").SetIsSuperAdmin(true).SaveX(ctx)
	deptSvc := NewDepartmentService()
	dataPermSvc := NewDataPermissionService(deptSvc)
	svc := &SuperAdminService{client: client, dataPermSvc: dataPermSvc, deptSvc: deptSvc}
	// ListAdmins 带 CurrentUID，应正常返回（超管不过滤）
	res, err := svc.ListAdmins(ctx, &ListAdminsRequest{CurrentUID: a.UID, Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("ListAdmins 失败: %v", err)
	}
	if res == nil {
		t.Fatal("ListAdmins 返回 nil")
	}
}
```

字段名 `ListAdminsRequest` / `Page` / `PageSize` 以实际为准；若构造 `RoleAssignmentService` 内部还有 cache 字段，按实际加 `cache: nil`。

- [ ] **Step 5：跑测试**

```powershell
cd d:\www\admin ; go test ./internal/service/ -count=1
```
预期：全 PASS。若失败逐一修。

- [ ] **Step 6：commit**

```powershell
cd d:\www\admin ; git add internal/service/super_admin_service_test.go internal/service/data_permission_test.go ; git commit -m "test(data-permission): 第四轮加固 5 个 E2E + 测试签名适配"
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
预期：exit 0，所有包 ok。

- [ ] **Step 3：git log 检查**

```powershell
cd d:\www\admin ; git log --oneline -12
```
确认本轮 8 个 commit 在。

---

## 收尾

完成后应达成：
- Role.data_scope 写入受 op 范围约束
- 跨 scope（DeptAndChild）分配按实际部门展开判断
- ListAdmins 不再现场 new dataPermSvc
- 累计第四轮 3 个 P 级修复
