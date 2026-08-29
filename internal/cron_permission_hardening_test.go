package internal

import (
	"testing"

	"go-admin/internal/ent"
)

// TestShouldSeedNormalAdminPermission_ExcludesCronJobAPIs 验证定时任务管理
// API 不再播种给 normal_admin（curl 型任务可发起服务端请求，属高危 SSRF 载体）。
func TestShouldSeedNormalAdminPermission_ExcludesCronJobAPIs(t *testing.T) {
	paths := []string{
		"/api/v1/admin/auth/cron-jobs",
		"/api/v1/admin/auth/cron-jobs/{id}",
		"/api/v1/admin/auth/cron-jobs/{id}/run",
		"/api/v1/admin/auth/cron-jobs/{id}/logs/{filename}",
	}
	for _, path := range paths {
		p := newStubSystemPermission(t, func(sp *ent.SystemPermission) {
			sp.Type = "api"
			sp.Code = "cron-job:test"
			sp.Path = path
		})
		if shouldSeedNormalAdminPermission(p) {
			t.Errorf("cron 管理 API %q 不应播种给 normal_admin", path)
		}
	}
}
