package service

import (
	"errors"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"

	"github.com/wuwuseo/cmf/bootstrap"
	cmfConfig "github.com/wuwuseo/cmf/config"

	_ "github.com/mattn/go-sqlite3"
)

func setupSuperAdminTest(t *testing.T) (*ent.Client, *bootstrap.Bootstrap, func()) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	prevORM := global.ORM
	global.ORM = client

	cfg := &cmfConfig.Config{}
	cfg.App.Name = "test-admin"
	cfg.App.Secret = "test-secret-key-for-jwt"

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

// ======================== SuperAdminService 测试 ========================

func TestSuperAdminService_CreateAdmin_Success(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	svc := NewSuperAdminService()
	result, err := svc.CreateAdmin(ctx, &CreateAdminRequest{
		Email:    "newadmin@test.com",
		Password: "adminpass123",
		Username: "newadmin",
		Nickname: "新管理员",
	})
	if err != nil {
		t.Fatalf("创建管理员失败: %v", err)
	}

	a, err := client.Admin.Query().Where(admin.UID(result.ID)).Only(ctx)
	if err != nil {
		t.Fatalf("查询新创建的管理员失败: %v", err)
	}
	if a.Email != "newadmin@test.com" {
		t.Errorf("期望邮箱为 newadmin@test.com, 实际=%s", a.Email)
	}
	if a.Username != "newadmin" {
		t.Errorf("期望用户名为 newadmin, 实际=%s", a.Username)
	}
	if a.Nickname != "新管理员" {
		t.Errorf("期望昵称为 新管理员, 实际=%s", a.Nickname)
	}
	if a.Status != admin.StatusInactive {
		t.Errorf("期望初始状态为 inactive, 实际=%s", a.Status)
	}
}

func TestSuperAdminService_CreateAdmin_RejectsSuperAdminRole(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	adminRole, err := client.Role.Create().
		SetName("admin").
		SetDisplayName("超级管理员").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建超级管理员角色失败: %v", err)
	}

	svc := NewSuperAdminService()
	_, err = svc.CreateAdmin(ctx, &CreateAdminRequest{
		Email:         "forbidden-admin@test.com",
		Password:      "adminpass123",
		Username:      "forbiddenadmin",
		RoleIDs:       []int{adminRole.ID},
		PrimaryRoleID: adminRole.ID,
	})
	if !errors.Is(err, ErrSuperAdminRoleAssignmentForbidden) {
		t.Errorf("期望错误为 ErrSuperAdminRoleAssignmentForbidden, 实际错误: %v", err)
	}
}

func TestSuperAdminService_CreateAdmin_EmailExists(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	client.Admin.Create().
		SetEmail("exists@test.com").
		SetUID("uid-exists").
		SetUsername("existsuser").
		SetPassword("pass").
		SetPasswordSalt("salt").
		SetStatus(admin.StatusActive).
		Save(ctx)

	svc := NewSuperAdminService()
	_, err := svc.CreateAdmin(ctx, &CreateAdminRequest{
		Email:    "exists@test.com",
		Password: "pass123",
		Username: "newuser",
	})
	if err == nil {
		t.Fatal("期望重复邮箱创建返回错误")
	}
}

func TestSuperAdminService_CreateAdmin_UsernameExists(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	client.Admin.Create().
		SetEmail("user1@test.com").
		SetUID("uid-user1").
		SetUsername("existinguser").
		SetPassword("pass").
		SetPasswordSalt("salt").
		SetStatus(admin.StatusActive).
		Save(ctx)

	svc := NewSuperAdminService()
	_, err := svc.CreateAdmin(ctx, &CreateAdminRequest{
		Email:    "user2@test.com",
		Password: "pass123",
		Username: "existinguser",
	})
	if err == nil {
		t.Fatal("期望重复用户名创建返回错误")
	}
}

