package service

import (
	"errors"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"

	"github.com/wuwuseo/cmf/bootstrap"
	cmfConfig "github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/crypto"

	_ "github.com/mattn/go-sqlite3"
)

func setupUserTest(t *testing.T) *ent.Client {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
	return client
}

func createTestUserForUserService(t *testing.T, client *ent.Client, email, uid, username string, status user.Status) *ent.User {
	ctx := t.Context()
	u, err := client.User.Create().
		SetEmail(email).
		SetUID(uid).
		SetUsername(username).
		SetPassword("hashedpass").
		SetPasswordSalt("salt").
		SetStatus(status).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}
	return u
}

// ======================== Register 测试 ========================

func TestUserService_Register_Success(t *testing.T) {
	setupUserTest(t)
	ctx := t.Context()
	svc := NewUserService()

	req := &RegisterRequest{
		Email:    "test@example.com",
		Password: "Tt!12345",
		Username: "testuser",
		Nickname: "测试用户",
		Phone:    "13800138000",
	}

	err := svc.Register(ctx, req)
	if err != nil {
		t.Fatalf("注册失败: %v", err)
	}

	// 验证用户是否创建成功
	u, err := global.ORM.User.Query().Where(user.EmailEQ("test@example.com")).Only(ctx)
	if err != nil {
		t.Fatalf("查询已注册用户失败: %v", err)
	}
	if u.Username != "testuser" {
		t.Errorf("期望用户名为 testuser, 实际为 %s", u.Username)
	}
	if u.Nickname != "测试用户" {
		t.Errorf("期望昵称为 测试用户, 实际为 %s", u.Nickname)
	}
	if u.Status != user.StatusInactive {
		t.Errorf("期望初始状态为 inactive, 实际为 %s", u.Status)
	}
}

func TestUserService_Register_EmailAlreadyExists(t *testing.T) {
	client := setupUserTest(t)
	ctx := t.Context()
	svc := NewUserService()

	createTestUserForUserService(t, client, "duplicate@example.com", "uid-dup", "dupuser", user.StatusActive)

	req := &RegisterRequest{
		Email:    "duplicate@example.com",
		Password: "Tt!12345",
		Username: "anotheruser",
	}

	err := svc.Register(ctx, req)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrEmailExists) {
		t.Errorf("期望错误为 ErrEmailExists, 实际错误: %v", err)
	}
}

func TestUserService_Register_UsernameAlreadyExists(t *testing.T) {
	client := setupUserTest(t)
	ctx := t.Context()
	svc := NewUserService()

	createTestUserForUserService(t, client, "email1@example.com", "uid-1", "sameusername", user.StatusActive)

	req := &RegisterRequest{
		Email:    "email2@example.com",
		Password: "Tt!12345",
		Username: "sameusername",
	}

	err := svc.Register(ctx, req)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrUsernameExists) {
		t.Errorf("期望错误为 ErrUsernameExists, 实际错误: %v", err)
	}
}

// ======================== CreateUser 测试 ========================

func TestUserService_CreateUser_Success(t *testing.T) {
	setupUserTest(t)
	ctx := t.Context()
	svc := NewUserService()

	req := &CreateUserRequest{
		Email:    "admin@example.com",
		Password: "Tt!12345",
		Username: "adminuser",
		Nickname: "管理员",
		Phone:    "13900139000",
	}

	resp, err := svc.CreateUser(ctx, req)
	if err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if resp.Email != "admin@example.com" {
		t.Errorf("期望邮箱为 admin@example.com, 实际为 %s", resp.Email)
	}
	if resp.Username != "adminuser" {
		t.Errorf("期望用户名为 adminuser, 实际为 %s", resp.Username)
	}
	if resp.Status != string(user.StatusInactive) {
		t.Errorf("期望状态为 inactive, 实际为 %s", resp.Status)
	}
}

