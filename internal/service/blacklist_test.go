package service

import (
	"errors"
	"testing"
	"time"

	"go-admin/internal/ent/blacklist"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func setupBlacklistTest(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
}

func createTestUser(t *testing.T, email, uid, username, nickname string) (int, string) {
	ctx := t.Context()
	u, err := global.ORM.User.Create().
		SetEmail(email).
		SetPassword("password123").
		SetUID(uid).
		SetUsername(username).
		SetNickname(nickname).
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}
	return u.ID, u.UID
}

// ======================== AddToBlacklist 测试 ========================

func TestBlacklistService_AddToBlacklist_Success(t *testing.T) {
	setupBlacklistTest(t)
	ctx := t.Context()
	svc := NewBlacklistService(nil)

	userID, _ := createTestUser(t, "blacklist_test@example.com", "uid-blacklist-test", "blacklist_test", "黑名单测试用户")

	err := svc.AddToBlacklist(ctx, userID, "违规操作", "admin-uid", "管理员")
	if err != nil {
		t.Fatalf("加入黑名单失败: %v", err)
	}

	u, err := global.ORM.User.Get(ctx, userID)
	if err != nil {
		t.Fatalf("查询用户失败: %v", err)
	}
	if u.Status != user.StatusSuspended {
		t.Errorf("期望用户状态为 %s, 实际为 %s", user.StatusSuspended, u.Status)
	}

	exist, err := global.ORM.Blacklist.Query().Where(blacklist.UserIDEQ(userID)).Exist(ctx)
	if err != nil {
		t.Fatalf("查询黑名单记录失败: %v", err)
	}
	if !exist {
		t.Fatal("期望黑名单记录存在，但不存在")
	}
}

func TestBlacklistService_AddToBlacklist_UserNotFound(t *testing.T) {
	setupBlacklistTest(t)
	ctx := t.Context()
	svc := NewBlacklistService(nil)

	err := svc.AddToBlacklist(ctx, 99999, "违规操作", "admin-uid", "管理员")
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("期望错误为 ErrUserNotFound, 实际为 %v", err)
	}
}

func TestBlacklistService_AddToBlacklist_CannotBlacklistSelf(t *testing.T) {
	setupBlacklistTest(t)
	ctx := t.Context()
	svc := NewBlacklistService(nil)

	userID, uid := createTestUser(t, "self_block@example.com", "uid-self-block", "self_block", "自己拉黑测试")

	err := svc.AddToBlacklist(ctx, userID, "违规操作", uid, "自己")
	if err == nil {
		t.Fatal("期望返回不能拉黑自己的错误，但未返回")
	}
	if !errors.Is(err, ErrCannotBlacklistSelf) {
		t.Errorf("期望错误为 ErrCannotBlacklistSelf, 实际为 %v", err)
	}
}

func TestBlacklistService_AddToBlacklist_AlreadyBlacklisted(t *testing.T) {
	setupBlacklistTest(t)
	ctx := t.Context()
	svc := NewBlacklistService(nil)

	userID, _ := createTestUser(t, "already_black@example.com", "uid-already-black", "already_black", "已拉黑用户")

	err := svc.AddToBlacklist(ctx, userID, "首次拉黑", "admin-uid", "管理员")
	if err != nil {
		t.Fatalf("首次加入黑名单失败: %v", err)
	}

	err = svc.AddToBlacklist(ctx, userID, "重复拉黑", "admin-uid", "管理员")
	if err == nil {
		t.Fatal("期望返回已拉黑错误，但未返回")
	}
	if !errors.Is(err, ErrUserBlacklisted) {
		t.Errorf("期望错误为 ErrUserBlacklisted, 实际为 %v", err)
	}
}

// ======================== RemoveFromBlacklist 测试 ========================

func TestBlacklistService_RemoveFromBlacklist_Success(t *testing.T) {
	setupBlacklistTest(t)
	ctx := t.Context()
	svc := NewBlacklistService(nil)

	userID, _ := createTestUser(t, "remove_black@example.com", "uid-remove-black", "remove_black", "移除黑名单测试")

	err := svc.AddToBlacklist(ctx, userID, "违规", "admin-uid", "管理员")
	if err != nil {
		t.Fatalf("加入黑名单失败: %v", err)
	}

	err = svc.RemoveFromBlacklist(ctx, userID, "admin-uid")
	if err != nil {
		t.Fatalf("移除黑名单失败: %v", err)
	}

	u, err := global.ORM.User.Get(ctx, userID)
	if err != nil {
		t.Fatalf("查询用户失败: %v", err)
	}
	if u.Status != user.StatusInactive {
		t.Errorf("期望用户状态为 %s, 实际为 %s", user.StatusInactive, u.Status)
	}

	exist, err := global.ORM.Blacklist.Query().Where(blacklist.UserIDEQ(userID)).Exist(ctx)
	if err != nil {
		t.Fatalf("查询黑名单记录失败: %v", err)
	}
	if exist {
		t.Fatal("期望黑名单记录已删除，但仍存在")
	}
}

