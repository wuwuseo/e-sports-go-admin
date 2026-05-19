package migrate

import (
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/user"

	_ "github.com/mattn/go-sqlite3"
)

func setupMigrationTest(t *testing.T) *ent.Client {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	return client
}

func TestMigration_CopyAdminUsersToAdmins(t *testing.T) {
	client := setupMigrationTest(t)
	ctx := t.Context()

	adminRole, err := client.Role.Create().
		SetName("admin").
		SetDisplayName("管理员").
		SetIsSystem(true).
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建管理员角色失败: %v", err)
	}

	memberRole, err := client.Role.Create().
		SetName("member").
		SetDisplayName("普通用户").
		SetIsSystem(false).
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建普通角色失败: %v", err)
	}

	adminUser, err := client.User.Create().
		SetEmail("admin@company.com").
		SetUID("uid-admin-old").
		SetUsername("adminuser").
		SetPassword("hashed_admin_pass").
		SetPasswordSalt("salt").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建管理员用户失败: %v", err)
	}

	client.UserRole.Create().
		SetUserID(adminUser.ID).
		SetRoleID(adminRole.ID).
		SetIsPrimary(true).
		Save(ctx)

	normalUser, err := client.User.Create().
		SetEmail("normal@company.com").
		SetUID("uid-normal").
		SetUsername("normaluser").
		SetPassword("hashed_pass").
		SetPasswordSalt("salt").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建普通用户失败: %v", err)
	}

	client.UserRole.Create().
		SetUserID(normalUser.ID).
		SetRoleID(memberRole.ID).
		Save(ctx)

	err = MigrateUsersToAdmins(ctx, client)
	if err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	adminCount, err := client.Admin.Query().Count(ctx)
	if err != nil {
		t.Fatalf("查询管理员数量失败: %v", err)
	}
	if adminCount != 1 {
		t.Fatalf("期望迁移1个管理员, 实际=%d", adminCount)
	}

	a, err := client.Admin.Query().Only(ctx)
	if err != nil {
		t.Fatalf("查询迁移后的管理员失败: %v", err)
	}
	if a.Email != "admin@company.com" {
		t.Errorf("期望邮箱为 admin@company.com, 实际=%s", a.Email)
	}
	if a.Username != "adminuser" {
		t.Errorf("期望用户名为 adminuser, 实际=%s", a.Username)
	}
}

func TestMigration_NoDataLoss_PlainUsersUnaffected(t *testing.T) {
	client := setupMigrationTest(t)
	ctx := t.Context()

	adminRole, err := client.Role.Create().
		SetName("admin").
		SetDisplayName("管理员").
		SetIsSystem(true).
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建管理员角色失败: %v", err)
	}

	adminUser, err := client.User.Create().
		SetEmail("admin@test.com").
		SetUID("uid-admin").
		SetUsername("admin").
		SetPassword("pass").
		SetPasswordSalt("salt").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建管理员用户失败: %v", err)
	}

	client.UserRole.Create().
		SetUserID(adminUser.ID).
		SetRoleID(adminRole.ID).
		Save(ctx)

	_, err = client.User.Create().
		SetEmail("user@test.com").
		SetUID("uid-user").
		SetUsername("user").
		SetPassword("pass").
		SetPasswordSalt("salt").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建普通用户失败: %v", err)
	}

	err = MigrateUsersToAdmins(ctx, client)
	if err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	normal, err := client.User.Query().Where(user.UID("uid-user")).Only(ctx)
	if err != nil {
		t.Fatalf("普通用户数据丢失: %v", err)
	}
	if normal.Email != "user@test.com" {
		t.Errorf("普通用户邮箱被篡改")
	}
	if normal.Username != "user" {
		t.Errorf("普通用户名被篡改")
	}
}
