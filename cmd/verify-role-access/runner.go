package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wuwuseo/cmf/crypto"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/adminrole"
	"go-admin/internal/ent/casbinrule"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/systempermission"
)

// verifyUIDBase 是验证账号专用的 UID 起始段。
// 不占用后端 utils.GenerateUID 的自增区间：那个计数器只在进程内自增并持久化到
// data/.uid_counter，本工具是独立进程，读到的是过期值，直接用必然和运行中后端
// 已分配的 UID 撞唯一索引。
const verifyUIDBase = 900000000

// builtinRoleOrder 是验证顺序，也是报告顺序。与 internal/init.go 的播种清单一一对应。
var builtinRoleOrder = []string{
	"admin",
	"normal_admin",
	"cs_admin",
	"finance_admin",
	"examiner_admin",
	"operation_editor_admin",
	"cs_readonly",
	"finance_readonly",
}

type runner struct {
	cfg    *options
	db     *ent.Client
	domain string

	uidMu  sync.Mutex
	uidSeq int

	loginLog []time.Time

	roles    []roleInfo
	perms    map[string]permissionDef // code -> 权限定义
	permByID map[int]permissionDef    // id -> 权限定义（用于回溯父级菜单）
	menuCode map[string]string        // menu_path -> code
	policies map[string][]policyRule  // 角色名 -> Casbin p 规则

	reports []*roleReport
	created []*account
}

type roleInfo struct {
	ID          int
	Name        string
	DisplayName string
	DataScope   string
}

type permissionDef struct {
	ID       int
	ParentID int
	Code     string
	Type     string
	Path     string
	Method   string
	MenuPath string
}

type policyRule struct {
	Object string
	Action string
	Type   string
}

type account struct {
	Role     roleInfo
	Index    int
	Username string
	Email    string
	AdminID  int
	UID      string
	Password string
	IsSuper  bool
	cli      *apiClient
	report   *accountReport
}

// isWildcard 判断角色是否拥有 Casbin 全通配授权（超级管理员）。
func (r *runner) isWildcard(roleName string) bool {
	for _, p := range r.policies[roleName] {
		if p.Object == "*" && p.Action == "*" {
			return true
		}
	}
	return false
}

func (r *runner) run(ctx context.Context) error {
	fmt.Printf("=== 角色权限验证 ===\n")
	fmt.Printf("后端: %s\n", r.cfg.baseURL)
	fmt.Printf("域:   %s\n", r.domain)
	fmt.Printf("账号: 每角色 %d 个（超级管理员复用已有账号）\n\n", r.cfg.accounts)

	if err := r.load(ctx); err != nil {
		return err
	}
	if len(r.roles) == 0 {
		return fmt.Errorf("没有找到任何内置角色，请确认后端已启动并完成种子初始化")
	}

	for _, rl := range r.roles {
		rep := r.verifyRole(ctx, rl)
		r.reports = append(r.reports, rep)
		r.printRoleReport(rep)
	}

	r.printSummary()
	if r.cfg.reportPath != "" {
		data, err := json.MarshalIndent(r.reports, "", "  ")
		if err != nil {
			return fmt.Errorf("生成报告失败: %w", err)
		}
		if err := os.WriteFile(r.cfg.reportPath, append(data, '\n'), 0600); err != nil {
			return fmt.Errorf("保存报告失败: %w", err)
		}
	}
	if r.cfg.cleanup {
		r.cleanupAccounts(ctx)
	}
	return nil
}

func (r *runner) load(ctx context.Context) error {
	query := r.db.Role.Query().Where(role.StatusEQ(role.StatusActive))
	if len(r.cfg.only) > 0 {
		query.Where(role.NameIn(r.cfg.only...))
	}
	rows, err := query.Order(ent.Asc(role.FieldID)).All(ctx)
	if err != nil {
		return fmt.Errorf("查询角色失败: %w", err)
	}
	byName := make(map[string]*ent.Role, len(rows))
	for _, row := range rows {
		byName[row.Name] = row
	}
	names := append([]string(nil), builtinRoleOrder...)
	if len(r.cfg.only) > 0 {
		names = r.cfg.only
	} else {
		known := make(map[string]bool)
		for _, name := range names {
			known[name] = true
		}
		for _, row := range rows {
			if !known[row.Name] {
				names = append(names, row.Name)
			}
		}
	}
	for _, name := range names {
		row, ok := byName[name]
		if !ok {
			if len(r.cfg.only) > 0 {
				return fmt.Errorf("角色 %s 不存在或未启用", name)
			}
			continue
		}
		r.roles = append(r.roles, roleInfo{
			ID:          row.ID,
			Name:        row.Name,
			DisplayName: row.DisplayName,
			DataScope:   string(row.DataScope),
		})
	}
	if len(r.roles) == 0 {
		return nil
	}

	if err := r.loadPermissions(ctx); err != nil {
		return err
	}
	return r.loadPolicies(ctx)
}

