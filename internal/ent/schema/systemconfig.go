package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SystemConfig holds the schema definition for the SystemConfig entity.
type SystemConfig struct {
	ent.Schema
}

// Fields of the SystemConfig.
func (SystemConfig) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").
			Immutable().
			Unique().
			StructTag(`json:"id" autoincr:"true"`),
		field.String("name").
			NotEmpty().
			MaxLen(100).
			StructTag(`json:"name"`).
			Comment("配置名称"),
		field.String("key").
			Unique().
			NotEmpty().
			MaxLen(100).
			StructTag(`json:"key"`).
			Comment("配置键"),
		field.String("value").
			Optional().
			MaxLen(2048).
			StructTag(`json:"value,omitempty"`).
			Comment("配置值"),
		field.String("type").
			Default("string").
			MaxLen(20).
			StructTag(`json:"type"`).
			Comment("值类型:string/number/boolean/json"),
		field.String("group").
			Optional().
			MaxLen(50).
			StructTag(`json:"group,omitempty"`).
			Comment("分组"),
		field.Int("status").
			Default(1).
			StructTag(`json:"status"`).
			Comment("状态 1启用 0禁用"),
		field.String("remark").
			Optional().
			MaxLen(500).
			StructTag(`json:"remark,omitempty"`).
			Comment("备注"),
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

// Indexes of the SystemConfig.
func (SystemConfig) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("key").Unique(),
		index.Fields("group"),
		index.Fields("status"),
	}
}
