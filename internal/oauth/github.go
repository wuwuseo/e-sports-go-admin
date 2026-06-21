package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/endpoints"
)

const (
	githubProviderName = "github"
	maxGitHubBodySize  = 1 << 20
)

var ErrVerifiedEmailRequired = errors.New("verified provider email required")

type GitHubOption func(*GitHubProvider)

type GitHubProvider struct {
	apiBaseURL string
	httpClient *http.Client
	endpoint   oauth2.Endpoint
}

func NewGitHubProvider(options ...GitHubOption) *GitHubProvider {
	provider := &GitHubProvider{
		apiBaseURL: "https://api.github.com",
		httpClient: &http.Client{Timeout: 10 * time.Second},
		endpoint:   endpoints.GitHub,
	}
	for _, option := range options {
		option(provider)
	}
	return provider
}

func WithGitHubAPIBaseURL(baseURL string) GitHubOption {
	return func(provider *GitHubProvider) {
		provider.apiBaseURL = strings.TrimRight(baseURL, "/")
	}
}

func WithGitHubHTTPClient(client *http.Client) GitHubOption {
	return func(provider *GitHubProvider) {
		if client != nil {
			provider.httpClient = client
		}
	}
}

func WithGitHubOAuthEndpoint(endpoint oauth2.Endpoint) GitHubOption {
	return func(provider *GitHubProvider) { provider.endpoint = endpoint }
}

func (p *GitHubProvider) Name() string        { return githubProviderName }
func (p *GitHubProvider) DisplayName() string { return "GitHub" }

func (p *GitHubProvider) oauthConfig(config ProviderConfig) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     config.ClientID,
		ClientSecret: config.ClientSecret,
		RedirectURL:  config.RedirectURL,
		Scopes:       []string{"read:user", "user:email"},
		Endpoint:     p.endpoint,
	}
}

func (p *GitHubProvider) AuthCodeURL(config ProviderConfig, state, verifier string) string {
	return p.oauthConfig(config).AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))
}

func (p *GitHubProvider) Exchange(ctx context.Context, config ProviderConfig, code, verifier string) (*oauth2.Token, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, p.httpClient)
	return p.oauthConfig(config).Exchange(ctx, code, oauth2.VerifierOption(verifier))
}

type gitHubUser struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}

type gitHubEmail struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

func (p *GitHubProvider) FetchIdentity(ctx context.Context, token *oauth2.Token) (*Identity, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, p.httpClient)
	client := oauth2.NewClient(ctx, oauth2.StaticTokenSource(token))

	var user gitHubUser
	if err := p.getJSON(ctx, client, "/user", &user); err != nil {
		return nil, fmt.Errorf("fetch GitHub user: %w", err)
	}
	if user.ID <= 0 {
		return nil, errors.New("GitHub user returned an invalid stable ID")
	}
	var emails []gitHubEmail
	if err := p.getJSON(ctx, client, "/user/emails", &emails); err != nil {
		return nil, fmt.Errorf("fetch GitHub emails: %w", err)
	}

	email := selectVerifiedGitHubEmail(emails)
	if email == "" {
		return nil, ErrVerifiedEmailRequired
	}
	return &Identity{
		ID:            strconv.FormatInt(user.ID, 10),
		Email:         email,
		EmailVerified: true,
		Username:      user.Login,
		Name:          user.Name,
		AvatarURL:     user.AvatarURL,
		Extra: map[string]any{
			"login": user.Login,
		},
	}, nil
}

func selectVerifiedGitHubEmail(emails []gitHubEmail) string {
	for _, email := range emails {
		if email.Primary && email.Verified {
			return email.Email
		}
	}
	for _, email := range emails {
		if email.Verified {
			return email.Email
		}
	}
	return ""
}

func (p *GitHubProvider) getJSON(ctx context.Context, client *http.Client, path string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.apiBaseURL+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "go-admin-oauth")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxGitHubBodySize))
		return fmt.Errorf("GitHub API returned status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxGitHubBodySize+1))
	if err != nil {
		return err
	}
	if len(body) > maxGitHubBodySize {
		return errors.New("GitHub API response exceeds size limit")
	}
	if err := json.Unmarshal(body, target); err != nil {
		return err
	}
	return nil
}