func (r *runner) loadPermissions(ctx context.Context) error {
	rows, err := r.db.SystemPermission.Query().
		Where(systempermission.StatusEQ(1), systempermission.Or(systempermission.DomainEQ(r.domain), systempermission.IsCommonEQ(true))).
		All(ctx)
	if err != nil {
		return fmt.Errorf("查询权限定义失败: %w", err)
	}
	r.perms = make(map[string]permissionDef, len(rows))
	r.permByID = make(map[int]permissionDef, len(rows))
	r.menuCode = make(map[string]string)
	for _, row := range rows {
		def := permissionDef{
			ID:       row.ID,
			ParentID: row.ParentID,
			Code:     row.Code,
			Type:     string(row.Type),
			Path:     row.Path,
			Method:   strings.ToUpper(row.Method),
			MenuPath: normalizeMenuPath(row.MenuPath),
		}
		r.perms[row.Code] = def
		r.permByID[def.ID] = def
		if def.Type == "menu" && def.MenuPath != "" {
			r.menuCode[def.MenuPath] = def.Code
		}
	}
	return nil
}

func (r *runner) loadPolicies(ctx context.Context) error {
	rows, err := r.db.CasbinRule.Query().
		Where(casbinrule.PtypeEQ("p"), casbinrule.V1EQ(r.domain)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("查询 Casbin 策略失败: %w", err)
	}
	r.policies = make(map[string][]policyRule)
	for _, row := range rows {
		r.policies[row.V0] = append(r.policies[row.V0], policyRule{
			Object: row.V2,
			Action: row.V3,
			Type:   row.V4,
		})
	}
	return nil
}

func (r *runner) hasPolicy(roleName, object, action string) bool {
	for _, p := range r.policies[roleName] {
		if (p.Object == "*" || p.Object == object) && (p.Action == "*" || strings.EqualFold(p.Action, action)) {
			return true
		}
	}
	return false
}

func (r *runner) hasPermission(roleName, code string) bool {
	def, ok := r.perms[code]
	if !ok {
		return false
	}
	if def.Type == "menu" {
		return r.hasPolicy(roleName, def.MenuPath, "menu")
	}
	return r.hasPolicy(roleName, def.Path, def.Method)
}

// verifyRole 准备该角色的一组账号并逐个登录验证。
func (r *runner) verifyRole(ctx context.Context, rl roleInfo) *roleReport {
	rep := &roleReport{Role: rl}
	count := r.cfg.accounts
	if rl.Name == "admin" {
		// 内置超管角色禁止分配给其他账号（rejectSuperAdminRoleIDs），只能用超管本身验证。
		count = 1
	}

	accounts := make([]*account, 0, count)
	for i := 1; i <= count; i++ {
		acc, err := r.ensureAccount(ctx, rl, i)
		if err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("准备账号 %d 失败: %v", i, err))
			continue
		}
		accounts = append(accounts, acc)
		r.created = append(r.created, acc)
	}

	for _, acc := range accounts {
		r.waitLoginSlot()
		if err := acc.cli.login(acc.Username, acc.Password); err != nil {
			acc.report.Checks = append(acc.report.Checks, check{
				Name: "登录", Passed: false, Detail: err.Error(),
			})
			rep.Accounts = append(rep.Accounts, acc.report)
			continue
		}
		acc.report.Checks = append(acc.report.Checks,
			check{Name: "登录", Passed: true, Detail: "取得有效访问令牌"},
			r.checkMenu(acc),
			r.checkAllowPermission(acc),
			r.checkDenyPermission(acc),
			r.checkAdminListScope(acc),
		)
		acc.report.Checks = append(acc.report.Checks, r.checkPageDataScope(ctx, acc)...)
		rep.Accounts = append(rep.Accounts, acc.report)
	}

	if iso := r.checkCustomerIsolation(ctx, rl, accounts); iso != nil {
		rep.Isolation = iso
	}
	return rep
}

