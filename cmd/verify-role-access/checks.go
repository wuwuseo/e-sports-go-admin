package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscustomer"
)

// 优先用作「应放行」样本的只读接口：都是列表型 GET，不带副作用。
var preferredAllowPaths = []string{
	"/api/v1/esports/admin/customers",
	"/api/v1/esports/admin/orders",
	"/api/v1/esports/admin/players",
	"/api/v1/esports/admin/settlements",
	"/api/v1/esports/admin/wallets",
	"/api/v1/esports/admin/pages",
	"/api/v1/esports/admin/scripts",
	"/api/v1/esports/admin/assessments",
}

// checkMenu 断言系统与功能集菜单均与 Casbin 授权及父级闭包一致。
func (r *runner) checkMenu(acc *account) check {
	const name = "菜单路由"
	expected := r.expectedMenus(acc.Role.Name)
	actual, err := acc.cli.menuPaths()
	if err != nil {
		return check{Name: name, Detail: err.Error()}
	}

	var missing, extra []string
	for _, path := range sortedKeys(expected) {
		if _, ok := actual[path]; !ok {
			missing = append(missing, fmt.Sprintf("%s(%s)", path, expected[path]))
		}
	}
	for _, path := range sortedKeys(actual) {
		if _, ok := expected[path]; !ok {
			extra = append(extra, fmt.Sprintf("%s(%s)", path, actual[path]))
		}
	}

	detail := fmt.Sprintf("期望 %d / 实际 %d", len(expected), len(actual))
	if len(actual) > 0 {
		detail += "；菜单: " + strings.Join(sortedKeys(actual), ", ")
	} else {
		detail += "；未配置菜单授权"
	}
	if len(missing) > 0 {
		detail += "；缺菜单: " + strings.Join(missing, ", ")
	}
	if len(extra) > 0 {
		detail += "；越权菜单: " + strings.Join(extra, ", ")
	}
	return check{Name: name, Passed: len(missing) == 0 && len(extra) == 0, Detail: detail}
}

// checkAllowPermission 从角色已授权的 GET 接口里抽一条，验证真的被放行。
func (r *runner) checkAllowPermission(acc *account) check {
	const name = "API 放行"
	def, ok := r.pickAllowSample(acc.Role.Name)
	if !ok {
		return check{Name: name, Skipped: true, Detail: "该角色没有可抽样的 GET 接口授权"}
	}
	code, status, _, err := acc.cli.call(http.MethodGet, def.Path, nil)
	if err != nil {
		return check{Name: name, Detail: fmt.Sprintf("%s %s 请求失败: %v", def.Method, def.Path, err)}
	}
	// 必须同时取得成功业务码和 HTTP 状态，避免把 404/500 当成验证通过。
	passed := successfulResponse(code, status)
	detail := fmt.Sprintf("%s %s -> code=%d http=%d (%s)", def.Method, def.Path, code, status, def.Code)
	return check{Name: name, Passed: passed, Detail: detail}
}

// checkDenyPermission 从角色未授权的 GET 接口里抽一条，验证被 403 拒绝（越权检测）。
func (r *runner) checkDenyPermission(acc *account) check {
	const name = "API 拒绝"
	def, ok := r.pickDenySample(acc.Role.Name)
	if !ok {
		return check{Name: name, Skipped: true, Detail: "该角色已授权全部 GET 接口，无拒绝样本"}
	}
	code, status, _, err := acc.cli.call(http.MethodGet, def.Path, nil)
	if err != nil {
		return check{Name: name, Detail: fmt.Sprintf("%s %s 请求失败: %v", def.Method, def.Path, err)}
	}
	passed := code == http.StatusForbidden && status >= 200 && status < 500
	detail := fmt.Sprintf("%s %s -> code=%d http=%d（期望 403）", def.Method, def.Path, code, status)
	if !passed && (code == 1 || code == 2) {
		detail += "；该接口被越权放行"
	}
	return check{Name: name, Passed: passed, Detail: detail}
}

