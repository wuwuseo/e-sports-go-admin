package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SystemPermission holds the schema definition for the SystemPermission entity.
type SystemPermission struct {
	ent.Schema
}

// Fields of the SystemPermission.
func (SystemPermission) Fields() []ent.Field {
	return []ent.Field{
		field.String("domain").Default("default").Comment("所属域"),
		field.String("type").Comment("权限类型：api/menu/button"),
		field.String("name").Comment("权限名称"),
		field.String("code").Comment("权限标识"),
		field.String("path").Optional().Comment("API路径"),
		field.String("method").Optional().Comment("HTTP方法：GET/POST/PUT/DELETE等"),
		field.String("menu_path").Optional().Comment("菜单路由"),
		field.String("icon").Optional().Comment("菜单图标"),
		field.String("component").Optional().Comment("前端组件路径"),
		field.Int("parent_id").Default(0).Comment("父级ID"),
		field.Int("sort_order").Default(0).Comment("排序"),
		field.Bool("is_common").Default(false).Comment("是否公共权限（所有域共享）"),
		field.Int8("status").Default(1).Comment("状态：0禁用 1启用"),
		field.String("description").Optional().Comment("描述"),
		field.Time("created_at").Default(time.Now).Comment("创建时间"),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).Comment("更新时间"),
	}
}

// Indexes of the SystemPermission.
func (SystemPermission) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("domain", "code").Unique(),
		index.Fields("domain"),
		index.Fields("type"),
	}
}