// waitLoginSlot 按后端登录限流（每 IP 每分钟 10 次）排队。
// 全量验证有 15 次登录，直接连发必然把自家 IP 打到 429；这里留 1 次余量，
// 把节奏压到每分钟 9 次以内，避免把「验证结果」污染成限流噪音。
func (r *runner) waitLoginSlot() {
	const window = time.Minute
	const maxLogins = 9

	now := time.Now()
	kept := r.loginLog[:0]
	for _, t := range r.loginLog {
		if now.Sub(t) < window {
			kept = append(kept, t)
		}
	}
	r.loginLog = kept
	if len(r.loginLog) >= maxLogins {
		if wait := window - now.Sub(r.loginLog[0]); wait >= time.Second {
			fmt.Printf("  … 登录限流排队，等待 %s\n", wait.Round(time.Second))
			time.Sleep(wait)
		}
	}
	r.loginLog = append(r.loginLog, time.Now())
}

// nextUID 从 verifyUIDBase 起按库内真值分配一个未被占用的 UID。
// 每次都回查 admin.uid 唯一索引，避免重复运行或人工建号造成的冲突。
func (r *runner) nextUID(ctx context.Context) (string, error) {
	for attempt := 0; attempt < 50; attempt++ {
		r.uidMu.Lock()
		r.uidSeq++
		seq := r.uidSeq
		r.uidMu.Unlock()

		candidate := strconv.Itoa(verifyUIDBase + seq)
		exists, err := r.db.Admin.Query().Where(admin.UIDEQ(candidate)).Exist(ctx)
		if err != nil {
			return "", fmt.Errorf("检查 UID %s 是否占用失败: %w", candidate, err)
		}
		if !exists {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("连续 50 次未能分配到空闲 UID")
}

// ensureAccount 幂等准备账号：存在则重置密码并重新绑定角色，不存在则创建。
func (r *runner) ensureAccount(ctx context.Context, rl roleInfo, index int) (*account, error) {
	acc := &account{
		Role:    rl,
		Index:   index,
		IsSuper: rl.Name == "admin",
		cli:     newAPIClient(r.cfg.baseURL, r.cfg.timeout),
		report:  &accountReport{Index: index},
	}

	var row *ent.Admin
	if acc.IsSuper {
		acc.Username = r.cfg.adminUsername
		acc.Password = r.cfg.adminPassword
	} else {
		acc.Username = fmt.Sprintf("verify_%s_%d", rl.Name, index)
		acc.Password = r.cfg.password
	}

	found, err := r.db.Admin.Query().Where(admin.UsernameEQ(acc.Username)).Only(ctx)
	switch {
	case err == nil:
		row = found
	case ent.IsNotFound(err):
		row = nil
	default:
		return nil, fmt.Errorf("查询账号 %s 失败: %w", acc.Username, err)
	}

	if acc.IsSuper {
		if row == nil {
			return nil, fmt.Errorf("超级管理员账号 %q 不存在", acc.Username)
		}
		if row.Status != admin.StatusActive {
			return nil, fmt.Errorf("超级管理员账号 %q 状态为 %s，无法登录", acc.Username, row.Status)
		}
	} else {
		if row != nil && (row.Email != acc.Username+"@verify.local" || !strings.HasPrefix(row.Nickname, "角色验证-")) {
			return nil, fmt.Errorf("同名账号 %s 不是本工具的验证账号，拒绝重置", acc.Username)
		}
		hashed, salt, err := crypto.HashPassword(acc.Password)
		if err != nil {
			return nil, fmt.Errorf("生成密码哈希失败: %w", err)
		}
		if row == nil {
			acc.Email = acc.Username + "@verify.local"
			uid, uidErr := r.nextUID(ctx)
			if uidErr != nil {
				return nil, uidErr
			}
			row, err = r.db.Admin.Create().
				SetEmail(acc.Email).
				SetUsername(acc.Username).
				SetPassword(hashed).
				SetPasswordSalt(salt).
				SetUID(uid).
				SetNickname("角色验证-" + rl.DisplayName).
				SetStatus(admin.StatusActive).
				Save(ctx)
			if err != nil {
				return nil, fmt.Errorf("创建账号 %s 失败: %w", acc.Username, err)
			}
		} else {
			acc.Email = row.Email
			row, err = row.Update().
				SetPassword(hashed).
				SetPasswordSalt(salt).
				SetStatus(admin.StatusActive).
				SetIsSuperAdmin(false).
				Save(ctx)
			if err != nil {
				return nil, fmt.Errorf("重置账号 %s 密码失败: %w", acc.Username, err)
			}
		}
		// 角色绑定以本工具为唯一真值：先清空再绑定，避免历史残留导致越权判定失真。
		if _, err := r.db.AdminRole.Delete().Where(adminrole.AdminIDEQ(row.ID)).Exec(ctx); err != nil {
			return nil, fmt.Errorf("清理账号 %s 旧角色失败: %w", acc.Username, err)
		}
		if _, err := r.db.AdminRole.Create().
			SetAdminID(row.ID).
			SetRoleID(rl.ID).
			SetIsPrimary(true).
			Save(ctx); err != nil {
			return nil, fmt.Errorf("绑定账号 %s 角色失败: %w", acc.Username, err)
		}
	}

	acc.AdminID = row.ID
	acc.UID = row.UID
	if acc.IsSuper {
		acc.Email = row.Email
	}
	acc.report.Username = acc.Username
	acc.report.AdminID = row.ID
	acc.report.UID = row.UID
	acc.report.IsSuper = acc.IsSuper
	return acc, nil
}

// cleanupAccounts 删除本工具创建的验证账号（admin 超管账号不动）。
func (r *runner) cleanupAccounts(ctx context.Context) {
	removed := 0
	for _, acc := range r.created {
		if acc.IsSuper {
			continue
		}
		if _, err := r.db.AdminRole.Delete().Where(adminrole.AdminIDEQ(acc.AdminID)).Exec(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "清理账号 %s 的角色失败: %v\n", acc.Username, err)
			continue
		}
		if err := r.db.Admin.DeleteOneID(acc.AdminID).Exec(ctx); err != nil && !ent.IsNotFound(err) {
			fmt.Fprintf(os.Stderr, "删除账号 %s 失败: %v\n", acc.Username, err)
			continue
		}
		removed++
	}
	fmt.Printf("\n清理：已删除 %d 个验证账号\n", removed)
}

// sortedMenus 返回全部启用菜单（menu_path -> code），用于超管的期望集合。
func (r *runner) sortedMenus() map[string]string {
	out := make(map[string]string)
	for _, def := range r.perms {
		if def.Type == "menu" && def.MenuPath != "" {
			out[def.MenuPath] = def.Code
		}
	}
	return out
}

// expectedMenus 返回角色应看到的全部菜单（menu_path -> code）。
// 菜单接口会为「只授权了子菜单」的角色自动补齐父级节点（buildUserPermissionTree 会
// 沿 parent_id 向上回溯），因此期望集必须包含已授权菜单的祖先闭包，否则父菜单会被
// 误报成越权。
func (r *runner) expectedMenus(roleName string) map[string]string {
	if r.isWildcard(roleName) {
		return r.sortedMenus()
	}
	out := make(map[string]string)
	for _, p := range r.policies[roleName] {
		if p.Type != "menu" || p.Object == "*" {
			continue
		}
		code := r.menuCode[p.Object]
		if code == "" {
			continue
		}
		out[p.Object] = code
	}
	return withMenuAncestors(out, r.perms, r.permByID)
}

// withMenuAncestors 原地补齐已授权菜单的祖先节点，返回同一个 map。
// 只补 menu 类型且有 menu_path 的祖先，包含系统菜单与功能集菜单。
func withMenuAncestors(granted map[string]string, byCode map[string]permissionDef, byID map[int]permissionDef) map[string]string {
	// 先固化已授权菜单码，避免边遍历边写入 map（虽然合法，但遍历顺序不确定，易踩坑）。
	codes := make([]string, 0, len(granted))
	for _, code := range granted {
		codes = append(codes, code)
	}
	for _, code := range codes {
		def, ok := byCode[code]
		if !ok {
			continue
		}
		visited := map[int]bool{def.ID: true}
		for parentID := def.ParentID; parentID != 0 && !visited[parentID]; {
			visited[parentID] = true
			parent, ok := byID[parentID]
			if !ok {
				break
			}
			if parent.Type == "menu" && parent.MenuPath != "" {
				granted[parent.MenuPath] = parent.Code
			}
			parentID = parent.ParentID
		}
	}
	return granted
}

// sortedKeys 返回稳定的排序结果，保证报告可读且可 diff。
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
