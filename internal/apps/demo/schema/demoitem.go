package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

type DemoItem struct{ ent.Schema }

func (DemoItem) Fields() []ent.Field {
	return []ent.Field{field.String("title").NotEmpty().StructTag(`json:"title"`)}
}

func (DemoItem) Edges() []ent.Edge {
	return []ent.Edge{edge.From("category", DemoCategory.Type).Ref("items").Unique()}
}
