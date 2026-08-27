package service

import (
	"testing"
)

// 本文件补充 data_permission.go 中三个核心纯函数的直接单元测试。
//
// 背景：commit 6a1ec18 将 data_permission.go 中的内联权限校验逻辑重构为
// 三个可复用的纯函数（shouldBypassCheck / isDepartmentInScope / isCustomDeptSubset），
// 整个数据权限校验体系（ensureAdminInScope / ensureDepartmentInScope /
// ensureRolesNotEscalating / ensureRolesNotEscalatingForTarget / adminInOperatorScope /
// ensureRoleManageableForAssignedAdmins / ensureRoleScopeWritable）都依赖它们。
//
// 这三个函数是数据权限校验的新基石，行为漂移将直接导致：
//   - 提权（fail-open）：非超管被错误放行
//   - 越权：操作者可访问超出自身数据范围的部门
//   - 数据范围升级：可分配超出自身范围的 Custom 角色部门
//
// 此前仅通过集成路径间接覆盖，任一分支漂移都难以定位。
// 本文件为它们建立最小、直接、可独立运行的回归网。

// -------------------------------------------------------------------
// shouldBypassCheck 单元测试
// 语义：op==nil 或 op.IsSuperAdmin 永远放行；
//      allowAllScope=true 时 op.Scope==DataScopeAll 也放行；
//      其他情况必须放行 false，强制进入具体作用域校验分支。
// -------------------------------------------------------------------

func TestShouldBypassCheck_NilOpAlwaysTrue(t *testing.T) {
	// nil op 在两个分支下都应返回 true（内部调用上下文）
	if !shouldBypassCheck(nil, false) {
		t.Fatal("nil op + allowAllScope=false 应返回 true")
	}
	if !shouldBypassCheck(nil, true) {
		t.Fatal("nil op + allowAllScope=true 应返回 true")
	}
}

func TestShouldBypassCheck_SuperAdminAlwaysTrue(t *testing.T) {
	// 超管身份独立于 allowAllScope，永远放行
	for _, allowAll := range []bool{false, true} {
		op := &EffectiveDataScope{IsSuperAdmin: true, Scope: DataScopeSelf}
		if !shouldBypassCheck(op, allowAll) {
			t.Fatalf("超管 + allowAllScope=%v 应放行", allowAll)
		}
	}
}

func TestShouldBypassCheck_NonSuperAdminAllowAllFalse_RespectsScope(t *testing.T) {
	// allowAllScope=false 时（admin/department 校验路径），
	// 即使 op.Scope==DataScopeAll 也必须返回 false，
	// 由调用方的 switch 自行处理 DataScopeAll 放行逻辑。
	// 这一约束保证 helper 不会因 DataScopeAll 误放行而让数据范围校验被绕过。
	op := &EffectiveDataScope{Scope: DataScopeAll}
	if shouldBypassCheck(op, false) {
		t.Fatal("非超管 + allowAllScope=false + Scope=All 应返回 false（由调用方 switch 处理）")
	}
}

func TestShouldBypassCheck_NonSuperAdminAllowAllTrue_DataScopeAllPasses(t *testing.T) {
	// allowAllScope=true 时（角色相关校验路径），
	// op.Scope==DataScopeAll 应被识别为放行（角色 scope 不超过操作者范围）。
	op := &EffectiveDataScope{Scope: DataScopeAll}
	if !shouldBypassCheck(op, true) {
		t.Fatal("非超管 + allowAllScope=true + Scope=All 应放行")
	}
}

func TestShouldBypassCheck_NonSuperAdmin_NarrowScopeNeverBypasses(t *testing.T) {
	// 任何非超管 + 非 DataScopeAll 的组合都应返回 false，
	// 强制进入 DepartmentIDs 命中校验。
	for _, scope := range []DataScope{DataScopeSelf, DataScopeDepartment, DataScopeDepartmentAndChild, DataScopeCustom, DataScope("future_scope")} {
		op := &EffectiveDataScope{Scope: scope, DepartmentIDs: []int{1, 2, 3}}
		for _, allowAll := range []bool{false, true} {
			if shouldBypassCheck(op, allowAll) {
				t.Fatalf("非超管 + Scope=%s + allowAllScope=%v 不应放行", scope, allowAll)
			}
		}
	}
}

