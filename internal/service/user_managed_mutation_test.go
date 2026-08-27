package service

import (
	"errors"
	"testing"
	"time"

	"go-admin/internal/ent/blacklist"
	"go-admin/internal/ent/casbinrule"
	"go-admin/internal/ent/message"
	"go-admin/internal/ent/messagepreference"
	"go-admin/internal/ent/oauthsession"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/thirdpartyuser"
	"go-admin/internal/ent/user"
	"go-admin/internal/ent/userrole"
	"go-admin/internal/global"

	"github.com/wuwuseo/cmf/crypto"
)

func stringPointer(value string) *string { return &value }

func TestUserServiceUpdateManagedUser(t *testing.T) {
	client := setupUserTest(t)
	ctx := t.Context()
	target := createTestUserForUserService(t, client, "old@example.com", "managed-update", "oldname", user.StatusInactive)
	createTestUserForUserService(t, client, "taken@example.com", "managed-other", "takenname", user.StatusActive)

	svc := NewUserService()
	req := &UpdateManagedUserRequest{
		Email:    stringPointer("new@example.com"),
		Username: stringPointer("newname"),
		Nickname: stringPointer("New Nickname"),
		Phone:    stringPointer("13800138000"),
		Avatar:   stringPointer("https://example.com/avatar.png"),
		Password: stringPointer("N3w!Passwd"),
	}
	if err := svc.UpdateManagedUser(ctx, target.ID, req); err != nil {
		t.Fatalf("UpdateManagedUser() error = %v", err)
	}

	got, err := client.User.Get(ctx, target.ID)
	if err != nil {
		t.Fatalf("query updated user: %v", err)
	}
	if got.Email != *req.Email || got.Username != *req.Username || got.Nickname != *req.Nickname || got.Phone != *req.Phone || got.Avatar != *req.Avatar {
		t.Fatalf("updated user = %#v", got)
	}
	matched, err := crypto.VerifyPassword(*req.Password, got.Password, got.PasswordSalt)
	if err != nil || !matched {
		t.Fatalf("updated password did not verify: matched=%v err=%v", matched, err)
	}

	if err := svc.UpdateManagedUser(ctx, target.ID, &UpdateManagedUserRequest{Email: stringPointer("taken@example.com")}); !errors.Is(err, ErrEmailExists) {
		t.Fatalf("duplicate email error = %v, want %v", err, ErrEmailExists)
	}
	if err := svc.UpdateManagedUser(ctx, target.ID, &UpdateManagedUserRequest{Username: stringPointer("takenname")}); !errors.Is(err, ErrUsernameExists) {
		t.Fatalf("duplicate username error = %v, want %v", err, ErrUsernameExists)
	}
	if err := svc.UpdateManagedUser(ctx, 999999, &UpdateManagedUserRequest{}); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("missing user error = %v, want %v", err, ErrUserNotFound)
	}
}

