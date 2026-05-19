package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"entgo.io/ent/schema/mixin"
)

// AdminRole 管理员-角色关联表
type AdminRole struct {
	ent.Schema
}

// Fields of the AdminRole.
func (AdminRole) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").
			Immutable().
			Unique().
			StructTag(`json:"id,omitempty" autoincr:"true"`),
		field.Int("admin_id").
			Comment("管理员ID"),
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

// Edges of the AdminRole.
func (AdminRole) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("admin", Admin.Type).Unique().Required().Field("admin_id"),
		edge.To("role", Role.Type).Unique().Required().Field("role_id"),
	}
}

// Indexes of the AdminRole.
func (AdminRole) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("admin_id", "role_id").Unique(),
		index.Fields("admin_id"),
		index.Fields("role_id"),
		index.Fields("admin_id", "is_primary"),
	}
}

func (AdminRole) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.Time{},
	}
}
