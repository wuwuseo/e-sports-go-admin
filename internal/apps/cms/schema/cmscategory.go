package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

type CmsCategory struct{ ent.Schema }

func (CmsCategory) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().MaxLen(100).StructTag(`json:"name"`),
		field.Int("status").Default(1).StructTag(`json:"status"`),
		field.Time("created_at").Default(time.Now).Immutable().StructTag(`json:"created_at"`),
	}
}
