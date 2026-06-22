package service

import (
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/role"
	"go-admin/internal/global"

	"github.com/golang-jwt/jwt/v5"
	"github.com/wuwuseo/cmf/bootstrap"
	cmfConfig "github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/crypto"

	_ "github.com/mattn/go-sqlite3"
)

func setupAdminServiceTest(t *testing.T) (*ent.Client, *bootstrap.Bootstrap, func()) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	prevORM := global.ORM
	global.ORM = client

	cfg := &cmfConfig.Config{}
	cfg.App.Name = "test-admin"
	cfg.App.AdminSecret = "admin-test-secret-key"
	cfg.App.AdminLoginExpires = 7200
	cfg.App.AdminRefreshExpires = 86400

	bs := bootstrap.NewBootstrap()
	bs.RegisterService("config", cfg)

	prevBootstrap := global.Bootstrap
	global.Bootstrap = bs

	cleanup := func() {
		client.Close()
		global.ORM = prevORM
		global.Bootstrap = prevBootstrap
	}

	return client, bs, cleanup
}

func createTestAdminWithPassword(t *testing.T, client *ent.Client, email, uid, username, password string, status admin.Status) *ent.Admin {
	ctx := t.Context()
	hashedPassword, salt, err := crypto.HashPassword(password)
	if err != nil {
		t.Fatalf("加密管理员密码失败: %v", err)
	}
	a, err := client.Admin.Create().
		SetEmail(email).
		SetUID(uid).
		SetUsername(username).
		SetPassword(hashedPassword).
		SetPasswordSalt(salt).
		SetStatus(status).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试管理员失败: %v", err)
	}
	return a
}

func createTestAdminRaw(t *testing.T, client *ent.Client, email, uid, username, password string, status admin.Status) *ent.Admin {
	ctx := t.Context()
	a, err := client.Admin.Create().
		SetEmail(email).
		SetUID(uid).
		SetUsername(username).
		SetPassword(password).
		SetPasswordSalt("salt").
		SetStatus(status).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试管理员失败: %v", err)
	}
	return a
}

// ======================== Admin Schema 验证测试 ========================

func TestAdminSchema_CreateAdminTable(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	ctx := t.Context()

	a, err := client.Admin.Create().
		SetEmail("admin@test.com").
		SetPassword("hashedpass").
		SetPasswordSalt("salt").
		SetUID("uid-admin-001").
		SetUsername("superadmin").
		SetNickname("超级管理员").
		SetStatus(admin.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建管理员记录失败: %v", err)
	}

	if a.ID == 0 {
		t.Fatal("期望管理员ID不为0")
	}
	if a.Email != "admin@test.com" {
		t.Errorf("期望邮箱为 admin@test.com, 实际=%s", a.Email)
	}
	if a.Username != "superadmin" {
		t.Errorf("期望用户名为 superadmin, 实际=%s", a.Username)
	}
	if a.Status != admin.StatusActive {
		t.Errorf("期望状态为 active, 实际=%s", a.Status)
	}
}

// ======================== AdminService Login 测试 ========================

func TestAdminService_Login_Success(t *testing.T) {
	client, _, cleanup := setupAdminServiceTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	createTestAdminWithPassword(t, client, "admin@test.com", "uid-admin-login", "adminuser", "Tt!12345", admin.StatusActive)

	svc := NewAdminService()
	resp, err := svc.Login(ctx, &AdminLoginRequest{
		Email:    "admin@test.com",
		Password: "Tt!12345",
	})
	if err != nil {
		t.Fatalf("管理员登录失败: %v", err)
	}

	if resp.AccessToken == "" {
		t.Fatal("期望返回访问令牌，但为空")
	}
	if resp.RefreshToken == "" {
		t.Fatal("期望返回刷新令牌，但为空")
	}
	if resp.TokenType != "Bearer" {
		t.Errorf("期望 TokenType=Bearer, 实际=%s", resp.TokenType)
	}

	token, err := jwt.Parse(resp.AccessToken, func(token *jwt.Token) (interface{}, error) {
		return []byte("admin-test-secret-key"), nil
	})
	if err != nil {
		t.Fatalf("解析访问令牌失败: %v", err)
	}
	if !token.Valid {
		t.Fatal("访问令牌无效")
	}
	claims := token.Claims.(jwt.MapClaims)
	if claims["token_type"] != "access" {
		t.Errorf("期望token_type=access, 实际=%v", claims["token_type"])
	}
	if claims["subject"] != "adminuser" {
		t.Errorf("期望subject=adminuser, 实际=%v", claims["subject"])
	}
	if claims["id"] != "uid-admin-login" {
		t.Errorf("期望id=uid-admin-login, 实际=%v", claims["id"])
	}
}

