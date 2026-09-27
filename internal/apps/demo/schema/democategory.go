package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

type DemoCategory struct{ ent.Schema }

func (DemoCategory) Fields() []ent.Field {
	return []ent.Field{field.String("name").NotEmpty().StructTag(`json:"name"`)}
}

func (DemoCategory) Edges() []ent.Edge {
	return []ent.Edge{edge.To("items", DemoItem.Type)}
}
