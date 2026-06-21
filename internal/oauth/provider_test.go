package oauth

import (
	"context"
	"testing"

	"golang.org/x/oauth2"
)

type stubProvider struct{ name string }

func (p stubProvider) Name() string                                      { return p.name }
func (p stubProvider) DisplayName() string                               { return p.name }
func (p stubProvider) AuthCodeURL(ProviderConfig, string, string) string { return "" }
func (p stubProvider) Exchange(context.Context, ProviderConfig, string, string) (*oauth2.Token, error) {
	return nil, nil
}
func (p stubProvider) FetchIdentity(context.Context, *oauth2.Token) (*Identity, error) {
	return nil, nil
}

func TestRegistryRegistersProvidersByName(t *testing.T) {
	registry := NewRegistry(stubProvider{name: "github"})
	provider, ok := registry.Get("github")
	if !ok || provider.Name() != "github" {
		t.Fatal("github provider not registered")
	}
	if _, ok := registry.Get("google"); ok {
		t.Fatal("google must remain an unregistered extension point")
	}
}