func TestAdminService_Login_AcceptsUsernameField(t *testing.T) {
	client, _, cleanup := setupAdminServiceTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	createTestAdminWithPassword(t, client, "username-login@test.com", "uid-username-login", "username_login", "Tt!12345", admin.StatusActive)

	svc := NewAdminService()
	resp, err := svc.Login(ctx, &AdminLoginRequest{
		Username: "username_login",
		Password: "Tt!12345",
	})
	if err != nil {
		t.Fatalf("绠＄悊鍛樼敤 username 瀛楁鐧诲綍澶辫触: %v", err)
	}
	if resp.AccessToken == "" {
		t.Fatal("鏈熸湜杩斿洖璁块棶浠ょ墝锛屼絾涓虹┖")
	}
}

func TestAdminService_Login_WrongPassword(t *testing.T) {
	client, _, cleanup := setupAdminServiceTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	createTestAdminWithPassword(t, client, "wrong@test.com", "uid-wrong", "wronguser", "Tt!12345", admin.StatusActive)

	svc := NewAdminService()
	_, err := svc.Login(ctx, &AdminLoginRequest{
		Email:    "wrong@test.com",
		Password: "Tt!00000",
	})
	if err == nil {
		t.Fatal("期望错误密码返回错误，但未返回")
	}
	if err.Error() != "invalid password" {
		t.Errorf("期望错误消息为 'invalid password', 实际=%s", err.Error())
	}
}

func TestAdminService_Login_InactiveAdmin(t *testing.T) {
	client, _, cleanup := setupAdminServiceTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	createTestAdminWithPassword(t, client, "inactive@test.com", "uid-inactive", "inactiveuser", "Tt!12345", admin.StatusInactive)

	svc := NewAdminService()
	_, err := svc.Login(ctx, &AdminLoginRequest{
		Email:    "inactive@test.com",
		Password: "Tt!12345",
	})
	if err == nil {
		t.Fatal("期望未激活管理员登录返回错误，但未返回")
	}
	if err.Error() != "admin is not active" {
		t.Errorf("期望错误消息为 'admin is not active', 实际=%s", err.Error())
	}
}

func TestAdminService_Login_AdminNotFound(t *testing.T) {
	_, _, cleanup := setupAdminServiceTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	svc := NewAdminService()
	_, err := svc.Login(ctx, &AdminLoginRequest{
		Email:    "nonexist@test.com",
		Password: "Tt!12345",
	})
	if err == nil {
		t.Fatal("期望不存在的管理员登录返回错误，但未返回")
	}
	if err.Error() != "admin not found" {
		t.Errorf("期望错误消息为 'admin not found', 实际=%s", err.Error())
	}
}

func TestAdminService_Login_UsesAdminSecret(t *testing.T) {
	client, _, cleanup := setupAdminServiceTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	createTestAdminWithPassword(t, client, "secret@test.com", "uid-secret", "secretuser", "Tt!12345", admin.StatusActive)

	svc := NewAdminService()
	resp, err := svc.Login(ctx, &AdminLoginRequest{
		Email:    "secret@test.com",
		Password: "Tt!12345",
	})
	if err != nil {
		t.Fatalf("管理员登录失败: %v", err)
	}

	_, err = jwt.Parse(resp.AccessToken, func(token *jwt.Token) (interface{}, error) {
		return []byte("test-secret-key-for-jwt"), nil
	})
	if err == nil {
		t.Fatal("期望使用错误的jwt密钥解析失败，但解析成功（说明未使用AdminSecret）")
	}

	token, err := jwt.Parse(resp.AccessToken, func(token *jwt.Token) (interface{}, error) {
		return []byte("admin-test-secret-key"), nil
	})
	if err != nil {
		t.Fatalf("使用正确的admin密钥解析令牌失败: %v", err)
	}
	if !token.Valid {
		t.Fatal("访问令牌无效")
	}
}

