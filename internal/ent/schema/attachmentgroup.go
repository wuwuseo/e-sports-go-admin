package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// AttachmentGroup holds the schema definition for the AttachmentGroup entity.
type AttachmentGroup struct {
	ent.Schema
}

// Fields of the AttachmentGroup.
func (AttachmentGroup) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").
			Immutable().
			Unique().
			StructTag(`json:"id,omitempty" autoincr:"true"`),
		field.String("name").
			Unique().
			MaxLen(255).
			NotEmpty().
			StructTag(`json:"name"`).
			Comment("分组名称"),
		field.String("description").
			Optional().
			MaxLen(500).
			StructTag(`json:"description,omitempty"`).
			Comment("描述"),
		field.Int("sort").
			Default(0).
			StructTag(`json:"sort"`).
			Comment("排序"),
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			StructTag(`json:"created_at"`).
			Comment("创建时间"),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			StructTag(`json:"updated_at"`).
			Comment("更新时间"),
	}
}

// Edges of the AttachmentGroup.
func (AttachmentGroup) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("attachments", Attachment.Type),
	}
}
