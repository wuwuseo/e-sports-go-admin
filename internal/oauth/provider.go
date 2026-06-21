package oauth

import (
	"context"
	"sort"
	"strings"

	"golang.org/x/oauth2"
)

// ProviderConfig contains decrypted runtime credentials for one OAuth provider.
type ProviderConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

// Identity is the provider-neutral external user profile used by login services.
type Identity struct {
	ID            string
	Email         string
	EmailVerified bool
	Username      string
	Name          string
	AvatarURL     string
	Extra         map[string]any
}

// Provider is implemented once per external login channel.
type Provider interface {
	Name() string
	DisplayName() string
	AuthCodeURL(config ProviderConfig, state, verifier string) string
	Exchange(ctx context.Context, config ProviderConfig, code, verifier string) (*oauth2.Token, error)
	FetchIdentity(ctx context.Context, token *oauth2.Token) (*Identity, error)
}

// Registry resolves registered providers without coupling login logic to them.
type Registry struct {
	providers map[string]Provider
}

func NewRegistry(providers ...Provider) *Registry {
	registry := &Registry{providers: make(map[string]Provider, len(providers))}
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		registry.providers[strings.ToLower(provider.Name())] = provider
	}
	return registry
}

func (r *Registry) Get(name string) (Provider, bool) {
	if r == nil {
		return nil, false
	}
	provider, ok := r.providers[strings.ToLower(name)]
	return provider, ok
}

func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
