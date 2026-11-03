package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"

	"go-admin/internal/ent"
	"go-admin/internal/ent/cronjob"
	"go-admin/internal/ent/esportsassessment"
	"go-admin/internal/ent/systemconfig"
)

// checkPageDataScope 检查代表页面真正使用的数据范围，避免把接口拒绝等同于行隔离。
func (r *runner) checkPageDataScope(ctx context.Context, acc *account) []check {
	switch acc.Role.Name {
	case "admin", "normal_admin", "cs_admin":
		// 管理员列表与双账号客户归属检查分别由原有检查负责。
		return nil
	case "finance_admin":
		config, err := r.db.SystemConfig.Query().Where(systemconfig.KeyEQ("app.esports.finance_report_role_names")).Only(ctx)
		if err != nil && !ent.IsNotFound(err) {
			return []check{{Name: "财务报表授权配置", Detail: err.Error()}}
		}
		allowed := false
		if config != nil {
			for _, name := range strings.Split(config.Value, ",") {
				allowed = allowed || strings.TrimSpace(name) == acc.Role.Name
			}
		}
		var finance check
		if allowed {
			finance = checkReportScope(acc, "finance", "full")
		} else {
			code, status, _, err := acc.cli.call(http.MethodGet, "/api/v1/esports/admin/reports/finance", nil)
			finance = check{Name: "数据权限(财务报表额外授权)", Passed: err == nil && code == http.StatusForbidden,
				Detail: fmt.Sprintf("角色未列入 finance_report_role_names，期望拒绝，code=%d http=%d err=%v", code, status, err)}
		}
		return []check{finance, checkReportScope(acc, "owner_overview", "owner")}
	case "examiner_admin":
		ids, err := r.db.EsportsAssessment.Query().Order(ent.Desc(esportsassessment.FieldID)).Limit(200).IDs(ctx)
		if err != nil {
			return []check{{Name: "数据权限(考核任务)", Detail: err.Error()}}
		}
		return []check{checkSharedIDs(acc, "/api/v1/esports/admin/assessments?limit=200", "考核任务", ids)}
	case "operation_editor_admin", "cs_readonly", "finance_readonly":
		checks := []check{checkPageChannels(acc)}
		if !r.hasPermission(acc.Role.Name, "esports:pages:draft_save") {
			// 空正文不会形成有效草稿；必须在权限中间件被拒绝。
			code, status, _, err := acc.cli.call(http.MethodPut, "/api/v1/esports/admin/pages/uni_customer_home/draft", map[string]any{})
			checks = append(checks, check{Name: "只读边界(保存草稿)", Passed: err == nil && code == http.StatusForbidden,
				Detail: fmt.Sprintf("PUT draft -> code=%d http=%d err=%v（期望 403）", code, status, err)})
		}
		return checks
	case "test":
		ids, err := r.db.CronJob.Query().Order(ent.Desc(cronjob.FieldID)).Limit(100).IDs(ctx)
		if err != nil {
			return []check{{Name: "数据权限(定时任务)", Detail: err.Error()}}
		}
		return []check{checkSharedIDs(acc, "/api/v1/admin/auth/cron-jobs?page=1&page_size=100", "定时任务", ids)}
	default:
		return []check{{Name: "页面数据权限", Skipped: true, Detail: "自定义角色尚无对应的页面数据范围断言"}}
	}
}

func checkReportScope(acc *account, reportType, expected string) check {
	code, status, raw, err := acc.cli.call(http.MethodGet, "/api/v1/esports/admin/reports/"+reportType, nil)
	var env envelope
	var data struct {
		DataScope string `json:"data_scope"`
	}
	parseErr := json.Unmarshal(raw, &env)
	if parseErr == nil {
		parseErr = json.Unmarshal(env.Data, &data)
	}
	return check{Name: "数据权限(报表 " + reportType + ")",
		Passed: err == nil && parseErr == nil && successfulResponse(code, status) && data.DataScope == expected,
		Detail: fmt.Sprintf("data_scope=%q（期望 %s），code=%d http=%d err=%v", data.DataScope, expected, code, status, err)}
}

func checkSharedIDs(acc *account, path, label string, expected []int) check {
	items, _, code, err := acc.cli.list(path)
	actual := make([]int, 0, len(items))
	for _, item := range items {
		var row struct {
			ID int `json:"id"`
		}
		if parseErr := json.Unmarshal(item, &row); parseErr != nil {
			err = parseErr
			break
		}
		actual = append(actual, row.ID)
	}
	if expected == nil {
		expected = []int{}
	}
	sort.Ints(actual)
	sort.Ints(expected)
	return check{Name: "数据权限(" + label + ")",
		Passed: err == nil && (code == 1 || code == 2) && reflect.DeepEqual(actual, expected),
		Detail: fmt.Sprintf("授权后共享列表，库内预期 %d 条 / 实际 %d 条，code=%d err=%v", len(expected), len(actual), code, err)}
}

func checkPageChannels(acc *account) check {
	const base = "/api/v1/esports/admin/pages/"
	code, status, _, err := acc.cli.call(http.MethodGet, base+"uni_customer_home", nil)
	if err != nil || !successfulResponse(code, status) {
		return check{Name: "数据权限(装修渠道)", Detail: fmt.Sprintf("客户首页读取失败 code=%d http=%d err=%v", code, status, err)}
	}
	items, _, listCode, err := acc.cli.list("/api/v1/esports/admin/pages")
	if err != nil || (listCode != 1 && listCode != 2) {
		return check{Name: "数据权限(装修渠道)", Detail: fmt.Sprintf("列表读取失败 code=%d err=%v", listCode, err)}
	}
	for _, item := range items {
		var row struct {
			Channel string `json:"channel"`
		}
		if err := json.Unmarshal(item, &row); err != nil || row.Channel != "uni_customer_home" {
			return check{Name: "数据权限(装修渠道)", Detail: "列表包含非客户首页渠道或格式错误"}
		}
	}
	blockedCode, blockedStatus, _, err := acc.cli.call(http.MethodGet, base+"uni_player_home", nil)
	return check{Name: "数据权限(装修渠道)",
		Passed: err == nil && blockedCode == http.StatusBadRequest && blockedStatus < 500,
		Detail: fmt.Sprintf("客户首页可读；陪玩首页 code=%d http=%d（期望 400 拒绝非白名单渠道）", blockedCode, blockedStatus)}
}
