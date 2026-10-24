package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsSeatTransferRequest 是陪玩转单申请：陪玩接单后无法服务时发起，
// **必须经客服确认后才生效**——确认前席位与订单状态都不变，避免陪玩单方面退出占位。
// 确认通过时释放该席位（回到待补位）并按剩余席位把订单退回待接单或待派单。
type EsportsSeatTransferRequest struct{ ent.Schema }

func (EsportsSeatTransferRequest) Fields() []ent.Field {
	return []ent.Field{
		field.Int("order_id").Positive().StructTag(`json:"order_id"`),
		field.Int("seat_id").Positive().StructTag(`json:"seat_id"`),
		field.Int("seat_index").Positive().StructTag(`json:"seat_index"`),
		// 发起转单的陪玩（申请人）。
		field.Int("player_id").Positive().StructTag(`json:"player_id"`),
		field.String("reason").NotEmpty().MaxLen(255).StructTag(`json:"reason"`),
		field.Enum("status").Values("pending", "approved", "rejected", "cancelled").
			Default("pending").StructTag(`json:"status"`),
		// 幂等键：陪玩端重复提交只生成一条申请。
		field.String("idem_key").NotEmpty().MaxLen(120).StructTag(`json:"idem_key"`),
		field.Int("decided_by_admin_id").Optional().Nillable().StructTag(`json:"decided_by_admin_id"`),
		field.Time("decided_at").Optional().Nillable().StructTag(`json:"decided_at"`),
		// 客服确认备注（通过或驳回都留痕）。
		field.String("decision_note").MaxLen(255).Default("").StructTag(`json:"decision_note"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsSeatTransferRequest) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("idem_key").Unique(),
		index.Fields("seat_id", "status"),
		index.Fields("status", "created_at"),
		index.Fields("player_id", "status"),
	}
}
