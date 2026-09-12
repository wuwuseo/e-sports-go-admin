package service

import (
	"context"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/loginlog"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"

	"github.com/wuwuseo/cmf/bootstrap"
	cmfConfig "github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/crypto"

	_ "github.com/mattn/go-sqlite3"
)

func setupUserLoginTest(t *testing.T) (*ent.Client, func()) {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:userlogin?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	prevORM := global.ORM
	global.ORM = client

	cfg := &cmfConfig.Config{}
	cfg.App.Name = "test-user-login"
	cfg.App.Secret = "user-test-secret-key"
	cfg.App.LoginExpires = 7200
	cfg.App.RefreshExpires = 86400
	bs := bootstrap.NewBootstrap()
	bs.RegisterService("config", cfg)
	prevBootstrap := global.Bootstrap
	global.Bootstrap = bs

	cleanup := func() {
		global.ORM = prevORM
		global.Bootstrap = prevBootstrap
		client.Close()
	}
	return client, cleanup
}

func TestUserService_Login_RecordsLoginLogOnSuccess(t *testing.T) {
	client, cleanup := setupUserLoginTest(t)
	defer cleanup()
	stopLog := startDefaultLoginLogService(t)
	defer stopLog()

	hashed, salt, _ := crypto.HashPassword("User@2024")
	u, err := client.User.Create().
		SetEmail("someone@example.com").
		SetUID("177").
		SetUsername("someone").
		SetPassword(hashed).
		SetPasswordSalt(salt).
		SetStatus("active").
		Save(context.Background())
	if err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	userSvc := NewUserService()
	ctx := ContextWithLoginMeta(context.Background(), "7.7.7.7", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36")
	if _, err := userSvc.Login(ctx, &LoginRequest{Email: "someone@example.com", Password: "User@2024"}); err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	waitForLoginLogCount(t, client, 1)

	row, err := client.LoginLog.Query().Only(context.Background())
	if err != nil {
		t.Fatalf("查询登录日志失败: %v", err)
	}
	if row.LoginType != loginlog.LoginTypeUser || row.Status != loginlog.StatusSuccess {
		t.Fatalf("应为 user/success 日志: %+v", row)
	}
	if row.UserID != u.ID || row.IP != "7.7.7.7" {
		t.Fatalf("字段不符: %+v", row)
	}

	updated, err := client.User.Get(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("查询用户失败: %v", err)
	}
	if updated.LastIP != "7.7.7.7" {
		t.Fatalf("last_ip 应在登录成功后写入，got %q", updated.LastIP)
	}
}

func TestUserService_Login_RecordsLoginLogOnFailure(t *testing.T) {
	client, cleanup := setupUserLoginTest(t)
	defer cleanup()
	stopLog := startDefaultLoginLogService(t)
	defer stopLog()

	hashed, salt, _ := crypto.HashPassword("User@2024")
	if _, err := client.User.Create().
		SetEmail("someone@example.com").
		SetUID("178").
		SetUsername("someone").
		SetPassword(hashed).
		SetPasswordSalt(salt).
		SetStatus("active").
		Save(context.Background()); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	userSvc := NewUserService()
	ctx := ContextWithLoginMeta(context.Background(), "6.6.6.6", "")
	if _, err := userSvc.Login(ctx, &LoginRequest{Email: "someone@example.com", Password: "bad"}); err == nil {
		t.Fatal("错误密码应登录失败")
	}
	waitForLoginLogCount(t, client, 1)

	row, err := client.LoginLog.Query().Only(context.Background())
	if err != nil {
		t.Fatalf("查询登录日志失败: %v", err)
	}
	if row.Status != loginlog.StatusFail || row.LoginType != loginlog.LoginTypeUser {
		t.Fatalf("失败日志字段不符: %+v", row)
	}
}
