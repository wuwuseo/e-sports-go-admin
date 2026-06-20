package internal

import (
	"strings"
	"testing"

	"go-admin/internal/ent"
)

// 本文件补充 init.go 中尚未直接覆盖的纯函数单元测试。
// 这些函数都是高内聚、低副作用的纯函数，是 init 流程核心策略（插件权限排除、Casbin 三元组构造）
// 的关键拼图；任一行为变化都会直接影响系统启动时构建的 Casbin 策略集。
//
// 涵盖：
//  1. isPluginManagementSystemPermission —— 插件权限识别的所有路径；
//  2. shouldSeedNormalAdminPermission —— 反义包装的简洁回归；
//  3. buildCasbinTupleFromSystemPermission —— Menu/Button/API 三种类型的优先级与降级；
//  4. firstNonEmpty —— 多值首非空选择器。

// -------------------------------------------------------------------
// isPluginManagementSystemPermission 单元测试
// -------------------------------------------------------------------

func newStubSystemPermission(t *testing.T, opts func(p *ent.SystemPermission)) *ent.SystemPermission {
	t.Helper()
	// ent.SystemPermission 字段全部暴露，这里用零值即可；opts 用于在测试中覆盖。
	p := &ent.SystemPermission{
		Type:     "api",
		Name:     "stub",
		Code:     "stub",
		MenuPath: "",
		Path:     "",
		Method:   "GET",
	}
	opts(p)
	return p
}

func TestIsPluginManagementSystemPermission_CodeExactMatches(t *testing.T) {
	for _, code := range []string{"plugin", "system:plugin"} {
		p := newStubSystemPermission(t, func(p *ent.SystemPermission) { p.Code = code })
		if !isPluginManagementSystemPermission(p) {
			t.Fatalf("Code=%q 应识别为插件权限", code)
		}
	}
}

func TestIsPluginManagementSystemPermission_CodeHasPluginPrefix(t *testing.T) {
	for _, code := range []string{"plugin:install", "plugin:uninstall", "plugin:list", "plugin:x"} {
		p := newStubSystemPermission(t, func(p *ent.SystemPermission) { p.Code = code })
		if !isPluginManagementSystemPermission(p) {
			t.Fatalf("Code=%q 应识别为插件权限（plugin: 前缀）", code)
		}
	}
}

func TestIsPluginManagementSystemPermission_MenuPathExact(t *testing.T) {
	p := newStubSystemPermission(t, func(p *ent.SystemPermission) {
		p.Type = "menu"
		p.MenuPath = "/system/plugin"
		p.Code = "irrelevant"
	})
	if !isPluginManagementSystemPermission(p) {
		t.Fatal("MenuPath=/system/plugin 应识别为插件权限")
	}
}

func TestIsPluginManagementSystemPermission_PathPrefixUnderPlugins(t *testing.T) {
	for _, pth := range []string{
		"/api/v1/admin/auth/plugins",
		"/api/v1/admin/auth/plugins/list",
		"/api/v1/admin/auth/plugins/123",
	} {
		p := newStubSystemPermission(t, func(p *ent.SystemPermission) {
			p.Type = "api"
			p.Path = pth
			p.Code = "unrelated"
		})
		if !isPluginManagementSystemPermission(p) {
			t.Fatalf("Path=%q 应识别为插件权限（/api/v1/admin/auth/plugins 前缀）", pth)
		}
	}
}

func TestIsPluginManagementSystemPermission_NonPluginReturnsFalse(t *testing.T) {
	p := newStubSystemPermission(t, func(p *ent.SystemPermission) {
		p.Type = "api"
		p.Code = "role:list"
		p.Path = "/api/v1/admin/auth/roles"
		p.Method = "GET"
		p.MenuPath = "/system/role"
	})
	if isPluginManagementSystemPermission(p) {
		t.Fatal("非插件权限应返回 false")
	}
}

func TestIsPluginManagementSystemPermission_TrimsWhitespace(t *testing.T) {
	// 当 Code 或 Path 含有前后空白时，识别仍应正确（防止上游写入时的格式漂移）。
	p := newStubSystemPermission(t, func(p *ent.SystemPermission) {
		p.Type = "api"
		p.Code = "  plugin:install  "
	})
	if !isPluginManagementSystemPermission(p) {
		t.Fatal("包含空白的 Code='plugin:install' 仍应识别为插件权限")
	}

	p2 := newStubSystemPermission(t, func(p *ent.SystemPermission) {
		p.Type = "api"
		p.Path = "  /api/v1/admin/auth/plugins  "
	})
	if !isPluginManagementSystemPermission(p2) {
		t.Fatal("包含空白的 Path 仍应识别为插件权限")
	}
}

