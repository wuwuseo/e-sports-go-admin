package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsStoredValuePackage 是储值卡套餐（§4.4）：预设「本金 + 赠送」档位。
// 开卡时选定套餐即按套餐写入本金与赠送权益，替代入参直接给定赠送额；
// 不同档位（充 100 送 10 / 充 200 送 30 / 充 500 送 100）即多条套餐记录，开卡时择一。
type EsportsStoredValuePackage struct{ ent.Schema }

func (EsportsStoredValuePackage) Fields() []ent.Field {
	return []ent.Field{
		field.String("code").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"code"`),
		field.String("name").NotEmpty().MaxLen(120).StructTag(`json:"name"`),
		// 套餐本金（售价）：开卡时按此写入卡本金。
		field.Int64("principal_cents").Positive().StructTag(`json:"principal_cents"`),
		// 套餐赠送额：开卡时按此写入卡赠送权益（0 = 无赠送）。
		field.Int64("bonus_cents").Default(0).StructTag(`json:"bonus_cents"`),
		field.Enum("status").Values("on_sale", "off_sale").Default("on_sale").StructTag(`json:"status"`),
		field.Int("sort_order").Default(0).StructTag(`json:"sort_order"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsStoredValuePackage) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("code").Unique(),
		index.Fields("status", "sort_order"),
	}
}
