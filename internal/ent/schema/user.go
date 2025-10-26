package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/mixin"
)

// User holds the schema definition for the User entity.
type User struct {
	ent.Schema
}

// Fields of the User.
func (User) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").Immutable().Unique().StructTag(`json:"id,omitempty" autoincr:"true"`),
		field.String("username").MaxLen(255),
		field.String("password").
			MaxLen(255).
			Sensitive(),
		field.String("password_salt").
			MaxLen(255).
			Sensitive().
			Optional(),
		field.String("nickname").MaxLen(255),
		field.String("uid").MaxLen(255),
	}
}

// Edges of the User.
func (User) Edges() []ent.Edge {
	return nil
}

func (User) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.Time{},
	}
}