func TestIsPluginManagementSystemPermission_PrefixOnlyFalsePositiveGuard(t *testing.T) {
	// 类似命名（如 "plugin" 出现在中间）应被识别为 false：
	// Code="myplugin" 不应误判。
	p := newStubSystemPermission(t, func(p *ent.SystemPermission) { p.Code = "myplugin" })
	if isPluginManagementSystemPermission(p) {
		t.Fatal("Code='myplugin' 不应以 'plugin:' 前缀匹配（HasPrefix 而非 Contains）")
	}
	// MenuPath 前缀为 "/system/plugins-admin" 也不应误判。
	p2 := newStubSystemPermission(t, func(p *ent.SystemPermission) {
		p.Type = "menu"
		p.MenuPath = "/system/plugins-admin"
	})
	if isPluginManagementSystemPermission(p2) {
		t.Fatal("MenuPath='/system/plugins-admin' 不应误判为插件（精确匹配）")
	}
}

// -------------------------------------------------------------------
// shouldSeedNormalAdminPermission 单元测试
// -------------------------------------------------------------------

func TestShouldSeedNormalAdminPermission_IsNotPlugin(t *testing.T) {
	cases := []struct {
		name string
		p    *ent.SystemPermission
		want bool
	}{
		{
			"普通 API 应被播种",
			newStubSystemPermission(t, func(p *ent.SystemPermission) {
				p.Type = "api"
				p.Code = "role:list"
				p.Path = "/api/v1/admin/auth/roles"
			}),
			true,
		},
		{
			"插件 menu 不应被播种",
			newStubSystemPermission(t, func(p *ent.SystemPermission) {
				p.Type = "menu"
				p.MenuPath = "/system/plugin"
				p.Code = "plugin"
			}),
			false,
		},
		{
			"plugin:install 按钮不应被播种",
			newStubSystemPermission(t, func(p *ent.SystemPermission) {
				p.Type = "button"
				p.Code = "plugin:install"
			}),
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldSeedNormalAdminPermission(tc.p)
			if got != tc.want {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
		})
	}
}

// -------------------------------------------------------------------
// buildCasbinTupleFromSystemPermission 单元测试
// -------------------------------------------------------------------

func TestBuildCasbinTupleFromSystemPermission_MenuUsesMenuPathFirst(t *testing.T) {
	p := newStubSystemPermission(t, func(p *ent.SystemPermission) {
		p.Type = "menu"
		p.MenuPath = "/system/user"
		p.Code = "user"
		p.Name = "用户管理"
	})
	obj, act, permType := buildCasbinTupleFromSystemPermission(p)
	if obj != "/system/user" {
		t.Fatalf("Menu 应优先使用 MenuPath 作为 object，got %q", obj)
	}
	if act != "menu" || permType != "menu" {
		t.Fatalf("menu 期望 (menu, menu)，got (%q, %q)", act, permType)
	}
}

func TestBuildCasbinTupleFromSystemPermission_MenuFallsBackToCodeAndName(t *testing.T) {
	cases := []struct {
		name     string
		menuPath string
		code     string
		pname    string
		want     string
	}{
		{"MenuPath 空时降级到 Code", "", "user", "用户", "user"},
		{"MenuPath 与 Code 都空时降级到 Name", "", "", "用户", "用户"},
		{"MenuPath 与 Code 都非空时优先 MenuPath", "/system/u", "user", "用户", "/system/u"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newStubSystemPermission(t, func(p *ent.SystemPermission) {
				p.Type = "menu"
				p.MenuPath = tc.menuPath
				p.Code = tc.code
				p.Name = tc.pname
			})
			obj, _, _ := buildCasbinTupleFromSystemPermission(p)
			if obj != tc.want {
				t.Fatalf("want %q, got %q", tc.want, obj)
			}
		})
	}
}

