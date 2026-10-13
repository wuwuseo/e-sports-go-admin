package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsFulfillmentMakeup 是售后「补履约任务」（§13.6）：由售后方案（plan_action=makeup_service）
// 执行时创建，记录承接陪玩与席位占位、补足服务量、公司承担报酬，并由客服验收。
//
// 补履约**不增加原订单实收与成交额**，也不改原订单的服务段（原订单服务段对 (order_id, player_id)
// 有唯一约束，同一陪玩无法在同单重复开段），因此补履约自带服务段起止与服务量。
// 一张售后单只允许创建一个补履约任务：重复执行靠 after_sale_id 唯一索引兜底（幂等）。
type EsportsFulfillmentMakeup struct{ ent.Schema }

func (EsportsFulfillmentMakeup) Fields() []ent.Field {
	return []ent.Field{
		field.String("makeup_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"makeup_no"`),
		field.Int("after_sale_id").Positive().StructTag(`json:"after_sale_id"`),
		field.Int("order_id").Positive().StructTag(`json:"order_id"`),
		// kind：makeup_time 补钟（补足服务量）/ replace_player 换人（换人重新履约）。
		field.Enum("kind").Values("makeup_time", "replace_player").StructTag(`json:"kind"`),
		// 承接陪玩的席位占位（§13.6「记录成员占位」）。
		field.Int("player_id").Positive().StructTag(`json:"player_id"`),
		field.Int("seat_index").Positive().StructTag(`json:"seat_index"`),
		// 方案约定补足的服务量，单位与订单 SKU 的 service_unit_name 一致。
		field.Int64("units").Default(0).StructTag(`json:"units"`),
		// 公司承担报酬（分）：不增加原订单实收与成交额。
		field.Int64("company_pay_cents").Default(0).StructTag(`json:"company_pay_cents"`),
		// pending 待服务 → servicing 服务中 → completed 已验收；cancelled 已取消。
		field.Enum("status").Values("pending", "servicing", "completed", "cancelled").
			Default("pending").StructTag(`json:"status"`),
		field.Int("created_by_admin_id").Optional().Nillable().StructTag(`json:"created_by_admin_id"`),
		// 客服验收（§13.6「客服验收」）。
		field.Int("accepted_by_admin_id").Optional().Nillable().StructTag(`json:"accepted_by_admin_id"`),
		field.Time("accepted_at").Optional().Nillable().StructTag(`json:"accepted_at"`),
		// 补履约自带的服务段起止（不改原订单服务段）。
		field.Time("started_at").Optional().Nillable().StructTag(`json:"started_at"`),
		field.Time("finished_at").Optional().Nillable().StructTag(`json:"finished_at"`),
		// 系统按开始/结束点击间隔记录的服务秒数（与 §6.2 双来源口径一致，补履约首版只留系统来源）。
		field.Int64("system_seconds").Default(0).StructTag(`json:"system_seconds"`),
		field.String("note").MaxLen(255).Default("").StructTag(`json:"note"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsFulfillmentMakeup) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("makeup_no").Unique(),
		index.Fields("after_sale_id").Unique(),
		index.Fields("order_id", "status"),
		index.Fields("status", "created_at"),
	}
}