func TestAdminService_Login_UpdatesLastLogin(t *testing.T) {
	client, _, cleanup := setupAdminServiceTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	a := createTestAdminWithPassword(t, client, "lastlogin@test.com", "uid-lastlogin", "lastuser", "Tt!12345", admin.StatusActive)
	if !a.LastLogin.IsZero() {
		t.Fatal("新建管理员last_login不应有值")
	}

	svc := NewAdminService()
	_, err := svc.Login(ctx, &AdminLoginRequest{
		Email:    "lastlogin@test.com",
		Password: "Tt!12345",
	})
	if err != nil {
		t.Fatalf("管理员登录失败: %v", err)
	}

	updated, err := client.Admin.Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("查询更新后的管理员失败: %v", err)
	}
	if updated.LastLogin.IsZero() {
		t.Fatal("登录后last_login应被更新")
	}
}

func TestAdminService_RefreshToken_Success(t *testing.T) {
	client, _, cleanup := setupAdminServiceTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	createTestAdminWithPassword(t, client, "refresh@test.com", "uid-refresh", "refreshuser", "Tt!12345", admin.StatusActive)

	svc := NewAdminService()
	loginResp, err := svc.Login(ctx, &AdminLoginRequest{
		Email:    "refresh@test.com",
		Password: "Tt!12345",
	})
	if err != nil {
		t.Fatalf("管理员登录失败: %v", err)
	}

	resp, err := svc.RefreshToken(ctx, loginResp.RefreshToken)
	if err != nil {
		t.Fatalf("刷新Token失败: %v", err)
	}

	if resp.AccessToken == "" {
		t.Fatal("期望返回新的访问令牌")
	}
	if resp.RefreshToken == "" {
		t.Fatal("期望返回新的刷新令牌")
	}

	token, err := jwt.Parse(resp.AccessToken, func(token *jwt.Token) (interface{}, error) {
		return []byte("admin-test-secret-key"), nil
	})
	if err != nil {
		t.Fatalf("解析新访问令牌失败: %v", err)
	}
	if !token.Valid {
		t.Fatal("新访问令牌无效")
	}

	claims := token.Claims.(jwt.MapClaims)
	if claims["token_type"] != "access" {
		t.Errorf("期望token_type=access, 实际=%v", claims["token_type"])
	}
}

// ======================== AdminService 个人信息管理测试 ========================

func TestAdminService_GetAdminByUID_Success(t *testing.T) {
	client, _, cleanup := setupAdminServiceTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	createTestAdminRaw(t, client, "get@test.com", "uid-admin-get", "getadmin", "pass", admin.StatusActive)

	svc := NewAdminService()
	result, err := svc.GetAdminByUID(ctx, "uid-admin-get")
	if err != nil {
		t.Fatalf("获取管理员信息失败: %v", err)
	}

	if result.ID != "uid-admin-get" {
		t.Errorf("期望ID=uid-admin-get, 实际=%s", result.ID)
	}
	if result.Email != "get@test.com" {
		t.Errorf("期望Email=get@test.com, 实际=%s", result.Email)
	}
	if result.Username != "getadmin" {
		t.Errorf("期望Username=getadmin, 实际=%s", result.Username)
	}
}

func TestAdminService_GetAdminByUID_ReturnsAdminRoles(t *testing.T) {
	client, _, cleanup := setupAdminServiceTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	a := createTestAdminRaw(t, client, "roles@test.com", "uid-admin-roles", "roleadmin", "pass", admin.StatusActive)
	r, err := client.Role.Create().
		SetName("normal_admin").
		SetDisplayName("普通管理员").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}
	_, err = client.AdminRole.Create().
		SetAdminID(a.ID).
		SetRoleID(r.ID).
		SetIsPrimary(true).
		Save(ctx)
	if err != nil {
		t.Fatalf("分配管理员角色失败: %v", err)
	}

	svc := NewAdminService()
	result, err := svc.GetAdminByUID(ctx, "uid-admin-roles")
	if err != nil {
		t.Fatalf("获取管理员信息失败: %v", err)
	}
	if result.IsSuperAdmin {
		t.Fatal("普通管理员不应返回 is_super_admin=true")
	}
	if len(result.Roles) != 1 || result.Roles[0] != "normal_admin" {
		t.Fatalf("期望返回 normal_admin 角色，实际=%v", result.Roles)
	}
}

