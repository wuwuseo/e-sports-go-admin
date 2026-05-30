package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Message 站内消息实体（对应表 admin_messages）
type Message struct {
	ent.Schema
}

// Fields 站内消息字段定义
func (Message) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").
			Immutable().
			Unique().
			StructTag(`json:"id,omitempty" autoincr:"true"`),
		field.String("user_uid").
			MaxLen(64).
			Default("").
			StructTag(`json:"user_uid"`).
			Comment("接收者UID，空字符串表示全员广播"),
		field.String("type").
			MaxLen(20).
			NotEmpty().
			StructTag(`json:"type"`).
			Comment("消息类型 system/business/task"),
		field.String("level").
			MaxLen(10).
			Default("info").
			StructTag(`json:"level"`).
			Comment("消息级别 info/warning/error"),
		field.String("title").
			MaxLen(200).
			NotEmpty().
			StructTag(`json:"title"`).
			Comment("标题"),
		field.Text("content").
			StructTag(`json:"content"`).
			Comment("内容（纯文本）"),
		field.Text("content_html").
			Optional().
			StructTag(`json:"content_html,omitempty"`).
			Comment("内容（富文本HTML），可选"),
		field.JSON("extra", map[string]any{}).
			Optional().
			StructTag(`json:"extra,omitempty"`).
			Comment("业务扩展字段"),
		field.Time("read_at").
			Optional().
			Nillable().
			StructTag(`json:"read_at,omitempty"`).
			Comment("已读时间，nil 表示未读"),
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			StructTag(`json:"created_at"`).
			Comment("创建时间"),
	}
}

// Indexes 站内消息索引
func (Message) Indexes() []ent.Index {
	return []ent.Index{
		// 列表分页：按用户、按创建时间倒序
		index.Fields("user_uid", "created_at"),
		// 未读统计：按用户、类型、已读时间过滤
		index.Fields("user_uid", "type", "read_at"),
		// 兼容旧索引：按用户、已读状态、ID
		index.Fields("user_uid", "read_at", "id"),
		index.Fields("type"),
		index.Fields("created_at"),
	}
}