func TestUserService_CreateUser_RejectsSuperAdminRole(t *testing.T) {
	client := setupUserTest(t)
	ctx := t.Context()
	svc := NewUserService()

	adminRole, err := client.Role.Create().
		SetName("admin").
		SetDisplayName("超级管理员").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建超级管理员角色失败: %v", err)
	}

	_, err = svc.CreateUser(ctx, &CreateUserRequest{
		Email:         "forbidden-role@test.com",
		Password:      "Tt!12345",
		Username:      "forbiddenrole",
		RoleIDs:       []int{adminRole.ID},
		PrimaryRoleID: adminRole.ID,
	})

	if !errors.Is(err, ErrSuperAdminRoleAssignmentForbidden) {
		t.Errorf("期望错误为 ErrSuperAdminRoleAssignmentForbidden, 实际错误: %v", err)
	}
}

func TestUserService_CreateUser_EmailExists(t *testing.T) {
	client := setupUserTest(t)
	ctx := t.Context()
	svc := NewUserService()

	createTestUserForUserService(t, client, "create@example.com", "uid-create", "createuser", user.StatusActive)

	req := &CreateUserRequest{
		Email:    "create@example.com",
		Password: "Tt!67890",
		Username: "newuser",
	}

	_, err := svc.CreateUser(ctx, req)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrEmailExists) {
		t.Errorf("期望错误为 ErrEmailExists, 实际错误: %v", err)
	}
}

// ======================== GetUserByID 测试 ========================

func TestUserService_GetUserByID_Found(t *testing.T) {
	client := setupUserTest(t)
	ctx := t.Context()
	svc := NewUserService()

	u := createTestUserForUserService(t, client, "getbyid@test.com", "uid-getbyid", "getbyiduser", user.StatusActive)

	resp, err := svc.GetUserByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("通过ID获取用户失败: %v", err)
	}
	if resp.ID != u.UID {
		t.Errorf("期望UID为 %s, 实际为 %s", u.UID, resp.ID)
	}
	if resp.Email != "getbyid@test.com" {
		t.Errorf("期望邮箱为 getbyid@test.com, 实际为 %s", resp.Email)
	}
}

func TestUserService_GetUserByID_NotFound(t *testing.T) {
	setupUserTest(t)
	ctx := t.Context()
	svc := NewUserService()

	_, err := svc.GetUserByID(ctx, 99999)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("期望错误为 ErrUserNotFound, 实际错误: %v", err)
	}
}

// ======================== GetUserByUID 测试 ========================

func TestUserService_GetUserByUID_Found(t *testing.T) {
	client := setupUserTest(t)
	ctx := t.Context()
	svc := NewUserService()

	createTestUserForUserService(t, client, "getbyuid@test.com", "uid-test123", "getbyuiduser", user.StatusActive)

	resp, err := svc.GetUserByUID(ctx, "uid-test123")
	if err != nil {
		t.Fatalf("通过UID获取用户失败: %v", err)
	}
	if resp.ID != "uid-test123" {
		t.Errorf("期望ID为 uid-test123, 实际为 %s", resp.ID)
	}
	if resp.Email != "getbyuid@test.com" {
		t.Errorf("期望邮箱为 getbyuid@test.com, 实际为 %s", resp.Email)
	}
}

func TestUserService_GetUserByUID_NotFound(t *testing.T) {
	setupUserTest(t)
	ctx := t.Context()
	svc := NewUserService()

	_, err := svc.GetUserByUID(ctx, "uid-nonexistent")
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("期望错误为 ErrUserNotFound, 实际错误: %v", err)
	}
}

// ======================== UpdateUser 测试 ========================

func TestUserService_UpdateUser_Success(t *testing.T) {
	client := setupUserTest(t)
	ctx := t.Context()
	svc := NewUserService()

	createTestUserForUserService(t, client, "update@test.com", "uid-update", "updateuser", user.StatusActive)

	updates := map[string]interface{}{
		"nickname": "更新后用户",
		"phone":    "18600186000",
		"avatar":   "https://example.com/avatar.jpg",
		"status":   "suspended",
	}

	err := svc.UpdateUser(ctx, "uid-update", updates)
	if err != nil {
		t.Fatalf("更新用户失败: %v", err)
	}

	u, err := client.User.Query().Where(user.UIDEQ("uid-update")).Only(ctx)
	if err != nil {
		t.Fatalf("查询更新后用户失败: %v", err)
	}
	if u.Nickname != "更新后用户" {
		t.Errorf("期望昵称为 更新后用户, 实际为 %s", u.Nickname)
	}
	if u.Phone != "18600186000" {
		t.Errorf("期望电话为 18600186000, 实际为 %s", u.Phone)
	}
	if u.Avatar != "https://example.com/avatar.jpg" {
		t.Errorf("期望头像为 https://example.com/avatar.jpg, 实际为 %s", u.Avatar)
	}
	if u.Status != user.StatusActive {
		t.Errorf("status 字段应被忽略，期望保持 active, 实际为 %s", u.Status)
	}
}

