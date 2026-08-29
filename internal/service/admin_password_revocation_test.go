package service

import (
	"testing"

	cmfCache "github.com/wuwuseo/cmf/cache"
	cmfConfig "github.com/wuwuseo/cmf/config"

	"go-admin/internal/ent/admin"
)

// -------------------------------------------------------------------
// L2 回归：管理员修改密码后吊销全部已签发会话
// -------------------------------------------------------------------

func TestAdminService_ChangePassword_RevokesAllSessions(t *testing.T) {
	client, _, cleanup := setupAdminServiceTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	createTestAdminWithPassword(t, client, "revoketest@test.com", "uid-admin-revoketest", "revoketest", "Tt!12345", admin.StatusActive)

	// 带内存缓存的黑名单服务，RecordLogout 写入的登出标记可被读取
	cfg := &cmfConfig.Config{}
	cfg.Cache.Default = "memory"
	cfg.Cache.Stores = map[string]struct {
		Driver     string `mapstructure:"driver"`
		DefaultTTL int    `mapstructure:"default_ttl"`
		Options    any    `mapstructure:"options"`
	}{
		"memory": {Driver: "memory", DefaultTTL: 3600},
	}
	cacheInst := cmfCache.NewCache(ctx, cfg)
	svc := &AdminService{client: client, blacklistSvc: &BlacklistService{cache: cacheInst, client: client}}

	if err := svc.ChangePassword(ctx, "uid-admin-revoketest", "Tt!12345", "Tt!67890"); err != nil {
		t.Fatalf("修改密码失败: %v", err)
	}

	loggedOut, err := svc.blacklistSvc.IsLoggedOut(ctx, "uid-admin-revoketest")
	if err != nil {
		t.Fatalf("读取登出标记失败: %v", err)
	}
	if !loggedOut {
		t.Fatal("改密成功后必须写入登出标记，吊销该管理员全部已签发令牌")
	}
}

func TestAdminService_ChangePassword_NilBlacklistServiceDoesNotPanic(t *testing.T) {
	client, _, cleanup := setupAdminServiceTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	createTestAdminWithPassword(t, client, "nilbl@test.com", "uid-admin-nilbl", "nilbladmin", "Tt!12345", admin.StatusActive)

	// NewAdminService() 构造（blacklistSvc 为 nil）时改密仍应成功
	svc := NewAdminService()
	if err := svc.ChangePassword(ctx, "uid-admin-nilbl", "Tt!12345", "Tt!67890"); err != nil {
		t.Fatalf("blacklistSvc 为 nil 时修改密码不应失败: %v", err)
	}
}
