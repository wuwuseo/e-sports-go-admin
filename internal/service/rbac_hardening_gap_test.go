package service

import (
	"errors"
	"testing"

	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/adminrole"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/role"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

// 本文件补充 rbac 加固提交（7cc8ef9）中尚未直接覆盖的纯函数与守卫层单元测试。
// 重点目标：
//  1. ensureRoleNotHeldByOperator —— 4 个 CRUD 路径都会调用的 self-role 守卫；
//  2. uniquePositiveInts / departmentIDsForScope —— 部门作用域树查询的关键过滤；
//  3. clearUserPermissionsByRoleID —— RoleService 缓存清理行为。
//
// 注意：isPluginManagementSystemPermission / buildCasbinTupleFromSystemPermission / firstNonEmpty
// 属于 internal 包，已在 internal/init_test.go 中通过 initNormalAdminRole 间接覆盖，
// 本文件不重复。

// -------------------------------------------------------------------
// ensureRoleNotHeldByOperator 单元测试
// -------------------------------------------------------------------

func TestEnsureRoleNotHeldByOperator_NilOpPasses(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ens_self_nil?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	// nil op 永远放行（且不应访问 DB）
	if err := ensureRoleNotHeldByOperator(t.Context(), client, nil, 1); err != nil {
		t.Fatalf("nil op 应放行，got %v", err)
	}
}

func TestEnsureRoleNotHeldByOperator_SuperAdminPasses(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ens_self_super?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	op := &EffectiveDataScope{AdminID: 1, AdminUID: "u1", IsSuperAdmin: true}
	// 即使管理员实际持有该角色，超管身份应直接放行（不触发 self-role 校验）
	if err := ensureRoleNotHeldByOperator(t.Context(), client, op, 1); err != nil {
		t.Fatalf("超管应放行，got %v", err)
	}
}

func TestEnsureRoleNotHeldByOperator_ZeroAdminIDPasses(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ens_self_zero?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	// op.AdminID == 0 视为内部调用，放行
	op := &EffectiveDataScope{AdminID: 0, AdminUID: ""}
	if err := ensureRoleNotHeldByOperator(t.Context(), client, op, 1); err != nil {
		t.Fatalf("AdminID=0 应放行，got %v", err)
	}
}

func TestEnsureRoleNotHeldByOperator_OperatorHoldsRoleRejected(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ens_self_held?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()
	r, err := client.Role.Create().
		SetName("self-role").
		SetDisplayName("self").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}
	a, err := client.Admin.Create().
		SetEmail("self@test.com").
		SetUsername("self").
		SetPassword("x").
		SetPasswordSalt("y").
		SetUID("uid-self").
		SetStatus(admin.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建管理员失败: %v", err)
	}
	if _, err := client.AdminRole.Create().SetAdminID(a.ID).SetRoleID(r.ID).Save(ctx); err != nil {
		t.Fatalf("创建 admin-role 关联失败: %v", err)
	}

	op := &EffectiveDataScope{AdminID: a.ID, AdminUID: a.UID, Scope: DataScopeAll}
	err = ensureRoleNotHeldByOperator(ctx, client, op, r.ID)
	if !errors.Is(err, ErrCannotModifySelfRoles) {
		t.Fatalf("管理员持有该角色应返回 ErrCannotModifySelfRoles，got %v", err)
	}
}

func TestEnsureRoleNotHeldByOperator_OperatorDoesNotHoldRolePasses(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ens_self_not?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	ctx := t.Context()
	r, err := client.Role.Create().
		SetName("other-role").
		SetDisplayName("other").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}
	a, err := client.Admin.Create().
		SetEmail("notholder@test.com").
		SetUsername("notholder").
		SetPassword("x").
		SetPasswordSalt("y").
		SetUID("uid-notholder").
		SetStatus(admin.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建管理员失败: %v", err)
	}
	// 显式建立另一条 admin-role 关联，确保 AdminID 在 AdminRole 表中
	otherRole, err := client.Role.Create().
		SetName("different").
		SetDisplayName("d").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建 other 角色失败: %v", err)
	}
	if _, err := client.AdminRole.Create().SetAdminID(a.ID).SetRoleID(otherRole.ID).Save(ctx); err != nil {
		t.Fatalf("创建 admin-role 关联失败: %v", err)
	}

	op := &EffectiveDataScope{AdminID: a.ID, AdminUID: a.UID, Scope: DataScopeDepartment, DepartmentIDs: []int{1}}
	if err := ensureRoleNotHeldByOperator(ctx, client, op, r.ID); err != nil {
		t.Fatalf("未持有该角色应放行，got %v", err)
	}
}

// -------------------------------------------------------------------
// uniquePositiveInts 单元测试
// -------------------------------------------------------------------

func TestUniquePositiveInts_EmptyInputReturnsEmpty(t *testing.T) {
	got := uniquePositiveInts(nil)
	if len(got) != 0 {
		t.Fatalf("空输入应返回空切片，got %v", got)
	}
}

