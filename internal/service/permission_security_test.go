package service

import (
	"errors"
	"strings"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func setupPermissionSecurityTest(t *testing.T) *ent.Client {
	t.Helper()

	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	client := enttest.Open(t, "sqlite3", "file:"+dbName+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	return client
}

func createPermissionSecurityRole(t *testing.T, client *ent.Client, name string) *ent.Role {
	t.Helper()

	r, err := client.Role.Create().
		SetName(name).
		SetDisplayName(name).
		SetStatus(role.StatusActive).
		Save(t.Context())
	if err != nil {
		t.Fatalf("create role %s failed: %v", name, err)
	}
	return r
}

func createPermissionSecurityUserWithRole(t *testing.T, client *ent.Client, uid string, roleEntity *ent.Role) {
	t.Helper()

	u, err := client.User.Create().
		SetEmail(uid + "@example.com").
		SetUsername(uid).
		SetPassword("password").
		SetPasswordSalt("salt").
		SetUID(uid).
		SetStatus(user.StatusActive).
		Save(t.Context())
	if err != nil {
		t.Fatalf("create user %s failed: %v", uid, err)
	}

	_, err = client.UserRole.Create().
		SetUserID(u.ID).
		SetRoleID(roleEntity.ID).
		Save(t.Context())
	if err != nil {
		t.Fatalf("create user role for %s failed: %v", uid, err)
	}
}

func TestPermissionService_ListPermissions_FiltersSensitiveBeforePagination(t *testing.T) {
	client := setupPermissionSecurityTest(t)
	ctx := t.Context()
	svc := NewPermissionService(nil)

	normalRole := createPermissionSecurityRole(t, client, "normal_admin")
	createPermissionSecurityUserWithRole(t, client, "uid-normal", normalRole)
	createPermissionSecurityRole(t, client, "editor")

	_, err := client.CasbinRule.Create().
		SetPtype("p").
		SetV0("editor").
		SetV1("default").
		SetV2("/api/v1/admin/auth/roles/{id}/permissions").
		SetV3("GET").
		SetV4(string(PermissionTypeAPI)).
		Save(ctx)
	if err != nil {
		t.Fatalf("create sensitive permission failed: %v", err)
	}
	_, err = client.CasbinRule.Create().
		SetPtype("p").
		SetV0("editor").
		SetV1("default").
		SetV2("/api/safe").
		SetV3("GET").
		SetV4(string(PermissionTypeAPI)).
		Save(ctx)
	if err != nil {
		t.Fatalf("create safe permission failed: %v", err)
	}

	resp, err := svc.ListPermissions(ctx, "uid-normal", &ListPermissionsRequest{
		PaginationRequest: PaginationRequest{Page: 1, PageSize: 1},
		RoleName:          "editor",
		Domain:            "default",
		Type:              PermissionTypeAPI,
	})
	if err != nil {
		t.Fatalf("list permissions failed: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("expected total 1 after sensitive filtering, got %d", resp.Total)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected first page to contain the safe permission, got %d items", len(resp.Data))
	}
	if resp.Data[0].Object != "/api/safe" {
		t.Fatalf("expected /api/safe, got %s", resp.Data[0].Object)
	}
}

func TestSystemPermissionService_List_FiltersSensitiveBeforePagination(t *testing.T) {
	client := setupPermissionSecurityTest(t)
	ctx := t.Context()
	svc := NewSystemPermissionService()

	normalRole := createPermissionSecurityRole(t, client, "normal_admin")
	createPermissionSecurityUserWithRole(t, client, "uid-normal", normalRole)

	_, err := client.SystemPermission.Create().
		SetDomain("default").
		SetType(string(SystemPermissionTypeAPI)).
		SetName("Sensitive").
		SetCode("sensitive:api").
		SetPath("/api/v1/admin/auth/system-permissions").
		SetMethod("GET").
		SetSortOrder(1).
		SetStatus(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("create sensitive system permission failed: %v", err)
	}
	_, err = client.SystemPermission.Create().
		SetDomain("default").
		SetType(string(SystemPermissionTypeAPI)).
		SetName("Safe").
		SetCode("safe:api").
		SetPath("/api/safe").
		SetMethod("GET").
		SetSortOrder(2).
		SetStatus(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("create safe system permission failed: %v", err)
	}

	resp, err := svc.List(ctx, "uid-normal", &ListSystemPermissionsRequest{
		ListRequest: ListRequest{
			PaginationRequest: PaginationRequest{Page: 1, PageSize: 1},
		},
		Type: SystemPermissionTypeAPI,
	})
	if err != nil {
		t.Fatalf("list system permissions failed: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("expected total 1 after sensitive filtering, got %d", resp.Total)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected first page to contain the safe permission, got %d items", len(resp.Data))
	}
	if resp.Data[0].Path != "/api/safe" {
		t.Fatalf("expected /api/safe, got %s", resp.Data[0].Path)
	}
}

func TestPermissionService_BlocksSelfRolePermissionMutation(t *testing.T) {
	client := setupPermissionSecurityTest(t)
	ctx := t.Context()
	svc := NewPermissionService(nil)

	normalRole := createPermissionSecurityRole(t, client, "normal_admin")
	createPermissionSecurityUserWithRole(t, client, "uid-normal", normalRole)

	_, err := client.CasbinRule.Create().
		SetPtype("p").
		SetV0("normal_admin").
		SetV1("default").
		SetV2("/api/existing").
		SetV3("GET").
		SetV4(string(PermissionTypeAPI)).
		Save(ctx)
	if err != nil {
		t.Fatalf("create existing permission failed: %v", err)
	}

	cases := []struct {
		name string
		run  func() error
	}{
		{
			name: "create",
			run: func() error {
				return svc.CreatePermission(ctx, "uid-normal", &CreatePermissionRequest{
					RoleName: "normal_admin",
					Domain:   "default",
					Type:     PermissionTypeAPI,
					Object:   "/api/new",
					Action:   "GET",
				})
			},
		},
		{
			name: "update",
			run: func() error {
				return svc.UpdatePermission(ctx, "uid-normal", &UpdatePermissionRequest{
					RoleName:  "normal_admin",
					Domain:    "default",
					Type:      PermissionTypeAPI,
					Object:    "/api/existing",
					Action:    "GET",
					NewObject: "/api/changed",
					NewAction: "GET",
				})
			},
		},
		{
			name: "delete",
			run: func() error {
				return svc.DeletePermission(ctx, "uid-normal", "normal_admin", "default", "/api/existing", "GET")
			},
		},
		{
			name: "assign",
			run: func() error {
				return svc.AssignPermissionsToRole(ctx, "uid-normal", "normal_admin", "default", []PermissionRule{
					{Object: "/api/new", Action: "GET"},
				})
			},
		},
		{
			name: "remove",
			run: func() error {
				return svc.RemovePermissionsFromRole(ctx, "uid-normal", "normal_admin", "default", []PermissionRule{
					{Object: "/api/existing", Action: "GET"},
				})
			},
		},
		{
			name: "save",
			run: func() error {
				return svc.SaveRolePermissions(ctx, "uid-normal", normalRole.ID, &SaveRolePermissionsRequest{
					Domain: "default",
					Permissions: []PermissionRule{
						{Object: "/api/new", Action: "GET"},
					},
				})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if !errors.Is(err, ErrSelfRolePermissionMutationForbidden) {
				t.Fatalf("expected ErrSelfRolePermissionMutationForbidden, got %v", err)
			}
		})
	}
}

func TestPermissionService_RejectsEmptyOperatorForMutations(t *testing.T) {
	client := setupPermissionSecurityTest(t)
	ctx := t.Context()
	svc := NewPermissionService(nil)

	editorRole := createPermissionSecurityRole(t, client, "editor")

	err := svc.SaveRolePermissions(ctx, "", editorRole.ID, &SaveRolePermissionsRequest{
		Domain: "default",
		Permissions: []PermissionRule{
			{Object: "/api/new", Action: "GET"},
		},
	})
	if !errors.Is(err, ErrPermissionOperatorRequired) {
		t.Fatalf("expected ErrPermissionOperatorRequired, got %v", err)
	}
}
