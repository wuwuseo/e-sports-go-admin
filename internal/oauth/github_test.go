package oauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

func TestGitHubAuthCodeURLUsesStateAndPKCE(t *testing.T) {
	provider := NewGitHubProvider()
	config := ProviderConfig{ClientID: "client-id", RedirectURL: "https://api.example.com/callback"}
	authURL := provider.AuthCodeURL(config, "state-value", "verifier-value")

	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth URL: %v", err)
	}
	query := parsed.Query()
	if query.Get("state") != "state-value" {
		t.Fatalf("unexpected state %q", query.Get("state"))
	}
	if query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" {
		t.Fatal("PKCE S256 challenge missing")
	}
	if !strings.Contains(query.Get("scope"), "user:email") {
		t.Fatalf("user:email scope missing: %q", query.Get("scope"))
	}
}

func TestGitHubFetchIdentitySelectsVerifiedEmail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access-token" {
			t.Fatalf("missing bearer token: %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/user":
			_, _ = w.Write([]byte(`{"id":12345,"login":"octocat","name":"The Octocat","avatar_url":"https://example.com/avatar.png"}`))
		case "/user/emails":
			_, _ = w.Write([]byte(`[{"email":"private@example.com","primary":true,"verified":false},{"email":"verified@example.com","primary":false,"verified":true}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	provider := NewGitHubProvider(WithGitHubAPIBaseURL(server.URL), WithGitHubHTTPClient(server.Client()))
	identity, err := provider.FetchIdentity(context.Background(), &oauth2.Token{AccessToken: "access-token", TokenType: "Bearer"})
	if err != nil {
		t.Fatalf("FetchIdentity: %v", err)
	}
	if identity.ID != "12345" || identity.Username != "octocat" {
		t.Fatalf("unexpected identity: %#v", identity)
	}
	if identity.Email != "verified@example.com" || !identity.EmailVerified {
		t.Fatalf("unexpected verified email: %#v", identity)
	}
}

func TestGitHubFetchIdentityRequiresVerifiedEmail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			_, _ = w.Write([]byte(`{"id":99,"login":"no-email"}`))
		case "/user/emails":
			_, _ = w.Write([]byte(`[{"email":"unverified@example.com","primary":true,"verified":false}]`))
		}
	}))
	t.Cleanup(server.Close)

	provider := NewGitHubProvider(WithGitHubAPIBaseURL(server.URL), WithGitHubHTTPClient(server.Client()))
	_, err := provider.FetchIdentity(context.Background(), &oauth2.Token{AccessToken: "access-token", TokenType: "Bearer"})
	if !errors.Is(err, ErrVerifiedEmailRequired) {
		t.Fatalf("expected ErrVerifiedEmailRequired, got %v", err)
	}
}

func TestGitHubFetchIdentityRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			_, _ = w.Write([]byte(`{"id":123,"login":"octocat"}` + strings.Repeat(" ", maxGitHubBodySize+1)))
		case "/user/emails":
			_, _ = w.Write([]byte(`[{"email":"verified@example.com","primary":true,"verified":true}]`))
		}
	}))
	t.Cleanup(server.Close)

	provider := NewGitHubProvider(WithGitHubAPIBaseURL(server.URL), WithGitHubHTTPClient(server.Client()))
	if _, err := provider.FetchIdentity(context.Background(), &oauth2.Token{AccessToken: "access-token"}); err == nil {
		t.Fatal("expected oversized GitHub response to be rejected")
	}
}

func TestGitHubFetchIdentityRejectsInvalidStableID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			_, _ = w.Write([]byte(`{"id":0,"login":"octocat"}`))
		case "/user/emails":
			_, _ = w.Write([]byte(`[{"email":"verified@example.com","primary":true,"verified":true}]`))
		}
	}))
	t.Cleanup(server.Close)

	provider := NewGitHubProvider(WithGitHubAPIBaseURL(server.URL), WithGitHubHTTPClient(server.Client()))
	if _, err := provider.FetchIdentity(context.Background(), &oauth2.Token{AccessToken: "access-token"}); err == nil {
		t.Fatal("expected invalid GitHub stable ID to be rejected")
	}
}
