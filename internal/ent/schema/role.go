package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"entgo.io/ent/schema/mixin"
)

// Role holds the schema definition for the Role entity.
type Role struct {
	ent.Schema
}

// Fields of the Role.
func (Role) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").
			Immutable().
			Unique().
			StructTag(`json:"id,omitempty" autoincr:"true"`),
		field.String("name").
			Unique().
			MaxLen(100).
			NotEmpty().
			Comment("角色名称"),
		field.String("display_name").
			MaxLen(255).
			NotEmpty().
			Comment("显示名称"),
		field.String("description").
			Optional().
			MaxLen(500).
			Comment("角色描述"),
		field.Bool("is_system").
			Default(false).
			Comment("是否系统内置角色"),
		field.Enum("status").
			Values("active", "inactive").
			Default("active").
			Comment("角色状态"),
		field.String("created_by").
			Optional().
			MaxLen(255).
			Comment("创建者ID"),
		field.String("updated_by").
			Optional().
			MaxLen(255).
			Comment("更新者ID"),
	}
}

// Edges of the Role.
// func (Role) Edges() []ent.Edge {
// 	return []ent.Edge{
// 		edge.From("users", User.Type).Ref("roles").Through("user_roles", UserRole.Type),
// 	}
// }

// Indexes of the Role.
func (Role) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("name").Unique(),
		index.Fields("status"),
	}
}

func (Role) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.Time{},
	}
}
