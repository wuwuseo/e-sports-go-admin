package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsProduct 是商品父实体（§15.1）：聚合一个分类下、可挂多个规格 SKU 的展示单元。
// 既有独立 SKU 可继续存在（product_id 可空），迁移后由运营逐步挂到父商品下统一维护。
// 创建/编辑商品时可在同一入口维护子 SKU（见 product.go 的 saveProduct / updateProduct）。
type EsportsProduct struct{ ent.Schema }

func (EsportsProduct) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().MaxLen(160).StructTag(`json:"name"`),
		field.Int("category_id").Optional().Nillable().StructTag(`json:"category_id"`),
		field.String("description").MaxLen(500).Default("").StructTag(`json:"description"`),
		field.Enum("status").Values("draft", "on_sale", "off_sale").Default("draft").StructTag(`json:"status"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsProduct) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("category_id", "status"),
		index.Fields("status"),
	}
}
