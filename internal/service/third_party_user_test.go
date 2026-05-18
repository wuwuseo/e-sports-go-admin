package service

import (
	"errors"
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/thirdpartyuser"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func setupThirdPartyUserTest(t *testing.T) *ent.Client {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
	return client
}

func createTestThirdPartyUser(t *testing.T, client *ent.Client, userID int, platform, openid string) *ent.ThirdPartyUser {
	ctx := t.Context()
	tp, err := client.ThirdPartyUser.Create().
		SetUserID(userID).
		SetPlatform(platform).
		SetOpenid(openid).
		SetStatus(thirdpartyuser.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试第三方用户失败: %v", err)
	}
	return tp
}

func createTestUserForThirdParty(t *testing.T, client *ent.Client, email string) *ent.User {
	ctx := t.Context()
	u, err := client.User.Create().
		SetEmail(email).
		SetPassword("testpass").
		SetUID("test-uid").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}
	return u
}

// ======================== BindThirdPartyUser 测试 ========================

func TestThirdPartyUserService_BindThirdPartyUser_Success(t *testing.T) {
	client := setupThirdPartyUserTest(t)
	ctx := t.Context()
	svc := NewThirdPartyUserService()

	u := createTestUserForThirdParty(t, client, "bind@test.com")

	req := &BindThirdPartyRequest{
		UserID:       u.ID,
		Platform:     "wechat",
		PlatformName: "微信",
		Openid:       "wx-openid-123",
		Unionid:      "wx-unionid-456",
		Nickname:     "测试用户",
		AvatarURL:    "https://example.com/avatar.jpg",
		Source:       "mobile",
		SourceUserID: "src-123",
		Company:      "测试公司",
	}

	tp, err := svc.BindThirdPartyUser(ctx, req)
	if err != nil {
		t.Fatalf("绑定第三方用户失败: %v", err)
	}
	if tp.UserID != u.ID {
		t.Errorf("期望UserID为 %d, 实际为 %d", u.ID, tp.UserID)
	}
	if tp.Platform != "wechat" {
		t.Errorf("期望Platform为 wechat, 实际为 %s", tp.Platform)
	}
	if tp.Openid != "wx-openid-123" {
		t.Errorf("期望Openid为 wx-openid-123, 实际为 %s", tp.Openid)
	}
	if tp.Unionid != "wx-unionid-456" {
		t.Errorf("期望Unionid为 wx-unionid-456, 实际为 %s", tp.Unionid)
	}
	if tp.Nickname != "测试用户" {
		t.Errorf("期望Nickname为 测试用户, 实际为 %s", tp.Nickname)
	}
	if tp.AvatarURL != "https://example.com/avatar.jpg" {
		t.Errorf("期望AvatarURL为 https://example.com/avatar.jpg, 实际为 %s", tp.AvatarURL)
	}
	if tp.Source != "mobile" {
		t.Errorf("期望Source为 mobile, 实际为 %s", tp.Source)
	}
	if tp.SourceUserID != "src-123" {
		t.Errorf("期望SourceUserID为 src-123, 实际为 %s", tp.SourceUserID)
	}
	if tp.Company != "测试公司" {
		t.Errorf("期望Company为 测试公司, 实际为 %s", tp.Company)
	}
	if tp.PlatformName != "微信" {
		t.Errorf("期望PlatformName为 微信, 实际为 %s", tp.PlatformName)
	}
}

func TestThirdPartyUserService_BindThirdPartyUser_AlreadyBound(t *testing.T) {
	client := setupThirdPartyUserTest(t)
	ctx := t.Context()
	svc := NewThirdPartyUserService()

	u1 := createTestUserForThirdParty(t, client, "user1@test.com")
	createTestThirdPartyUser(t, client, u1.ID, "wechat", "wx-openid-123")

	u2 := createTestUserForThirdParty(t, client, "user2@test.com")
	req := &BindThirdPartyRequest{
		UserID:   u2.ID,
		Platform: "wechat",
		Openid:   "wx-openid-123", // 相同的 openid+platform
	}

	_, err := svc.BindThirdPartyUser(ctx, req)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrThirdPartyBound) {
		t.Errorf("期望错误为 ErrThirdPartyBound, 实际错误: %v", err)
	}
}

// ======================== UnbindThirdPartyUser 测试 ========================

