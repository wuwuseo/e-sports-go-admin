package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Slide 幻灯片实体(对应表 slides)
type Slide struct {
	ent.Schema
}

// Fields 幻灯片字段定义
func (Slide) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").
			Immutable().
			Unique().
			StructTag(`json:"id" autoincr:"true"`),
		field.String("title").
			NotEmpty().
			MaxLen(100).
			StructTag(`json:"title"`).
			Comment("标题,同时作为图片alt文本"),
		field.String("image").
			NotEmpty().
			MaxLen(500).
			StructTag(`json:"image"`).
			Comment("图片URL字符串,约定上传到公开目录"),
		field.String("link").
			Optional().
			MaxLen(500).
			StructTag(`json:"link,omitempty"`).
			Comment("点击跳转地址"),
		field.String("group_code").
			NotEmpty().
			MaxLen(100).
			StructTag(`json:"group_code"`).
			Comment("分组编码,对应字典类型slide_group的字典值"),
		field.Int("sort").
			Default(0).
			StructTag(`json:"sort"`).
			Comment("排序,升序"),
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

// Indexes 幻灯片索引
func (Slide) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("group_code", "status"),
	}
}