func TestBlacklistService_RemoveFromBlacklist_UserNotFound(t *testing.T) {
	setupBlacklistTest(t)
	ctx := t.Context()
	svc := NewBlacklistService(nil)

	err := svc.RemoveFromBlacklist(ctx, 99999, "admin-uid")
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("期望错误为 ErrUserNotFound, 实际为 %v", err)
	}
}

func TestBlacklistService_RemoveFromBlacklist_NotBlacklisted(t *testing.T) {
	setupBlacklistTest(t)
	ctx := t.Context()
	svc := NewBlacklistService(nil)

	userID, _ := createTestUser(t, "not_black@example.com", "uid-not-black", "not_black", "未拉黑用户")

	err := svc.RemoveFromBlacklist(ctx, userID, "admin-uid")
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrUserNotBlacklisted) {
		t.Errorf("期望错误为 ErrUserNotBlacklisted, 实际为 %v", err)
	}
}

// ======================== ListBlacklist 测试 ========================

func TestBlacklistService_ListBlacklist_NoData(t *testing.T) {
	setupBlacklistTest(t)
	ctx := t.Context()
	svc := NewBlacklistService(nil)

	resp, err := svc.ListBlacklist(ctx, 1, 10)
	if err != nil {
		t.Fatalf("获取黑名单列表失败: %v", err)
	}
	if resp.Total != 0 {
		t.Errorf("期望总数为 0, 实际为 %d", resp.Total)
	}
	if len(resp.Data) != 0 {
		t.Errorf("期望数据长度为 0, 实际为 %d", len(resp.Data))
	}
}

