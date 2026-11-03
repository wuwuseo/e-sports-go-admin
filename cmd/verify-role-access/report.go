package main

import (
	"fmt"
)

// check 是一条断言结果。Passed=false 且 Skipped=true 表示未验证（不计失败）。
type check struct {
	Name    string
	Passed  bool
	Skipped bool
	Detail  string
}

type accountReport struct {
	Index    int
	Username string
	AdminID  int
	UID      string
	IsSuper  bool
	Checks   []check
}

type roleReport struct {
	Role      roleInfo
	Accounts  []*accountReport
	Isolation *check
	Errors    []string
}

func statusLabel(c check) string {
	switch {
	case c.Skipped:
		return "SKIP"
	case c.Passed:
		return "PASS"
	default:
		return "FAIL"
	}
}

func (r *runner) printRoleReport(rep *roleReport) {
	menuCount, apiCount, buttonCount := r.rolePermissionStats(rep.Role.Name)
	fmt.Printf("[%s] %s  data_scope=%s  授权: 菜单 %d / API %d / 按钮 %d\n",
		rep.Role.Name, rep.Role.DisplayName, rep.Role.DataScope, menuCount, apiCount, buttonCount)
	for _, e := range rep.Errors {
		fmt.Printf("  ERROR %s\n", e)
	}
	for _, acc := range rep.Accounts {
		fmt.Printf("  - %s (id=%d, uid=%s)\n", acc.Username, acc.AdminID, acc.UID)
		for _, c := range acc.Checks {
			fmt.Printf("      %-6s %-18s %s\n", statusLabel(c), c.Name, c.Detail)
		}
	}
	if rep.Isolation != nil {
		c := *rep.Isolation
		fmt.Printf("  - %-6s %-18s %s\n", statusLabel(c), c.Name, c.Detail)
	}
	fmt.Println()
}

// rolePermissionStats 返回角色授权的菜单 / API / 按钮数量，用于报告头部快照。
func (r *runner) rolePermissionStats(roleName string) (menu, api, button int) {
	for _, p := range r.policies[roleName] {
		switch p.Type {
		case "menu":
			menu++
		case "api":
			api++
		case "button":
			button++
		}
	}
	return menu, api, button
}

func (r *runner) printSummary() {
	var totalChecks, passed, failed, skipped int
	var failures []string
	accountCount := 0

	for _, rep := range r.reports {
		accountCount += len(rep.Accounts)
		for _, e := range rep.Errors {
			totalChecks++
			failed++
			failures = append(failures, fmt.Sprintf("%s: %s", rep.Role.Name, e))
		}
		for _, acc := range rep.Accounts {
			for _, c := range acc.Checks {
				totalChecks++
				switch {
				case c.Skipped:
					skipped++
				case c.Passed:
					passed++
				default:
					failed++
					failures = append(failures, fmt.Sprintf("%s/%s %s: %s", rep.Role.Name, acc.Username, c.Name, c.Detail))
				}
			}
		}
		if rep.Isolation != nil {
			totalChecks++
			c := *rep.Isolation
			switch {
			case c.Skipped:
				skipped++
			case c.Passed:
				passed++
			default:
				failed++
				failures = append(failures, fmt.Sprintf("%s %s: %s", rep.Role.Name, c.Name, c.Detail))
			}
		}
	}

	fmt.Println("=== 汇总 ===")
	fmt.Printf("角色 %d / 账号 %d / 检查项 %d\n", len(r.reports), accountCount, totalChecks)
	fmt.Printf("通过 %d / 失败 %d / 跳过 %d\n", passed, failed, skipped)
	if len(failures) > 0 {
		fmt.Println("\n失败明细:")
		for _, f := range failures {
			fmt.Println("  - " + f)
		}
	}
	if skipped > 0 {
		fmt.Println("\n跳过项说明：跳过多为「该角色没有对应的数据域接口」或「已授权全部 GET 接口，无拒绝样本」，不代表缺陷。")
	}
}

func (r *runner) allPassed() bool {
	for _, rep := range r.reports {
		if len(rep.Errors) > 0 {
			return false
		}
		if rep.Isolation != nil && !rep.Isolation.Passed && !rep.Isolation.Skipped {
			return false
		}
		for _, acc := range rep.Accounts {
			for _, c := range acc.Checks {
				if !c.Passed && !c.Skipped {
					return false
				}
			}
		}
	}
	return true
}
