package internal

import (
	"reflect"
	"sort"
	"testing"

	"go-admin/internal/casbin/adapter"
	"go-admin/internal/ent"
	"go-admin/internal/ent/casbinrule"
	"go-admin/internal/ent/role"
	"go-admin/internal/global"

	cmfapp "github.com/wuwuseo/cmf/app"
	cmfCasbin "github.com/wuwuseo/cmf/casbin"
	cmfconfig "github.com/wuwuseo/cmf/config"
)

// 角色播种回归（§2.1-01 / §16.5-03）：
//   - 幂等播种客服 / 财务 / 考官 / 运营编辑四类最小权限角色，白名单精确到权限 code；
//   - 排单与报单权限各自独立成项；
//   - 客服 / 财务另播种只读角色，只持有页面装修读权限，写接口在策略层被拒（403 同源）。

// esportsMinimalRoleTestModel 是权限判定的最小 casbin 模型，与运行时一致
// （middleware.PermissionMiddleware 走同一套 keyMatch2 匹配）。
const esportsMinimalRoleTestModel = `
[request_definition]
r = sub, dom, obj, act

[policy_definition]
p = sub, dom, obj, act

[role_definition]
g = _, _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = g(r.sub, p.sub, r.dom) && r.dom == p.dom && (p.obj == "*" || keyMatch2(r.obj, p.obj)) && (p.act == "*" || r.act == p.act)
`

// seedEsportsPermissionRows 按功能集声明在 DB 中建立 esports 系统权限行，
// 返回 code → 权限行，供角色播种与断言共用（等价于 apps.Provision 的权限播种）。
func seedEsportsPermissionRows(t *testing.T, client *ent.Client) map[string]*ent.SystemPermission {
	t.Helper()

	var app cmfapp.App
	found := false
	for _, candidate := range cmfapp.Registered() {
		if candidate.Code == "esports" {
			app = candidate
			found = true
			break
		}
	}
	if !found {
		t.Fatal("esports 功能集未注册")
	}

	byCode := make(map[string]*ent.SystemPermission, len(app.Permissions))
	for _, p := range app.Permissions {
		row, err := client.SystemPermission.Create().
			SetDomain("default").
			SetType(string(p.Type)).
			SetName(p.Name).
			SetCode(p.Code).
			SetPath(p.Path).
			SetMethod(p.Method).
			SetMenuPath(p.MenuPath).
			SetStatus(1).
			SetDescription(p.Code).
			Save(t.Context())
		if err != nil {
			t.Fatalf("建立权限行 %s 失败: %v", p.Code, err)
		}
		byCode[p.Code] = row
	}
	return byCode
}

// casbinRuleKeyFromPermission 与 init.go 的规则构造保持一致。
func casbinRuleKeyFromPermission(p *ent.SystemPermission) string {
	switch p.Type {
	case "menu":
		return p.MenuPath + "|menu"
	case "button":
		return p.Code + "|button"
	default:
		return p.Path + "|" + p.Method
	}
}

// grantedPermissionCodes 返回角色已授予的权限 code 集合。
func grantedPermissionCodes(t *testing.T, client *ent.Client, roleName string, byCode map[string]*ent.SystemPermission) map[string]bool {
	t.Helper()

	reverse := map[string]string{}
	for code, p := range byCode {
		reverse[casbinRuleKeyFromPermission(p)] = code
	}

	rows, err := client.CasbinRule.Query().Where(
		casbinrule.PtypeEQ("p"),
		casbinrule.V0EQ(roleName),
	).All(t.Context())
	if err != nil {
		t.Fatalf("查询 %s 的 casbin 规则失败: %v", roleName, err)
	}

	granted := make(map[string]bool, len(rows))
	for _, row := range rows {
		code, ok := reverse[row.V2+"|"+row.V3]
		if !ok {
			granted["<未知规则:"+row.V2+"|"+row.V3+">"] = true
			continue
		}
		granted[code] = true
	}
	return granted
}

