package service

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/thirdpartyuser"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"
	appoauth "go-admin/internal/oauth"

	"github.com/wuwuseo/cmf/bootstrap"
	cmfConfig "github.com/wuwuseo/cmf/config"
	"golang.org/x/oauth2"
)

type fakeGitHubProvider struct {
	identity *appoauth.Identity
}

func (p *fakeGitHubProvider) Name() string        { return "github" }
func (p *fakeGitHubProvider) DisplayName() string { return "GitHub" }
func (p *fakeGitHubProvider) AuthCodeURL(_ appoauth.ProviderConfig, state, verifier string) string {
	return "https://github.test/authorize?state=" + url.QueryEscape(state) + "&verifier=" + url.QueryEscape(verifier)
}
func (p *fakeGitHubProvider) Exchange(context.Context, appoauth.ProviderConfig, string, string) (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: "provider-token", TokenType: "Bearer"}, nil
}
func (p *fakeGitHubProvider) FetchIdentity(context.Context, *oauth2.Token) (*appoauth.Identity, error) {
	return p.identity, nil
}

func setupOAuthAuthTest(t *testing.T) (*ent.Client, *OAuthAuthService, *fakeGitHubProvider) {
	client, cipher := setupOAuthServiceTest(t)
	provider := &fakeGitHubProvider{identity: &appoauth.Identity{
		ID:            "12345",
		Email:         "octocat@example.com",
		EmailVerified: true,
		Username:      "octocat",
		Name:          "The Octocat",
		AvatarURL:     "https://example.com/avatar.png",
	}}
	registry := appoauth.NewRegistry(provider)
	configService := NewOAuthProviderConfigService(client, cipher, registry)
	secret := "github-secret"
	_, err := configService.Upsert(t.Context(), OAuthProviderConfigInput{
		Provider:         "github",
		DisplayName:      "GitHub",
		ClientID:         "github-client",
		ClientSecret:     &secret,
		CallbackURL:      "https://api.example.com/api/v1/public/oauth/github/callback",
		UserRedirectURL:  "https://app.example.com/oauth/callback",
		AdminRedirectURL: "https://admin.example.com/oauth/callback",
		UserEnabled:      true,
		AdminEnabled:     true,
	})
	if err != nil {
		t.Fatalf("configure provider: %v", err)
	}
	now := time.Date(2026, 6, 21, 2, 0, 0, 0, time.UTC)
	sessions := NewOAuthSessionService(client, cipher, func() time.Time { return now })

	cfg := &cmfConfig.Config{}
	cfg.App.Name = "oauth-test"
	cfg.App.Secret = "user-secret"
	cfg.App.LoginExpires = 3600
	cfg.App.RefreshExpires = 7200
	cfg.App.AdminSecret = "admin-secret"
	cfg.App.AdminLoginExpires = 1800
	cfg.App.AdminRefreshExpires = 3600
	bs := bootstrap.NewBootstrap()
	bs.RegisterService("config", cfg)
	previousBootstrap := global.Bootstrap
	previousORM := global.ORM
	global.Bootstrap = bs
	global.ORM = client
	t.Cleanup(func() {
		global.Bootstrap = previousBootstrap
		global.ORM = previousORM
	})

	return client, NewOAuthAuthService(client, registry, configService, sessions, func() time.Time { return now }), provider
}

func TestOAuthAuthServiceCreatesUserAndExchangesLoginCode(t *testing.T) {
	client, service, _ := setupOAuthAuthTest(t)
	started, err := service.Begin(t.Context(), "github", "user", "login", "")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if started.AuthorizationURL == "" || started.State == "" {
		t.Fatalf("unexpected begin result: %#v", started)
	}

	callback, err := service.HandleCallback(t.Context(), "github", started.State, "provider-code")
	if err != nil {
		t.Fatalf("HandleCallback: %v", err)
	}
	if callback.LoginCode == "" || callback.RedirectURL != "https://app.example.com/oauth/callback" {
		t.Fatalf("unexpected callback result: %#v", callback)
	}

	created, err := client.User.Query().Only(t.Context())
	if err != nil {
		t.Fatalf("query user: %v", err)
	}
	if created.Status != user.StatusActive || created.Password != "" || created.Email != "octocat@example.com" {
		t.Fatalf("unexpected OAuth user: %#v", created)
	}
	identity, err := client.ThirdPartyUser.Query().Where(thirdpartyuser.PlatformEQ("github")).Only(t.Context())
	if err != nil || identity.UserID != created.ID || identity.Openid != "12345" {
		t.Fatalf("unexpected user identity: %#v, err=%v", identity, err)
	}

	tokens, err := service.ExchangeLoginCode(t.Context(), callback.LoginCode)
	if err != nil {
		t.Fatalf("ExchangeLoginCode: %v", err)
	}
	if tokens.AccessToken == "" || tokens.RefreshToken == "" {
		t.Fatalf("missing site tokens: %#v", tokens)
	}
	if _, err := service.ExchangeLoginCode(t.Context(), callback.LoginCode); !errors.Is(err, ErrOAuthLoginCodeInvalid) {
		t.Fatalf("expected one-time code rejection, got %v", err)
	}
}