func TestShouldBypassCheck_SuperAdminBeatsAllowAllScope(t *testing.T) {
	// 验证 op.IsSuperAdmin 的短路优先级：即使 allowAllScope=false 且 Scope 为窄作用域，
	// 超管身份也必须放行（不应让"窄 scope"意外覆盖"超管"判断）。
	op := &EffectiveDataScope{IsSuperAdmin: true, Scope: DataScopeSelf}
	if !shouldBypassCheck(op, false) {
		t.Fatal("超管身份应优先于 scope 判断放行")
	}
}

// -------------------------------------------------------------------
// isDepartmentInScope 单元测试
// 语义：op==nil / 超管 由调用方 shouldBypassCheck 处理，调用前已保证 op 非 nil 且非超管。
//      DataScopeAll → true（防御性兜底：超管走 bypass 路径，这里仅防御二次进入）；
//      DataScopeSelf → false（Self 必须只命中自己，由调用方短路 target.UID==op.AdminUID）；
//      Department/DepartmentAndChild/Custom → 命中 DepartmentIDs 即 true；
//      其他 → false（fail-closed）。
// -------------------------------------------------------------------

func TestIsDepartmentInScope_AllReturnsTrue(t *testing.T) {
	// DataScopeAll 命中即视为在范围内（即便超管已被外层短路，这里保持防御性 true）
	op := &EffectiveDataScope{Scope: DataScopeAll}
	for _, id := range []int{0, 1, 999} {
		if !isDepartmentInScope(op, id) {
			t.Fatalf("All scope + deptID=%d 应返回 true", id)
		}
	}
}

func TestIsDepartmentInScope_SelfAlwaysFalse(t *testing.T) {
	// DataScopeSelf 强制返回 false；调用方在进入此函数前应已通过
	// target.UID == op.AdminUID 短路放行（"访问自己"）。
	op := &EffectiveDataScope{Scope: DataScopeSelf, DepartmentIDs: []int{1, 2, 3}}
	for _, id := range []int{1, 2, 3, 999} {
		if isDepartmentInScope(op, id) {
			t.Fatalf("Self scope + deptID=%d 应返回 false", id)
		}
	}
}

func TestIsDepartmentInScope_DepartmentVariants_HitAndMiss(t *testing.T) {
	// Department / DepartmentAndChild / Custom 三种 scope 应共享同一命中表
	for _, scope := range []DataScope{DataScopeDepartment, DataScopeDepartmentAndChild, DataScopeCustom} {
		op := &EffectiveDataScope{Scope: scope, DepartmentIDs: []int{10, 20, 30}}

		// 命中
		for _, hit := range []int{10, 20, 30} {
			if !isDepartmentInScope(op, hit) {
				t.Fatalf("%s scope + deptID=%d（应命中）却返回 false", scope, hit)
			}
		}
		// 未命中
		for _, miss := range []int{0, 1, 9, 31, 999} {
			if isDepartmentInScope(op, miss) {
				t.Fatalf("%s scope + deptID=%d（未命中）却返回 true", scope, miss)
			}
		}
	}
}

func TestIsDepartmentInScope_ZeroDepartmentIDRejectsForNarrowScopes(t *testing.T) {
	// 部门模式下 deptID=0 必须被拒绝（"未分配部门的账号在部门模式下不可见"）。
	for _, scope := range []DataScope{DataScopeDepartment, DataScopeDepartmentAndChild, DataScopeCustom} {
		op := &EffectiveDataScope{Scope: scope, DepartmentIDs: []int{0, 1, 2}}
		if isDepartmentInScope(op, 0) {
			t.Fatalf("%s scope + deptID=0 应返回 false（fail-closed）", scope)
		}
	}
}

func TestIsDepartmentInScope_EmptyDepartmentIDsForNarrowScopes(t *testing.T) {
	// 窄 scope 但 DepartmentIDs 为空时：任何 deptID 都应返回 false（fail-closed）。
	// 防止"分配了角色但没拿到部门"的操作者越权访问任意部门。
	for _, scope := range []DataScope{DataScopeDepartment, DataScopeDepartmentAndChild, DataScopeCustom} {
		op := &EffectiveDataScope{Scope: scope, DepartmentIDs: nil}
		if isDepartmentInScope(op, 1) {
			t.Fatalf("%s scope + nil DepartmentIDs 应返回 false", scope)
		}
	}
}

