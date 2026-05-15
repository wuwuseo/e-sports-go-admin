package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"entgo.io/ent/schema/mixin"
)

// UserRole holds the schema definition for the UserRole entity.
type UserRole struct {
	ent.Schema
}

// Fields of the UserRole.
func (UserRole) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").
			Immutable().
			Unique().
			StructTag(`json:"id,omitempty" autoincr:"true"`),
		field.Int("user_id").
			Comment("用户ID"),
		field.Int("role_id").
			Comment("角色ID"),
		field.Bool("is_primary").
			Default(false).
			StructTag(`json:"is_primary"`).
			Comment("是否为主角色"),
		field.String("created_by").
			Optional().
			MaxLen(255).
			Comment("分配者ID"),
	}
}

// Edges of the UserRole.
func (UserRole) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("user", User.Type).Unique().Required().Field("user_id"),
		edge.To("role", Role.Type).Unique().Required().Field("role_id"),
	}
}

// Indexes of the UserRole.
func (UserRole) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "role_id").Unique(),
		index.Fields("user_id"),
		index.Fields("role_id"),
		index.Fields("user_id", "is_primary"),
	}
}

func (UserRole) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.Time{},
	}
}