func TestOAuthAuthServiceRequiresAdminPrebinding(t *testing.T) {
	client, service, _ := setupOAuthAuthTest(t)
	adminAccount, err := client.Admin.Create().
		SetEmail("admin@example.com").
		SetUID("admin-uid").
		SetUsername("admin").
		SetPassword("hashed-password").
		SetPasswordSalt("salt").
		SetStatus(admin.StatusActive).
		Save(t.Context())
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}

	login, err := service.Begin(t.Context(), "github", "admin", "login", "")
	if err != nil {
		t.Fatalf("begin admin login: %v", err)
	}
	if _, err := service.HandleCallback(t.Context(), "github", login.State, "provider-code"); !errors.Is(err, ErrOAuthAdminNotBound) {
		t.Fatalf("expected unbound admin rejection, got %v", err)
	}

	bind, err := service.Begin(t.Context(), "github", "admin", "bind", adminAccount.UID)
	if err != nil {
		t.Fatalf("begin admin bind: %v", err)
	}
	bound, err := service.HandleCallback(t.Context(), "github", bind.State, "provider-code")
	if err != nil {
		t.Fatalf("bind callback: %v", err)
	}
	if bound.LoginCode != "" || bound.FlowType != "bind" {
		t.Fatalf("unexpected bind result: %#v", bound)
	}

	login, err = service.Begin(t.Context(), "github", "admin", "login", "")
	if err != nil {
		t.Fatalf("begin bound admin login: %v", err)
	}
	callback, err := service.HandleCallback(t.Context(), "github", login.State, "provider-code")
	if err != nil {
		t.Fatalf("bound admin callback: %v", err)
	}
	tokens, err := service.ExchangeLoginCode(t.Context(), callback.LoginCode)
	if err != nil || tokens.AccessToken == "" {
		t.Fatalf("exchange admin login code: tokens=%#v err=%v", tokens, err)
	}
}

func TestOAuthAuthServiceMatchesVerifiedEmailCaseInsensitively(t *testing.T) {
	client, authService, provider := setupOAuthAuthTest(t)
	existing, err := client.User.Create().
		SetEmail("OctoCat@Example.com").
		SetUID("existing-user-uid").
		SetPassword("configured-password").
		SetStatus(user.StatusActive).
		Save(t.Context())
	if err != nil {
		t.Fatalf("create existing user: %v", err)
	}
	provider.identity.Email = "octocat@example.com"

	started, err := authService.Begin(t.Context(), "github", "user", "login", "")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := authService.HandleCallback(t.Context(), "github", started.State, "provider-code"); err != nil {
		t.Fatalf("HandleCallback: %v", err)
	}
	count, err := client.User.Query().Count(t.Context())
	if err != nil || count != 1 {
		t.Fatalf("verified email created duplicate user: count=%d err=%v", count, err)
	}
	binding, err := client.ThirdPartyUser.Query().Only(t.Context())
	if err != nil || binding.UserID != existing.ID {
		t.Fatalf("identity was not attached to existing user: %#v err=%v", binding, err)
	}
}

func TestOAuthAuthServiceConsumesProviderDenialState(t *testing.T) {
	_, authService, _ := setupOAuthAuthTest(t)
	started, err := authService.Begin(t.Context(), "github", "user", "login", "")
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	result, err := authService.HandleCallbackError(t.Context(), "github", started.State, "access_denied")
	if err == nil || result == nil || result.RedirectURL != "https://app.example.com/oauth/callback" {
		t.Fatalf("unexpected denial result: %#v err=%v", result, err)
	}
	if _, err := authService.HandleCallbackError(t.Context(), "github", started.State, "access_denied"); !errors.Is(err, ErrOAuthSessionInvalid) {
		t.Fatalf("expected denied state replay rejection, got %v", err)
	}
}