func (r *runner) pickAllowSample(roleName string) (permissionDef, bool) {
	// 优先抽取代表菜单实际使用的列表接口。
	preferred := map[string]string{
		"normal_admin":           "/api/v1/admin/auth/admins",
		"finance_admin":          "/api/v1/esports/admin/settlements",
		"examiner_admin":         "/api/v1/esports/admin/assessments",
		"operation_editor_admin": "/api/v1/esports/admin/pages",
		"cs_readonly":            "/api/v1/esports/admin/pages",
		"finance_readonly":       "/api/v1/esports/admin/pages",
		"test":                   "/api/v1/admin/auth/cron-jobs",
	}
	for _, def := range r.perms {
		if def.Type == "api" && def.Method == http.MethodGet && def.Path == preferred[roleName] && r.hasPolicy(roleName, def.Path, def.Method) {
			return def, true
		}
	}
	var candidates []permissionDef
	seen := make(map[string]struct{})
	for _, def := range r.perms {
		if def.Type != "api" || def.Method != http.MethodGet || def.Path == "" {
			continue
		}
		if strings.Contains(def.Path, "{") || !strings.HasPrefix(def.Path, "/api/") {
			continue
		}
		if !r.hasPolicy(roleName, def.Path, def.Method) {
			continue
		}
		if _, dup := seen[def.Path]; dup {
			continue
		}
		seen[def.Path] = struct{}{}
		candidates = append(candidates, def)
	}
	if len(candidates) == 0 {
		return permissionDef{}, false
	}
	sort.Slice(candidates, func(i, j int) bool {
		ri, rj := allowRank(candidates[i].Path), allowRank(candidates[j].Path)
		if ri != rj {
			return ri < rj
		}
		return candidates[i].Path < candidates[j].Path
	})
	return candidates[0], true
}

func allowRank(path string) int {
	for i, p := range preferredAllowPaths {
		if p == path {
			return i
		}
	}
	if strings.HasPrefix(path, "/api/v1/esports/") {
		return len(preferredAllowPaths)
	}
	return len(preferredAllowPaths) + 1
}

func (r *runner) pickDenySample(roleName string) (permissionDef, bool) {
	var candidates []permissionDef
	seen := make(map[string]struct{})
	for _, def := range r.perms {
		if def.Type != "api" || def.Method != http.MethodGet || def.Path == "" {
			continue
		}
		if strings.Contains(def.Path, "{") || !strings.HasPrefix(def.Path, "/api/v1/esports/") {
			continue
		}
		if r.hasPolicy(roleName, def.Path, def.Method) {
			continue
		}
		if _, dup := seen[def.Path]; dup {
			continue
		}
		seen[def.Path] = struct{}{}
		candidates = append(candidates, def)
	}
	if len(candidates) == 0 {
		return permissionDef{}, false
	}
	// 优先挑「敏感度更高」的财务 / 结算类接口作为拒绝样本。
	sort.Slice(candidates, func(i, j int) bool {
		ri, rj := denyRank(candidates[i].Path), denyRank(candidates[j].Path)
		if ri != rj {
			return ri < rj
		}
		return candidates[i].Path < candidates[j].Path
	})
	return candidates[0], true
}

func denyRank(path string) int {
	switch {
	case strings.Contains(path, "/settlements"):
		return 0
	case strings.Contains(path, "/wallets"):
		return 1
	case strings.Contains(path, "/customers"):
		return 2
	case strings.Contains(path, "/orders"):
		return 3
	default:
		return 10
	}
}

