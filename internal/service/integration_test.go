package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"

	"github.com/golang-jwt/jwt/v5"
	cmfCache "github.com/wuwuseo/cmf/cache"
	cmfConfig "github.com/wuwuseo/cmf/config"
	cmfJwt "github.com/wuwuseo/cmf/jwt"
	"github.com/wuwuseo/cmf/crypto"
	"github.com/wuwuseo/cmf/bootstrap"
	"github.com/wuwuseo/cmf/filesystem"

	_ "github.com/mattn/go-sqlite3"
)

// ======================== 集成测试辅助函数 ========================

func setupIntegrationTest(t *testing.T) (*ent.Client, *bootstrap.Bootstrap, func()) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	prevORM := global.ORM
	global.ORM = client

	// 创建测试用临时目录用于文件存储
	tmpDir := t.TempDir()
	storageDir := filepath.Join(tmpDir, "storage")
	os.MkdirAll(storageDir, 0755)

	// 构建测试配置
	cfg := &cmfConfig.Config{}
	cfg.App.Name = "test-admin"
	cfg.App.Secret = "test-secret-key-for-jwt"
	cfg.App.LoginExpires = 86400
	cfg.App.RefreshExpires = 604800

	// 创建 Bootstrap 并注册 config
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

func createActiveUser(t *testing.T, client *ent.Client, email, uid, username, password string) *ent.User {
	ctx := t.Context()
	hashedPassword, salt, err := crypto.HashPassword(password)
	if err != nil {
		t.Fatalf("加密密码失败: %v", err)
	}
	u, err := client.User.Create().
		SetEmail(email).
		SetUID(uid).
		SetUsername(username).
		SetPassword(hashedPassword).
		SetPasswordSalt(salt).
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建活跃用户失败: %v", err)
	}
	return u
}

// ======================== JWT 认证集成测试 ========================

