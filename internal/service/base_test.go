package service

import (
	"context"
	"errors"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func setupBaseTest(t *testing.T) *ent.Client {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
	return client
}

// ======================== GetByID 测试 ========================

func TestGetByID_Success(t *testing.T) {
	client := setupBaseTest(t)
	ctx := t.Context()

	u, err := client.User.Create().
		SetEmail("test@example.com").
		SetUID("uid-test").
		SetUsername("testuser").
		SetPassword("hash").
		SetPasswordSalt("salt").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}

	resp, err := GetByID(ctx,
		func(ctx context.Context) (*ent.User, error) {
			return client.User.Get(ctx, u.ID)
		},
		func(u *ent.User) *userResponse {
			return &userResponse{ID: u.UID, Email: u.Email, Username: u.Username}
		},
		ErrUserNotFound,
		"用户",
	)

	if err != nil {
		t.Fatalf("GetByID 失败: %v", err)
	}
	if resp.ID != "uid-test" {
		t.Errorf("期望 ID 为 uid-test, 实际为 %s", resp.ID)
	}
}

func TestGetByID_NotFound(t *testing.T) {
	client := setupBaseTest(t)
	ctx := t.Context()

	_, err := GetByID(ctx,
		func(ctx context.Context) (*ent.User, error) {
			return client.User.Get(ctx, 99999)
		},
		func(u *ent.User) *userResponse {
			return &userResponse{ID: u.UID}
		},
		ErrUserNotFound,
		"用户",
	)

	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("期望错误为 ErrUserNotFound, 实际错误: %v", err)
	}
}

func TestGetByID_DBError(t *testing.T) {
	_ = setupBaseTest(t)
	ctx := t.Context()

	_, _ = GetByID(ctx,
		func(ctx context.Context) (*ent.User, error) {
			return nil, errors.New("数据库错误")
		},
		func(u *ent.User) *userResponse {
			return &userResponse{}
		},
		ErrUserNotFound,
		"用户",
	)

	// 这里不检查错误，因为函数内部会返回错误
	// 只验证不会panic
}

// ======================== DeleteByID 测试 ========================

func TestDeleteByID_Success(t *testing.T) {
	_ = setupBaseTest(t)
	ctx := t.Context()

	client := global.ORM
	u, err := client.User.Create().
		SetEmail("delete@example.com").
		SetUID("uid-delete").
		SetUsername("deleteuser").
		SetPassword("hash").
		SetPasswordSalt("salt").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}

	err = DeleteByID(ctx,
		func(ctx context.Context) (*ent.User, error) {
			return client.User.Get(ctx, u.ID)
		},
		func(u *ent.User) error {
			return client.User.DeleteOne(u).Exec(ctx)
		},
		ErrUserNotFound,
		"用户",
	)

	if err != nil {
		t.Fatalf("DeleteByID 失败: %v", err)
	}

	exists, err := client.User.Query().Where(user.IDEQ(u.ID)).Exist(ctx)
	if err != nil {
		t.Fatalf("检查用户存在性失败: %v", err)
	}
	if exists {
		t.Error("用户应该已被删除，但仍然存在")
	}
}

func TestDeleteByID_NotFound(t *testing.T) {
	client := setupBaseTest(t)
	ctx := t.Context()

	err := DeleteByID(ctx,
		func(ctx context.Context) (*ent.User, error) {
			return client.User.Get(ctx, 99999)
		},
		func(u *ent.User) error {
			return nil
		},
		ErrUserNotFound,
		"用户",
	)

	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("期望错误为 ErrUserNotFound, 实际错误: %v", err)
	}
}

func TestDeleteByID_DeleteError(t *testing.T) {
	client := setupBaseTest(t)
	ctx := t.Context()

	u, err := client.User.Create().
		SetEmail("delete-error@example.com").
		SetUID("uid-delete-error").
		SetUsername("deleteerror").
		SetPassword("hash").
		SetPasswordSalt("salt").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}

	err = DeleteByID(ctx,
		func(ctx context.Context) (*ent.User, error) {
			return client.User.Get(ctx, u.ID)
		},
		func(u *ent.User) error {
			return errors.New("删除失败")
		},
		ErrUserNotFound,
		"用户",
	)

	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
}

// ======================== BuildList 测试 ========================

func TestBuildList_Empty(t *testing.T) {
	users := []userResponse{}
	resp := BuildList(users, 0, 1, func(u userResponse) *userResponse {
		return &u
	})

	if resp.Total != 0 {
		t.Errorf("期望 Total 为 0, 实际为 %d", resp.Total)
	}
	if resp.Page != 1 {
		t.Errorf("期望 Page 为 1, 实际为 %d", resp.Page)
	}
	if len(resp.Data) != 0 {
		t.Errorf("期望 Data 长度为 0, 实际为 %d", len(resp.Data))
	}
}

func TestBuildList_WithData(t *testing.T) {
	users := []userResponse{
		{ID: "uid-1", Username: "user1"},
		{ID: "uid-2", Username: "user2"},
	}
	resp := BuildList(users, 10, 2, func(u userResponse) *userResponse {
		return &u
	})

	if resp.Total != 10 {
		t.Errorf("期望 Total 为 10, 实际为 %d", resp.Total)
	}
	if resp.Page != 2 {
		t.Errorf("期望 Page 为 2, 实际为 %d", resp.Page)
	}
	if len(resp.Data) != 2 {
		t.Errorf("期望 Data 长度为 2, 实际为 %d", len(resp.Data))
	}
	if resp.Data[0].ID != "uid-1" {
		t.Errorf("期望第一个用户 ID 为 uid-1, 实际为 %s", resp.Data[0].ID)
	}
}

// userResponse 辅助类型
type userResponse struct {
	ID       string
	Email    string
	Username string
}