func TestSuperAdminService_ListAdmins_Success(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	for i := 0; i < 3; i++ {
		_ = createTestAdminRaw(t, client,
			"list"+string(rune('a'+i))+"@test.com",
			"uid-list-"+string(rune('a'+i)),
			"listadmin"+string(rune('a'+i)),
			"pass",
			admin.StatusActive,
		)
	}

	svc := NewSuperAdminService()
	resp, err := svc.ListAdmins(ctx, &AdminListRequest{
		Page:     1,
		PageSize: 10,
	})
	if err != nil {
		t.Fatalf("查询管理员列表失败: %v", err)
	}

	if resp.Total < 3 {
		t.Errorf("期望至少3条记录, 实际=%d", resp.Total)
	}
}

func TestSuperAdminService_ListAdmins_Pagination(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	for i := 0; i < 5; i++ {
		_ = createTestAdminRaw(t, client,
			"page"+string(rune('a'+i))+"@test.com",
			"uid-page-"+string(rune('a'+i)),
			"pageadmin"+string(rune('a'+i)),
			"pass",
			admin.StatusActive,
		)
	}

	svc := NewSuperAdminService()
	resp, err := svc.ListAdmins(ctx, &AdminListRequest{
		Page:     1,
		PageSize: 2,
	})
	if err != nil {
		t.Fatalf("查询管理员列表失败: %v", err)
	}

	if len(resp.Data) != 2 {
		t.Errorf("期望返回2条记录, 实际=%d", len(resp.Data))
	}
	if resp.Total < 5 {
		t.Errorf("期望总数至少为5, 实际=%d", resp.Total)
	}
}

func TestSuperAdminService_ActivateAdmin_Success(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	a := createTestAdminRaw(t, client, "activate@test.com", "uid-activate", "activateadmin", "pass", admin.StatusInactive)

	svc := NewSuperAdminService()
	err := svc.ActivateAdmin(ctx, nil, a.ID)
	if err != nil {
		t.Fatalf("激活管理员失败: %v", err)
	}

	updated, err := client.Admin.Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("查询更新后的管理员失败: %v", err)
	}
	if updated.Status != admin.StatusActive {
		t.Errorf("期望状态为 active, 实际=%s", updated.Status)
	}
}

func TestSuperAdminService_DeactivateAdmin_Success(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	a := createTestAdminRaw(t, client, "deactivate@test.com", "uid-deactivate", "deactivateadmin", "pass", admin.StatusActive)

	svc := NewSuperAdminService()
	err := svc.DeactivateAdmin(ctx, nil, a.ID)
	if err != nil {
		t.Fatalf("停用管理员失败: %v", err)
	}

	updated, err := client.Admin.Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("查询更新后的管理员失败: %v", err)
	}
	if updated.Status != admin.StatusInactive {
		t.Errorf("期望状态为 inactive, 实际=%s", updated.Status)
	}
}

// TestSuperAdminService_ActivateSelf_Forbidden 验证不允许激活自己
func TestSuperAdminService_ActivateSelf_Forbidden(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()
	a := createTestAdminRaw(t, client, "self@test.com", "u-self", "selfadmin", "pass", admin.StatusInactive)

	svc := NewSuperAdminService()
	op := &EffectiveDataScope{AdminID: a.ID, AdminUID: a.UID, IsSuperAdmin: true}
	err := svc.ActivateAdmin(ctx, op, a.ID)
	if !errors.Is(err, ErrCannotToggleSelfStatus) {
		t.Fatalf("期望 ErrCannotToggleSelfStatus，got %v", err)
	}
}

// TestSuperAdminService_DeactivateSelf_Forbidden 验证不允许停用自己
func TestSuperAdminService_DeactivateSelf_Forbidden(t *testing.T) {
	client, _, cleanup := setupSuperAdminTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()
	a := createTestAdminRaw(t, client, "selfd@test.com", "u-selfd", "selfdadmin", "pass", admin.StatusActive)

	svc := NewSuperAdminService()
	op := &EffectiveDataScope{AdminID: a.ID, AdminUID: a.UID, IsSuperAdmin: true}
	err := svc.DeactivateAdmin(ctx, op, a.ID)
	if !errors.Is(err, ErrCannotToggleSelfStatus) {
		t.Fatalf("期望 ErrCannotToggleSelfStatus，got %v", err)
	}
}