func assertGranted(t *testing.T, roleName string, granted map[string]bool, codes ...string) {
	t.Helper()
	for _, code := range codes {
		if !granted[code] {
			t.Errorf("角色 %s 应持有权限 %s", roleName, code)
		}
	}
}

func assertNotGranted(t *testing.T, roleName string, granted map[string]bool, codes ...string) {
	t.Helper()
	for _, code := range codes {
		if granted[code] {
			t.Errorf("角色 %s 不得持有权限 %s", roleName, code)
		}
	}
}

func sortedCodes(granted map[string]bool) []string {
	out := make([]string, 0, len(granted))
	for code := range granted {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}

func TestInitMinimalRolesSeedsFourLeastPrivilegeRoles(t *testing.T) {
	client := setupInitTest(t)
	byCode := seedEsportsPermissionRows(t, client)

	if err := initMinimalRoles(t.Context(), client, "default"); err != nil {
		t.Fatalf("initMinimalRoles() error = %v", err)
	}

	for roleName, displayName := range map[string]string{
		"cs_admin":               "客服",
		"finance_admin":          "财务",
		"examiner_admin":         "考官",
		"operation_editor_admin": "运营编辑",
	} {
		row, err := client.Role.Query().Where(role.NameEQ(roleName)).Only(t.Context())
		if err != nil {
			t.Fatalf("角色 %s 未播种: %v", roleName, err)
		}
		if row.DisplayName != displayName || !row.IsSystem || row.Status != role.StatusActive {
			t.Fatalf("角色 %s 属性不符: %+v", roleName, row)
		}
	}

	granted := map[string]map[string]bool{}
	for _, roleName := range []string{"cs_admin", "finance_admin", "examiner_admin", "operation_editor_admin"} {
		granted[roleName] = grantedPermissionCodes(t, client, roleName, byCode)
	}

	// 排单权限独立成项：报单菜单与排单菜单是两个不同权限项，报单角色默认不含排单。
	assertGranted(t, "cs_admin", granted["cs_admin"],
		"esports:report:menu", "esports:dispatch:menu",
		"esports:orders:create", "esports:dispatch:board")
	if casbinRuleKeyFromPermission(byCode["esports:report:menu"]) == casbinRuleKeyFromPermission(byCode["esports:dispatch:menu"]) {
		t.Fatal("报单与排单必须是两个独立的权限项")
	}

	// 客服：不得持有改价审批、退款审批、结算锁定/发放、收款账户增删审、发布审核。
	assertNotGranted(t, "cs_admin", granted["cs_admin"],
		"esports:orders:reprice", "esports:orders:price_request", "esports:orders:price_decision",
		"esports:aftersales:approve", "esports:aftersales:execute", "esports:aftersales:refund_execute",
		"esports:settlements:lock", "esports:settlements:pay",
		"esports:payments:account_create", "esports:payments:account_review", "esports:payments:account_disable",
		"esports:pages:publish_approve", "esports:pages:publish_reject",
		"esports:wallets:adjust", "esports:wallets:recharge")
	assertGranted(t, "cs_admin", granted["cs_admin"],
		"esports:orders:close", "esports:aftersales:plan", "esports:cs:workbench", "esports:dispatch:assign",
		// 代客收款：客服需要替客户走余额/储值卡核销并提交转账凭证（§4.3）。
		// 「收款执行」不是「资金审批」——核销凭证、退款审批、结算锁定/发放、账户审核仍全部归财务，
		// 因此 pay_balance / stored_value:pay_order / payments:proof_submit 属于客服白名单，
		// 而 wallets:adjust（直接调整余额）与 wallets:recharge（直接充值）依然禁止。
		"esports:orders:pay_balance", "esports:stored_value:pay_order", "esports:payments:proof_submit")

	// 财务：可持有资金查看 / 提现审核；同样不得持有改价审批与收款账户增删审。
	assertGranted(t, "finance_admin", granted["finance_admin"],
		"esports:wallets:list", "esports:wallets:transactions",
		"esports:settlements:withdrawals", "esports:settlements:withdrawal_review",
		"esports:stats:finance")
	assertNotGranted(t, "finance_admin", granted["finance_admin"],
		"esports:orders:reprice", "esports:orders:price_decision",
		"esports:aftersales:approve", "esports:aftersales:execute",
		"esports:settlements:lock", "esports:settlements:pay",
		"esports:payments:account_create", "esports:payments:account_review", "esports:payments:account_disable")

	// 考官：可持有考核分配 / 打分 / 申诉复核。
	assertGranted(t, "examiner_admin", granted["examiner_admin"],
		"esports:assessments:assign", "esports:assessments:score", "esports:assessments:appeal_review")
	assertNotGranted(t, "examiner_admin", granted["examiner_admin"],
		"esports:assessments:rules_set", "esports:orders:reprice", "esports:settlements:pay")

	// 运营编辑：可持有装修草稿与发布申请，不得持有发布审核。
	assertGranted(t, "operation_editor_admin", granted["operation_editor_admin"],
		"esports:pages:draft_save", "esports:pages:publish", "esports:pages:list")
	assertNotGranted(t, "operation_editor_admin", granted["operation_editor_admin"],
		"esports:pages:publish_approve", "esports:pages:publish_reject",
		"esports:orders:reprice", "esports:settlements:pay")
}

func TestInitMinimalRolesSeedsReadonlyRolesForCsAndFinance(t *testing.T) {
	client := setupInitTest(t)
	byCode := seedEsportsPermissionRows(t, client)

	if err := initMinimalRoles(t.Context(), client, "default"); err != nil {
		t.Fatalf("initMinimalRoles() error = %v", err)
	}

	for _, roleName := range []string{"cs_readonly", "finance_readonly"} {
		if _, err := client.Role.Query().Where(role.NameEQ(roleName)).Only(t.Context()); err != nil {
			t.Fatalf("只读角色 %s 未播种: %v", roleName, err)
		}
		granted := grantedPermissionCodes(t, client, roleName, byCode)

		// 只读角色只持有装修读权限（+ 菜单导航项），不得持有任何写权限。
		assertGranted(t, roleName, granted, "esports:pages:list", "esports:pages:detail", "esports:pages:versions")
		assertNotGranted(t, roleName, granted,
			"esports:pages:draft_save", "esports:pages:publish", "esports:pages:rollback",
			"esports:pages:publish_approve", "esports:pages:publish_reject",
			"esports:pages:asset_create", "esports:pages:asset_delete")

		for code := range granted {
			switch code {
			case "esports:pages:list", "esports:pages:detail", "esports:pages:versions",
				"esports:page:menu", "esports:operations:menu":
			default:
				t.Errorf("只读角色 %s 持有白名单之外的权限 %s", roleName, code)
			}
		}
	}
}

// TestReadonlyRoleDeniedPagesWriteByPolicy 用运行时同一套 casbin 模型证明
// 只读角色调用装修写接口被拒（PermissionMiddleware 判定即 enforcer.Enforce）。
func TestReadonlyRoleDeniedPagesWriteByPolicy(t *testing.T) {
	client := setupInitTest(t)
	seedEsportsPermissionRows(t, client)
	if err := initMinimalRoles(t.Context(), client, "default"); err != nil {
		t.Fatalf("initMinimalRoles() error = %v", err)
	}

	prevManager := global.EnforcerManager
	cfg := &cmfconfig.Config{}
	cfg.Casbin.DomainsDefault = "default"
	cfg.Casbin.Domains = []struct {
		Name      string `mapstructure:"name"`
		AutoLoad  bool   `mapstructure:"auto_load"`
		ModelPath string `mapstructure:"model_path"`
		ModelText string `mapstructure:"model_text"`
	}{{Name: "default", AutoLoad: true, ModelText: esportsMinimalRoleTestModel}}
	adp, err := adapter.NewAdapterWithClient(client)
	if err != nil {
		t.Fatalf("建立 casbin 适配器失败: %v", err)
	}
	global.EnforcerManager = cmfCasbin.InitEnforcerManager(adp, cfg)
	t.Cleanup(func() { global.EnforcerManager = prevManager })

	enforcer, err := global.EnforcerManager.GetEnforcer("default")
	if err != nil {
		t.Fatalf("获取 enforcer 失败: %v", err)
	}
	if err := enforcer.LoadPolicy(); err != nil {
		t.Fatalf("加载策略失败: %v", err)
	}

	const uid = "uid-readonly-cs"
	if _, err := enforcer.AddRoleForUserInDomain(uid, "cs_readonly", "default"); err != nil {
		t.Fatalf("绑定只读角色失败: %v", err)
	}

	// 判定对象与 middleware.PermissionMiddleware 一致：使用路由模板（{param} 形式），
	// 而不是请求的具体路径。
	for _, tt := range []struct {
		method string
		object string
		want   bool
	}{
		{"GET", "/api/v1/esports/admin/pages", true},
		{"GET", "/api/v1/esports/admin/pages/{channel}", true},
		{"GET", "/api/v1/esports/admin/pages/{channel}/versions", true},
		{"PUT", "/api/v1/esports/admin/pages/{channel}/draft", false},
		{"POST", "/api/v1/esports/admin/pages/{channel}/publish", false},
		{"POST", "/api/v1/esports/admin/pages/{channel}/rollback", false},
		{"POST", "/api/v1/esports/admin/pages/{channel}/publish-review/{versionId}/approve", false},
		{"POST", "/api/v1/esports/admin/page-assets", false},
		{"DELETE", "/api/v1/esports/admin/page-assets/{id}", false},
	} {
		allowed, err := enforcer.Enforce(uid, "default", tt.object, tt.method)
		if err != nil {
			t.Fatalf("enforce %s %s 失败: %v", tt.method, tt.object, err)
		}
		if allowed != tt.want {
			t.Errorf("只读角色 %s %s 允许=%v，期望 %v", tt.method, tt.object, allowed, tt.want)
		}
	}
}

func TestInitMinimalRolesIsIdempotentAndPreservesManualGrants(t *testing.T) {
	client := setupInitTest(t)
	byCode := seedEsportsPermissionRows(t, client)

	// 预先存在的角色视为运维手工维护过的角色，播种不得增删其授权。
	existing, err := client.Role.Create().
		SetName("cs_admin").
		SetDisplayName("客服").
		SetIsSystem(true).
		SetStatus(role.StatusActive).
		Save(t.Context())
	if err != nil {
		t.Fatalf("预建 cs_admin 失败: %v", err)
	}
	manual := byCode["esports:settlements:pay"]
	if _, err := client.CasbinRule.Create().
		SetPtype("p").SetV0("cs_admin").SetV1("default").
		SetV2(manual.Path).SetV3(manual.Method).SetV4("api").SetV5(manual.Description).
		Save(t.Context()); err != nil {
		t.Fatalf("写入手工授权失败: %v", err)
	}

	countRules := func() int {
		n, err := client.CasbinRule.Query().
			Where(casbinrule.PtypeEQ("p"), casbinrule.V0EQ("cs_admin")).Count(t.Context())
		if err != nil {
			t.Fatalf("统计 casbin 规则失败: %v", err)
		}
		return n
	}

	if err := initMinimalRoles(t.Context(), client, "default"); err != nil {
		t.Fatalf("首次播种失败: %v", err)
	}
	roleCountAfterFirst, err := client.Role.Query().Count(t.Context())
	if err != nil {
		t.Fatalf("统计角色失败: %v", err)
	}
	rulesAfterFirst := countRules()
	if rulesAfterFirst != 1 {
		t.Fatalf("已存在角色的授权被改动：规则数 %d，期望保持 1", rulesAfterFirst)
	}
	if existing.ID == 0 {
		t.Fatal("预建角色丢失")
	}

	if err := initMinimalRoles(t.Context(), client, "default"); err != nil {
		t.Fatalf("二次播种失败: %v", err)
	}
	if got := countRules(); got != rulesAfterFirst {
		t.Fatalf("重复播种产生了重复规则：%d → %d", rulesAfterFirst, got)
	}
	roleCountAfterSecond, err := client.Role.Query().Count(t.Context())
	if err != nil {
		t.Fatalf("统计角色失败: %v", err)
	}
	if roleCountAfterSecond != roleCountAfterFirst {
		t.Fatalf("重复播种产生了重复角色：%d → %d", roleCountAfterFirst, roleCountAfterSecond)
	}

	// 新播种角色在第二次播种后仍不得出现重复规则。
	first, err := client.CasbinRule.Query().Where(casbinrule.PtypeEQ("p"), casbinrule.V0EQ("finance_readonly")).All(t.Context())
	if err != nil {
		t.Fatalf("查询 finance_readonly 规则失败: %v", err)
	}
	seen := map[string]int{}
	for _, row := range first {
		seen[row.V2+"|"+row.V3]++
	}
	for key, n := range seen {
		if n != 1 {
			t.Fatalf("finance_readonly 出现重复规则 %s × %d", key, n)
		}
	}
	if len(first) == 0 {
		t.Fatal("finance_readonly 未获得任何授权")
	}
}

// TestMinimalRoleWhitelistsAreExplicitAndNonEmpty 保证白名单是显式 code 清单，
// 不会退化成「全部非敏感权限」式的通配授权。
func TestMinimalRoleWhitelistsAreExplicitAndNonEmpty(t *testing.T) {
	for _, spec := range minimalRoleSpecs() {
		if len(spec.permissionCodes) == 0 {
			t.Fatalf("角色 %s 的白名单为空", spec.name)
		}
		for _, code := range spec.permissionCodes {
			if code == "*" || code == "" || !hasPrefix(code, "esports:") {
				t.Fatalf("角色 %s 的白名单包含非显式 esports code：%q", spec.name, code)
			}
		}
	}
	names := make([]string, 0, len(minimalRoleSpecs()))
	for _, spec := range minimalRoleSpecs() {
		names = append(names, spec.name)
	}
	sort.Strings(names)
	want := []string{"cs_admin", "cs_readonly", "examiner_admin", "finance_admin", "finance_readonly", "operation_editor_admin"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("最小权限角色清单 = %v，期望 %v", names, want)
	}
}

func hasPrefix(value, prefix string) bool {
	return len(value) >= len(prefix) && value[:len(prefix)] == prefix
}

// TestMinimalRoleWhitelistCodesExistInFeatureSet 白名单里的每个 code 都必须真实存在，
// 否则播种会静默漏授权（宁少勿多可以接受少授权，但不能容忍写错的 code）。
func TestMinimalRoleWhitelistCodesExistInFeatureSet(t *testing.T) {
	declared := map[string]bool{}
	for _, app := range cmfapp.Registered() {
		if app.Code != "esports" {
			continue
		}
		for _, p := range app.Permissions {
			declared[p.Code] = true
		}
	}
	if len(declared) == 0 {
		t.Fatal("esports 功能集未注册")
	}
	for _, spec := range minimalRoleSpecs() {
		for _, code := range spec.permissionCodes {
			if !declared[code] {
				t.Errorf("角色 %s 的白名单引用了不存在的权限 code：%s", spec.name, code)
			}
		}
	}
}

// TestMinimalRoleSeedingSkipsCleanlyWhenFeatureDisabled 功能集未启用（无权限行）时，
// 播种只建角色不建授权，不报错也不伪造授权。
func TestMinimalRoleSeedingSkipsCleanlyWhenFeatureDisabled(t *testing.T) {
	client := setupInitTest(t)

	if err := initMinimalRoles(t.Context(), client, "default"); err != nil {
		t.Fatalf("无权限行时播种应降级而非失败: %v", err)
	}
	if n, err := client.Role.Query().Where(role.NameEQ("cs_admin")).Count(t.Context()); err != nil || n != 1 {
		t.Fatalf("角色未创建: n=%d err=%v", n, err)
	}
	if n, err := client.CasbinRule.Query().Where(casbinrule.PtypeEQ("p")).Count(t.Context()); err != nil || n != 0 {
		t.Fatalf("无权限行时不应产生授权: n=%d err=%v", n, err)
	}
}
