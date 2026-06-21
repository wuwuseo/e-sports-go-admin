package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// OAuthSession stores short-lived OAuth state and one-time login codes.
type OAuthSession struct {
	ent.Schema
}

func (OAuthSession) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").Immutable().Unique().StructTag(`json:"id,omitempty" autoincr:"true"`),
		field.String("state_hash").MaxLen(64).NotEmpty().Unique().StructTag(`json:"-"`),
		field.String("login_code_hash").MaxLen(64).Optional().Unique().StructTag(`json:"-"`),
		field.String("provider").MaxLen(50).NotEmpty().StructTag(`json:"provider"`),
		field.Enum("subject_type").Values("user", "admin").StructTag(`json:"subject_type"`),
		field.Enum("flow_type").Values("login", "bind").StructTag(`json:"flow_type"`),
		field.String("subject_uid").MaxLen(255).Optional().StructTag(`json:"subject_uid,omitempty"`),
		field.String("pkce_verifier_ciphertext").MaxLen(4096).Sensitive(),
		field.Enum("status").Values("pending", "callback_consumed", "completed", "consumed", "failed").Default("pending").StructTag(`json:"status"`),
		field.String("login_subject_uid").MaxLen(255).Optional().StructTag(`json:"login_subject_uid,omitempty"`),
		field.Time("expires_at").StructTag(`json:"expires_at"`),
		field.Time("callback_consumed_at").Optional().Nillable().StructTag(`json:"callback_consumed_at,omitempty"`),
		field.Time("login_code_expires_at").Optional().Nillable().StructTag(`json:"login_code_expires_at,omitempty"`),
		field.Time("login_code_consumed_at").Optional().Nillable().StructTag(`json:"login_code_consumed_at,omitempty"`),
		field.String("error_code").MaxLen(100).Optional().StructTag(`json:"error_code,omitempty"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (OAuthSession) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("state_hash").Unique(),
		index.Fields("login_code_hash").Unique(),
		index.Fields("status", "expires_at"),
		index.Fields("provider"),
	}
}
