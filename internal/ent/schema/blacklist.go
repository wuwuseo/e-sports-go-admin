package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Blacklist 用户黑名单
type Blacklist struct {
	ent.Schema
}

func (Blacklist) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").
			Immutable().
			Unique().
			StructTag(`json:"id,omitempty" autoincr:"true"`),
		field.Int("user_id").
			Unique().
			StructTag(`json:"user_id"`).
			Comment("被拉黑用户ID"),
		field.String("reason").
			MaxLen(500).
			NotEmpty().
			StructTag(`json:"reason"`).
			Comment("拉黑原因"),
		field.String("operator_id").
			MaxLen(255).
			NotEmpty().
			StructTag(`json:"operator_id"`).
			Comment("操作人ID"),
		field.String("operator_name").
			MaxLen(255).
			NotEmpty().
			StructTag(`json:"operator_name"`).
			Comment("操作人姓名"),
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			StructTag(`json:"created_at"`).
			Comment("拉黑时间"),
	}
}

func (Blacklist) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("user", User.Type).Unique().Required().Field("user_id"),
	}
}

func (Blacklist) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id").Unique(),
	}
}