func TestBuildCasbinTupleFromSystemPermission_ButtonUsesCodeFirst(t *testing.T) {
	p := newStubSystemPermission(t, func(p *ent.SystemPermission) {
		p.Type = "button"
		p.Code = "user:create"
		p.Name = "新增用户"
	})
	obj, act, permType := buildCasbinTupleFromSystemPermission(p)
	if obj != "user:create" {
		t.Fatalf("Button 应优先使用 Code 作为 object，got %q", obj)
	}
	if act != "button" || permType != "button" {
		t.Fatalf("button 期望 (button, button)，got (%q, %q)", act, permType)
	}
}

func TestBuildCasbinTupleFromSystemPermission_ButtonFallsBackToName(t *testing.T) {
	p := newStubSystemPermission(t, func(p *ent.SystemPermission) {
		p.Type = "button"
		p.Code = ""
		p.Name = "新增用户"
	})
	obj, _, _ := buildCasbinTupleFromSystemPermission(p)
	if obj != "新增用户" {
		t.Fatalf("Button Code 为空时应降级到 Name，got %q", obj)
	}
}

func TestBuildCasbinTupleFromSystemPermission_APIUsesPathMethod(t *testing.T) {
	p := newStubSystemPermission(t, func(p *ent.SystemPermission) {
		p.Type = "api"
		p.Path = "/api/v1/admin/auth/users"
		p.Method = "POST"
	})
	obj, act, permType := buildCasbinTupleFromSystemPermission(p)
	if obj != "/api/v1/admin/auth/users" || act != "POST" || permType != "api" {
		t.Fatalf("api 期望 (path, method, api)，got (%q, %q, %q)", obj, act, permType)
	}
}

func TestBuildCasbinTupleFromSystemPermission_UnknownTypeReturnsEmpty(t *testing.T) {
	// 未知 Type 应得到空三元组（不能 panic）。Type 是字符串，未知值直接落到 default 分支。
	p := newStubSystemPermission(t, func(p *ent.SystemPermission) {
		p.Type = "future_type"
		p.Path = "x"
	})
	obj, act, permType := buildCasbinTupleFromSystemPermission(p)
	if obj != "" || act != "" || permType != "" {
		t.Fatalf("未知 type 期望空三元组，got (%q, %q, %q)", obj, act, permType)
	}
}

// -------------------------------------------------------------------
// firstNonEmpty 单元测试
// -------------------------------------------------------------------

func TestFirstNonEmpty_AllEmptyReturnsEmpty(t *testing.T) {
	if got := firstNonEmpty("", "", ""); got != "" {
		t.Fatalf("全空应返回空，got %q", got)
	}
}

func TestFirstNonEmpty_NoArgsReturnsEmpty(t *testing.T) {
	if got := firstNonEmpty(); got != "" {
		t.Fatalf("无参数应返回空，got %q", got)
	}
}

