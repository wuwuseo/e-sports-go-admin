package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsPlayerRecovery 是陪玩追偿明细：客户额外赔付由陪玩承担时另建的扣回单。
// 流程：售后执行时按方案的追偿金额生成（pending）→ 由非申请人的管理员审批（approved）→
// 执行扣回陪玩猫粮（recovered），或驳回（rejected）。追偿与「退款冲回分成」相互独立：
// 冲回是分成账上的负向明细，追偿是直接从陪玩可提现余额上扣回。
type EsportsPlayerRecovery struct{ ent.Schema }

func (EsportsPlayerRecovery) Fields() []ent.Field {
	return []ent.Field{
		field.String("recovery_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"recovery_no"`),
		field.Int("player_id").Positive().StructTag(`json:"player_id"`),
		// 来源：售后单与订单（0 表示无来源，用于管理员手工补录）。
		field.Int("after_sale_id").Default(0).StructTag(`json:"after_sale_id"`),
		field.Int("order_id").Default(0).StructTag(`json:"order_id"`),
		// 追偿金额（分）。
		field.Int64("amount_cents").Positive().StructTag(`json:"amount_cents"`),
		field.String("reason").NotEmpty().MaxLen(255).StructTag(`json:"reason"`),
		// pending 待审批 / approved 已批准待扣回 / recovered 已扣回 / rejected 已驳回。
		field.Enum("status").Values("pending", "approved", "recovered", "rejected").
			Default("pending").StructTag(`json:"status"`),
		field.Int("requested_by_admin_id").Optional().Nillable().StructTag(`json:"requested_by_admin_id"`),
		field.Int("approved_by_admin_id").Optional().Nillable().StructTag(`json:"approved_by_admin_id"`),
		field.Time("approved_at").Optional().Nillable().StructTag(`json:"approved_at"`),
		field.Time("recovered_at").Optional().Nillable().StructTag(`json:"recovered_at"`),
		// 扣回写入的猫粮流水 ID（幂等与追溯）。
		field.Int("wallet_tx_id").Optional().Nillable().StructTag(`json:"wallet_tx_id"`),
		field.String("reject_reason").MaxLen(255).Default("").StructTag(`json:"reject_reason"`),
		field.String("note").MaxLen(255).Default("").StructTag(`json:"note"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsPlayerRecovery) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("recovery_no").Unique(),
		index.Fields("player_id", "status"),
		index.Fields("status", "created_at"),
	}
}
