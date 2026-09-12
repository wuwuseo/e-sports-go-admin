package service

import (
	"context"
	"errors"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/user"
	"go-admin/internal/ent/userrole"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func setupUserCreateTest(t *testing.T) (*ent.Client, func()) {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:usercreate?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	prevORM := global.ORM
	global.ORM = client
	return client, func() {
		global.ORM = prevORM
		client.Close()
	}
}

func TestCreateUser_EnforcesOperatorRolePermissions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action string
		denied bool
	}{
		{name: "reject escalation", action: "DELETE", denied: true},
		{name: "allow subset", action: "GET"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, operator, _ := setupUserScopeFixture(t)
			ctx := t.Context()
			operatorRole := createUserScopeRole(t, client, "creator")
			client.AdminRole.Create().SetAdminID(operator.ID).SetRoleID(operatorRole.ID).SaveX(ctx)
			createUserScopeCasbinRule(t, client, operatorRole.Name, "/api/v1/admin/auth/users", "POST")
			createUserScopeCasbinRule(t, client, operatorRole.Name, "/api/v1/admin/auth/users", "GET")
			delegatedRole := createUserScopeRole(t, client, "delegated")
			createUserScopeCasbinRule(t, client, delegatedRole.Name, "/api/v1/admin/auth/users", tc.action)
			op := &EffectiveDataScope{Scope: DataScopeAll, AdminID: operator.ID, AdminUID: operator.UID}
			created, err := NewUserService().CreateUser(ctx, op, &CreateUserRequest{
				Email: "delegated@example.com", Username: "delegated", Password: "Str0ng!Pass",
				RoleIDs: []int{delegatedRole.ID}, PrimaryRoleID: delegatedRole.ID,
			})
			if tc.denied {
				if !errors.Is(err, ErrRoleScopeEscalation) {
					t.Fatalf("want ErrRoleScopeEscalation, got %v", err)
				}
				if client.User.Query().Where(user.EmailEQ("delegated@example.com")).ExistX(ctx) || client.UserRole.Query().ExistX(ctx) {
					t.Fatal("rejected creation must not persist a user or roles")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			u := userRowByUID(t, client, created.ID)
			if !client.UserRole.Query().Where(userrole.UserIDEQ(u.ID), userrole.RoleIDEQ(delegatedRole.ID)).ExistX(ctx) {
				t.Fatal("authorized role was not assigned")
			}
		})
	}
}

// userRowByUID 按 UID 取回用户实体，拿到数字主键（UserResponse.ID 是 UID 字符串）
func userRowByUID(t *testing.T, client *ent.Client, uid string) *ent.User {
	t.Helper()
	u, err := client.User.Query().Where(user.UIDEQ(uid)).Only(context.Background())
	if err != nil {
		t.Fatalf("按 UID 查询用户失败: %v", err)
	}
	return u
}

func TestCreateUser_PersistsRoles(t *testing.T) {
	client, cleanup := setupUserCreateTest(t)
	defer cleanup()

	ctx := context.Background()
	r1, err := client.Role.Create().SetName("editor").SetDisplayName("编辑").SetStatus("active").Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}
	r2, err := client.Role.Create().SetName("auditor").SetDisplayName("审计").SetStatus("active").Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	svc := NewUserService()
	created, err := svc.CreateUser(ctx, nil, &CreateUserRequest{
		Email:         "roles@example.com",
		Password:      "Str0ng!Pass",
		Username:      "withroles",
		RoleIDs:       []int{r1.ID, r2.ID},
		PrimaryRoleID: r2.ID,
	})
	if err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}

	u := userRowByUID(t, client, created.ID)
	rows, err := client.UserRole.Query().
		Where(userrole.UserIDEQ(u.ID)).
		All(ctx)
	if err != nil {
		t.Fatalf("查询用户角色失败: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("应持久化 2 条角色绑定，got %d", len(rows))
	}
	primaryCount := 0
	for _, row := range rows {
		if row.RoleID == r2.ID && row.IsPrimary {
			primaryCount++
		}
	}
	if primaryCount != 1 {
		t.Fatalf("主角色应为 r2 且仅一条主绑定，got %d", primaryCount)
	}
}

func TestCreateUser_WithoutRolesKeepsLegacyBehavior(t *testing.T) {
	client, cleanup := setupUserCreateTest(t)
	defer cleanup()

	ctx := context.Background()
	svc := NewUserService()
	created, err := svc.CreateUser(ctx, nil, &CreateUserRequest{
		Email:    "noroles@example.com",
		Password: "Str0ng!Pass",
		Username: "noroles",
	})
	if err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	u := userRowByUID(t, client, created.ID)
	n, err := client.UserRole.Query().Where(userrole.UserIDEQ(u.ID)).Count(ctx)
	if err != nil {
		t.Fatalf("查询用户角色失败: %v", err)
	}
	if n != 0 {
		t.Fatalf("未传角色时不应有绑定，got %d", n)
	}
}

func TestCreateUser_RejectsUnknownRole(t *testing.T) {
	client, cleanup := setupUserCreateTest(t)
	defer cleanup()

	ctx := context.Background()
	svc := NewUserService()
	_, err := svc.CreateUser(ctx, nil, &CreateUserRequest{
		Email:    "ghost@example.com",
		Password: "Str0ng!Pass",
		Username: "ghost",
		RoleIDs:  []int{999},
	})
	if err != ErrInvalidRoleIDs {
		t.Fatalf("应返回 ErrInvalidRoleIDs，got %v", err)
	}
	exists, err := client.User.Query().Where(user.EmailEQ("ghost@example.com")).Exist(ctx)
	if err != nil {
		t.Fatalf("查询用户失败: %v", err)
	}
	if exists {
		t.Fatal("角色无效时不应创建用户")
	}
}

func TestCreateUser_DefaultsPrimaryToFirstRole(t *testing.T) {
	client, cleanup := setupUserCreateTest(t)
	defer cleanup()

	ctx := context.Background()
	r1, err := client.Role.Create().SetName("viewer").SetDisplayName("查看").SetStatus("active").Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}
	r2, err := client.Role.Create().SetName("editor2").SetDisplayName("编辑2").SetStatus("active").Save(ctx)
	if err != nil {
		t.Fatalf("创建角色失败: %v", err)
	}

	svc := NewUserService()
	created, err := svc.CreateUser(ctx, nil, &CreateUserRequest{
		Email:    "defaultprimary@example.com",
		Password: "Str0ng!Pass",
		Username: "defaultprimary",
		RoleIDs:  []int{r1.ID, r2.ID},
	})
	if err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}

	u := userRowByUID(t, client, created.ID)
	rows, err := client.UserRole.Query().Where(userrole.UserIDEQ(u.ID)).All(ctx)
	if err != nil {
		t.Fatalf("查询用户角色失败: %v", err)
	}
	primary := 0
	for _, row := range rows {
		if row.IsPrimary {
			primary = row.RoleID
		}
	}
	if primary != r1.ID {
		t.Fatalf("未指定主角色时应默认第一个，got %d", primary)
	}
}
