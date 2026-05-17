package service

import (
	"errors"
	"testing"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func setupActivationTest(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
}

func TestUserActivationService_ActivateUser_Success(t *testing.T) {
	setupActivationTest(t)
	ctx := t.Context()

	u, err := global.ORM.User.Create().
		SetEmail("inactive@test.com").
		SetPassword("password").
		SetStatus(user.StatusInactive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}

	svc := NewUserActivationService()
	err = svc.ActivateUser(ctx, u.ID, "operator-uid")
	if err != nil {
		t.Fatalf("激活用户失败: %v", err)
	}

	updated, err := global.ORM.User.Get(ctx, u.ID)
	if err != nil {
		t.Fatalf("查询用户失败: %v", err)
	}
	if updated.Status != user.StatusActive {
		t.Errorf("期望用户状态为 %s，实际为 %s", user.StatusActive, updated.Status)
	}
}

func TestUserActivationService_ActivateUser_UserNotFound(t *testing.T) {
	setupActivationTest(t)
	ctx := t.Context()

	svc := NewUserActivationService()
	err := svc.ActivateUser(ctx, 99999, "operator-uid")
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("期望错误为 ErrUserNotFound，实际错误: %v", err)
	}
}

func TestUserActivationService_ActivateUser_AlreadyActive(t *testing.T) {
	setupActivationTest(t)
	ctx := t.Context()

	u, err := global.ORM.User.Create().
		SetEmail("active@test.com").
		SetPassword("password").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}

	svc := NewUserActivationService()
	err = svc.ActivateUser(ctx, u.ID, "operator-uid")
	if !errors.Is(err, ErrUserAlreadyActive) {
		t.Errorf("期望错误为 ErrUserAlreadyActive，实际错误: %v", err)
	}
}

func TestUserActivationService_ActivateUser_Blacklisted(t *testing.T) {
	setupActivationTest(t)
	ctx := t.Context()

	u, err := global.ORM.User.Create().
		SetEmail("blacklisted@test.com").
		SetPassword("password").
		SetStatus(user.StatusInactive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}

	_, err = global.ORM.Blacklist.Create().
		SetUserID(u.ID).
		SetReason("违规操作").
		SetOperatorID("admin-uid").
		SetOperatorName("管理员").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建黑名单记录失败: %v", err)
	}

	svc := NewUserActivationService()
	err = svc.ActivateUser(ctx, u.ID, "operator-uid")
	if !errors.Is(err, ErrUserBlacklisted) {
		t.Errorf("期望错误为 ErrUserBlacklisted，实际错误: %v", err)
	}
}

func TestUserActivationService_DeactivateUser_Success(t *testing.T) {
	setupActivationTest(t)
	ctx := t.Context()

	u, err := global.ORM.User.Create().
		SetEmail("deactivate@test.com").
		SetPassword("password").
		SetUID("user-to-deactivate").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}

	svc := NewUserActivationService()
	err = svc.DeactivateUser(ctx, u.ID, "different-operator-uid")
	if err != nil {
		t.Fatalf("停用用户失败: %v", err)
	}

	updated, err := global.ORM.User.Get(ctx, u.ID)
	if err != nil {
		t.Fatalf("查询用户失败: %v", err)
	}
	if updated.Status != user.StatusInactive {
		t.Errorf("期望用户状态为 %s，实际为 %s", user.StatusInactive, updated.Status)
	}
}

func TestUserActivationService_DeactivateUser_UserNotFound(t *testing.T) {
	setupActivationTest(t)
	ctx := t.Context()

	svc := NewUserActivationService()
	err := svc.DeactivateUser(ctx, 99999, "operator-uid")
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("期望错误为 ErrUserNotFound，实际错误: %v", err)
	}
}

func TestUserActivationService_DeactivateUser_AlreadyInactive(t *testing.T) {
	setupActivationTest(t)
	ctx := t.Context()

	u, err := global.ORM.User.Create().
		SetEmail("inactive2@test.com").
		SetPassword("password").
		SetStatus(user.StatusInactive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}

	svc := NewUserActivationService()
	err = svc.DeactivateUser(ctx, u.ID, "operator-uid")
	if !errors.Is(err, ErrUserAlreadyInactive) {
		t.Errorf("期望错误为 ErrUserAlreadyInactive，实际错误: %v", err)
	}
}

func TestUserActivationService_DeactivateUser_CannotDeactivateSelf(t *testing.T) {
	setupActivationTest(t)
	ctx := t.Context()

	u, err := global.ORM.User.Create().
		SetEmail("self@test.com").
		SetPassword("password").
		SetUID("self-uid-001").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}

	svc := NewUserActivationService()
	err = svc.DeactivateUser(ctx, u.ID, "self-uid-001")
	if !errors.Is(err, ErrCannotDeactivateSelf) {
		t.Errorf("期望错误为 ErrCannotDeactivateSelf，实际错误: %v", err)
	}
}
