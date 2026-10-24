package internal

import (
	"os"
	"strings"
	"testing"
)

// 陪玩报单（esports）管理端路由由 internal/apps/apps.go 在 /api/v1/esports 下自带鉴权链
// 独立挂载，不是 /api/v1/admin/auth 那棵树，因此审计中间件必须在 router.go 里按前缀
// 单独挂载，且必须早于 apps.Mount（后注册的路由才能被前缀中间件覆盖）。
// 本测试锁定该挂载点（② / ），防止后续重构把它删掉。
func TestRouterMountsAuditForEsportsAdminRoutes(t *testing.T) {
	body, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("读取 router.go 失败：%v", err)
	}
	source := string(body)

	mountCall := `v1.Use("/esports/admin", middleware.Audit(middleware.AdminAuditRules(), opLogService))`
	mountIndex := strings.Index(source, mountCall)
	if mountIndex < 0 {
		t.Fatalf("router.go 未为 esports 管理端路由挂载审计中间件：缺少 %s", mountCall)
	}

	appsMountIndex := strings.Index(source, "apps.Mount(v1, config, blacklistService)")
	if appsMountIndex < 0 {
		t.Fatal("router.go 未找到 apps.Mount 调用")
	}
	if mountIndex > appsMountIndex {
		t.Fatal("esports 审计中间件必须在 apps.Mount 之前注册，否则不会覆盖功能集路由")
	}
}