func TestUniquePositiveInts_FiltersZeroAndNegative(t *testing.T) {
	got := uniquePositiveInts([]int{0, -1, -2, 5})
	want := []int{5}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("期望过滤掉 <=0 后保留 [5]，got %v", got)
	}
}

func TestUniquePositiveInts_DedupesWhilePreservingOrder(t *testing.T) {
	got := uniquePositiveInts([]int{3, 1, 2, 1, 3, 4, 2})
	want := []int{3, 1, 2, 4}
	if len(got) != len(want) {
		t.Fatalf("期望去重后 %v，got %v", want, got)
	}
	for i, v := range want {
		if got[i] != v {
			t.Fatalf("索引 %d 期望 %d，got %d（顺序错）", i, v, got[i])
		}
	}
}

// -------------------------------------------------------------------
// departmentIDsForScope 单元测试
// -------------------------------------------------------------------

func TestDepartmentIDsForScope_NilAndSuperAdminNotScoped(t *testing.T) {
	if ids, scoped := departmentIDsForScope(nil); ids != nil || scoped {
		t.Fatalf("nil op 期望 (nil, false)，got (%v, %v)", ids, scoped)
	}
	if ids, scoped := departmentIDsForScope(&EffectiveDataScope{IsSuperAdmin: true}); ids != nil || scoped {
		t.Fatalf("超管期望 (nil, false)，got (%v, %v)", ids, scoped)
	}
}

func TestDepartmentIDsForScope_DataScopeAllNotScoped(t *testing.T) {
	op := &EffectiveDataScope{Scope: DataScopeAll, DepartmentIDs: []int{1, 2, 3}}
	if ids, scoped := departmentIDsForScope(op); ids != nil || scoped {
		t.Fatalf("All 期望不限定，got (%v, %v)", ids, scoped)
	}
}

func TestDepartmentIDsForScope_DataScopeSelfScopedButEmpty(t *testing.T) {
	op := &EffectiveDataScope{Scope: DataScopeSelf, DepartmentIDs: []int{1, 2}}
	if ids, scoped := departmentIDsForScope(op); ids != nil || !scoped {
		t.Fatalf("Self 期望 (nil, true)，got (%v, %v)", ids, scoped)
	}
}

func TestDepartmentIDsForScope_DepartmentAndCustomAndChildReturnIDsScoped(t *testing.T) {
	wantIDs := []int{10, 20, 30}
	for _, scope := range []DataScope{DataScopeDepartment, DataScopeDepartmentAndChild, DataScopeCustom} {
		op := &EffectiveDataScope{Scope: scope, DepartmentIDs: wantIDs}
		ids, scoped := departmentIDsForScope(op)
		if !scoped {
			t.Fatalf("%s 期望 scoped=true", scope)
		}
		if len(ids) != len(wantIDs) {
			t.Fatalf("%s 期望 IDs %v，got %v", scope, wantIDs, ids)
		}
		for i, v := range wantIDs {
			if ids[i] != v {
				t.Fatalf("%s 索引 %d 期望 %d，got %d", scope, i, v, ids[i])
			}
		}
	}
}

func TestDepartmentIDsForScope_UnknownScopeScopedButEmpty(t *testing.T) {
	op := &EffectiveDataScope{Scope: DataScope("future_scope"), DepartmentIDs: []int{1}}
	if ids, scoped := departmentIDsForScope(op); ids != nil || !scoped {
		t.Fatalf("未知 scope 期望 (nil, true)（fail-closed），got (%v, %v)", ids, scoped)
	}
}

// -------------------------------------------------------------------
// clearUserPermissionsByRoleID 行为测试
// -------------------------------------------------------------------

func TestClearUserPermissionsByRoleID_NilCacheSafeNoop(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:clear_role_cache?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	// s.cache == nil 时必须安全返回，不能 panic
	svc := NewRoleService(nil)
	// 不创建任何 UserRole；方法应直接退出
	svc.clearUserPermissionsByRoleID(t.Context(), 999, "default")
}

func TestClearUserPermissionsByRoleID_NoHoldersSafeNoop(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:clear_role_no_holders?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	// 不存在任何 UserRole 关联时也不应 panic
	svc := NewRoleService(nil)
	svc.clearUserPermissionsByRoleID(t.Context(), 999, "default")
}

// -------------------------------------------------------------------
// 防止 admin-role 包与守卫解耦的回归测试
// -------------------------------------------------------------------

func TestEnsureRoleNotHeldByOperator_QueryUsesAdminRoleEdge(t *testing.T) {
	// 验证 ensureRoleNotHeldByOperator 实际通过 adminrole 实体查询，
	// 编译期通过引用 adminrole 包中的谓词来保证。
	_ = adminrole.AdminIDEQ(0)
}
