package service

import (
	"errors"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/role"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func setupSystemPermissionOperatorTest(t *testing.T) *ent.Client {
	client := enttest.Open(t, "sqlite3", "file:sysperm-op?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
	return client
}

func TestSystemPermissionService_EnsureSuperAdminOrSystem_SystemOperatorBypasses(t *testing.T) {
	_ = setupSystemPermissionOperatorTest(t)
	svc := NewSystemPermissionService()
	ctx := withPermissionSystemOperator(t.Context())
	if err := svc.ensureSuperAdminOrSystem(ctx, ""); err != nil {
		t.Fatalf("系统级调用应 bypass 校验, got %v", err)
	}
}

func TestSystemPermissionService_EnsureSuperAdminOrSystem_EmptyOperator(t *testing.T) {
	_ = setupSystemPermissionOperatorTest(t)
	svc := NewSystemPermissionService()
	if err := svc.ensureSuperAdminOrSystem(t.Context(), ""); !errors.Is(err, ErrPermissionOperatorRequired) {
		t.Errorf("空操作者应返回 ErrPermissionOperatorRequired, got %v", err)
	}
}

func TestSystemPermissionService_EnsureSuperAdminOrSystem_NonSuperAdmin(t *testing.T) {
	client := setupSystemPermissionOperatorTest(t)
	ctx := t.Context()
	a, err := client.Admin.Create().
		SetEmail("plain@example.com").
		SetUID("uid-plain").
		SetUsername("plain").
		SetPassword("pass").
		SetPasswordSalt("salt").
		SetStatus(admin.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建管理员失败: %v", err)
	}

	svc := NewSystemPermissionService()
	if err := svc.ensureSuperAdminOrSystem(ctx, a.UID); !errors.Is(err, ErrSuperAdminProtected) {
		t.Errorf("非超管应返回 ErrSuperAdminProtected, got %v", err)
	}
}

func TestSystemPermissionService_EnsureSuperAdminOrSystem_SuperAdmin(t *testing.T) {
	client := setupSystemPermissionOperatorTest(t)
	ctx := t.Context()

	superRole, err := client.Role.Create().
		SetName(SuperAdminRoleName).
		SetDisplayName("超级管理员").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建超管角色失败: %v", err)
	}

	a, err := client.Admin.Create().
		SetEmail("super@example.com").
		SetUID("uid-super").
		SetUsername("super").
		SetPassword("pass").
		SetPasswordSalt("salt").
		SetStatus(admin.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建管理员失败: %v", err)
	}

	if _, err := client.AdminRole.Create().SetAdminID(a.ID).SetRoleID(superRole.ID).Save(ctx); err != nil {
		t.Fatalf("分配超管角色失败: %v", err)
	}

	svc := NewSystemPermissionService()
	if err := svc.ensureSuperAdminOrSystem(ctx, a.UID); err != nil {
		t.Fatalf("超管应通过校验, got %v", err)
	}
}
