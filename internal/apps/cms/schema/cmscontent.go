package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type CmsContent struct{ ent.Schema }

func (CmsContent) Fields() []ent.Field {
	return []ent.Field{
		field.String("title").NotEmpty().MaxLen(200).StructTag(`json:"title"`),
		field.Enum("kind").Values("article", "page").Default("article").StructTag(`json:"kind"`),
		field.Enum("format").Values("markdown", "html", "image").Default("markdown").StructTag(`json:"format"`),
		field.Text("content").Default("").StructTag(`json:"content"`),
		field.String("image").Default("").MaxLen(500).StructTag(`json:"image"`),
		field.Int("category_id").Default(0).StructTag(`json:"category_id"`),
		field.Int("status").Default(0).StructTag(`json:"status"`),
		field.Time("created_at").Default(time.Now).Immutable().StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (CmsContent) Indexes() []ent.Index {
	return []ent.Index{index.Fields("kind", "status", "created_at"), index.Fields("category_id")}
}