// checkAdminListScope 验证管理员列表的数据域收敛：非超管只能看到 data_scope 允许的账号。
func (r *runner) checkAdminListScope(acc *account) check {
	const name = "数据域(管理员列表)"
	hasList := r.hasPermission(acc.Role.Name, "admin:list")

	path := "/api/v1/admin/auth/admins?page=1&page_size=100"
	items, total, code, err := acc.cli.list(path)
	if err != nil {
		return check{Name: name, Detail: err.Error()}
	}
	if code == http.StatusForbidden {
		if hasList {
			return check{Name: name, Detail: "角色已授权 admin:list，但请求被 403 拒绝"}
		}
		return check{Name: name, Passed: true, Detail: "无 admin:list 权限，403 拒绝符合最小权限"}
	}
	if code != 1 && code != 2 {
		return check{Name: name, Detail: fmt.Sprintf("请求失败 code=%d", code)}
	}

	rows := decodeAdmins(items)
	switch {
	case acc.IsSuper:
		passed := total >= 2
		return check{Name: name, Passed: passed, Detail: fmt.Sprintf("超管应看到全部账号，实际 total=%d", total)}
	case acc.Role.DataScope == "all":
		passed := total >= 2
		return check{Name: name, Passed: passed, Detail: fmt.Sprintf("data_scope=all，实际 total=%d", total)}
	case acc.Role.DataScope == "self":
		okSelf := total == 1 && len(rows) == 1 && rows[0].UID == acc.UID
		detail := fmt.Sprintf("data_scope=self，实际 total=%d", total)
		if !okSelf {
			detail += fmt.Sprintf("；返回 %s", adminUIDs(rows))
		}
		return check{Name: name, Passed: okSelf, Detail: detail}
	default:
		// department / department_and_child / custom 需按部门树展开，本工具不做断言，只报告。
		return check{Name: name, Skipped: true, Detail: fmt.Sprintf("data_scope=%s，total=%d（不做断言）", acc.Role.DataScope, total)}
	}
}

func decodeAdmins(items []json.RawMessage) []adminRow {
	rows := make([]adminRow, 0, len(items))
	for _, item := range items {
		var row adminRow
		if err := json.Unmarshal(item, &row); err == nil {
			rows = append(rows, row)
		}
	}
	return rows
}

func adminUIDs(rows []adminRow) string {
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		parts = append(parts, fmt.Sprintf("%s(uid=%s)", row.Username, row.UID))
	}
	return strings.Join(parts, ", ")
}

// checkCustomerIsolation 验证同角色两个账号之间的客户档案归属隔离。
// 只在角色同时具备「建客户 / 查客户列表 / 查客户档案」三项权限时执行。
func (r *runner) checkCustomerIsolation(ctx context.Context, rl roleInfo, accounts []*account) (result *check) {
	const name = "数据权限(客户归属隔离)"
	required := []string{"esports:customers:create", "esports:customers:list", "esports:customers:profile"}
	for _, code := range required {
		if !r.hasPermission(rl.Name, code) {
			return nil
		}
	}
	if len(accounts) < 2 {
		return &check{Name: name, Skipped: true, Detail: "账号数不足 2 个，无法验证隔离"}
	}

	a, b := accounts[0], accounts[1]
	if a.cli.token == "" || b.cli.token == "" {
		return &check{Name: name, Detail: "两个账号必须均成功登录，才能验证隔离"}
	}
	nameA := fmt.Sprintf("验证客户-%s-A", rl.Name)
	nameB := fmt.Sprintf("验证客户-%s-B", rl.Name)
	var createdIDs []int
	defer func() {
		if !r.cfg.keepData && len(createdIDs) > 0 {
			if _, err := r.db.EsportsCustomer.Delete().Where(esportscustomer.IDIn(createdIDs...)).Exec(ctx); err != nil {
				result.Passed = false
				result.Detail += "；清理本次测试客户失败: " + err.Error()
			}
		}
	}()

	idA, createdA, err := r.ensureCustomer(ctx, a, nameA)
	if err != nil {
		return &check{Name: name, Detail: "A 建客户失败: " + err.Error()}
	}
	if createdA {
		createdIDs = append(createdIDs, idA)
	}
	idB, createdB, err := r.ensureCustomer(ctx, b, nameB)
	if err != nil {
		return &check{Name: name, Detail: "B 建客户失败: " + err.Error()}
	}
	if createdB {
		createdIDs = append(createdIDs, idB)
	}
	passedA, detailA := checkCustomerVisibility(a, idA, idB)
	passedB, detailB := checkCustomerVisibility(b, idB, idA)
	return &check{Name: name, Passed: passedA && passedB, Detail: "A: " + detailA + "；B: " + detailB}
}

