package service

import (
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"

	"github.com/golang-jwt/jwt/v5"
	"github.com/wuwuseo/cmf/bootstrap"
	cmfConfig "github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/crypto"

	_ "github.com/mattn/go-sqlite3"
)

func setupTokenIssuerTest(t *testing.T) (*ent.Client, func()) {
	client := enttest.Open(t, "sqlite3", "file:tokenissue?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	prevORM := global.ORM
	global.ORM = client

	cfg := &cmfConfig.Config{}
	cfg.App.Name = "test-token"
	cfg.App.Secret = "user-test-secret-key"
	cfg.App.LoginExpires = 3600
	cfg.App.RefreshExpires = 86400
	cfg.App.AdminSecret = "admin-test-secret-key"
	cfg.App.AdminLoginExpires = 7200
	cfg.App.AdminRefreshExpires = 86400

	bs := bootstrap.NewBootstrap()
	bs.RegisterService("config", cfg)

	prevBootstrap := global.Bootstrap
	global.Bootstrap = bs

	cleanup := func() {
		global.ORM = prevORM
		global.Bootstrap = prevBootstrap
	}

	return client, cleanup
}

func createTokenTestUser(t *testing.T, client *ent.Client, uid, username string, status user.Status) *ent.User {
	ctx := t.Context()
	u, err := client.User.Create().
		SetUID(uid).
		SetUsername(username).
		SetEmail(username + "@test.com").
		SetPassword("hashed").
		SetPasswordSalt("salt").
		SetStatus(status).
		Save(ctx)
	if err != nil {
		t.Fatalf("create test user failed: %v", err)
	}
	return u
}

func createTokenTestAdmin(t *testing.T, client *ent.Client, uid, username string, status admin.Status) *ent.Admin {
	ctx := t.Context()
	hashedPassword, salt, err := crypto.HashPassword("password")
	if err != nil {
		t.Fatalf("hash password failed: %v", err)
	}
	a, err := client.Admin.Create().
		SetUID(uid).
		SetUsername(username).
		SetEmail(username + "@test.com").
		SetPassword(hashedPassword).
		SetPasswordSalt(salt).
		SetStatus(status).
		Save(ctx)
	if err != nil {
		t.Fatalf("create test admin failed: %v", err)
	}
	return a
}

func TestIssueUserToken_Success(t *testing.T) {
	client, cleanup := setupTokenIssuerTest(t)
	defer cleanup()

	now := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	createdUser := createTokenTestUser(t, client, "user-1", "testuser", user.StatusActive)

	token, err := issueUserToken(t.Context(), client, createdUser.UID, now)
	if err != nil {
		t.Fatalf("issueUserToken() error = %v", err)
	}

	if token == nil {
		t.Fatal("token should not be nil")
	}
	if token.TokenType != "Bearer" {
		t.Errorf("TokenType = %q, want Bearer", token.TokenType)
	}
	if token.ExpiresIn != 3600 {
		t.Errorf("ExpiresIn = %d, want 3600", token.ExpiresIn)
	}
	if token.AccessToken == "" || token.RefreshToken == "" {
		t.Error("access token and refresh token should not be empty")
	}

	updatedUser, err := client.User.Query().Where(user.UIDEQ(createdUser.UID)).Only(t.Context())
	if err != nil {
		t.Fatalf("query user failed: %v", err)
	}
	if !updatedUser.LastLogin.Equal(now) {
		t.Errorf("LastLogin = %v, want %v", updatedUser.LastLogin, now)
	}
}

func TestIssueUserToken_NotFound(t *testing.T) {
	client, cleanup := setupTokenIssuerTest(t)
	defer cleanup()

	_, err := issueUserToken(t.Context(), client, "non-existent", time.Now())
	if err != ErrUserNotFound {
		t.Errorf("expected ErrUserNotFound, got %v", err)
	}
}

func TestIssueUserToken_InactiveStatus(t *testing.T) {
	client, cleanup := setupTokenIssuerTest(t)
	defer cleanup()

	createdUser := createTokenTestUser(t, client, "user-inactive", "inactive", user.StatusInactive)

	_, err := issueUserToken(t.Context(), client, createdUser.UID, time.Now())
	if err != ErrInvalidUserStatus {
		t.Errorf("expected ErrInvalidUserStatus, got %v", err)
	}
}

func TestIssueAdminToken_Success(t *testing.T) {
	client, cleanup := setupTokenIssuerTest(t)
	defer cleanup()

	now := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	createdAdmin := createTokenTestAdmin(t, client, "admin-1", "testadmin", admin.StatusActive)

	token, err := issueAdminToken(t.Context(), client, createdAdmin.UID, now)
	if err != nil {
		t.Fatalf("issueAdminToken() error = %v", err)
	}

	if token == nil {
		t.Fatal("token should not be nil")
	}
	if token.TokenType != "Bearer" {
		t.Errorf("TokenType = %q, want Bearer", token.TokenType)
	}
	if token.ExpiresIn != 7200 {
		t.Errorf("ExpiresIn = %d, want 7200", token.ExpiresIn)
	}
	if token.AccessToken == "" || token.RefreshToken == "" {
		t.Error("access token and refresh token should not be empty")
	}

	updatedAdmin, err := client.Admin.Query().Where(admin.UIDEQ(createdAdmin.UID)).Only(t.Context())
	if err != nil {
		t.Fatalf("query admin failed: %v", err)
	}
	if !updatedAdmin.LastLogin.Equal(now) {
		t.Errorf("LastLogin = %v, want %v", updatedAdmin.LastLogin, now)
	}
}

func TestIssueAdminToken_NotFound(t *testing.T) {
	client, cleanup := setupTokenIssuerTest(t)
	defer cleanup()

	_, err := issueAdminToken(t.Context(), client, "non-existent", time.Now())
	if err != ErrAdminNotFound {
		t.Errorf("expected ErrAdminNotFound, got %v", err)
	}
}

func TestIssueAdminToken_InactiveStatus(t *testing.T) {
	client, cleanup := setupTokenIssuerTest(t)
	defer cleanup()

	createdAdmin := createTokenTestAdmin(t, client, "admin-inactive", "inactive", admin.StatusInactive)

	_, err := issueAdminToken(t.Context(), client, createdAdmin.UID, time.Now())
	if err != ErrInvalidAdminStatus {
		t.Errorf("expected ErrInvalidAdminStatus, got %v", err)
	}
}

func TestIssueAdminToken_ClaimsContainCorrectIssuer(t *testing.T) {
	client, cleanup := setupTokenIssuerTest(t)
	defer cleanup()

	createdAdmin := createTokenTestAdmin(t, client, "admin-claims", "claimsadmin", admin.StatusActive)
	token, err := issueAdminToken(t.Context(), client, createdAdmin.UID, time.Now())
	if err != nil {
		t.Fatalf("issueAdminToken() error = %v", err)
	}

	parsedToken, err := jwt.Parse(token.AccessToken, func(*jwt.Token) (any, error) {
		return []byte("admin-test-secret-key"), nil
	}, jwt.WithoutClaimsValidation())
	if err != nil {
		t.Fatalf("parse token failed: %v", err)
	}

	claims := parsedToken.Claims.(jwt.MapClaims)
	if claims["issuer"] != "test-token" {
		t.Errorf("issuer = %v, want test-token", claims["issuer"])
	}
	if claims["subject"] != "claimsadmin" {
		t.Errorf("subject = %v, want claimsadmin", claims["subject"])
	}
	if claims["id"] != "admin-claims" {
		t.Errorf("id = %v, want admin-claims", claims["id"])
	}
}
