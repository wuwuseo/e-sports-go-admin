package service

import (
	"errors"
	"testing"

	"go-admin/internal/ent/user"
)

func TestUserServiceLoginRejectsOAuthOnlyUserAsInvalidPassword(t *testing.T) {
	client, _, _ := setupOAuthAuthTest(t)
	_, err := client.User.Create().
		SetEmail("passwordless@example.com").
		SetUID("passwordless-uid").
		SetStatus(user.StatusActive).
		Save(t.Context())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	_, err = NewUserService().Login(t.Context(), &LoginRequest{Email: "passwordless@example.com", Password: "guess"})
	if !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("expected ErrInvalidPassword, got %v", err)
	}
}
