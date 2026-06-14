package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Attachment holds the schema definition for the Attachment entity.
type Attachment struct {
	ent.Schema
}

// Fields of the Attachment.
func (Attachment) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").
			Immutable().
			Unique().
			StructTag(`json:"id,omitempty" autoincr:"true"`),
		field.String("name").
			MaxLen(255).
			NotEmpty().
			StructTag(`json:"name"`).
			Comment("文件名/显示名"),
		field.String("original_name").
			MaxLen(255).
			NotEmpty().
			StructTag(`json:"original_name"`).
			Comment("原始文件名"),
		field.String("path").
			MaxLen(1024).
			NotEmpty().
			StructTag(`json:"path"`).
			Comment("存储路径/key"),
		field.String("disk").
			MaxLen(64).
			Optional().
			StructTag(`json:"disk,omitempty"`).
			Comment("存储磁盘"),
		field.String("driver").
			MaxLen(64).
			Optional().
			StructTag(`json:"driver,omitempty"`).
			Comment("存储驱动"),
		field.String("directory").
			MaxLen(1024).
			Optional().
			StructTag(`json:"directory,omitempty"`).
			Comment("上传目录"),
		field.String("content_hash").
			MaxLen(64).
			Optional().
			StructTag(`json:"content_hash,omitempty"`).
			Comment("文件内容SHA-256"),
		field.String("original_name_hash").
			MaxLen(64).
			Optional().
			StructTag(`json:"original_name_hash,omitempty"`).
			Comment("原始文件名SHA-256"),
		field.String("mime_type").
			MaxLen(255).
			StructTag(`json:"mime_type"`).
			Comment("MIME类型"),
		field.String("extension").
			MaxLen(50).
			StructTag(`json:"extension"`).
			Comment("文件扩展名"),
		field.Int64("size").
			StructTag(`json:"size"`).
			Comment("文件大小字节数"),
		field.Int("group_id").
			Optional().
			StructTag(`json:"group_id,omitempty"`).
			Comment("分组ID"),
		field.String("uploaded_by").
			Optional().
			StructTag(`json:"uploaded_by,omitempty"`).
			Comment("上传用户UID"),
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

// Edges of the Attachment.
func (Attachment) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("group", AttachmentGroup.Type).
			Ref("attachments").
			Unique().
			Field("group_id"),
	}
}

// Indexes of the Attachment.
func (Attachment) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("group_id"),
		index.Fields("mime_type"),
		index.Fields("created_at"),
		index.Fields("content_hash", "size", "original_name_hash").Unique(),
	}
}
