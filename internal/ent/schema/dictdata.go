package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// DictData holds the schema definition for the DictData entity.
type DictData struct {
	ent.Schema
}

// Fields of the DictData.
func (DictData) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").
			Immutable().
			Unique().
			StructTag(`json:"id" autoincr:"true"`),
		field.String("label").
			NotEmpty().
			MaxLen(100).
			StructTag(`json:"label"`).
			Comment("字典标签"),
		field.String("value").
			NotEmpty().
			MaxLen(100).
			StructTag(`json:"value"`).
			Comment("字典值"),
		field.String("dict_type").
			NotEmpty().
			MaxLen(100).
			StructTag(`json:"dict_type"`).
			Comment("字典类型编码"),
		field.Int("sort").
			Default(0).
			StructTag(`json:"sort"`).
			Comment("排序"),
		field.Int("status").
			Default(1).
			StructTag(`json:"status"`).
			Comment("状态 1启用 0禁用"),
		field.String("css_class").
			Optional().
			MaxLen(100).
			StructTag(`json:"css_class,omitempty"`).
			Comment("样式class"),
		field.Bool("is_default").
			Default(false).
			StructTag(`json:"is_default"`).
			Comment("是否默认"),
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

// Indexes of the DictData.
func (DictData) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("dict_type"),
		index.Fields("status"),
	}
}