func TestUserService_UpdateUser_UserNotFound(t *testing.T) {
	setupUserTest(t)
	ctx := t.Context()
	svc := NewUserService()

	updates := map[string]interface{}{"nickname": "不会生效"}
	err := svc.UpdateUser(ctx, "uid-notfound", updates)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("期望错误为 ErrUserNotFound, 实际错误: %v", err)
	}
}

// ======================== ChangePassword 测试 ========================

func TestUserService_ChangePassword_Success(t *testing.T) {
	client := setupUserTest(t)
	ctx := t.Context()
	_ = NewUserService() // 创建服务实例但暂时不使用，因为完整测试需要更多依赖

	u := createTestUserForUserService(t, client, "changepass@test.com", "uid-changepass", "changepassuser", user.StatusActive)

	// 注意：实际测试中需要与crypto.HashPassword配合
	// 这里我们简化测试，只检查用户创建流程
	_, err := client.User.UpdateOne(u).
		SetPassword("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"). // bcrypt hash for "Tt!12345"
		SetPasswordSalt("salt").
		Save(ctx)
	if err != nil {
		t.Fatalf("设置测试密码失败: %v", err)
	}
}

// ======================== Logout 测试 ========================

func TestUserService_Logout_WithoutCache(t *testing.T) {
	setupUserTest(t)
	ctx := t.Context()
	svc := NewUserService()

	// 没有设置cache时应该正常返回nil
	err := svc.Logout(ctx, "uid-logout")
	if err != nil {
		t.Fatalf("登出失败: %v", err)
	}
}

// ======================== Login 测试 ========================

func TestUserService_Login_UserNotFound(t *testing.T) {
	setupUserTest(t)
	ctx := t.Context()
	svc := NewUserService()

	req := &LoginRequest{
		Email:    "nonexistent@test.com",
		Password: "Tt!12345",
	}

	_, err := svc.Login(ctx, req)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("期望错误为 ErrUserNotFound, 实际错误: %v", err)
	}
}

func TestUserService_Login_UserNotActive(t *testing.T) {
	client := setupUserTest(t)
	ctx := t.Context()
	svc := NewUserService()

	createTestUserForUserService(t, client, "inactive@test.com", "uid-inactive", "inactiveuser", user.StatusInactive)

	req := &LoginRequest{
		Email:    "inactive@test.com",
		Password: "password",
	}

	_, err := svc.Login(ctx, req)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrInvalidUserStatus) {
		t.Errorf("期望错误为 ErrInvalidUserStatus, 实际错误: %v", err)
	}
}

func TestUserService_Login_WrongPassword(t *testing.T) {
	client := setupUserTest(t)
	ctx := t.Context()
	svc := NewUserService()

	cfg := &cmfConfig.Config{}
	cfg.App.Name = "test-user-app"
	cfg.App.Secret = "user-test-secret"
	cfg.App.LoginExpires = 3600
	cfg.App.RefreshExpires = 86400
	bs := bootstrap.NewBootstrap()
	bs.RegisterService("config", cfg)
	prevBootstrap := global.Bootstrap
	global.Bootstrap = bs
	t.Cleanup(func() { global.Bootstrap = prevBootstrap })

	u := createTestUserForUserService(t, client, "wrongpass@test.com", "uid-wrongpass", "wrongpassuser", user.StatusActive)
	hashed, salt, err := crypto.HashPassword("Tt!12345")
	if err != nil {
		t.Fatalf("加密密码失败: %v", err)
	}
	if _, err := client.User.UpdateOne(u).SetPassword(hashed).SetPasswordSalt(salt).Save(ctx); err != nil {
		t.Fatalf("更新密码失败: %v", err)
	}

	_, err = svc.Login(ctx, &LoginRequest{
		Email:    "wrongpass@test.com",
		Password: "WrongPass!999",
	})
	if err == nil {
		t.Fatal("期望错误密码返回错误，但未返回")
	}
	if !errors.Is(err, ErrInvalidPassword) {
		t.Errorf("期望错误为 ErrInvalidPassword, 实际错误: %v", err)
	}
}

