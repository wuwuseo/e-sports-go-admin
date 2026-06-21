package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AdminThirdPartyUser stores external identities bound to administrators.
type AdminThirdPartyUser struct {
	ent.Schema
}

func (AdminThirdPartyUser) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").Immutable().Unique().StructTag(`json:"id,omitempty" autoincr:"true"`),
		field.Int("admin_id").StructTag(`json:"admin_id"`).Comment("?????ID"),
		field.String("platform").MaxLen(50).NotEmpty().StructTag(`json:"platform"`),
		field.String("platform_name").MaxLen(100).Optional().StructTag(`json:"platform_name,omitempty"`),
		field.String("openid").MaxLen(255).NotEmpty().StructTag(`json:"openid"`),
		field.String("nickname").MaxLen(255).Optional().StructTag(`json:"nickname,omitempty"`),
		field.String("avatar_url").MaxLen(1024).Optional().StructTag(`json:"avatar_url,omitempty"`),
		field.JSON("extra_data", map[string]any{}).Optional().StructTag(`json:"extra_data,omitempty"`),
		field.Enum("status").Values("active", "inactive").Default("active").StructTag(`json:"status"`),
		field.Time("last_sync_at").Optional().Nillable().StructTag(`json:"last_sync_at,omitempty"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (AdminThirdPartyUser) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("admin", Admin.Type).Unique().Required().Field("admin_id"),
	}
}

func (AdminThirdPartyUser) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("platform", "openid").Unique(),
		index.Fields("admin_id", "platform").Unique(),
		index.Fields("admin_id"),
		index.Fields("platform"),
	}
}
