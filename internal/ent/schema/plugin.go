package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type Plugin struct {
	ent.Schema
}

func (Plugin) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").
			Immutable().
			Unique().
			StructTag(`json:"id,omitempty" autoincr:"true"`),
		field.String("plugin_id").
			Unique().
			MaxLen(255).
			NotEmpty().
			StructTag(`json:"plugin_id"`).
			Comment("插件唯一标识"),
		field.String("name").
			MaxLen(255).
			NotEmpty().
			StructTag(`json:"name"`).
			Comment("插件名称"),
		field.String("version").
			MaxLen(64).
			NotEmpty().
			StructTag(`json:"version"`).
			Comment("插件版本"),
		field.String("author").
			Optional().
			MaxLen(255).
			StructTag(`json:"author,omitempty"`).
			Comment("插件作者"),
		field.String("description").
			Optional().
			MaxLen(1000).
			StructTag(`json:"description,omitempty"`).
			Comment("插件描述"),
		field.Enum("status").
			Values("installed", "enabled", "disabled", "error").
			Default("installed").
			StructTag(`json:"status"`).
			Comment("插件状态"),
		field.String("install_path").
			MaxLen(2048).
			NotEmpty().
			StructTag(`json:"install_path"`).
			Comment("插件安装路径"),
		field.JSON("manifest_json", map[string]any{}).
			StructTag(`json:"manifest_json"`).
			Comment("插件 manifest JSON"),
		field.String("error_message").
			Optional().
			MaxLen(2048).
			StructTag(`json:"error_message,omitempty"`).
			Comment("最近一次错误信息"),
		field.Time("installed_at").
			Default(time.Now).
			Immutable().
			StructTag(`json:"installed_at"`).
			Comment("安装时间"),
		field.Time("enabled_at").
			Optional().
			Nillable().
			StructTag(`json:"enabled_at,omitempty"`).
			Comment("启用时间"),
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

func (Plugin) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("plugin_id"),
		index.Fields("status"),
		index.Fields("plugin_id", "version").Unique(),
	}
}
