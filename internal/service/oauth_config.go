package service

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"go-admin/internal/ent"
	"go-admin/internal/ent/oauthproviderconfig"
	appoauth "go-admin/internal/oauth"
)

var (
	ErrOAuthProviderNotFound = errors.New("OAuth provider is not registered")
	ErrOAuthProviderDisabled = errors.New("OAuth provider is disabled")
	ErrOAuthConfigIncomplete = errors.New("OAuth provider configuration is incomplete")
	ErrOAuthInvalidURL       = errors.New("OAuth URL must use HTTPS, except for localhost development")
)

const oauthSecretMask = "********"

type OAuthProviderConfigInput struct {
	Provider         string  `json:"provider" validate:"required,max=50"`
	DisplayName      string  `json:"display_name" validate:"required,max=100"`
	ClientID         string  `json:"client_id" validate:"max=255"`
	ClientSecret     *string `json:"client_secret" validate:"omitempty,max=2048"`
	ClearSecret      bool    `json:"clear_secret"`
	CallbackURL      string  `json:"callback_url" validate:"max=2048"`
	UserRedirectURL  string  `json:"user_redirect_url" validate:"max=2048"`
	AdminRedirectURL string  `json:"admin_redirect_url" validate:"max=2048"`
	UserEnabled      bool    `json:"user_enabled"`
	AdminEnabled     bool    `json:"admin_enabled"`
}

type OAuthProviderConfigResponse struct {
	Provider         string `json:"provider"`
	DisplayName      string `json:"display_name"`
	ClientID         string `json:"client_id"`
	ClientSecret     string `json:"client_secret,omitempty"`
	SecretConfigured bool   `json:"secret_configured"`
	CallbackURL      string `json:"callback_url"`
	UserRedirectURL  string `json:"user_redirect_url"`
	AdminRedirectURL string `json:"admin_redirect_url"`
	UserEnabled      bool   `json:"user_enabled"`
	AdminEnabled     bool   `json:"admin_enabled"`
}

type PublicOAuthProvider struct {
	Provider    string `json:"provider"`
	DisplayName string `json:"display_name"`
}

type OAuthProviderConfigService struct {
	client   *ent.Client
	cipher   *appoauth.SecretCipher
	registry *appoauth.Registry
}

func NewOAuthProviderConfigService(client *ent.Client, cipher *appoauth.SecretCipher, registry *appoauth.Registry) *OAuthProviderConfigService {
	return &OAuthProviderConfigService{client: client, cipher: cipher, registry: registry}
}

func (s *OAuthProviderConfigService) Upsert(ctx context.Context, input OAuthProviderConfigInput) (*OAuthProviderConfigResponse, error) {
	providerName := strings.ToLower(strings.TrimSpace(input.Provider))
	if _, ok := s.registry.Get(providerName); !ok {
		return nil, ErrOAuthProviderNotFound
	}
	if err := validateOAuthURLs(input.CallbackURL, input.UserRedirectURL, input.AdminRedirectURL); err != nil {
		return nil, err
	}
	existing, err := s.client.OAuthProviderConfig.Query().Where(oauthproviderconfig.ProviderEQ(providerName)).Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	secretCiphertext := ""
	if existing != nil {
		secretCiphertext = existing.ClientSecretCiphertext
	}
	if input.ClearSecret {
		secretCiphertext = ""
	}
	if input.ClientSecret != nil && *input.ClientSecret != oauthSecretMask {
		if *input.ClientSecret == "" {
			secretCiphertext = ""
		} else {
			secretCiphertext, err = s.cipher.Encrypt(*input.ClientSecret)
			if err != nil {
				return nil, err
			}
		}
	}

	if existing == nil {
		existing, err = s.client.OAuthProviderConfig.Create().
			SetProvider(providerName).
			SetDisplayName(input.DisplayName).
			SetClientID(input.ClientID).
			SetClientSecretCiphertext(secretCiphertext).
			SetCallbackURL(input.CallbackURL).
			SetUserRedirectURL(input.UserRedirectURL).
			SetAdminRedirectURL(input.AdminRedirectURL).
			SetUserEnabled(input.UserEnabled).
			SetAdminEnabled(input.AdminEnabled).
			Save(ctx)
	} else {
		update := s.client.OAuthProviderConfig.UpdateOne(existing).
			SetDisplayName(input.DisplayName).
			SetClientID(input.ClientID).
			SetCallbackURL(input.CallbackURL).
			SetUserRedirectURL(input.UserRedirectURL).
			SetAdminRedirectURL(input.AdminRedirectURL).
			SetUserEnabled(input.UserEnabled).
			SetAdminEnabled(input.AdminEnabled)
		if secretCiphertext == "" {
			update.ClearClientSecretCiphertext()
		} else {
			update.SetClientSecretCiphertext(secretCiphertext)
		}
		existing, err = update.Save(ctx)
	}
	if err != nil {
		return nil, err
	}
	return toOAuthProviderConfigResponse(existing), nil
}

func (s *OAuthProviderConfigService) Get(ctx context.Context, provider string) (*OAuthProviderConfigResponse, error) {
	config, err := s.client.OAuthProviderConfig.Query().Where(oauthproviderconfig.ProviderEQ(strings.ToLower(provider))).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrOAuthProviderNotFound
		}
		return nil, err
	}
	return toOAuthProviderConfigResponse(config), nil
}