func TestIntegration_UserLogin_Success(t *testing.T) {
	client, _, cleanup := setupIntegrationTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	createActiveUser(t, client, "login@test.com", "uid-login-integration", "loginuser", "mypassword123")

	svc := NewUserService()
	resp, err := svc.Login(ctx, &LoginRequest{
		Email:    "login@test.com",
		Password: "mypassword123",
	})
	if err != nil {
		t.Fatalf("登录失败: %v", err)
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
	if resp.ExpiresIn != 86400 {
		t.Errorf("期望 ExpiresIn=86400, 实际=%d", resp.ExpiresIn)
	}

	token, err := jwt.Parse(resp.AccessToken, func(token *jwt.Token) (interface{}, error) {
		return []byte("test-secret-key-for-jwt"), nil
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
	if claims["subject"] != "loginuser" {
		t.Errorf("期望subject=loginuser, 实际=%v", claims["subject"])
	}
	if claims["id"] != "uid-login-integration" {
		t.Errorf("期望id=uid-login-integration, 实际=%v", claims["id"])
	}
}

func TestIntegration_UserLogin_InvalidPassword(t *testing.T) {
	client, _, cleanup := setupIntegrationTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	createActiveUser(t, client, "badpass@test.com", "uid-badpass", "badpassuser", "correctpassword")

	svc := NewUserService()
	_, err := svc.Login(ctx, &LoginRequest{
		Email:    "badpass@test.com",
		Password: "wrongpassword",
	})
	if err == nil {
		t.Fatal("期望错误密码返回错误，但未返回")
	}
}

func TestIntegration_UserLogin_UserNotFound(t *testing.T) {
	_, _, cleanup := setupIntegrationTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	svc := NewUserService()
	_, err := svc.Login(ctx, &LoginRequest{
		Email:    "nonexistent@test.com",
		Password: "anypassword",
	})
	if err == nil {
		t.Fatal("期望不存在用户返回错误，但未返回")
	}
}

func TestIntegration_UserLogin_InactiveUser(t *testing.T) {
	client, _, cleanup := setupIntegrationTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	hashedPassword, salt, _ := crypto.HashPassword("pass")
	client.User.Create().
		SetEmail("inactive@test.com").
		SetUID("uid-inactive").
		SetUsername("inactiveuser").
		SetPassword(hashedPassword).
		SetPasswordSalt(salt).
		SetStatus(user.StatusInactive).
		Save(ctx)

	svc := NewUserService()
	_, err := svc.Login(ctx, &LoginRequest{
		Email:    "inactive@test.com",
		Password: "pass",
	})
	if err == nil {
		t.Fatal("期望未激活用户登录返回错误，但未返回")
	}
}

// ======================== JWT RefreshToken 集成测试 ========================

func TestIntegration_RefreshToken_Success(t *testing.T) {
	client, bs, cleanup := setupIntegrationTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	createActiveUser(t, client, "refresh@test.com", "uid-refresh", "refreshuser", "refreshpass")

	svc := NewUserService()
	loginResp, err := svc.Login(ctx, &LoginRequest{
		Email:    "refresh@test.com",
		Password: "refreshpass",
	})
	if err != nil {
		t.Fatalf("登录失败: %v", err)
	}

	refreshToken := loginResp.RefreshToken
	config, _ := bs.GetService("config")
	cfg := config.(*cmfConfig.Config)
	token, err := jwt.Parse(refreshToken, func(token *jwt.Token) (interface{}, error) {
		return []byte(cfg.App.Secret), nil
	})
	if err != nil {
		t.Fatalf("解析刷新令牌失败: %v", err)
	}

	claims := token.Claims.(jwt.MapClaims)
	if claims["token_type"] != "refresh" {
		t.Errorf("期望token_type=refresh, 实际=%v", claims["token_type"])
	}

	refreshResp, err := svc.RefreshToken(ctx, refreshToken)
	if err != nil {
		t.Fatalf("刷新令牌失败: %v", err)
	}

	if refreshResp.AccessToken == "" {
		t.Fatal("期望返回新的访问令牌，但为空")
	}
}

func TestIntegration_RefreshToken_InvalidToken(t *testing.T) {
	_, _, cleanup := setupIntegrationTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	svc := NewUserService()
	_, err := svc.RefreshToken(ctx, "invalid-token-format")
	if err == nil {
		t.Fatal("期望无效令牌返回错误，但未返回")
	}
}

func TestIntegration_RefreshToken_WrongTokenType(t *testing.T) {
	client, _, cleanup := setupIntegrationTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	createActiveUser(t, client, "wrongtype@test.com", "uid-wrongtype", "wrongtypeuser", "pass")

	svc := NewUserService()
	loginResp, _ := svc.Login(ctx, &LoginRequest{
		Email:    "wrongtype@test.com",
		Password: "pass",
	})

	// 尝试用访问令牌刷新（应该失败，因为token_type不是refresh）
	_, err := svc.RefreshToken(ctx, loginResp.AccessToken)
	if err == nil {
		t.Fatal("期望使用访问令牌刷新返回错误，但未返回")
	}
}

func TestIntegration_RefreshToken_UserDeactivated(t *testing.T) {
	client, _, cleanup := setupIntegrationTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	u := createActiveUser(t, client, "deactivate@test.com", "uid-deactivate", "deactuser", "deactpass")

	svc := NewUserService()
	loginResp, err := svc.Login(ctx, &LoginRequest{
		Email:    "deactivate@test.com",
		Password: "deactpass",
	})
	if err != nil {
		t.Fatalf("登录失败: %v", err)
	}

	client.User.UpdateOne(u).SetStatus(user.StatusInactive).Save(ctx)

	_, err = svc.RefreshToken(ctx, loginResp.RefreshToken)
	if err == nil {
		t.Fatal("期望已停用用户刷新令牌返回错误，但未返回")
	}
}

// ======================== ChangePassword 集成测试 ========================

func TestIntegration_ChangePassword_Success(t *testing.T) {
	client, _, cleanup := setupIntegrationTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	createActiveUser(t, client, "changepass@test.com", "uid-changepass-int", "changepassuser", "oldpassword123")

	svc := NewUserService()
	err := svc.ChangePassword(ctx, "uid-changepass-int", "oldpassword123", "newpassword456")
	if err != nil {
		t.Fatalf("修改密码失败: %v", err)
	}

	// 验证新密码可以登录
	_, err = svc.Login(ctx, &LoginRequest{
		Email:    "changepass@test.com",
		Password: "newpassword456",
	})
	if err != nil {
		t.Fatalf("使用新密码登录失败: %v", err)
	}
}

func TestIntegration_ChangePassword_WrongOldPassword(t *testing.T) {
	client, _, cleanup := setupIntegrationTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	createActiveUser(t, client, "wrongold@test.com", "uid-wrongold", "wrongolduser", "correctpass")

	svc := NewUserService()
	err := svc.ChangePassword(ctx, "uid-wrongold", "wrongoldpassword", "newpass")
	if err == nil {
		t.Fatal("期望错误旧密码返回错误，但未返回")
	}
}

// ======================== 文件系统集成测试 ========================

func TestIntegration_AttachmentService_WithFilesystem(t *testing.T) {
	client, _, cleanup := setupIntegrationTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	// 创建测试文件系统
	tmpDir := t.TempDir()
	storageDir := filepath.Join(tmpDir, "storage")
	os.MkdirAll(storageDir, 0755)

	cfg := &cmfConfig.Config{}
	cfg.Filesystem.Default = "local"
	cfg.Filesystem.Disks = map[string]struct {
		Driver  string `mapstructure:"driver"`
		Options any    `mapstructure:"options"`
	}{
		"local": {
			Driver: "local",
			Options: map[string]interface{}{
				"root": storageDir,
			},
		},
	}

	fs, err := filesystem.NewFilesystemFromConfig(cfg)
	if err != nil {
		t.Fatalf("创建文件系统失败: %v", err)
	}

	// 存储测试文件
	testData := []byte("hello world test content")
	testPath := "/test/file.txt"
	err = fs.Set(testPath, testData, 0)
	if err != nil {
		t.Fatalf("存储测试文件失败: %v", err)
	}

	// 创建附件记录
	a, err := client.Attachment.Create().
		SetName("file.txt").
		SetOriginalName("file.txt").
		SetPath(testPath).
		SetMimeType("text/plain").
		SetExtension("txt").
		SetSize(int64(len(testData))).
		SetUploadedBy("testuser").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建附件记录失败: %v", err)
	}

	// 创建带文件系统的附件服务
	svc := NewAttachmentService(fs)

	// 测试获取文件数据
	data, mimeType, originalName, err := svc.GetFileData(ctx, a.ID)
	if err != nil {
		t.Fatalf("获取文件数据失败: %v", err)
	}
	if string(data) != string(testData) {
		t.Errorf("期望文件内容为 '%s', 实际为 '%s'", testData, string(data))
	}
	if mimeType != "text/plain" {
		t.Errorf("期望MIME类型为 text/plain, 实际为 %s", mimeType)
	}
	if originalName != "file.txt" {
		t.Errorf("期望原文件名为 file.txt, 实际为 %s", originalName)
	}

	// 测试删除附件
	err = svc.Delete(ctx, a.ID)
	if err != nil {
		t.Fatalf("删除附件失败: %v", err)
	}

	// 验证附件已删除
	_, err = client.Attachment.Get(ctx, a.ID)
	if !ent.IsNotFound(err) {
		t.Errorf("期望附件已删除，但查询未返回NotFound错误: %v", err)
	}
}

// ======================== 缓存系统集成测试 ========================

func TestIntegration_CacheBasedServices(t *testing.T) {
	client, _, cleanup := setupIntegrationTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	// 创建测试缓存配置（使用内存缓存驱动）
	cfg := &cmfConfig.Config{}
	cfg.Cache.Default = "memory"
	cfg.Cache.Stores = map[string]struct {
		Driver     string `mapstructure:"driver"`
		DefaultTTL int    `mapstructure:"default_ttl"`
		Options    any    `mapstructure:"options"`
	}{
		"memory": {
			Driver:     "memory",
			DefaultTTL: 3600,
		},
	}

	_ = cmfCache.NewCache(ctx, cfg)

	// 创建活跃用户和角色
	createActiveUser(t, client, "cache@test.com", "uid-cache-test", "cacheuser", "pass")

	r, err := client.Role.Create().
		SetName("cache-role").
		SetDisplayName("缓存角色").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	// 创建基于缓存的服务
	permSvc := NewPermissionService(nil)

	// 分配权限给角色
	err = permSvc.AssignPermissionsToRole(ctx, "cache-role", "default", []PermissionRule{
		{Object: "/api/cache/test", Action: "GET"},
	})
	if err != nil {
		t.Fatalf("分配权限失败: %v", err)
	}

	// 获取角色权限
	perms, err := permSvc.GetRolePermissions(ctx, "cache-role", "default")
	if err != nil {
		t.Fatalf("获取角色权限失败: %v", err)
	}
	if len(perms) != 1 {
		t.Errorf("期望1个权限, 实际为 %d", len(perms))
	}

	// 第二次获取
	perms2, err := permSvc.GetRolePermissions(ctx, "cache-role", "default")
	if err != nil {
		t.Fatalf("第二次获取角色权限失败: %v", err)
	}
	if len(perms2) != 1 {
		t.Errorf("期望1个权限, 实际为 %d", len(perms2))
	}

	_ = r
}

// ======================== Cron 任务执行流程集成测试 ========================

func TestIntegration_CronJob_EnableDisableFlow(t *testing.T) {
	client, _, cleanup := setupIntegrationTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	// 创建定时任务管理器
	tmpDir := t.TempDir()
	client2, manager := setupCronJobTestWithClient(t, client, tmpDir)
	svc := NewCronJobService(manager)

	// 创建定时任务
	disabled := false
	resp, err := svc.Create(ctx, &CreateCronJobRequest{
		Name:       "集成测试任务",
		Type:       "curl",
		Expression: "0 */10 * * * *",
		Command:    `{"method":"GET","url":"http://example.com"}`,
		Enabled:    &disabled,
	})
	if err != nil {
		t.Fatalf("创建定时任务失败: %v", err)
	}

	// 测试启用
	err = svc.Enable(ctx, resp.ID)
	if err != nil {
		t.Fatalf("启用定时任务失败: %v", err)
	}

	// 测试禁用
	err = svc.Disable(ctx, resp.ID)
	if err != nil {
		t.Fatalf("禁用定时任务失败: %v", err)
	}

	// 列出所有任务验证
	listResp, err := svc.List(ctx, &ListCronJobsRequest{})
	if err != nil {
		t.Fatalf("获取任务列表失败: %v", err)
	}
	if listResp.Total != 1 {
		t.Errorf("期望1个任务, 实际为 %d", listResp.Total)
	}

	_ = client2
}

// ======================== 端到端集成测试 ========================

func TestIntegration_E2E_UserFlow(t *testing.T) {
	client, _, cleanup := setupIntegrationTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	// ===== 阶段1: 用户注册 =====
	svc := NewUserService()
	err := svc.Register(ctx, &RegisterRequest{
		Email:    "e2e@test.com",
		Password: "e2epassword",
		Username: "e2euser",
		Nickname: "端到端用户",
		Phone:    "13800000000",
	})
	if err != nil {
		t.Fatalf("注册失败: %v", err)
	}

	// 获取注册用户
	u, err := client.User.Query().Where(user.EmailEQ("e2e@test.com")).Only(ctx)
	if err != nil {
		t.Fatalf("查询注册用户失败: %v", err)
	}

	// ===== 阶段2: 激活用户 =====
	activationSvc := NewUserActivationService()
	err = activationSvc.ActivateUser(ctx, u.ID, "operator-uid")
	if err != nil {
		t.Fatalf("激活用户失败: %v", err)
	}

	// ===== 阶段3: 用户登录 =====
	loginResp, err := svc.Login(ctx, &LoginRequest{
		Email:    "e2e@test.com",
		Password: "e2epassword",
	})
	if err != nil {
		t.Fatalf("登录失败: %v", err)
	}
	if loginResp.AccessToken == "" || loginResp.RefreshToken == "" {
		t.Fatal("登录后未返回令牌")
	}

	// ===== 阶段4: 刷新令牌 =====
	refreshResp, err := svc.RefreshToken(ctx, loginResp.RefreshToken)
	if err != nil {
		t.Fatalf("刷新令牌失败: %v", err)
	}
	if refreshResp.AccessToken == "" {
		t.Fatal("刷新后未返回新访问令牌")
	}

	// ===== 阶段5: 修改密码 =====
	err = svc.ChangePassword(ctx, u.UID, "e2epassword", "newE2Epassword")
	if err != nil {
		t.Fatalf("修改密码失败: %v", err)
	}

	// ===== 阶段6: 新密码登录 =====
	newLoginResp, err := svc.Login(ctx, &LoginRequest{
		Email:    "e2e@test.com",
		Password: "newE2Epassword",
	})
	if err != nil {
		t.Fatalf("新密码登录失败: %v", err)
	}
	if newLoginResp.AccessToken == "" {
		t.Fatal("新密码登录未返回令牌")
	}

	// ===== 阶段7: 更新用户信息 =====
	err = svc.UpdateUser(ctx, u.UID, map[string]interface{}{
		"nickname": "更新后端到端用户",
		"phone":    "13900000000",
	})
	if err != nil {
		t.Fatalf("更新用户失败: %v", err)
	}

	userResp, err := svc.GetUserByUID(ctx, u.UID)
	if err != nil {
		t.Fatalf("获取用户失败: %v", err)
	}
	if userResp.Nickname != "更新后端到端用户" {
		t.Errorf("期望昵称为 更新后端到端用户, 实际为 %s", userResp.Nickname)
	}
	if userResp.Phone != "13900000000" {
		t.Errorf("期望电话为 13900000000, 实际为 %s", userResp.Phone)
	}
}

func TestIntegration_E2E_RolePermissionFlow(t *testing.T) {
	client, _, cleanup := setupIntegrationTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	// ===== 阶段1: 创建用户 =====
	u := createActiveUser(t, client, "e2e-role@test.com", "uid-e2e-role", "e2eroleuser", "rolepass")

	// ===== 阶段2: 创建角色 =====
	roleSvc := NewRoleService(nil)
	roleResp, err := roleSvc.Create(ctx, &CreateRoleRequest{
		Name:        "e2e-editor",
		DisplayName: "端到端编辑者",
	})
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	// ===== 阶段3: 分配角色给用户 =====
	// 注意：AssignRoles 依赖 EnforcerManager，
	// 此处使用直接创建 UserRole 关联的方式模拟角色分配
	_, err = client.UserRole.Create().
		SetUserID(u.ID).
		SetRoleID(roleResp.ID).
		SetIsPrimary(true).
		SetCreatedBy("e2eroleuser").
		Save(ctx)
	if err != nil {
		t.Fatalf("分配角色失败: %v", err)
	}

	// 同时需要创建 Casbin 分组规则
	_, err = client.CasbinRule.Create().
		SetPtype("g").
		SetV0(u.UID).
		SetV1("e2e-editor").
		SetV2("default").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建Casbin分组规则失败: %v", err)
	}

	// ===== 阶段4: 分配权限给角色 =====
	permSvc := NewPermissionService(nil)
	err = permSvc.AssignPermissionsToRole(ctx, "e2e-editor", "default", []PermissionRule{
		{Object: "/api/articles", Action: "GET"},
	})
	if err != nil {
		t.Fatalf("分配权限失败: %v", err)
	}

	// ===== 阶段5: 验证权限 =====
	perms, err := permSvc.GetUserAPIPermissions(ctx, u.UID, "default")
	if err != nil {
		t.Fatalf("获取用户API权限失败: %v", err)
	}
	if len(perms) == 0 {
		t.Error("期望用户至少有一个API权限")
	}

	// ===== 阶段6: 检查具体权限 =====
	hasPerm, err := permSvc.CheckPermission(ctx, u.UID, "default", "/api/articles", "GET")
	if err != nil {
		t.Fatalf("检查权限失败: %v", err)
	}
	if !hasPerm {
		t.Error("期望用户有 /api/articles GET 权限")
	}

	// ===== 阶段7: 验证没有额外权限 =====
	hasPerm, err = permSvc.CheckPermission(ctx, u.UID, "default", "/api/articles", "DELETE")
	if err != nil {
		t.Fatalf("检查权限失败: %v", err)
	}
	if hasPerm {
		t.Error("期望用户没有 /api/articles DELETE 权限")
	}
}

func TestIntegration_E2E_ConfigAndDictFlow(t *testing.T) {
	client, _, cleanup := setupIntegrationTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	// ===== 阶段1: 创建字典数据 =====
	dictSvc := NewDictDataService()
	sortVal := 1
	dictResp, err := dictSvc.Create(ctx, &CreateDictDataRequest{
		DictType:  "sys_config_group",
		Label:     "端到端配置组",
		Value:     "e2e-config",
		Sort:      &sortVal,
		Status:    nil,
		IsDefault: nil,
	})
	if err != nil {
		t.Fatalf("创建字典数据失败: %v", err)
	}
	if dictResp.Value != "e2e-config" {
		t.Errorf("期望字典值为 e2e-config, 实际为 %s", dictResp.Value)
	}

	// ===== 阶段2: 创建系统配置 =====
	status := 1
	configSvc := NewConfigService(nil)
	configResp, err := configSvc.Create(ctx, &CreateConfigRequest{
		Name:   "端到端配置",
		Key:    "e2e.test.config",
		Value:  "e2e-value",
		Type:   "string",
		Group:  "e2e-config",
		Status: &status,
		Remark: "端到端测试配置",
	})
	if err != nil {
		t.Fatalf("创建系统配置失败: %v", err)
	}

	// ===== 阶段3: 获取配置分组 =====
	groups, err := configSvc.GetGroups(ctx)
	if err != nil {
		t.Fatalf("获取配置分组失败: %v", err)
	}
	if len(groups) == 0 {
		t.Log("没有配置分组（预期内，因为未初始化种子数据）")
	}

	// ===== 阶段4: 按Key获取配置 =====
	retrieved, err := configSvc.GetByKey(ctx, "e2e.test.config")
	if err != nil {
		t.Fatalf("按Key获取配置失败: %v", err)
	}
	if retrieved.Value != "e2e-value" {
		t.Errorf("期望值为 e2e-value, 实际为 %s", retrieved.Value)
	}

	// ===== 阶段5: 删除配置 =====
	err = configSvc.Delete(ctx, configResp.ID)
	if err != nil {
		t.Fatalf("删除配置失败: %v", err)
	}

	_, err = configSvc.Get(ctx, configResp.ID)
	if err == nil {
		t.Fatal("期望已删除的配置查询返回错误，但未返回")
	}

	_ = client
}

// ======================== 构建JWT令牌的辅助函数 ========================

func buildTestRefreshToken(t *testing.T, uid string) string {
	t.Helper()
	cfg := &cmfConfig.Config{}
	cfg.App.Secret = "test-secret-key-for-jwt"
	cfg.App.Name = "test-admin"
	cfg.App.RefreshExpires = 604800

	claims := jwt.MapClaims{
		"id":         uid,
		"subject":    "testuser",
		"issuer":     "test-admin",
		"domain":     "default",
		"expires":    jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		"issued_at":  jwt.NewNumericDate(time.Now()),
		"token_type": "refresh",
	}
	token, err := cmfJwt.CreateToken(claims, cfg.App.Secret)
	if err != nil {
		t.Fatalf("创建测试刷新令牌失败: %v", err)
	}
	return token
}
