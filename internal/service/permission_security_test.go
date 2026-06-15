package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/casbinrule"
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

func createPermissionSecurityRule(t *testing.T, client *ent.Client, roleName, domain string, typ PermissionType, object, action string) {
	t.Helper()

	_, err := client.CasbinRule.Create().
		SetPtype("p").
		SetV0(roleName).
		SetV1(domain).
		SetV2(object).
		SetV3(action).
		SetV4(string(typ)).
		Save(t.Context())
	if err != nil {
		t.Fatalf("create permission rule %s %s failed: %v", object, action, err)
	}
}

func createPermissionSecuritySystemPermission(t *testing.T, ctx context.Context, client *ent.Client, typ SystemPermissionType, name, code string, parentID int, object, action string) *ent.SystemPermission {
	t.Helper()

	create := client.SystemPermission.Create().
		SetDomain("default").
		SetType(string(typ)).
		SetName(name).
		SetCode(code).
		SetParentID(parentID).
		SetSortOrder(1).
		SetStatus(1)

	switch typ {
	case SystemPermissionTypeAPI:
		create.SetPath(object).SetMethod(action)
	case SystemPermissionTypeMenu:
		create.SetMenuPath(object)
	}

	p, err := create.Save(ctx)
	if err != nil {
		t.Fatalf("create system permission %s failed: %v", code, err)
	}
	return p
}

func rolePermissionExists(t *testing.T, ctx context.Context, client *ent.Client, roleName, domain, object, action string) bool {
	t.Helper()

	exists, err := client.CasbinRule.Query().
		Where(
			casbinrule.PtypeEQ("p"),
			casbinrule.V0EQ(roleName),
			casbinrule.V1EQ(domain),
			casbinrule.V2EQ(object),
			casbinrule.V3EQ(action),
		).Exist(ctx)
	if err != nil {
		t.Fatalf("check permission %s %s failed: %v", object, action, err)
	}
	return exists
}

func systemPermissionTreeContainsCode(nodes []*SystemPermissionTreeNode, code string) bool {
	for _, node := range nodes {
		if node.Code == code || systemPermissionTreeContainsCode(node.Children, code) {
			return true
		}
	}
	return false
}