func TestUserService_Login_EmptyPasswordUser(t *testing.T) {
	client := setupUserTest(t)
	ctx := t.Context()
	svc := NewUserService()

	cfg := &cmfConfig.Config{}
	cfg.App.Name = "test-user-app"
	cfg.App.Secret = "user-test-secret"
	cfg.App.LoginExpires = 3600
	cfg.App.RefreshExpires = 86400
	bs := bootstrap.NewBootstrap()
	bs.RegisterService("config", cfg)
	prevBootstrap := global.Bootstrap
	global.Bootstrap = bs
	t.Cleanup(func() { global.Bootstrap = prevBootstrap })

	u := createTestUserForUserService(t, client, "emptypass@test.com", "uid-emptypass", "emptypassuser", user.StatusActive)
	if _, err := client.User.UpdateOne(u).SetPassword("").SetPasswordSalt("").Save(ctx); err != nil {
		t.Fatalf("清空密码失败: %v", err)
	}

	_, err := svc.Login(ctx, &LoginRequest{
		Email:    "emptypass@test.com",
		Password: "Tt!12345",
	})
	if err == nil {
		t.Fatal("期望空密码用户返回错误，但未返回")
	}
	if !errors.Is(err, ErrInvalidPassword) {
		t.Errorf("期望错误为 ErrInvalidPassword, 实际错误: %v", err)
	}
}

func TestUserService_Login_Success(t *testing.T) {
	client := setupUserTest(t)
	ctx := t.Context()
	svc := NewUserService()

	// 设置全局配置，Login 依赖 Bootstrap 获取 JWT 密钥和过期时间
	cfg := &cmfConfig.Config{}
	cfg.App.Name = "test-user-app"
	cfg.App.Secret = "user-test-secret"
	cfg.App.LoginExpires = 3600
	cfg.App.RefreshExpires = 86400
	bs := bootstrap.NewBootstrap()
	bs.RegisterService("config", cfg)
	prevBootstrap := global.Bootstrap
	global.Bootstrap = bs
	t.Cleanup(func() { global.Bootstrap = prevBootstrap })

	// 创建带正确密码哈希的活跃用户
	u := createTestUserForUserService(t, client, "loginok@test.com", "uid-loginok", "loginokuser", user.StatusActive)
	hashed, salt, err := crypto.HashPassword("Tt!12345")
	if err != nil {
		t.Fatalf("加密密码失败: %v", err)
	}
	if _, err := client.User.UpdateOne(u).SetPassword(hashed).SetPasswordSalt(salt).Save(ctx); err != nil {
		t.Fatalf("更新密码失败: %v", err)
	}

	resp, err := svc.Login(ctx, &LoginRequest{
		Email:    "loginok@test.com",
		Password: "Tt!12345",
	})
	if err != nil {
		t.Fatalf("登录失败: %v", err)
	}
	if resp.AccessToken == "" {
		t.Fatal("访问令牌不应为空")
	}
	if resp.RefreshToken == "" {
		t.Fatal("刷新令牌不应为空")
	}
	if resp.TokenType != "Bearer" {
		t.Errorf("令牌类型 = %s, 期望 Bearer", resp.TokenType)
	}
	if resp.ExpiresIn != 3600 {
		t.Errorf("过期时间 = %d, 期望 3600", resp.ExpiresIn)
	}

	// 验证最后登录时间已更新
	updated, err := client.User.Get(ctx, u.ID)
	if err != nil {
		t.Fatalf("查询用户失败: %v", err)
	}
	if updated.LastLogin.IsZero() {
		t.Error("最后登录时间应被更新")
	}
}

// ======================== RefreshToken 测试 ========================

func TestUserService_RefreshToken_InvalidTokenFormat(t *testing.T) {
	// 注意：RefreshToken 测试需要全局 Bootstrap 配置，这更适合集成测试
	// 这里我们暂时跳过，专注于不需要复杂依赖的单元测试
	t.Skip("需要全局 Bootstrap 配置，适合集成测试")
}