func TestFirstNonEmpty_PicksFirstNonEmpty(t *testing.T) {
	cases := []struct {
		name   string
		values []string
		want   string
	}{
		{"首非空", []string{"a", "b", "c"}, "a"},
		{"跳过空字符串", []string{"", "b", "c"}, "b"},
		{"跳过多个空", []string{"", "", "c"}, "c"},
		{"保留空字符串不被视为 set", []string{"x", "", "y"}, "x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstNonEmpty(tc.values...); got != tc.want {
				t.Fatalf("want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestFirstNonEmpty_OnlyFirstEmptyReturnsSecond(t *testing.T) {
	// 变长参数：第二个参数存在且非空时应直接返回。
	if got := firstNonEmpty("", "second"); got != "second" {
		t.Fatalf("want second, got %q", got)
	}
}

// -------------------------------------------------------------------
// 防止 plugin 识别逻辑在重构中引入字符串字面量漂移的回归测试
// -------------------------------------------------------------------

func TestIsPluginManagementSystemPermission_RejectsNonTrivialSubstrings(t *testing.T) {
	// 防止未来把 `code == "plugin"` 改成 `strings.Contains(code, "plugin")` 而引入误判。
	for _, code := range []string{"plugin-admin", "myplugin", "system:pluginadmin", "pluginify"} {
		p := newStubSystemPermission(t, func(p *ent.SystemPermission) { p.Code = code })
		if isPluginManagementSystemPermission(p) {
			t.Fatalf("Code=%q 不应被识别为插件权限（避免 Contains 漂移）", code)
		}
	}
}

// -------------------------------------------------------------------
// 防止 plugin 路径前缀识别在重构中引入 false positive
// -------------------------------------------------------------------

func TestIsPluginManagementSystemPermission_PathPrefixBoundary(t *testing.T) {
	// 当前实现使用 strings.HasPrefix 判定（不带路径段边界），因此
	// /api/v1/admin/auth/plugins-admin 会被识别为插件权限。
	// 此处将"当前行为"显式锁定，以防止后续实现从 HasPrefix 切换到
	// 段级匹配时悄悄改变语义。
	p := newStubSystemPermission(t, func(p *ent.SystemPermission) {
		p.Type = "api"
		p.Path = "/api/v1/admin/auth/plugins-admin"
	})
	if !isPluginManagementSystemPermission(p) {
		t.Fatal("Path='/api/v1/admin/auth/plugins-admin' 当前应被识别为插件（HasPrefix 行为锁定）")
	}

	// 明确非插件情形：完全不包含 plugins 子串
	p2 := newStubSystemPermission(t, func(p *ent.SystemPermission) {
		p.Type = "api"
		p.Path = "/api/v1/admin/auth/roles"
	})
	if isPluginManagementSystemPermission(p2) {
		t.Fatal("Path='/api/v1/admin/auth/roles' 不应误判为插件")
	}
}

// -------------------------------------------------------------------
// 防止 buildCasbinTuple 行为漂移：menu 优先级与 service.systemPermissionObject 一致
// -------------------------------------------------------------------

func TestBuildCasbinTupleFromSystemPermission_MenuPathWhitespacesPreserved(t *testing.T) {
	// 菜单路径若含前后空白会破坏 URL 匹配。说明调用方必须在上游 Trim，
	// 此处仅记录当前行为（不裁剪）以供回归断言。
	p := newStubSystemPermission(t, func(p *ent.SystemPermission) {
		p.Type = "menu"
		p.MenuPath = "  /system/user  "
	})
	obj, _, _ := buildCasbinTupleFromSystemPermission(p)
	if !strings.Contains(obj, "/system/user") {
		t.Fatalf("MenuPath 含空白时仍应返回原值（文档化当前行为），got %q", obj)
	}
}

// -------------------------------------------------------------------
// cleanupNormalAdminSeededExcludedPermissions 直接单元测试
//
// 该函数在 init 流程中负责清理 normal_admin 角色上的"不应被默认种子化"权限
// （目前即插件管理类权限）。其行为必须严格遵循以下不变量：
//   1. 当角色是新建时（roleCreated=true），调用方才会传入；本测试直接覆盖
//      函数本身，验证清理逻辑。
//   2. Description 为空的权限绝不能被误删（无法精确匹配 Casbin V5）。
//   3. Object 为空的权限（无法构造 Casbin 键）应被跳过。
//   4. 仅在 shouldSeedNormalAdminPermission 返回 false 且 Description 非空
//      且 Object 非空时才执行删除。
// -------------------------------------------------------------------

func TestCleanupNormalAdminSeededExcludedPermissions_EmptyPerms(t *testing.T) {
	client := setupInitTest(t)
	ctx := t.Context()
	removed, err := cleanupNormalAdminSeededExcludedPermissions(ctx, client, "normal_admin", "default", nil)
	if err != nil {
		t.Fatalf("清理失败：%v", err)
	}
	if removed != 0 {
		t.Fatalf("空 perms 应返回 0，got %d", removed)
	}
}

func TestCleanupNormalAdminSeededExcludedPermissions_SkipsSeedablePermission(t *testing.T) {
	client := setupInitTest(t)
	ctx := t.Context()

	// 普通 API（非插件、可种子化）应被跳过，不应执行删除。
	regular := createInitSystemPermission(t, client, "api", "List roles", "role:list", 0,
		"/api/v1/admin/auth/roles", "GET", "List roles API")
	createInitCasbinRule(t, client, "normal_admin", "default", regular.Path, regular.Method, "api", regular.Description)

	removed, err := cleanupNormalAdminSeededExcludedPermissions(ctx, client, "normal_admin", "default", []*ent.SystemPermission{regular})
	if err != nil {
		t.Fatalf("清理失败：%v", err)
	}
	if removed != 0 {
		t.Fatalf("可种子化权限应被跳过，removed=%d", removed)
	}
	if !initCasbinRuleExists(t, client, "normal_admin", "default", regular.Path, regular.Method) {
		t.Fatal("可种子化权限的 Casbin 规则不应被清理函数误删")
	}
}

func TestCleanupNormalAdminSeededExcludedPermissions_SkipsEmptyDescription(t *testing.T) {
	client := setupInitTest(t)
	ctx := t.Context()

	// 构造一个"非种子化（插件 API）但 Description 为空"的权限
	// —— 属于初始化中的脏数据兜底分支，必须跳过避免误删。
	pluginAPI := createInitSystemPermission(t, client, "api", "List plugins", "plugin:list", 0,
		"/api/v1/admin/auth/plugins", "GET", "")
	createInitCasbinRule(t, client, "normal_admin", "default", pluginAPI.Path, pluginAPI.Method, "api", "")

	removed, err := cleanupNormalAdminSeededExcludedPermissions(ctx, client, "normal_admin", "default", []*ent.SystemPermission{pluginAPI})
	if err != nil {
		t.Fatalf("清理失败：%v", err)
	}
	if removed != 0 {
		t.Fatalf("Description 为空时不应执行删除，removed=%d", removed)
	}
	if !initCasbinRuleExists(t, client, "normal_admin", "default", pluginAPI.Path, pluginAPI.Method) {
		t.Fatal("Description 为空时不应触发 V5 匹配，避免误删其它规则")
	}
}

func TestCleanupNormalAdminSeededExcludedPermissions_RemovesOnlyMatchingRule(t *testing.T) {
	client := setupInitTest(t)
	ctx := t.Context()

	pluginAPI := createInitSystemPermission(t, client, "api", "List plugins", "plugin:list", 0,
		"/api/v1/admin/auth/plugins", "GET", "List plugins API")
	createInitCasbinRule(t, client, "normal_admin", "default", pluginAPI.Path, pluginAPI.Method, "api", pluginAPI.Description)

	// 一条 description 不匹配目标 seed 的"手动授权"规则，模拟运营后续手动加塞的权限
	createInitCasbinRule(t, client, "normal_admin", "default", pluginAPI.Path, pluginAPI.Method, "api", "manual grant by operator")

	// 其它角色上的同名规则，描述相同 —— 不应被误删
	createInitCasbinRule(t, client, "other_role", "default", pluginAPI.Path, pluginAPI.Method, "api", pluginAPI.Description)

	removed, err := cleanupNormalAdminSeededExcludedPermissions(ctx, client, "normal_admin", "default", []*ent.SystemPermission{pluginAPI})
	if err != nil {
		t.Fatalf("清理失败：%v", err)
	}
	if removed != 1 {
		t.Fatalf("应仅删除 1 条规则（精确匹配 V5 描述），got %d", removed)
	}
	if !initCasbinRuleExists(t, client, "normal_admin", "default", pluginAPI.Path, pluginAPI.Method) {
		t.Fatal("normal_admin 上仍应保留 description 不一致的规则")
	}
	if !initCasbinRuleExists(t, client, "other_role", "default", pluginAPI.Path, pluginAPI.Method) {
		t.Fatal("other_role 上的同名规则不应被误删")
	}
}

func TestCleanupNormalAdminSeededExcludedPermissions_DomainFilterRespected(t *testing.T) {
	client := setupInitTest(t)
	ctx := t.Context()

	pluginAPI := createInitSystemPermission(t, client, "api", "List plugins", "plugin:list", 0,
		"/api/v1/admin/auth/plugins", "GET", "List plugins API")
	// 同名规则放在另一个 domain 下，清理时不应跨域删除
	createInitCasbinRule(t, client, "normal_admin", "tenant-a", pluginAPI.Path, pluginAPI.Method, "api", pluginAPI.Description)

	removed, err := cleanupNormalAdminSeededExcludedPermissions(ctx, client, "normal_admin", "default", []*ent.SystemPermission{pluginAPI})
	if err != nil {
		t.Fatalf("清理失败：%v", err)
	}
	if removed != 0 {
		t.Fatalf("目标 domain 下没有规则时应返回 0，got %d", removed)
	}
	if !initCasbinRuleExists(t, client, "normal_admin", "tenant-a", pluginAPI.Path, pluginAPI.Method) {
		t.Fatal("其它 domain 的同名规则不应被误删")
	}
}
