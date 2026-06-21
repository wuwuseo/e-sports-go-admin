package service

import (
	"errors"
	"strings"
	"testing"

	appoauth "go-admin/internal/oauth"
)

func TestOAuthProviderConfigServiceEncryptsAndMasksSecret(t *testing.T) {
	client, cipher := setupOAuthServiceTest(t)
	registry := appoauth.NewRegistry(appoauth.NewGitHubProvider())
	service := NewOAuthProviderConfigService(client, cipher, registry)
	secret := "github-secret"

	response, err := service.Upsert(t.Context(), OAuthProviderConfigInput{
		Provider:         "github",
		DisplayName:      "GitHub",
		ClientID:         "github-client",
		ClientSecret:     &secret,
		CallbackURL:      "https://api.example.com/api/v1/public/oauth/github/callback",
		UserRedirectURL:  "https://app.example.com/oauth/callback",
		AdminRedirectURL: "https://admin.example.com/oauth/callback",
		UserEnabled:      true,
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if !response.SecretConfigured || response.ClientSecret != oauthSecretMask || response.ClientSecret == secret {
		t.Fatalf("secret response was not masked: %#v", response)
	}

	stored, err := client.OAuthProviderConfig.Query().Only(t.Context())
	if err != nil {
		t.Fatalf("query stored config: %v", err)
	}
	if stored.ClientSecretCiphertext == secret || strings.Contains(stored.ClientSecretCiphertext, secret) {
		t.Fatal("client secret stored in plaintext")
	}

	runtime, redirectURL, err := service.Runtime(t.Context(), "github", "user")
	if err != nil {
		t.Fatalf("Runtime: %v", err)
	}
	if runtime.ClientSecret != secret || redirectURL != response.UserRedirectURL {
		t.Fatalf("unexpected runtime config: %#v, redirect=%q", runtime, redirectURL)
	}
	masked := response.ClientSecret
	if _, err := service.Upsert(t.Context(), OAuthProviderConfigInput{
		Provider:         "github",
		DisplayName:      "GitHub",
		ClientID:         "github-client-updated",
		ClientSecret:     &masked,
		CallbackURL:      response.CallbackURL,
		UserRedirectURL:  response.UserRedirectURL,
		AdminRedirectURL: response.AdminRedirectURL,
		UserEnabled:      true,
	}); err != nil {
		t.Fatalf("Upsert with masked secret: %v", err)
	}
	runtime, _, err = service.Runtime(t.Context(), "github", "user")
	if err != nil || runtime.ClientSecret != secret {
		t.Fatalf("masked update replaced stored secret: %#v err=%v", runtime, err)
	}
	if _, _, err := service.Runtime(t.Context(), "github", "admin"); !errors.Is(err, ErrOAuthProviderDisabled) {
		t.Fatalf("expected admin provider disabled, got %v", err)
	}
}

func TestOAuthProviderConfigServiceRejectsUnregisteredProvider(t *testing.T) {
	client, cipher := setupOAuthServiceTest(t)
	service := NewOAuthProviderConfigService(client, cipher, appoauth.NewRegistry(appoauth.NewGitHubProvider()))
	if _, err := service.Upsert(t.Context(), OAuthProviderConfigInput{Provider: "google", DisplayName: "Google"}); !errors.Is(err, ErrOAuthProviderNotFound) {
		t.Fatalf("expected unregistered provider error, got %v", err)
	}
}

func TestOAuthProviderConfigServiceValidatesURLsAndSupportsListDelete(t *testing.T) {
	client, cipher := setupOAuthServiceTest(t)
	service := NewOAuthProviderConfigService(client, cipher, appoauth.NewRegistry(appoauth.NewGitHubProvider()))
	secret := "github-secret"
	input := OAuthProviderConfigInput{
		Provider:         "github",
		DisplayName:      "GitHub",
		ClientID:         "github-client",
		ClientSecret:     &secret,
		CallbackURL:      "http://api.example.com/oauth/callback",
		UserRedirectURL:  "https://app.example.com/oauth/callback",
		AdminRedirectURL: "https://admin.example.com/oauth/callback",
	}
	if _, err := service.Upsert(t.Context(), input); !errors.Is(err, ErrOAuthInvalidURL) {
		t.Fatalf("expected invalid URL error, got %v", err)
	}

	input.CallbackURL = "http://localhost:8080/api/v1/public/oauth/github/callback"
	if _, err := service.Upsert(t.Context(), input); err != nil {
		t.Fatalf("Upsert localhost config: %v", err)
	}
	configs, err := service.List(t.Context())
	if err != nil || len(configs) != 1 || configs[0].Provider != "github" || !configs[0].SecretConfigured {
		t.Fatalf("unexpected config list: %#v err=%v", configs, err)
	}
	if err := service.Delete(t.Context(), "github"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := service.Get(t.Context(), "github"); !errors.Is(err, ErrOAuthProviderNotFound) {
		t.Fatalf("expected deleted config to be missing, got %v", err)
	}
}

func TestOAuthProviderConfigServiceRejectsInvalidStoredURLAndEmptySecret(t *testing.T) {
	client, cipher := setupOAuthServiceTest(t)
	service := NewOAuthProviderConfigService(client, cipher, appoauth.NewRegistry(appoauth.NewGitHubProvider()))
	secret := "github-secret"
	input := OAuthProviderConfigInput{
		Provider:         "github",
		DisplayName:      "GitHub",
		ClientID:         "github-client",
		ClientSecret:     &secret,
		CallbackURL:      "https://api.example.com/api/v1/public/oauth/github/callback",
		UserRedirectURL:  "https://app.example.com/oauth/callback",
		AdminRedirectURL: "https://admin.example.com/oauth/callback",
		UserEnabled:      true,
	}
	if _, err := service.Upsert(t.Context(), input); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	stored, err := client.OAuthProviderConfig.Query().Only(t.Context())
	if err != nil {
		t.Fatalf("query config: %v", err)
	}
	if _, err := client.OAuthProviderConfig.UpdateOne(stored).
		SetUserRedirectURL("http://example.com/unsafe").
		Save(t.Context()); err != nil {
		t.Fatalf("tamper stored redirect: %v", err)
	}
	if _, _, err := service.Runtime(t.Context(), "github", "user"); !errors.Is(err, ErrOAuthInvalidURL) {
		t.Fatalf("expected unsafe stored redirect rejection, got %v", err)
	}

	input.UserRedirectURL = "https://app.example.com/oauth/callback"
	emptySecret := ""
	input.ClientSecret = &emptySecret
	if _, err := service.Upsert(t.Context(), input); err != nil {
		t.Fatalf("clear secret with empty value: %v", err)
	}
	if _, _, err := service.Runtime(t.Context(), "github", "user"); !errors.Is(err, ErrOAuthConfigIncomplete) {
		t.Fatalf("expected empty secret to make config incomplete, got %v", err)
	}
}
