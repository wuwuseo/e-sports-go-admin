package service

import (
	"context"
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"
	"go-admin/internal/utils"

	"github.com/golang-jwt/jwt/v5"
	"github.com/wuwuseo/cmf/crypto"

	_ "github.com/mattn/go-sqlite3"
)

func setupResetPasswordTest(t *testing.T) (*ent.Client, func()) {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:resetpwd?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	prevORM := global.ORM
	global.ORM = client
	return client, func() {
		global.ORM = prevORM
		client.Close()
	}
}

func TestUserPasswordChangesRevokeTokens(t *testing.T) {
	for _, mode := range []string{"reset", "change", "managed update"} {
		t.Run(mode, func(t *testing.T) {
			client, cleanup := setupUserLoginTest(t)
			defer cleanup()
			ctx := t.Context()
			hash, salt, err := crypto.HashPassword("OldPass!2024")
			if err != nil {
				t.Fatal(err)
			}
			u := client.User.Create().SetUID("reset-user").SetUsername("resetuser").SetEmail("reset@example.com").
				SetPassword(hash).SetPasswordSalt(salt).SetStatus("active").SaveX(ctx)
			// 无缓存服务也必须可靠吊销令牌，公开登录/刷新入口使用相同构造方式。
			svc := NewUserService()
			oldTokens, err := svc.Login(ctx, &LoginRequest{Email: u.Email, Password: "OldPass!2024"})
			if err != nil {
				t.Fatal(err)
			}
			newPassword := "NewPass!2024"
			switch mode {
			case "reset":
				newPassword, err = svc.ResetManagedUserPassword(ctx, u.ID)
			case "change":
				err = svc.ChangePassword(ctx, u.UID, "OldPass!2024", newPassword)
			case "managed update":
				err = svc.UpdateManagedUser(ctx, u.ID, &UpdateManagedUserRequest{Password: &newPassword})
			}
			if err != nil {
				t.Fatal(err)
			}
			if version := client.User.GetX(ctx, u.ID).TokenVersion; version != 1 {
				t.Fatalf("token_version=%d, want 1", version)
			}
			if _, err := svc.RefreshToken(ctx, oldTokens.RefreshToken); err == nil {
				t.Fatal("old refresh token still accepted")
			}
			fresh, err := svc.Login(ctx, &LoginRequest{Email: u.Email, Password: newPassword})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.RefreshToken(ctx, fresh.RefreshToken); err != nil {
				t.Fatalf("new session rejected: %v", err)
			}
			if _, err := svc.RefreshToken(ctx, oldTokens.RefreshToken); err == nil {
				t.Fatal("relogin revived old refresh token")
			}
			// OAuth 登录也必须签发当前版本。
			oauthTokens, err := issueUserToken(ctx, client, u.UID, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.RefreshToken(ctx, oauthTokens.RefreshToken); err != nil {
				t.Fatalf("OAuth session rejected: %v", err)
			}
		})
	}
}

func TestResetPasswordRevokesLegacyRefreshToken(t *testing.T) {
	client, cleanup := setupUserLoginTest(t)
	defer cleanup()
	ctx := t.Context()
	u := client.User.Create().SetUID("legacy").SetEmail("legacy@example.com").SetUsername("legacy").SetStatus("active").SaveX(ctx)
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"id": u.UID, "domain": "default", "token_type": "refresh", "exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString([]byte("user-test-secret-key"))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewUserService()
	if _, err := svc.RefreshToken(ctx, token); err != nil {
		t.Fatalf("unrevoked legacy token rejected: %v", err)
	}
	if _, err := svc.ResetManagedUserPassword(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RefreshToken(ctx, token); err == nil {
		t.Fatal("legacy token survived password reset")
	}
}

func TestUserService_ResetManagedUserPassword(t *testing.T) {
	client, cleanup := setupResetPasswordTest(t)
	defer cleanup()

	ctx := context.Background()
	oldHashed, salt, _ := crypto.HashPassword("OldPass!2024")
	u, err := client.User.Create().
		SetEmail("resetme@example.com").
		SetUID("201").
		SetUsername("resetme").
		SetPassword(oldHashed).
		SetPasswordSalt(salt).
		SetStatus("active").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	svc := NewUserService()
	newPwd, err := svc.ResetManagedUserPassword(ctx, u.ID)
	if err != nil {
		t.Fatalf("ResetManagedUserPassword() error = %v", err)
	}
	if len(newPwd) < 8 {
		t.Fatalf("临时密码长度不应低于 8 位，got %d", len(newPwd))
	}
	if err := utils.ValidatePasswordStrength(newPwd); err != nil {
		t.Fatalf("临时密码应通过强度校验: %v", err)
	}

	updated, err := client.User.Get(ctx, u.ID)
	if err != nil {
		t.Fatalf("查询用户失败: %v", err)
	}
	oldMatch, _ := crypto.VerifyPassword("OldPass!2024", updated.Password, updated.PasswordSalt)
	if oldMatch {
		t.Fatal("旧密码不应继续有效")
	}
	newMatch, _ := crypto.VerifyPassword(newPwd, updated.Password, updated.PasswordSalt)
	if !newMatch {
		t.Fatal("新密码应可登录")
	}
}

func TestUserService_ResetManagedUserPassword_NotFound(t *testing.T) {
	_, cleanup := setupResetPasswordTest(t)
	defer cleanup()

	svc := NewUserService()
	if _, err := svc.ResetManagedUserPassword(context.Background(), 424242); err != ErrUserNotFound {
		t.Fatalf("应返回 ErrUserNotFound，got %v", err)
	}
}

func TestSuperAdminService_ResetAdminPassword(t *testing.T) {
	client, cleanup := setupResetPasswordTest(t)
	defer cleanup()

	ctx := context.Background()
	oldHashed, salt, _ := crypto.HashPassword("OldPass!2024")
	a, err := client.Admin.Create().
		SetEmail("adminreset@example.com").
		SetUID("202").
		SetUsername("adminreset").
		SetPassword(oldHashed).
		SetPasswordSalt(salt).
		SetStatus("active").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建管理员失败: %v", err)
	}

	op := &EffectiveDataScope{IsSuperAdmin: true, AdminID: a.ID + 100}
	svc := NewSuperAdminService(nil, nil)
	adminResp, newPwd, err := svc.ResetAdminPassword(ctx, op, a.ID)
	if err != nil {
		t.Fatalf("ResetAdminPassword() error = %v", err)
	}
	if adminResp.ID != a.UID || newPwd == "" {
		t.Fatalf("响应不符: %+v pwd=%q", adminResp, newPwd)
	}

	updated, err := client.Admin.Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("查询管理员失败: %v", err)
	}
	oldMatch, _ := crypto.VerifyPassword("OldPass!2024", updated.Password, updated.PasswordSalt)
	if oldMatch {
		t.Fatal("旧密码不应继续有效")
	}
	newMatch, _ := crypto.VerifyPassword(newPwd, updated.Password, updated.PasswordSalt)
	if !newMatch {
		t.Fatal("新密码应可登录")
	}
}

func TestSuperAdminService_ResetAdminPassword_SelfForbidden(t *testing.T) {
	client, cleanup := setupResetPasswordTest(t)
	defer cleanup()

	ctx := context.Background()
	hashed, salt, _ := crypto.HashPassword("OldPass!2024")
	a, err := client.Admin.Create().
		SetEmail("self@example.com").
		SetUID("203").
		SetUsername("selfreset").
		SetPassword(hashed).
		SetPasswordSalt(salt).
		SetStatus("active").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建管理员失败: %v", err)
	}

	// 自己给自己重置密码会绕过"旧密码校验"，必须禁止（改密走 /password 接口）
	op := &EffectiveDataScope{IsSuperAdmin: true, AdminID: a.ID}
	svc := NewSuperAdminService(nil, nil)
	if _, _, err := svc.ResetAdminPassword(ctx, op, a.ID); err != ErrCannotResetSelfPassword {
		t.Fatalf("应返回 ErrCannotResetSelfPassword，got %v", err)
	}
}
