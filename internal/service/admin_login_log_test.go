package service

import (
	"context"
	"testing"
	"time"

	"go-admin/internal/ent/loginlog"

	"github.com/wuwuseo/cmf/crypto"
)

// startDefaultLoginLogService 在当前 global.ORM 上启动默认登录日志服务，
// 并返回关闭函数（用于测试结束前清空队列中的异步写入）。
func startDefaultLoginLogService(t *testing.T) func() {
	t.Helper()
	NewLoginLogServiceWithOptions(LoginLogOptions{
		QueueSize:      10,
		Workers:        1,
		MaxRetries:     1,
		WriteTimeout:   2 * time.Second,
		EnqueueTimeout: 100 * time.Millisecond,
	})
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = ShutdownDefaultLoginLogService(ctx)
	}
}

func TestAdminLogin_RecordsLoginLogOnSuccess(t *testing.T) {
	client, _, cleanup := setupAdminServiceTest(t)
	defer cleanup()
	stopLog := startDefaultLoginLogService(t)
	defer stopLog()

	hashed, salt, _ := crypto.HashPassword("Admin@2024")
	a, err := client.Admin.Create().
		SetEmail("boss@example.com").
		SetUID("77").
		SetUsername("boss").
		SetPassword(hashed).
		SetPasswordSalt(salt).
		SetStatus("active").
		Save(context.Background())
	if err != nil {
		t.Fatalf("创建管理员失败: %v", err)
	}

	adminSvc := NewAdminService()
	ctx := ContextWithLoginMeta(context.Background(), "9.9.9.9", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	if _, err := adminSvc.Login(ctx, &AdminLoginRequest{Username: "boss", Password: "Admin@2024"}); err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	waitForLoginLogCount(t, client, 1)

	row, err := client.LoginLog.Query().Only(context.Background())
	if err != nil {
		t.Fatalf("查询登录日志失败: %v", err)
	}
	if row.LoginType != loginlog.LoginTypeAdmin || row.Status != loginlog.StatusSuccess {
		t.Fatalf("应为 admin/success 日志: %+v", row)
	}
	if row.Username != "boss" || row.UserID != a.ID || row.IP != "9.9.9.9" {
		t.Fatalf("字段不符: %+v", row)
	}

	updated, err := client.Admin.Get(context.Background(), a.ID)
	if err != nil {
		t.Fatalf("查询管理员失败: %v", err)
	}
	if updated.LastIP != "9.9.9.9" {
		t.Fatalf("last_ip 应在登录成功后写入，got %q", updated.LastIP)
	}
}

func TestAdminLogin_RecordsLoginLogOnFailure(t *testing.T) {
	client, _, cleanup := setupAdminServiceTest(t)
	defer cleanup()
	stopLog := startDefaultLoginLogService(t)
	defer stopLog()

	hashed, salt, _ := crypto.HashPassword("Admin@2024")
	if _, err := client.Admin.Create().
		SetEmail("boss@example.com").
		SetUID("78").
		SetUsername("boss").
		SetPassword(hashed).
		SetPasswordSalt(salt).
		SetStatus("active").
		Save(context.Background()); err != nil {
		t.Fatalf("创建管理员失败: %v", err)
	}

	adminSvc := NewAdminService()
	ctx := ContextWithLoginMeta(context.Background(), "8.8.8.8", "curl/8.4.0")
	if _, err := adminSvc.Login(ctx, &AdminLoginRequest{Username: "boss", Password: "wrong-pass"}); err == nil {
		t.Fatal("错误密码应登录失败")
	}
	waitForLoginLogCount(t, client, 1)

	row, err := client.LoginLog.Query().Only(context.Background())
	if err != nil {
		t.Fatalf("查询登录日志失败: %v", err)
	}
	if row.Status != loginlog.StatusFail || row.Message == "" {
		t.Fatalf("失败日志应有状态与提示信息: %+v", row)
	}
	if row.IP != "8.8.8.8" {
		t.Fatalf("ip = %q, want 8.8.8.8", row.IP)
	}
}

func TestAdminLogin_WithoutLoginMetaSkipsLog(t *testing.T) {
	client, _, cleanup := setupAdminServiceTest(t)
	defer cleanup()

	hashed, salt, _ := crypto.HashPassword("Admin@2024")
	if _, err := client.Admin.Create().
		SetEmail("boss@example.com").
		SetUID("79").
		SetUsername("boss").
		SetPassword(hashed).
		SetPasswordSalt(salt).
		SetStatus("active").
		Save(context.Background()); err != nil {
		t.Fatalf("创建管理员失败: %v", err)
	}

	adminSvc := NewAdminService()
	// 无登录元数据（内部调用/单测场景）时不应写入登录日志
	if _, err := adminSvc.Login(context.Background(), &AdminLoginRequest{Username: "boss", Password: "Admin@2024"}); err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	n, err := client.LoginLog.Query().Count(context.Background())
	if err != nil {
		t.Fatalf("查询登录日志失败: %v", err)
	}
	if n != 0 {
		t.Fatalf("无登录元数据时不应写入登录日志，got %d", n)
	}
}