func TestBlacklistService_ListBlacklist_WithData(t *testing.T) {
	setupBlacklistTest(t)
	ctx := t.Context()
	svc := NewBlacklistService(nil)

	userID, userUID := createTestUser(t, "list_black@example.com", "uid-list-black", "list_black", "列表黑名单用户")

	err := svc.AddToBlacklist(ctx, userID, "测试原因", "op-uid", "操作人")
	if err != nil {
		t.Fatalf("加入黑名单失败: %v", err)
	}

	resp, err := svc.ListBlacklist(ctx, 1, 10)
	if err != nil {
		t.Fatalf("获取黑名单列表失败: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("期望总数为 1, 实际为 %d", resp.Total)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("期望数据长度为 1, 实际为 %d", len(resp.Data))
	}

	item := resp.Data[0]
	if item.UserID != userID {
		t.Errorf("期望用户ID为 %d, 实际为 %d", userID, item.UserID)
	}
	if item.UserUID != userUID {
		t.Errorf("期望用户UID为 %s, 实际为 %s", userUID, item.UserUID)
	}
	if item.UserName != "list_black" {
		t.Errorf("期望用户名为 list_black, 实际为 %s", item.UserName)
	}
	if item.UserEmail != "list_black@example.com" {
		t.Errorf("期望邮箱为 list_black@example.com, 实际为 %s", item.UserEmail)
	}
	if item.Reason != "测试原因" {
		t.Errorf("期望原因为 测试原因, 实际为 %s", item.Reason)
	}
	if item.OperatorID != "op-uid" {
		t.Errorf("期望操作人ID为 op-uid, 实际为 %s", item.OperatorID)
	}
	if item.OperatorName != "操作人" {
		t.Errorf("期望操作人姓名为 操作人, 实际为 %s", item.OperatorName)
	}
	if item.CreatedAt.IsZero() {
		t.Error("期望创建时间不为零值")
	}
}

func TestBlacklistService_ListBlacklist_Pagination(t *testing.T) {
	setupBlacklistTest(t)
	ctx := t.Context()
	svc := NewBlacklistService(nil)

	for i := 0; i < 5; i++ {
		email := "page_black" + string(rune('0'+i)) + "@example.com"
		uid := "uid-page-black-" + string(rune('0'+i))
		userID, _ := createTestUser(t, email, uid, "page_black_"+string(rune('0'+i)), "分页测试"+string(rune('0'+i)))

		err := svc.AddToBlacklist(ctx, userID, "分页测试原因"+string(rune('0'+i)), "op-uid", "操作人")
		if err != nil {
			t.Fatalf("加入黑名单失败: %v", err)
		}
	}

	resp, err := svc.ListBlacklist(ctx, 1, 2)
	if err != nil {
		t.Fatalf("获取第一页黑名单列表失败: %v", err)
	}
	if resp.Total != 5 {
		t.Errorf("期望总数为 5, 实际为 %d", resp.Total)
	}
	if len(resp.Data) != 2 {
		t.Errorf("期望第一页返回 2 条, 实际返回 %d 条", len(resp.Data))
	}
	if resp.Page != 1 {
		t.Errorf("期望当前页为 1, 实际为 %d", resp.Page)
	}

	resp2, err := svc.ListBlacklist(ctx, 2, 2)
	if err != nil {
		t.Fatalf("获取第二页黑名单列表失败: %v", err)
	}
	if len(resp2.Data) != 2 {
		t.Errorf("期望第二页返回 2 条, 实际返回 %d 条", len(resp2.Data))
	}

	resp3, err := svc.ListBlacklist(ctx, 3, 2)
	if err != nil {
		t.Fatalf("获取第三页黑名单列表失败: %v", err)
	}
	if len(resp3.Data) != 1 {
		t.Errorf("期望第三页返回 1 条, 实际返回 %d 条", len(resp3.Data))
	}
}

// ======================== IsBlacklisted 测试 ========================

func TestBlacklistService_IsBlacklisted_NotBlacklisted(t *testing.T) {
	setupBlacklistTest(t)
	ctx := t.Context()
	svc := NewBlacklistService(nil)

	isBlacklisted, err := svc.IsBlacklisted(ctx, "uid-not-exist")
	if err != nil {
		t.Fatalf("检查黑名单状态失败: %v", err)
	}
	if isBlacklisted {
		t.Error("期望用户不在黑名单中，但返回了 true")
	}
}

func TestBlacklistService_IsBlacklisted_IsBlacklisted(t *testing.T) {
	setupBlacklistTest(t)
	ctx := t.Context()
	svc := NewBlacklistService(nil)

	_, uid := createTestUser(t, "is_black@example.com", "uid-is-black", "is_black", "黑名单检查测试")

	isBlacklisted, err := svc.IsBlacklisted(ctx, uid)
	if err != nil {
		t.Fatalf("添加前检查黑名单状态失败: %v", err)
	}
	if isBlacklisted {
		t.Error("添加前期望用户不在黑名单中，但返回了 true")
	}

	u, err := global.ORM.User.Query().Where(user.UIDEQ(uid)).Only(ctx)
	if err != nil {
		t.Fatalf("查询用户失败: %v", err)
	}

	err = svc.AddToBlacklist(ctx, u.ID, "违规", "admin-uid", "管理员")
	if err != nil {
		t.Fatalf("加入黑名单失败: %v", err)
	}

	isBlacklisted, err = svc.IsBlacklisted(ctx, uid)
	if err != nil {
		t.Fatalf("添加后检查黑名单状态失败: %v", err)
	}
	if !isBlacklisted {
		t.Error("期望用户已被加入黑名单，但返回了 false")
	}
}

// ======================== IsLoggedOut / RecordLogout 测试 ========================

func TestBlacklistService_IsLoggedOut_NoCache(t *testing.T) {
	setupBlacklistTest(t)
	ctx := t.Context()
	svc := NewBlacklistService(nil)

	isLoggedOut, err := svc.IsLoggedOut(ctx, "uid-any")
	if err != nil {
		t.Fatalf("检查登出状态失败: %v", err)
	}
	if isLoggedOut {
		t.Error("无缓存时期望 IsLoggedOut 返回 false, 实际返回 true")
	}
}

func TestBlacklistService_RecordLogout_NoCache(t *testing.T) {
	setupBlacklistTest(t)
	ctx := t.Context()
	svc := NewBlacklistService(nil)

	err := svc.RecordLogout(ctx, "uid-any", time.Hour)
	if err != nil {
		t.Fatalf("无缓存时记录登出不期望返回错误, 实际返回: %v", err)
	}
}