func checkCustomerVisibility(acc *account, ownID, otherID int) (bool, string) {
	items, total, code, err := acc.cli.list("/api/v1/esports/admin/customers?page=1&page_size=100")
	if err != nil {
		return false, "拉客户列表失败: " + err.Error()
	}
	if code != 1 && code != 2 {
		return false, fmt.Sprintf("拉客户列表失败 code=%d", code)
	}
	rows := decodeCustomers(items)
	ownA, sawB := false, false
	crossOwner := ""
	for _, row := range rows {
		if row.ID == ownID {
			ownA = true
		}
		if row.ID == otherID {
			sawB = true
		}
		if (row.OwnerAdminID == nil || *row.OwnerAdminID != acc.AdminID) && crossOwner == "" {
			crossOwner = fmt.Sprintf("客户 id=%d 归属非本人", row.ID)
		}
	}

	// A 读取 B 的客户档案：归属隔离应在业务层拦截（403）。
	profilePath := fmt.Sprintf("/api/v1/esports/admin/customers/%d/profile", otherID)
	readCode, _, _, readErr := acc.cli.call(http.MethodGet, profilePath, nil)
	if readErr != nil {
		return false, "读取他人客户档案失败: " + readErr.Error()
	}
	ownCode, ownStatus, _, ownErr := acc.cli.call(http.MethodGet, fmt.Sprintf("/api/v1/esports/admin/customers/%d/profile", ownID), nil)

	passed := ownA && !sawB && crossOwner == "" && readCode == http.StatusForbidden && ownErr == nil && successfulResponse(ownCode, ownStatus)
	detail := fmt.Sprintf("列表 total=%d（含自己的 %v / 看到他人的 %v）；读他人档案 code=%d（期望 403）",
		total, ownA, sawB, readCode)
	if crossOwner != "" {
		detail += "；列表混入他人客户: " + crossOwner
	}
	return passed, detail
}

func (r *runner) ensureCustomer(ctx context.Context, acc *account, name string) (int, bool, error) {
	row, err := r.db.EsportsCustomer.Query().Where(esportscustomer.OwnerAdminIDEQ(acc.AdminID), esportscustomer.DisplayNameEQ(name)).First(ctx)
	if err == nil {
		return row.ID, false, nil
	}
	if !ent.IsNotFound(err) {
		return 0, false, err
	}
	id, err := createCustomerViaAPI(acc.cli, name)
	return id, err == nil, err
}

func createCustomerViaAPI(cli *apiClient, displayName string) (int, error) {
	code, _, raw, err := cli.call(http.MethodPost, "/api/v1/esports/admin/customers", map[string]string{
		"display_name": displayName,
	})
	if err != nil {
		return 0, err
	}
	if code != 1 && code != 2 {
		return 0, fmt.Errorf("code=%d body=%s", code, truncate(string(raw), 200))
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return 0, err
	}
	var row customerRow
	if err := json.Unmarshal(env.Data, &row); err != nil {
		return 0, err
	}
	if row.ID == 0 {
		return 0, fmt.Errorf("响应里没有客户 id")
	}
	return row.ID, nil
}

func decodeCustomers(items []json.RawMessage) []customerRow {
	rows := make([]customerRow, 0, len(items))
	for _, item := range items {
		var row customerRow
		if err := json.Unmarshal(item, &row); err == nil {
			rows = append(rows, row)
		}
	}
	return rows
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