func TestThirdPartyUserService_UnbindThirdPartyUser_Success(t *testing.T) {
	client := setupThirdPartyUserTest(t)
	ctx := t.Context()
	svc := NewThirdPartyUserService()

	u := createTestUserForThirdParty(t, client, "unbind@test.com")
	tp := createTestThirdPartyUser(t, client, u.ID, "wechat", "wx-789")

	err := svc.UnbindThirdPartyUser(ctx, tp.ID)
	if err != nil {
		t.Fatalf("解绑第三方用户失败: %v", err)
	}

	exists, err := client.ThirdPartyUser.Query().
		Where(thirdpartyuser.IDEQ(tp.ID)).
		Exist(ctx)
	if err != nil {
		t.Fatalf("检查存在性失败: %v", err)
	}
	if exists {
		t.Error("第三方绑定应该已被删除，但仍然存在")
	}
}

func TestThirdPartyUserService_UnbindThirdPartyUser_NotFound(t *testing.T) {
	setupThirdPartyUserTest(t)
	ctx := t.Context()
	svc := NewThirdPartyUserService()

	err := svc.UnbindThirdPartyUser(ctx, 99999)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrThirdPartyNotFound) {
		t.Errorf("期望错误为 ErrThirdPartyNotFound, 实际错误: %v", err)
	}
}

// ======================== ListUserThirdPartyAccounts 测试 ========================

func TestThirdPartyUserService_ListUserThirdPartyAccounts_Empty(t *testing.T) {
	client := setupThirdPartyUserTest(t)
	ctx := t.Context()
	svc := NewThirdPartyUserService()

	u := createTestUserForThirdParty(t, client, "list-empty@test.com")

	accounts, err := svc.ListUserThirdPartyAccounts(ctx, u.ID)
	if err != nil {
		t.Fatalf("获取用户第三方账号列表失败: %v", err)
	}
	if len(accounts) != 0 {
		t.Errorf("期望列表为空, 实际长度为 %d", len(accounts))
	}
}

func TestThirdPartyUserService_ListUserThirdPartyAccounts_ReturnsAll(t *testing.T) {
	client := setupThirdPartyUserTest(t)
	ctx := t.Context()
	svc := NewThirdPartyUserService()

	u := createTestUserForThirdParty(t, client, "list@test.com")
	createTestThirdPartyUser(t, client, u.ID, "wechat", "wx1")
	createTestThirdPartyUser(t, client, u.ID, "github", "gh1")
	createTestThirdPartyUser(t, client, u.ID, "google", "go1")

	accounts, err := svc.ListUserThirdPartyAccounts(ctx, u.ID)
	if err != nil {
		t.Fatalf("获取用户第三方账号列表失败: %v", err)
	}
	if len(accounts) != 3 {
		t.Errorf("期望列表长度为 3, 实际为 %d", len(accounts))
	}
}

func TestThirdPartyUserService_ListUserThirdPartyAccounts_OnlyForGivenUser(t *testing.T) {
	client := setupThirdPartyUserTest(t)
	ctx := t.Context()
	svc := NewThirdPartyUserService()

	u1 := createTestUserForThirdParty(t, client, "user1@test.com")
	u2 := createTestUserForThirdParty(t, client, "user2@test.com")
	createTestThirdPartyUser(t, client, u1.ID, "wechat", "wx-u1")
	createTestThirdPartyUser(t, client, u2.ID, "github", "gh-u2")

	accounts, err := svc.ListUserThirdPartyAccounts(ctx, u1.ID)
	if err != nil {
		t.Fatalf("获取用户1的第三方账号列表失败: %v", err)
	}
	if len(accounts) != 1 {
		t.Errorf("期望长度为 1, 实际为 %d", len(accounts))
	}
	if accounts[0].Platform != "wechat" {
		t.Errorf("期望Platform为 wechat, 实际为 %s", accounts[0].Platform)
	}
}

// ======================== ListThirdPartyUsers 测试 ========================

func TestThirdPartyUserService_ListThirdPartyUsers_Empty(t *testing.T) {
	setupThirdPartyUserTest(t)
	ctx := t.Context()
	svc := NewThirdPartyUserService()

	resp, err := svc.ListThirdPartyUsers(ctx, 1, 10, "", "")
	if err != nil {
		t.Fatalf("获取第三方用户列表失败: %v", err)
	}
	if resp.Total != 0 {
		t.Errorf("期望总数为 0, 实际为 %d", resp.Total)
	}
	if resp.Page != 1 {
		t.Errorf("期望页码为 1, 实际为 %d", resp.Page)
	}
	if len(resp.Data) != 0 {
		t.Errorf("期望数据长度为 0, 实际为 %d", len(resp.Data))
	}
}