func TestIsDepartmentInScope_UnknownScopeFailsClosed(t *testing.T) {
	// 未知 scope 必须 fail-closed：返回 false 拒绝（避免未来新增枚举值时 fail-open）。
	op := &EffectiveDataScope{Scope: DataScope("future_scope"), DepartmentIDs: []int{1, 2, 3}}
	if isDepartmentInScope(op, 1) {
		t.Fatal("未知 scope 应 fail-closed（返回 false）")
	}
}

func TestIsDepartmentInScope_DefendsAgainstAccidentalReentryFromSuperAdmin(t *testing.T) {
	// 即便调用方遗漏 shouldBypassCheck 短路（传入超管），
	// 该函数本身对 DataScopeAll 仍返回 true，行为一致（防御性兜底）。
	op := &EffectiveDataScope{IsSuperAdmin: true, Scope: DataScopeAll, DepartmentIDs: nil}
	if !isDepartmentInScope(op, 999) {
		t.Fatal("All scope 应放行（防御性兜底）")
	}
}

// -------------------------------------------------------------------
// isCustomDeptSubset 单元测试
// 语义：检查 deptIDs 是否为 op.DepartmentIDs 的子集。
//      - 空 deptIDs → true（无操作即满足）；
//      - 全部命中 → true；
//      - 任意一项未命中 → false。
// -------------------------------------------------------------------

func TestIsCustomDeptSubset_EmptyInputIsVacuouslyTrue(t *testing.T) {
	// 空集合是任何集合的子集：调用方传入空 deptIDs 时应放行。
	op := &EffectiveDataScope{DepartmentIDs: []int{1, 2, 3}}
	if !isCustomDeptSubset(op, nil) {
		t.Fatal("空 deptIDs 应返回 true")
	}
	if !isCustomDeptSubset(op, []int{}) {
		t.Fatal("空 deptIDs 应返回 true")
	}
}

func TestIsCustomDeptSubset_AllHitReturnsTrue(t *testing.T) {
	op := &EffectiveDataScope{DepartmentIDs: []int{1, 2, 3, 4, 5}}
	if !isCustomDeptSubset(op, []int{1, 3, 5}) {
		t.Fatal("全部命中应返回 true")
	}
}

func TestIsCustomDeptSubset_AnyMissReturnsFalse(t *testing.T) {
	op := &EffectiveDataScope{DepartmentIDs: []int{1, 2, 3, 4, 5}}
	for _, deptIDs := range [][]int{
		{1, 999},           // 第二个未命中
		{999},              // 首个未命中
		{1, 2, 3, 4, 5, 6}, // 末尾追加未命中
	} {
		if isCustomDeptSubset(op, deptIDs) {
			t.Fatalf("包含未命中元素时应返回 false：%v", deptIDs)
		}
	}
}

func TestIsCustomDeptSubset_OperatorHasNoDepartmentsFailsClosed(t *testing.T) {
	// 操作者无任何部门时：任何非空 deptIDs 都应被拒绝（fail-closed）。
	op := &EffectiveDataScope{DepartmentIDs: nil}
	if isCustomDeptSubset(op, []int{1}) {
		t.Fatal("op 无部门 + 非空 deptIDs 应返回 false")
	}
}

func TestIsCustomDeptSubset_OrderIndependent(t *testing.T) {
	// 验证与顺序无关：乱序输入也应被正确判定。
	op := &EffectiveDataScope{DepartmentIDs: []int{1, 2, 3}}
	for _, deptIDs := range [][]int{{3, 1, 2}, {2, 1, 3}, {1, 2, 3}} {
		if !isCustomDeptSubset(op, deptIDs) {
			t.Fatalf("顺序应不影响子集判定：%v", deptIDs)
		}
	}
}

func TestIsCustomDeptSubset_DuplicatesAreTolerated(t *testing.T) {
	// 重复元素应被正确处理：即使 deptIDs 中含重复项，只要所有项都命中 op.DepartmentIDs 即可。
	op := &EffectiveDataScope{DepartmentIDs: []int{1, 2, 3}}
	if !isCustomDeptSubset(op, []int{1, 1, 2, 2, 3, 3}) {
		t.Fatal("重复元素应被正确判定为子集")
	}
	// 含未命中元素时（即使伴随重复）也必须返回 false。
	if isCustomDeptSubset(op, []int{1, 1, 1, 999}) {
		t.Fatal("含未命中元素的重复应返回 false")
	}
}
