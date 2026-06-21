package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// OAuthProviderConfig stores runtime configuration for registered OAuth providers.
type OAuthProviderConfig struct {
	ent.Schema
}

func (OAuthProviderConfig) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").Immutable().Unique().StructTag(`json:"id,omitempty" autoincr:"true"`),
		field.String("provider").MaxLen(50).NotEmpty().Unique().StructTag(`json:"provider"`),
		field.String("display_name").MaxLen(100).NotEmpty().StructTag(`json:"display_name"`),
		field.String("client_id").MaxLen(255).Optional().StructTag(`json:"client_id,omitempty"`),
		field.String("client_secret_ciphertext").MaxLen(4096).Sensitive().Optional(),
		field.String("callback_url").MaxLen(2048).Optional().StructTag(`json:"callback_url,omitempty"`),
		field.String("user_redirect_url").MaxLen(2048).Optional().StructTag(`json:"user_redirect_url,omitempty"`),
		field.String("admin_redirect_url").MaxLen(2048).Optional().StructTag(`json:"admin_redirect_url,omitempty"`),
		field.Bool("user_enabled").Default(false).StructTag(`json:"user_enabled"`),
		field.Bool("admin_enabled").Default(false).StructTag(`json:"admin_enabled"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (OAuthProviderConfig) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("provider").Unique(),
		index.Fields("user_enabled"),
		index.Fields("admin_enabled"),
	}
}
