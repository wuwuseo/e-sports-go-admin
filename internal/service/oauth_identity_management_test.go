package service

import (
	"errors"
	"testing"

	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/adminthirdpartyuser"
	"go-admin/internal/ent/thirdpartyuser"
	"go-admin/internal/ent/user"
)

func TestOAuthAuthServiceProtectsLastUserLoginMethod(t *testing.T) {
	client, service, _ := setupOAuthAuthTest(t)
	u, err := client.User.Create().
		SetEmail("oauth-only@example.com").
		SetUID("oauth-only-uid").
		SetStatus(user.StatusActive).
		Save(t.Context())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	_, err = client.ThirdPartyUser.Create().
		SetUserID(u.ID).
		SetPlatform("github").
		SetOpenid("oauth-only-github").
		SetStatus(thirdpartyuser.StatusActive).
		Save(t.Context())
	if err != nil {
		t.Fatalf("create identity: %v", err)
	}

	if err := service.UnbindUser(t.Context(), u.UID, "github"); !errors.Is(err, ErrOAuthLastLoginMethod) {
		t.Fatalf("expected last login method protection, got %v", err)
	}
	if _, err := client.User.UpdateOne(u).SetPassword("configured-password-hash").Save(t.Context()); err != nil {
		t.Fatalf("set password: %v", err)
	}
	if err := service.UnbindUser(t.Context(), u.UID, "github"); err != nil {
		t.Fatalf("unbind with password: %v", err)
	}
}

func TestOAuthAuthServiceListsAndUnbindsAdminIdentity(t *testing.T) {
	client, service, _ := setupOAuthAuthTest(t)
	a, err := client.Admin.Create().
		SetEmail("admin-identities@example.com").
		SetUID("admin-identities-uid").
		SetUsername("admin-identities").
		SetPassword("hash").
		SetStatus(admin.StatusActive).
		Save(t.Context())
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	_, err = client.AdminThirdPartyUser.Create().
		SetAdminID(a.ID).
		SetPlatform("github").
		SetOpenid("admin-github-id").
		SetStatus(adminthirdpartyuser.StatusActive).
		Save(t.Context())
	if err != nil {
		t.Fatalf("create identity: %v", err)
	}

	identities, err := service.ListAdminIdentities(t.Context(), a.UID)
	if err != nil || len(identities) != 1 || identities[0].Provider != "github" {
		t.Fatalf("unexpected identities: %#v err=%v", identities, err)
	}
	if err := service.UnbindAdmin(t.Context(), a.UID, "github"); err != nil {
		t.Fatalf("unbind admin: %v", err)
	}
}

func TestOAuthAuthServiceListsAndDeletesManagedAdminIdentity(t *testing.T) {
	client, service, _ := setupOAuthAuthTest(t)
	a, err := client.Admin.Create().
		SetEmail("managed-admin@example.com").
		SetUID("managed-admin-uid").
		SetUsername("managed-admin").
		SetPassword("hash").
		SetStatus(admin.StatusActive).
		Save(t.Context())
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	identity, err := client.AdminThirdPartyUser.Create().
		SetAdminID(a.ID).
		SetPlatform("github").
		SetOpenid("managed-admin-github-id").
		SetStatus(adminthirdpartyuser.StatusActive).
		Save(t.Context())
	if err != nil {
		t.Fatalf("create identity: %v", err)
	}

	result, err := service.ListAllAdminIdentities(t.Context(), 1, 10, "github", "managed-admin")
	if err != nil || result.Total != 1 || len(result.Data) != 1 || result.Data[0].AdminUID != a.UID {
		t.Fatalf("unexpected managed identities: %#v err=%v", result, err)
	}
	if err := service.DeleteAdminIdentity(t.Context(), identity.ID); err != nil {
		t.Fatalf("DeleteAdminIdentity: %v", err)
	}
	if err := service.DeleteAdminIdentity(t.Context(), identity.ID); !errors.Is(err, ErrThirdPartyNotFound) {
		t.Fatalf("expected missing identity after delete, got %v", err)
	}
}