func TestThirdPartyUserService_ListThirdPartyUsers_ReturnsAllWithUserInfo(t *testing.T) {
	client := setupThirdPartyUserTest(t)
	ctx := t.Context()
	svc := NewThirdPartyUserService()

	u1 := createTestUserForThirdParty(t, client, "user1@test.com")
	u2 := createTestUserForThirdParty(t, client, "user2@test.com")
	_, err := client.User.UpdateOne(u1).SetUsername("测试用户1").Save(ctx)
	if err != nil {
		t.Fatalf("更新用户1失败: %v", err)
	}
	_, err = client.User.UpdateOne(u2).SetUsername("测试用户2").Save(ctx)
	if err != nil {
		t.Fatalf("更新用户2失败: %v", err)
	}
	createTestThirdPartyUser(t, client, u1.ID, "wechat", "wx1")
	createTestThirdPartyUser(t, client, u2.ID, "github", "gh1")

	resp, err := svc.ListThirdPartyUsers(ctx, 1, 10, "", "")
	if err != nil {
		t.Fatalf("获取第三方用户列表失败: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("期望总数为 2, 实际为 %d", resp.Total)
	}
	if len(resp.Data) != 2 {
		t.Errorf("期望数据长度为 2, 实际为 %d", len(resp.Data))
	}
}

func TestThirdPartyUserService_ListThirdPartyUsers_PlatformFilter(t *testing.T) {
	client := setupThirdPartyUserTest(t)
	ctx := t.Context()
	svc := NewThirdPartyUserService()

	u := createTestUserForThirdParty(t, client, "filter@test.com")
	createTestThirdPartyUser(t, client, u.ID, "wechat", "wx1")
	createTestThirdPartyUser(t, client, u.ID, "github", "gh1")
	createTestThirdPartyUser(t, client, u.ID, "wechat", "wx2")

	resp, err := svc.ListThirdPartyUsers(ctx, 1, 10, "wechat", "")
	if err != nil {
		t.Fatalf("按平台筛选失败: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("期望总数为 2, 实际为 %d", resp.Total)
	}
}

func TestThirdPartyUserService_ListThirdPartyUsers_KeywordSearch(t *testing.T) {
	client := setupThirdPartyUserTest(t)
	ctx := t.Context()
	svc := NewThirdPartyUserService()

	u1 := createTestUserForThirdParty(t, client, "keyword1@test.com")
	u2 := createTestUserForThirdParty(t, client, "keyword2@test.com")
	ctx2 := t.Context()
	_, err := client.ThirdPartyUser.Create().
		SetUserID(u1.ID).
		SetPlatform("wechat").
		SetOpenid("wx-nickname").
		SetNickname("小明").
		SetStatus(thirdpartyuser.StatusActive).
		Save(ctx2)
	if err != nil {
		t.Fatalf("创建带昵称的第三方用户失败: %v", err)
	}
	_, err = client.ThirdPartyUser.Create().
		SetUserID(u2.ID).
		SetPlatform("github").
		SetOpenid("gh-company").
		SetCompany("ABC公司").
		SetStatus(thirdpartyuser.StatusActive).
		Save(ctx2)
	if err != nil {
		t.Fatalf("创建带公司的第三方用户失败: %v", err)
	}

	// 搜索昵称
	resp1, err := svc.ListThirdPartyUsers(ctx, 1, 10, "", "小明")
	if err != nil {
		t.Fatalf("按昵称搜索失败: %v", err)
	}
	if resp1.Total != 1 {
		t.Errorf("期望搜索昵称结果为 1, 实际为 %d", resp1.Total)
	}

	// 搜索公司
	resp2, err := svc.ListThirdPartyUsers(ctx, 1, 10, "", "ABC公司")
	if err != nil {
		t.Fatalf("按公司搜索失败: %v", err)
	}
	if resp2.Total != 1 {
		t.Errorf("期望搜索公司结果为 1, 实际为 %d", resp2.Total)
	}
}

func TestThirdPartyUserService_ListThirdPartyUsers_Pagination(t *testing.T) {
	client := setupThirdPartyUserTest(t)
	ctx := t.Context()
	svc := NewThirdPartyUserService()

	u := createTestUserForThirdParty(t, client, "pagination@test.com")
	for i := 1; i <= 5; i++ {
		openid := "openid-" + string(rune('0'+i))
		createTestThirdPartyUser(t, client, u.ID, "platform", openid)
	}

	// 第1页，2条
	resp1, err := svc.ListThirdPartyUsers(ctx, 1, 2, "", "")
	if err != nil {
		t.Fatalf("获取第1页失败: %v", err)
	}
	if resp1.Total != 5 {
		t.Errorf("期望总数为 5, 实际为 %d", resp1.Total)
	}
	if len(resp1.Data) != 2 {
		t.Errorf("期望第1页 2 条, 实际 %d 条", len(resp1.Data))
	}

	// 第2页，2条
	resp2, err := svc.ListThirdPartyUsers(ctx, 2, 2, "", "")
	if err != nil {
		t.Fatalf("获取第2页失败: %v", err)
	}
	if len(resp2.Data) != 2 {
		t.Errorf("期望第2页 2 条, 实际 %d 条", len(resp2.Data))
	}

	// 第3页，1条
	resp3, err := svc.ListThirdPartyUsers(ctx, 3, 2, "", "")
	if err != nil {
		t.Fatalf("获取第3页失败: %v", err)
	}
	if len(resp3.Data) != 1 {
		t.Errorf("期望第3页 1 条, 实际 %d 条", len(resp3.Data))
	}
}

func TestThirdPartyUserService_ListThirdPartyUsers_SortByCreatedAtDesc(t *testing.T) {
	client := setupThirdPartyUserTest(t)
	ctx := t.Context()
	svc := NewThirdPartyUserService()

	u := createTestUserForThirdParty(t, client, "sort@test.com")

	// 创建两个记录，使用不同时间
	_, err := client.ThirdPartyUser.Create().
		SetUserID(u.ID).
		SetPlatform("wechat").
		SetOpenid("wx1").
		SetStatus(thirdpartyuser.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建tp1失败: %v", err)
	}
	// 确保时间不同
	time.Sleep(1 * time.Millisecond)
	_, err = client.ThirdPartyUser.Create().
		SetUserID(u.ID).
		SetPlatform("github").
		SetOpenid("gh1").
		SetStatus(thirdpartyuser.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建tp2失败: %v", err)
	}

	resp, err := svc.ListThirdPartyUsers(ctx, 1, 10, "", "")
	if err != nil {
		t.Fatalf("获取列表失败: %v", err)
	}
	if len(resp.Data) < 2 {
		t.Fatalf("期望至少2条数据, 实际 %d 条", len(resp.Data))
	}

	// 第一个应该是最新的（后创建的）
	if resp.Data[0].CreatedAt.Before(resp.Data[1].CreatedAt) {
		t.Errorf("期望列表应按创建时间降序排列，但实际并非如此")
	}
}

func TestThirdPartyUserService_ListThirdPartyUsers_LastSyncAt(t *testing.T) {
	client := setupThirdPartyUserTest(t)
	ctx := t.Context()
	svc := NewThirdPartyUserService()

	u := createTestUserForThirdParty(t, client, "lastsync@test.com")
	syncTime := time.Now()
	tp, err := client.ThirdPartyUser.Create().
		SetUserID(u.ID).
		SetPlatform("wechat").
		SetOpenid("wx-with-sync").
		SetStatus(thirdpartyuser.StatusActive).
		SetLastSyncAt(syncTime).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建带同步时间的第三方用户失败: %v", err)
	}

	resp, err := svc.ListThirdPartyUsers(ctx, 1, 10, "", "")
	if err != nil {
		t.Fatalf("获取列表失败: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("期望1条数据, 实际 %d 条", len(resp.Data))
	}
	if resp.Data[0].ID != tp.ID {
		t.Fatalf("期望ID为 %d, 实际为 %d", tp.ID, resp.Data[0].ID)
	}
	if resp.Data[0].LastSyncAt == nil || !resp.Data[0].LastSyncAt.Equal(syncTime) {
		t.Errorf("期望LastSyncAt正确设置")
	}
}
