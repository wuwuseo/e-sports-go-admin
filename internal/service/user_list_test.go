package service

import (
	"errors"
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func setupUserListTest(t *testing.T) *ent.Client {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
	return client
}

func createTestUserForList(t *testing.T, client *ent.Client, email, uid, username, nickname string, status user.Status) *ent.User {
	ctx := t.Context()
	u, err := client.User.Create().
		SetEmail(email).
		SetUID(uid).
		SetUsername(username).
		SetNickname(nickname).
		SetPassword("password").
		SetStatus(status).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}
	return u
}

// ======================== ListUsers 测试 ========================

func TestUserListService_ListUsers_EmptyList(t *testing.T) {
	setupUserListTest(t)
	ctx := t.Context()

	svc := NewUserListService()
	req := &UserListRequest{Page: 1, PageSize: 10}

	resp, err := svc.ListUsers(ctx, req)
	if err != nil {
		t.Fatalf("查询用户列表失败: %v", err)
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

func TestUserListService_ListUsers_ReturnsAllUsersWithCorrectFields(t *testing.T) {
	client := setupUserListTest(t)
	ctx := t.Context()

	u1 := createTestUserForList(t, client, "user1@test.com", "uid-001", "user1", "用户1", user.StatusActive)
	u2 := createTestUserForList(t, client, "user2@test.com", "uid-002", "user2", "用户2", user.StatusActive)

	svc := NewUserListService()
	req := &UserListRequest{Page: 1, PageSize: 10}

	resp, err := svc.ListUsers(ctx, req)
	if err != nil {
		t.Fatalf("查询用户列表失败: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("期望总数为 2, 实际为 %d", resp.Total)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("期望数据长度为 2, 实际为 %d", len(resp.Data))
	}

	idSet := make(map[int]bool)
	for _, item := range resp.Data {
		idSet[item.ID] = true
		switch item.ID {
		case u1.ID:
			if item.UID != "uid-001" {
				t.Errorf("期望 UID 为 uid-001, 实际为 %s", item.UID)
			}
			if item.Email != "user1@test.com" {
				t.Errorf("期望 Email 为 user1@test.com, 实际为 %s", item.Email)
			}
			if item.Username != "user1" {
				t.Errorf("期望 Username 为 user1, 实际为 %s", item.Username)
			}
			if item.Nickname != "用户1" {
				t.Errorf("期望 Nickname 为 用户1, 实际为 %s", item.Nickname)
			}
			if item.Status != string(user.StatusActive) {
				t.Errorf("期望 Status 为 active, 实际为 %s", item.Status)
			}
			if item.IsBlacklisted {
				t.Error("期望 IsBlacklisted 为 false")
			}
			if item.CreateTime.IsZero() {
				t.Error("期望 CreateTime 不为零值")
			}
		case u2.ID:
			if item.UID != "uid-002" {
				t.Errorf("期望 UID 为 uid-002, 实际为 %s", item.UID)
			}
			if item.Email != "user2@test.com" {
				t.Errorf("期望 Email 为 user2@test.com, 实际为 %s", item.Email)
			}
		}
	}
	if !idSet[u1.ID] {
		t.Error("期望结果包含用户1")
	}
	if !idSet[u2.ID] {
		t.Error("期望结果包含用户2")
	}
}

func TestUserListService_ListUsers_KeywordFilterByUsername(t *testing.T) {
	client := setupUserListTest(t)
	ctx := t.Context()

	createTestUserForList(t, client, "alice@test.com", "uid-alice", "alice_wang", "王丽", user.StatusActive)
	createTestUserForList(t, client, "bob@test.com", "uid-bob", "bob_li", "李波", user.StatusActive)
	createTestUserForList(t, client, "charlie@test.com", "uid-charlie", "charlie_zhang", "张超", user.StatusActive)

	svc := NewUserListService()
	req := &UserListRequest{Page: 1, PageSize: 10, Keyword: "alice"}

	resp, err := svc.ListUsers(ctx, req)
	if err != nil {
		t.Fatalf("关键字搜索用户列表失败: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("期望总数为 1, 实际为 %d", resp.Total)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("期望数据长度为 1, 实际为 %d", len(resp.Data))
	}
	if resp.Data[0].Username != "alice_wang" {
		t.Errorf("期望用户名为 alice_wang, 实际为 %s", resp.Data[0].Username)
	}
}

func TestUserListService_ListUsers_KeywordFilterByEmail(t *testing.T) {
	client := setupUserListTest(t)
	ctx := t.Context()

	createTestUserForList(t, client, "john@example.com", "uid-john", "john", "John", user.StatusActive)
	createTestUserForList(t, client, "jane@test.org", "uid-jane", "jane", "Jane", user.StatusActive)
	createTestUserForList(t, client, "bob@test.org", "uid-bob", "bob", "Bob", user.StatusActive)

	svc := NewUserListService()
	req := &UserListRequest{Page: 1, PageSize: 10, Keyword: "example"}

	resp, err := svc.ListUsers(ctx, req)
	if err != nil {
		t.Fatalf("邮箱关键字搜索用户列表失败: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("期望总数为 1, 实际为 %d", resp.Total)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("期望数据长度为 1, 实际为 %d", len(resp.Data))
	}
	if resp.Data[0].Email != "john@example.com" {
		t.Errorf("期望邮箱为 john@example.com, 实际为 %s", resp.Data[0].Email)
	}
}

func TestUserListService_ListUsers_StatusFilter(t *testing.T) {
	client := setupUserListTest(t)
	ctx := t.Context()

	createTestUserForList(t, client, "active1@test.com", "uid-active1", "active1", "活跃1", user.StatusActive)
	createTestUserForList(t, client, "inactive1@test.com", "uid-inactive1", "inactive1", "停用1", user.StatusInactive)
	createTestUserForList(t, client, "active2@test.com", "uid-active2", "active2", "活跃2", user.StatusActive)

	svc := NewUserListService()
	req := &UserListRequest{Page: 1, PageSize: 10, Status: "active"}

	resp, err := svc.ListUsers(ctx, req)
	if err != nil {
		t.Fatalf("状态筛选用户列表失败: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("期望总数为 2, 实际为 %d", resp.Total)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("期望数据长度为 2, 实际为 %d", len(resp.Data))
	}
	for _, item := range resp.Data {
		if item.Status != string(user.StatusActive) {
			t.Errorf("期望状态为 active, 实际为 %s", item.Status)
		}
	}
}

func TestUserListService_ListUsers_Pagination(t *testing.T) {
	client := setupUserListTest(t)
	ctx := t.Context()

	for i := 0; i < 5; i++ {
		email := "page" + string(rune('0'+i)) + "@test.com"
		uid := "uid-page-" + string(rune('0'+i))
		username := "page" + string(rune('0'+i))
		nickname := "分页" + string(rune('0'+i))
		createTestUserForList(t, client, email, uid, username, nickname, user.StatusActive)
	}

	svc := NewUserListService()

	resp1, err := svc.ListUsers(ctx, &UserListRequest{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("查询第一页失败: %v", err)
	}
	if resp1.Total != 5 {
		t.Errorf("期望总数为 5, 实际为 %d", resp1.Total)
	}
	if len(resp1.Data) != 2 {
		t.Errorf("期望第一页返回 2 条, 实际返回 %d 条", len(resp1.Data))
	}
	if resp1.Page != 1 {
		t.Errorf("期望当前页为 1, 实际为 %d", resp1.Page)
	}

	resp2, err := svc.ListUsers(ctx, &UserListRequest{Page: 2, PageSize: 2})
	if err != nil {
		t.Fatalf("查询第二页失败: %v", err)
	}
	if len(resp2.Data) != 2 {
		t.Errorf("期望第二页返回 2 条, 实际返回 %d 条", len(resp2.Data))
	}

	resp3, err := svc.ListUsers(ctx, &UserListRequest{Page: 3, PageSize: 2})
	if err != nil {
		t.Fatalf("查询第三页失败: %v", err)
	}
	if len(resp3.Data) != 1 {
		t.Errorf("期望第三页返回 1 条, 实际返回 %d 条", len(resp3.Data))
	}
}

func TestUserListService_ListUsers_RoleIDFilter(t *testing.T) {
	client := setupUserListTest(t)
	ctx := t.Context()

	u1 := createTestUserForList(t, client, "role1@test.com", "uid-role1", "role1", "角色1", user.StatusActive)
	createTestUserForList(t, client, "norole@test.com", "uid-norole", "norole", "无角色", user.StatusActive)

	role, err := client.Role.Create().SetName("test-role").SetDisplayName("测试角色").Save(ctx)
	if err != nil {
		t.Fatalf("创建测试角色失败: %v", err)
	}

	_, err = client.UserRole.Create().
		SetUserID(u1.ID).
		SetRoleID(role.ID).
		SetIsPrimary(true).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建用户角色关联失败: %v", err)
	}

	svc := NewUserListService()
	req := &UserListRequest{Page: 1, PageSize: 10, RoleID: role.ID}

	resp, err := svc.ListUsers(ctx, req)
	if err != nil {
		t.Fatalf("按角色筛选用户列表失败: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("期望总数为 1, 实际为 %d", resp.Total)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("期望数据长度为 1, 实际为 %d", len(resp.Data))
	}
	if resp.Data[0].ID != u1.ID {
		t.Errorf("期望用户ID为 %d, 实际为 %d", u1.ID, resp.Data[0].ID)
	}
}

func TestUserListService_ListUsers_BlacklistedFilter(t *testing.T) {
	client := setupUserListTest(t)
	ctx := t.Context()

	u1 := createTestUserForList(t, client, "blocked@test.com", "uid-blocked", "blocked", "已拉黑", user.StatusActive)
	createTestUserForList(t, client, "normal@test.com", "uid-normal", "normal", "正常", user.StatusActive)

	_, err := client.Blacklist.Create().
		SetUserID(u1.ID).
		SetReason("test").
		SetOperatorID("op").
		SetOperatorName("op").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建黑名单记录失败: %v", err)
	}

	svc := NewUserListService()
	isBlacklisted := true
	req := &UserListRequest{Page: 1, PageSize: 10, IsBlacklisted: &isBlacklisted}

	resp, err := svc.ListUsers(ctx, req)
	if err != nil {
		t.Fatalf("按黑名单筛选用户列表失败: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("期望总数为 1, 实际为 %d", resp.Total)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("期望数据长度为 1, 实际为 %d", len(resp.Data))
	}
	if resp.Data[0].Email != "blocked@test.com" {
		t.Errorf("期望邮箱为 blocked@test.com, 实际为 %s", resp.Data[0].Email)
	}
	if !resp.Data[0].IsBlacklisted {
		t.Error("期望 IsBlacklisted 为 true")
	}
}

func TestUserListService_ListUsers_NotBlacklistedFilter(t *testing.T) {
	client := setupUserListTest(t)
	ctx := t.Context()

	u1 := createTestUserForList(t, client, "blocked2@test.com", "uid-blocked2", "blocked2", "已拉黑2", user.StatusActive)
	u2 := createTestUserForList(t, client, "normal2@test.com", "uid-normal2", "normal2", "正常2", user.StatusActive)

	_, err := client.Blacklist.Create().
		SetUserID(u1.ID).
		SetReason("test").
		SetOperatorID("op").
		SetOperatorName("op").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建黑名单记录失败: %v", err)
	}

	svc := NewUserListService()
	isBlacklisted := false
	req := &UserListRequest{Page: 1, PageSize: 10, IsBlacklisted: &isBlacklisted}

	resp, err := svc.ListUsers(ctx, req)
	if err != nil {
		t.Fatalf("按非黑名单筛选用户列表失败: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("期望总数为 1, 实际为 %d", resp.Total)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("期望数据长度为 1, 实际为 %d", len(resp.Data))
	}
	if resp.Data[0].ID != u2.ID {
		t.Errorf("期望用户ID为 %d, 实际为 %d", u2.ID, resp.Data[0].ID)
	}
	if resp.Data[0].IsBlacklisted {
		t.Error("期望 IsBlacklisted 为 false")
	}
}

func TestUserListService_ListUsers_TimeRangeFilter(t *testing.T) {
	client := setupUserListTest(t)
	ctx := t.Context()

	createTestUserForList(t, client, "old@test.com", "uid-old", "old", "旧用户", user.StatusActive)
	createTestUserForList(t, client, "new@test.com", "uid-new", "new", "新用户", user.StatusActive)

	svc := NewUserListService()
	futureTime := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	req := &UserListRequest{
		Page:      1,
		PageSize:  10,
		EndTime:   futureTime,
	}

	resp, err := svc.ListUsers(ctx, req)
	if err != nil {
		t.Fatalf("按时间范围筛选用户列表失败: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("期望总数为 2（EndTime 在未来应包含全部）, 实际为 %d", resp.Total)
	}

	startTime := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	req2 := &UserListRequest{
		Page:      1,
		PageSize:  10,
		StartTime: startTime,
	}
	resp2, err := svc.ListUsers(ctx, req2)
	if err != nil {
		t.Fatalf("按未来StartTime筛选失败: %v", err)
	}
	if resp2.Total != 0 {
		t.Errorf("期望总数为 0（StartTime 在未来应无结果）, 实际为 %d", resp2.Total)
	}
}

func TestUserListService_ListUsers_SortByLastLoginDesc(t *testing.T) {
	client := setupUserListTest(t)
	ctx := t.Context()

	u1 := createTestUserForList(t, client, "sort1@test.com", "uid-sort1", "sort1", "排序1", user.StatusActive)
	u2 := createTestUserForList(t, client, "sort2@test.com", "uid-sort2", "sort2", "排序2", user.StatusActive)

	login1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	login2 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	_, err := client.User.UpdateOneID(u1.ID).SetStatus(user.StatusActive).SetLastLogin(login1).Save(ctx)
	if err != nil {
		t.Fatalf("更新用户最后登录时间失败: %v", err)
	}
	_, err = client.User.UpdateOneID(u2.ID).SetStatus(user.StatusActive).SetLastLogin(login2).Save(ctx)
	if err != nil {
		t.Fatalf("更新用户最后登录时间失败: %v", err)
	}

	svc := NewUserListService()
	req := &UserListRequest{Page: 1, PageSize: 10, SortField: "last_login", SortOrder: "desc"}

	resp, err := svc.ListUsers(ctx, req)
	if err != nil {
		t.Fatalf("按最后登录时间降序排列失败: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("期望总数为 2, 实际为 %d", resp.Total)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("期望数据长度为 2, 实际为 %d", len(resp.Data))
	}
	if resp.Data[0].ID != u2.ID {
		t.Errorf("期望第一个用户为最近登录的 u2(ID=%d), 实际为 %d", u2.ID, resp.Data[0].ID)
	}
}

func TestUserListService_ListUsers_SortByNameAsc(t *testing.T) {
	client := setupUserListTest(t)
	ctx := t.Context()

	createTestUserForList(t, client, "aa@test.com", "uid-aa", "aa", "AA", user.StatusActive)
	createTestUserForList(t, client, "bb@test.com", "uid-bb", "bb", "BB", user.StatusActive)

	client.User.Update().Where(user.UsernameEQ("aa")).SetName("A").Exec(ctx)
	client.User.Update().Where(user.UsernameEQ("bb")).SetName("B").Exec(ctx)

	svc := NewUserListService()
	req := &UserListRequest{Page: 1, PageSize: 10, SortField: "name", SortOrder: "asc"}

	resp, err := svc.ListUsers(ctx, req)
	if err != nil {
		t.Fatalf("按名称升序排列失败: %v", err)
	}
	if len(resp.Data) < 2 {
		t.Fatalf("期望至少 2 条数据, 实际为 %d 条", len(resp.Data))
	}
	if resp.Data[0].Name != "A" {
		t.Errorf("期望第一个用户 Name 为 A, 实际为 %s", resp.Data[0].Name)
	}
	if resp.Data[1].Name != "B" {
		t.Errorf("期望第二个用户 Name 为 B, 实际为 %s", resp.Data[1].Name)
	}
}

func TestUserListService_ListUsers_PlatformFilter(t *testing.T) {
	client := setupUserListTest(t)
	ctx := t.Context()

	u1 := createTestUserForList(t, client, "wechat@test.com", "uid-wechat", "wechat", "微信用户", user.StatusActive)
	createTestUserForList(t, client, "noplat@test.com", "uid-noplat", "noplat", "无平台", user.StatusActive)

	_, err := client.ThirdPartyUser.Create().
		SetUserID(u1.ID).
		SetPlatform("wechat").
		SetOpenid("wx-openid-123").
		SetPlatformName("微信").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建第三方用户记录失败: %v", err)
	}

	svc := NewUserListService()
	req := &UserListRequest{Page: 1, PageSize: 10, Platform: "wechat"}

	resp, err := svc.ListUsers(ctx, req)
	if err != nil {
		t.Fatalf("按平台筛选用户列表失败: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("期望总数为 1, 实际为 %d", resp.Total)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("期望数据长度为 1, 实际为 %d", len(resp.Data))
	}
	if resp.Data[0].ID != u1.ID {
		t.Errorf("期望用户ID为 %d, 实际为 %d", u1.ID, resp.Data[0].ID)
	}
}

// ======================== GetUserDetail 测试 ========================

func TestUserListService_GetUserDetail_Found(t *testing.T) {
	client := setupUserListTest(t)
	ctx := t.Context()

	u := createTestUserForList(t, client, "detail@test.com", "uid-detail", "detail_user", "详情用户", user.StatusActive)

	role, err := client.Role.Create().SetName("detail-role").SetDisplayName("详情角色").Save(ctx)
	if err != nil {
		t.Fatalf("创建测试角色失败: %v", err)
	}
	_, err = client.UserRole.Create().
		SetUserID(u.ID).
		SetRoleID(role.ID).
		SetIsPrimary(true).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建用户角色关联失败: %v", err)
	}

	_, err = client.ThirdPartyUser.Create().
		SetUserID(u.ID).
		SetPlatform("github").
		SetOpenid("gh-openid-456").
		SetPlatformName("GitHub").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建第三方用户记录失败: %v", err)
	}

	svc := NewUserListService()
	detail, err := svc.GetUserDetail(ctx, u.ID)
	if err != nil {
		t.Fatalf("获取用户详情失败: %v", err)
	}

	if detail.ID != u.ID {
		t.Errorf("期望用户ID为 %d, 实际为 %d", u.ID, detail.ID)
	}
	if detail.Email != "detail@test.com" {
		t.Errorf("期望邮箱为 detail@test.com, 实际为 %s", detail.Email)
	}
	if detail.UID != "uid-detail" {
		t.Errorf("期望 UID 为 uid-detail, 实际为 %s", detail.UID)
	}
	if detail.Username != "detail_user" {
		t.Errorf("期望用户名为 detail_user, 实际为 %s", detail.Username)
	}
	if detail.Nickname != "详情用户" {
		t.Errorf("期望昵称为 详情用户, 实际为 %s", detail.Nickname)
	}
	if detail.Status != string(user.StatusActive) {
		t.Errorf("期望状态为 active, 实际为 %s", detail.Status)
	}
	if detail.CreateTime.IsZero() {
		t.Error("期望 CreateTime 不为零值")
	}

	if len(detail.Roles) != 1 {
		t.Errorf("期望角色数量为 1, 实际为 %d", len(detail.Roles))
	} else {
		if detail.Roles[0].Name != "detail-role" {
			t.Errorf("期望角色名称为 detail-role, 实际为 %s", detail.Roles[0].Name)
		}
		if detail.Roles[0].DisplayName != "详情角色" {
			t.Errorf("期望角色显示名称为 详情角色, 实际为 %s", detail.Roles[0].DisplayName)
		}
		if !detail.Roles[0].IsPrimary {
			t.Error("期望角色为主角色")
		}
	}

	if detail.ThirdPartyCount != 1 {
		t.Errorf("期望第三方账号数量为 1, 实际为 %d", detail.ThirdPartyCount)
	}
	if len(detail.ThirdPartyAccounts) != 1 {
		t.Errorf("期望第三方账号列表长度为 1, 实际为 %d", len(detail.ThirdPartyAccounts))
	} else {
		if detail.ThirdPartyAccounts[0].Platform != "github" {
			t.Errorf("期望平台为 github, 实际为 %s", detail.ThirdPartyAccounts[0].Platform)
		}
		if detail.ThirdPartyAccounts[0].Openid != "gh-openid-456" {
			t.Errorf("期望 Openid 为 gh-openid-456, 实际为 %s", detail.ThirdPartyAccounts[0].Openid)
		}
	}
}

func TestUserListService_GetUserDetail_NotFound(t *testing.T) {
	setupUserListTest(t)
	ctx := t.Context()

	svc := NewUserListService()
	_, err := svc.GetUserDetail(ctx, 99999)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("期望错误为 ErrUserNotFound, 实际错误: %v", err)
	}
}