func systemPermissionResponsesContainCode(items []*SystemPermissionResponse, code string) bool {
	for _, item := range items {
		if item.Code == code {
			return true
		}
	}
	return false
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
	createPermissionSecurityRule(t, client, normalRole.Name, "default", PermissionTypeAPI, "/api/safe", "GET")

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

func TestSystemPermissionService_ListAndTree_ScopesToOperatorPermissions(t *testing.T) {
	client := setupPermissionSecurityTest(t)
	ctx := t.Context()
	svc := NewSystemPermissionService()

	normalRole := createPermissionSecurityRole(t, client, "normal_admin")
	createPermissionSecurityUserWithRole(t, client, "uid-normal", normalRole)

	adminRole := createPermissionSecurityRole(t, client, "admin")
	createPermissionSecurityUserWithRole(t, client, "uid-admin", adminRole)

	root := createPermissionSecuritySystemPermission(t, ctx, client, SystemPermissionTypeMenu, "System", "system", 0, "/system", string(PermissionTypeMenu))
	allowedMenu := createPermissionSecuritySystemPermission(t, ctx, client, SystemPermissionTypeMenu, "Users", "system:user", root.ID, "/system/user", string(PermissionTypeMenu))
	allowedAPI := createPermissionSecuritySystemPermission(t, ctx, client, SystemPermissionTypeAPI, "List users", "user:list", allowedMenu.ID, "/api/v1/admin/auth/users", "GET")
	pluginMenu := createPermissionSecuritySystemPermission(t, ctx, client, SystemPermissionTypeMenu, "Plugins", "plugin", root.ID, "/system/plugin", string(PermissionTypeMenu))
	pluginAPI := createPermissionSecuritySystemPermission(t, ctx, client, SystemPermissionTypeAPI, "List plugins", "plugin:list", pluginMenu.ID, "/api/v1/admin/auth/plugins", "GET")
	pluginButton := createPermissionSecuritySystemPermission(t, ctx, client, SystemPermissionTypeButton, "Install plugin", "plugin:install", pluginMenu.ID, "plugin:install", string(PermissionTypeButton))

	createPermissionSecurityRule(t, client, normalRole.Name, "default", PermissionTypeMenu, allowedMenu.MenuPath, string(PermissionTypeMenu))
	createPermissionSecurityRule(t, client, normalRole.Name, "default", PermissionTypeAPI, allowedAPI.Path, allowedAPI.Method)

	normalList, err := svc.List(ctx, "uid-normal", &ListSystemPermissionsRequest{
		ListRequest: ListRequest{PaginationRequest: PaginationRequest{Page: 1, PageSize: 0}},
		Domain:      "default",
	})
	if err != nil {
		t.Fatalf("list system permissions as normal admin failed: %v", err)
	}
	if !systemPermissionResponsesContainCode(normalList.Data, allowedMenu.Code) {
		t.Fatalf("expected normal admin list to include owned permission %s", allowedMenu.Code)
	}
	for _, hiddenCode := range []string{pluginMenu.Code, pluginAPI.Code, pluginButton.Code} {
		if systemPermissionResponsesContainCode(normalList.Data, hiddenCode) {
			t.Fatalf("normal admin list leaked unowned permission %s", hiddenCode)
		}
	}

	normalTree, err := svc.GetTree(ctx, "uid-normal", "default")
	if err != nil {
		t.Fatalf("get tree as normal admin failed: %v", err)
	}
	if !systemPermissionTreeContainsCode(normalTree, root.Code) || !systemPermissionTreeContainsCode(normalTree, allowedMenu.Code) {
		t.Fatalf("expected normal admin tree to include owned permission and parent, got %#v", normalTree)
	}
	for _, hiddenCode := range []string{pluginMenu.Code, pluginAPI.Code, pluginButton.Code} {
		if systemPermissionTreeContainsCode(normalTree, hiddenCode) {
			t.Fatalf("normal admin tree leaked unowned permission %s", hiddenCode)
		}
	}

	adminList, err := svc.List(ctx, "uid-admin", &ListSystemPermissionsRequest{
		ListRequest: ListRequest{PaginationRequest: PaginationRequest{Page: 1, PageSize: 0}},
		Domain:      "default",
	})
	if err != nil {
		t.Fatalf("list system permissions as super admin failed: %v", err)
	}
	if !systemPermissionResponsesContainCode(adminList.Data, pluginAPI.Code) {
		t.Fatalf("expected super admin list to include %s", pluginAPI.Code)
	}

	adminTree, err := svc.GetTree(ctx, "uid-admin", "default")
	if err != nil {
		t.Fatalf("get tree as super admin failed: %v", err)
	}
	if !systemPermissionTreeContainsCode(adminTree, pluginButton.Code) {
		t.Fatalf("expected super admin tree to include %s", pluginButton.Code)
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

func setupPermissionScopeMutationTest(t *testing.T) (*ent.Client, context.Context, *PermissionService, *ent.Role) {
	t.Helper()

	client := setupPermissionSecurityTest(t)
	ctx := t.Context()
	svc := NewPermissionService(nil)

	normalRole := createPermissionSecurityRole(t, client, "normal_admin")
	createPermissionSecurityUserWithRole(t, client, "uid-normal", normalRole)
	targetRole := createPermissionSecurityRole(t, client, "test")

	createPermissionSecurityRule(t, client, normalRole.Name, "default", PermissionTypeAPI, "/api/safe", "GET")
	createPermissionSecurityRule(t, client, normalRole.Name, "default", PermissionTypeAPI, "/api/old-safe", "GET")

	return client, ctx, svc, targetRole
}

func expectScopeEscalation(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrPermissionScopeEscalation) {
		t.Fatalf("expected ErrPermissionScopeEscalation, got %v", err)
	}
}

func TestPermissionService_RejectsScopeEscalationOnRolePermissionMutations(t *testing.T) {
	const pluginObject = "/api/v1/admin/auth/plugins"

	t.Run("assign", func(t *testing.T) {
		client, ctx, svc, _ := setupPermissionScopeMutationTest(t)

		err := svc.AssignPermissionsToRole(ctx, "uid-normal", "test", "default", []PermissionRule{
			{Object: pluginObject, Action: "GET"},
		})
		expectScopeEscalation(t, err)
		if rolePermissionExists(t, ctx, client, "test", "default", pluginObject, "GET") {
			t.Fatal("plugin permission was assigned after scope escalation")
		}
	})

	t.Run("save with unowned permission in request", func(t *testing.T) {
		client, ctx, svc, targetRole := setupPermissionScopeMutationTest(t)

		err := svc.SaveRolePermissions(ctx, "uid-normal", targetRole.ID, &SaveRolePermissionsRequest{
			Domain: "default",
			Permissions: []PermissionRule{
				{Object: pluginObject, Action: "GET"},
			},
		})
		expectScopeEscalation(t, err)
		if rolePermissionExists(t, ctx, client, "test", "default", pluginObject, "GET") {
			t.Fatal("plugin permission was saved after scope escalation")
		}
	})

	t.Run("create", func(t *testing.T) {
		client, ctx, svc, _ := setupPermissionScopeMutationTest(t)

		err := svc.CreatePermission(ctx, "uid-normal", &CreatePermissionRequest{
			RoleName: "test",
			Domain:   "default",
			Type:     PermissionTypeAPI,
			Object:   pluginObject,
			Action:   "GET",
		})
		expectScopeEscalation(t, err)
		if rolePermissionExists(t, ctx, client, "test", "default", pluginObject, "GET") {
			t.Fatal("plugin permission was created after scope escalation")
		}
	})

	t.Run("update", func(t *testing.T) {
		client, ctx, svc, _ := setupPermissionScopeMutationTest(t)
		createPermissionSecurityRule(t, client, "test", "default", PermissionTypeAPI, "/api/safe", "GET")

		err := svc.UpdatePermission(ctx, "uid-normal", &UpdatePermissionRequest{
			RoleName:  "test",
			Domain:    "default",
			Type:      PermissionTypeAPI,
			Object:    "/api/safe",
			Action:    "GET",
			NewObject: pluginObject,
			NewAction: "GET",
		})
		expectScopeEscalation(t, err)
		if !rolePermissionExists(t, ctx, client, "test", "default", "/api/safe", "GET") {
			t.Fatal("original safe permission was removed during failed update")
		}
		if rolePermissionExists(t, ctx, client, "test", "default", pluginObject, "GET") {
			t.Fatal("plugin permission was created during failed update")
		}
	})

	t.Run("delete", func(t *testing.T) {
		client, ctx, svc, _ := setupPermissionScopeMutationTest(t)
		createPermissionSecurityRule(t, client, "test", "default", PermissionTypeAPI, pluginObject, "GET")

		err := svc.DeletePermission(ctx, "uid-normal", "test", "default", pluginObject, "GET")
		expectScopeEscalation(t, err)
		if !rolePermissionExists(t, ctx, client, "test", "default", pluginObject, "GET") {
			t.Fatal("plugin permission was deleted after scope escalation")
		}
	})

	t.Run("remove", func(t *testing.T) {
		client, ctx, svc, _ := setupPermissionScopeMutationTest(t)
		createPermissionSecurityRule(t, client, "test", "default", PermissionTypeAPI, pluginObject, "GET")

		err := svc.RemovePermissionsFromRole(ctx, "uid-normal", "test", "default", []PermissionRule{
			{Object: pluginObject, Action: "GET"},
		})
		expectScopeEscalation(t, err)
		if !rolePermissionExists(t, ctx, client, "test", "default", pluginObject, "GET") {
			t.Fatal("plugin permission was removed after scope escalation")
		}
	})
}

func TestPermissionService_SaveRolePermissions_PreservesOutOfScopePermissions(t *testing.T) {
	const pluginObject = "/api/v1/admin/auth/plugins"

	client, ctx, svc, targetRole := setupPermissionScopeMutationTest(t)
	createPermissionSecurityRule(t, client, "test", "default", PermissionTypeAPI, pluginObject, "GET")
	createPermissionSecurityRule(t, client, "test", "default", PermissionTypeAPI, "/api/old-safe", "GET")

	err := svc.SaveRolePermissions(ctx, "uid-normal", targetRole.ID, &SaveRolePermissionsRequest{
		Domain: "default",
		Permissions: []PermissionRule{
			{Object: "/api/safe", Action: "GET"},
		},
	})
	if err != nil {
		t.Fatalf("save scoped permissions failed: %v", err)
	}
	if !rolePermissionExists(t, ctx, client, "test", "default", pluginObject, "GET") {
		t.Fatal("out-of-scope plugin permission was removed by scoped save")
	}
	if !rolePermissionExists(t, ctx, client, "test", "default", "/api/safe", "GET") {
		t.Fatal("in-scope permission was not saved")
	}
	if rolePermissionExists(t, ctx, client, "test", "default", "/api/old-safe", "GET") {
		t.Fatal("old in-scope permission was not replaced")
	}
}
