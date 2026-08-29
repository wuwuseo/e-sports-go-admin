package service

import "testing"

// TestSensitivePermissionObjects_IncludeCronJobAPIs 验证定时任务管理 API
// 全部列入高风险权限对象（仅超级管理员可见/可持有）。
//
// 背景：curl 型定时任务可配置任意 URL 发起服务端 HTTP 请求并回读响应，
// 属高危 SSRF 载体；historical normal_admin 播种的 cron 权限会在重启时
// 由 init.go 的 cleanupNormalAdminSeededExcludedPermissions 自动清除。
func TestSensitivePermissionObjects_IncludeCronJobAPIs(t *testing.T) {
	paths := []string{
		"/api/v1/admin/auth/cron-jobs",
		"/api/v1/admin/auth/cron-jobs/{id}",
		"/api/v1/admin/auth/cron-jobs/{id}/run",
		"/api/v1/admin/auth/cron-jobs/{id}/enable",
		"/api/v1/admin/auth/cron-jobs/{id}/disable",
		"/api/v1/admin/auth/cron-jobs/{id}/logs",
		"/api/v1/admin/auth/cron-jobs/{id}/logs/tail",
		"/api/v1/admin/auth/cron-jobs/{id}/logs/{filename}",
	}
	for _, path := range paths {
		if !IsSensitivePermissionObject(path) {
			t.Errorf("cron 管理 API %q 应为高风险权限对象", path)
		}
	}
}