func TestAdminService_GetAdminByUID_NotFound(t *testing.T) {
	_, _, cleanup := setupAdminServiceTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	svc := NewAdminService()
	_, err := svc.GetAdminByUID(ctx, "nonexistent-uid")
	if err == nil {
		t.Fatal("期望不存在的管理员返回错误")
	}
	if err.Error() != "admin not found" {
		t.Errorf("期望错误消息为 'admin not found', 实际=%s", err.Error())
	}
}

func TestAdminService_UpdateAdmin_Success(t *testing.T) {
	client, _, cleanup := setupAdminServiceTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	createTestAdminRaw(t, client, "update@test.com", "uid-admin-update", "updateadmin", "pass", admin.StatusActive)

	svc := NewAdminService()
	err := svc.UpdateAdmin(ctx, "uid-admin-update", map[string]interface{}{
		"nickname": "更新的昵称",
		"phone":    "13900139000",
	})
	if err != nil {
		t.Fatalf("更新管理员信息失败: %v", err)
	}

	updated, err := client.Admin.Query().Where(admin.UID("uid-admin-update")).Only(ctx)
	if err != nil {
		t.Fatalf("查询更新后的管理员失败: %v", err)
	}
	if updated.Nickname != "更新的昵称" {
		t.Errorf("期望昵称为'更新的昵称', 实际=%s", updated.Nickname)
	}
	if updated.Phone != "13900139000" {
		t.Errorf("期望手机为'13900139000', 实际=%s", updated.Phone)
	}
}

func TestAdminService_UpdateAdmin_NotFound(t *testing.T) {
	_, _, cleanup := setupAdminServiceTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	svc := NewAdminService()
	err := svc.UpdateAdmin(ctx, "nonexistent-uid", map[string]interface{}{
		"nickname": "test",
	})
	if err == nil {
		t.Fatal("期望不存在的管理员更新返回错误")
	}
	if err.Error() != "admin not found" {
		t.Errorf("期望错误消息为 'admin not found', 实际=%s", err.Error())
	}
}

func TestAdminService_ChangePassword_Success(t *testing.T) {
	client, _, cleanup := setupAdminServiceTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	createTestAdminWithPassword(t, client, "changepass@test.com", "uid-admin-changepass", "changepassadmin", "Tt!12345", admin.StatusActive)

	svc := NewAdminService()
	err := svc.ChangePassword(ctx, "uid-admin-changepass", "Tt!12345", "Tt!67890")
	if err != nil {
		t.Fatalf("修改密码失败: %v", err)
	}

	a, err := client.Admin.Query().Where(admin.UID("uid-admin-changepass")).Only(ctx)
	if err != nil {
		t.Fatalf("查询管理员失败: %v", err)
	}

	match, err := crypto.VerifyPassword("Tt!67890", a.Password, a.PasswordSalt)
	if err != nil {
		t.Fatalf("验证新密码失败: %v", err)
	}
	if !match {
		t.Fatal("新密码验证不匹配")
	}
}

func TestAdminService_ChangePassword_WrongOldPassword(t *testing.T) {
	client, _, cleanup := setupAdminServiceTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	createTestAdminWithPassword(t, client, "wrongold@test.com", "uid-admin-wrongold", "wrongold", "Tt!12345", admin.StatusActive)

	svc := NewAdminService()
	err := svc.ChangePassword(ctx, "uid-admin-wrongold", "Tt!00000", "Tt!67890")
	if err == nil {
		t.Fatal("期望错误旧密码返回错误")
	}
	if err.Error() != "invalid password" {
		t.Errorf("期望错误消息为 'invalid password', 实际=%s", err.Error())
	}
}

func TestAdminService_Logout_Success(t *testing.T) {
	client, _, cleanup := setupAdminServiceTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	createTestAdminWithPassword(t, client, "logout@test.com", "uid-admin-logout", "logoutadmin", "Tt!12345", admin.StatusActive)

	svc := NewAdminService()
	loginResp, err := svc.Login(ctx, &AdminLoginRequest{
		Email:    "logout@test.com",
		Password: "Tt!12345",
	})
	if err != nil {
		t.Fatalf("管理员登录失败: %v", err)
	}

	err = svc.Logout(ctx, "uid-admin-logout")
	if err != nil {
		t.Fatalf("退出登录失败: %v", err)
	}

	_ = client
	_ = loginResp
}