func (s *OAuthProviderConfigService) List(ctx context.Context) ([]OAuthProviderConfigResponse, error) {
	configs, err := s.client.OAuthProviderConfig.Query().
		Order(ent.Asc(oauthproviderconfig.FieldProvider)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]OAuthProviderConfigResponse, 0, len(configs))
	for _, config := range configs {
		if _, ok := s.registry.Get(config.Provider); !ok {
			continue
		}
		result = append(result, *toOAuthProviderConfigResponse(config))
	}
	return result, nil
}

func (s *OAuthProviderConfigService) Delete(ctx context.Context, provider string) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if _, ok := s.registry.Get(provider); !ok {
		return ErrOAuthProviderNotFound
	}
	deleted, err := s.client.OAuthProviderConfig.Delete().
		Where(oauthproviderconfig.ProviderEQ(provider)).
		Exec(ctx)
	if err != nil {
		return err
	}
	if deleted == 0 {
		return ErrOAuthProviderNotFound
	}
	return nil
}

func (s *OAuthProviderConfigService) Runtime(ctx context.Context, provider, subjectType string) (appoauth.ProviderConfig, string, error) {
	config, err := s.client.OAuthProviderConfig.Query().Where(oauthproviderconfig.ProviderEQ(strings.ToLower(provider))).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return appoauth.ProviderConfig{}, "", ErrOAuthProviderNotFound
		}
		return appoauth.ProviderConfig{}, "", err
	}
	if _, ok := s.registry.Get(config.Provider); !ok {
		return appoauth.ProviderConfig{}, "", ErrOAuthProviderNotFound
	}
	redirectURL := ""
	switch subjectType {
	case "user":
		if !config.UserEnabled {
			return appoauth.ProviderConfig{}, "", ErrOAuthProviderDisabled
		}
		redirectURL = config.UserRedirectURL
	case "admin":
		if !config.AdminEnabled {
			return appoauth.ProviderConfig{}, "", ErrOAuthProviderDisabled
		}
		redirectURL = config.AdminRedirectURL
	default:
		return appoauth.ProviderConfig{}, "", ErrOAuthProviderDisabled
	}
	if config.ClientID == "" || config.ClientSecretCiphertext == "" || config.CallbackURL == "" || redirectURL == "" {
		return appoauth.ProviderConfig{}, "", ErrOAuthConfigIncomplete
	}
	if err := validateOAuthURLs(config.CallbackURL, redirectURL); err != nil {
		return appoauth.ProviderConfig{}, "", err
	}
	secret, err := s.cipher.Decrypt(config.ClientSecretCiphertext)
	if err != nil || secret == "" {
		return appoauth.ProviderConfig{}, "", ErrOAuthConfigIncomplete
	}
	return appoauth.ProviderConfig{ClientID: config.ClientID, ClientSecret: secret, RedirectURL: config.CallbackURL}, redirectURL, nil
}

func (s *OAuthProviderConfigService) ListEnabled(ctx context.Context, subjectType string) ([]PublicOAuthProvider, error) {
	query := s.client.OAuthProviderConfig.Query()
	switch subjectType {
	case "user":
		query = query.Where(oauthproviderconfig.UserEnabledEQ(true))
	case "admin":
		query = query.Where(oauthproviderconfig.AdminEnabledEQ(true))
	default:
		return []PublicOAuthProvider{}, nil
	}
	configs, err := query.All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]PublicOAuthProvider, 0, len(configs))
	for _, config := range configs {
		if _, ok := s.registry.Get(config.Provider); !ok {
			continue
		}
		redirectURL := config.UserRedirectURL
		if subjectType == "admin" {
			redirectURL = config.AdminRedirectURL
		}
		if config.ClientID == "" || config.ClientSecretCiphertext == "" || config.CallbackURL == "" || redirectURL == "" {
			continue
		}
		if err := validateOAuthURLs(config.CallbackURL, redirectURL); err != nil {
			continue
		}
		secret, err := s.cipher.Decrypt(config.ClientSecretCiphertext)
		if err != nil || secret == "" {
			continue
		}
		result = append(result, PublicOAuthProvider{Provider: config.Provider, DisplayName: config.DisplayName})
	}
	return result, nil
}

func validateOAuthURLs(values ...string) error {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		parsed, err := url.ParseRequestURI(value)
		if err != nil || parsed.Host == "" {
			return ErrOAuthInvalidURL
		}
		host := strings.ToLower(parsed.Hostname())
		if parsed.Scheme == "https" {
			continue
		}
		if parsed.Scheme == "http" && (host == "localhost" || host == "127.0.0.1" || host == "::1") {
			continue
		}
		return ErrOAuthInvalidURL
	}
	return nil
}

func toOAuthProviderConfigResponse(config *ent.OAuthProviderConfig) *OAuthProviderConfigResponse {
	response := &OAuthProviderConfigResponse{
		Provider:         config.Provider,
		DisplayName:      config.DisplayName,
		ClientID:         config.ClientID,
		SecretConfigured: config.ClientSecretCiphertext != "",
		CallbackURL:      config.CallbackURL,
		UserRedirectURL:  config.UserRedirectURL,
		AdminRedirectURL: config.AdminRedirectURL,
		UserEnabled:      config.UserEnabled,
		AdminEnabled:     config.AdminEnabled,
	}
	if response.SecretConfigured {
		response.ClientSecret = oauthSecretMask
	}
	return response
}
