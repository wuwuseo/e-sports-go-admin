package schema

import (
	"testing"

	"entgo.io/ent"
)

func TestUserPasswordAllowsOAuthOnlyAccount(t *testing.T) {
	for _, f := range (User{}).Fields() {
		desc := f.Descriptor()
		if desc.Name != "password" {
			continue
		}
		if !desc.Optional {
			t.Fatal("password must be optional for OAuth-only users")
		}
		return
	}
	t.Fatal("password field not found")
}

func TestOAuthSchemasExposeRequiredFields(t *testing.T) {
	tests := []struct {
		name   string
		fields []ent.Field
		want   []string
	}{
		{
			name:   "admin third party identity",
			fields: (AdminThirdPartyUser{}).Fields(),
			want:   []string{"admin_id", "platform", "openid", "status"},
		},
		{
			name:   "provider config",
			fields: (OAuthProviderConfig{}).Fields(),
			want:   []string{"provider", "client_id", "client_secret_ciphertext", "callback_url", "user_redirect_url", "admin_redirect_url"},
		},
		{
			name:   "oauth session",
			fields: (OAuthSession{}).Fields(),
			want:   []string{"state_hash", "provider", "subject_type", "flow_type", "pkce_verifier_ciphertext", "expires_at"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen := make(map[string]bool, len(tt.fields))
			for _, f := range tt.fields {
				seen[f.Descriptor().Name] = true
			}
			for _, name := range tt.want {
				if !seen[name] {
					t.Fatalf("missing field %q", name)
				}
			}
		})
	}
}