func TestUserServiceDeleteManagedUserCleansOwnedData(t *testing.T) {
	client := setupUserTest(t)
	ctx := t.Context()
	previousEnforcer := global.EnforcerManager
	global.EnforcerManager = nil
	t.Cleanup(func() { global.EnforcerManager = previousEnforcer })

	target := createTestUserForUserService(t, client, "delete@example.com", "managed-delete", "deleteuser", user.StatusInactive)
	r, err := client.Role.Create().
		SetName("managed-delete-role").
		SetDisplayName("Managed delete role").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role: %v", err)
	}
	if _, err := client.UserRole.Create().SetUserID(target.ID).SetRoleID(r.ID).Save(ctx); err != nil {
		t.Fatalf("create user role: %v", err)
	}
	if _, err := client.Blacklist.Create().SetUserID(target.ID).SetReason("test").SetOperatorID("admin").SetOperatorName("admin").Save(ctx); err != nil {
		t.Fatalf("create blacklist: %v", err)
	}
	if _, err := client.ThirdPartyUser.Create().SetUserID(target.ID).SetPlatform("github").SetOpenid("managed-delete-openid").Save(ctx); err != nil {
		t.Fatalf("create third-party identity: %v", err)
	}
	if _, err := client.Message.Create().SetUserUID(target.UID).SetType("system").SetLevel("info").SetTitle("test").SetContent("test").Save(ctx); err != nil {
		t.Fatalf("create message: %v", err)
	}
	if _, err := client.MessagePreference.Create().SetUserUID(target.UID).Save(ctx); err != nil {
		t.Fatalf("create message preference: %v", err)
	}
	if _, err := client.OAuthSession.Create().
		SetStateHash("managed-delete-state").
		SetProvider("github").
		SetSubjectType(oauthsession.SubjectTypeUser).
		SetFlowType(oauthsession.FlowTypeBind).
		SetSubjectUID(target.UID).
		SetPkceVerifierCiphertext("ciphertext").
		SetExpiresAt(time.Now().Add(time.Hour)).
		Save(ctx); err != nil {
		t.Fatalf("create OAuth session: %v", err)
	}
	if _, err := client.CasbinRule.Create().SetPtype("g").SetV0(target.UID).SetV1(r.Name).SetV2("default").Save(ctx); err != nil {
		t.Fatalf("create Casbin role: %v", err)
	}

	if err := NewUserService().DeleteManagedUser(ctx, target.ID, "admin-uid"); err != nil {
		t.Fatalf("DeleteManagedUser() error = %v", err)
	}
	checks := []struct {
		name  string
		count func() (int, error)
	}{
		{"users", func() (int, error) { return client.User.Query().Where(user.IDEQ(target.ID)).Count(ctx) }},
		{"user roles", func() (int, error) { return client.UserRole.Query().Where(userrole.UserIDEQ(target.ID)).Count(ctx) }},
		{"blacklist entries", func() (int, error) { return client.Blacklist.Query().Where(blacklist.UserIDEQ(target.ID)).Count(ctx) }},
		{"third-party identities", func() (int, error) {
			return client.ThirdPartyUser.Query().Where(thirdpartyuser.UserIDEQ(target.ID)).Count(ctx)
		}},
		{"messages", func() (int, error) { return client.Message.Query().Where(message.UserUIDEQ(target.UID)).Count(ctx) }},
		{"message preferences", func() (int, error) {
			return client.MessagePreference.Query().Where(messagepreference.UserUIDEQ(target.UID)).Count(ctx)
		}},
		{"OAuth sessions", func() (int, error) {
			return client.OAuthSession.Query().Where(oauthsession.SubjectUIDEQ(target.UID)).Count(ctx)
		}},
		{"Casbin roles", func() (int, error) {
			return client.CasbinRule.Query().Where(casbinrule.PtypeEQ("g"), casbinrule.V0EQ(target.UID)).Count(ctx)
		}},
	}
	for _, check := range checks {
		count, err := check.count()
		if err != nil || count != 0 {
			t.Errorf("%s after delete: count=%d err=%v", check.name, count, err)
		}
	}
	if err := NewUserService().DeleteManagedUser(ctx, target.ID, "admin-uid"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("second delete error = %v, want %v", err, ErrUserNotFound)
	}
}

func TestUserServiceDeleteManagedUserRejectsSelfDelete(t *testing.T) {
	client := setupUserTest(t)
	ctx := t.Context()
	previousEnforcer := global.EnforcerManager
	global.EnforcerManager = nil
	t.Cleanup(func() { global.EnforcerManager = previousEnforcer })

	target := createTestUserForUserService(t, client, "self@example.com", "managed-self", "selfuser", user.StatusActive)

	if err := NewUserService().DeleteManagedUser(ctx, target.ID, target.UID); !errors.Is(err, ErrCannotDeleteSelf) {
		t.Fatalf("self delete error = %v, want %v", err, ErrCannotDeleteSelf)
	}

	// 自我保护触发时用户必须仍然存在
	exists, err := client.User.Query().Where(user.IDEQ(target.ID)).Exist(ctx)
	if err != nil || !exists {
		t.Fatalf("用户在自我保护后不应被删除: exists=%v err=%v", exists, err)
	}
}
