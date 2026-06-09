package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"entgo.io/ent/schema/mixin"
)

// Department holds the schema definition for the Department entity.
type Department struct {
	ent.Schema
}

// Fields of the Department.
func (Department) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").
			Immutable().
			Unique().
			StructTag(`json:"id,omitempty" autoincr:"true"`),
		field.String("name").
			MaxLen(100).
			NotEmpty(),
		field.Int("parent_id").
			Default(0),
		field.String("path").
			MaxLen(500).
			Default(""),
		field.Int("sort").
			Default(0),
		field.Enum("status").
			Values("active", "inactive").
			Default("active"),
		field.String("leader").
			MaxLen(100).
			Optional(),
		field.String("phone").
			MaxLen(20).
			Optional(),
		field.String("email").
			MaxLen(255).
			Optional(),
	}
}

// Edges of the Department.
func (Department) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("admins", Admin.Type).Ref("department_ref"),
	}
}

// Indexes of the Department.
func (Department) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("parent_id"),
		index.Fields("path"),
		index.Fields("status"),
		index.Fields("parent_id", "name").Unique(),
	}
}

func (Department) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.Time{},
	}
}
