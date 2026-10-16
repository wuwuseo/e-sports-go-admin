package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsPlayerCreditEvent 是陪玩责任信用记载（§13.5）。售后责任方判定为陪玩并执行完成后
// 由系统自动只增记载（fault_admitted），提供复核入口决定是否处罚：
// recorded（已记载，尚不影响接单）→ penalty_applied（复核确认责任并暂停接单）| cleared（复核判定不构成责任）。
// 记载本身不改接单资格，资格变更只由复核动作显式触发，避免自动处罚误伤。
type EsportsPlayerCreditEvent struct{ ent.Schema }

func (EsportsPlayerCreditEvent) Fields() []ent.Field {
	return []ent.Field{
		field.String("event_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"event_no"`),
		field.Int("player_id").Positive().StructTag(`json:"player_id"`),
		// 来源：订单 / 售后单 / 追偿单（0 表示无来源，用于管理员手工补录）。
		field.Int("order_id").Default(0).StructTag(`json:"order_id"`),
		field.Int("after_sale_id").Default(0).StructTag(`json:"after_sale_id"`),
		field.Int("recovery_id").Default(0).StructTag(`json:"recovery_id"`),
		field.Enum("kind").Values("fault_admitted", "recovery_executed", "manual").
			Default("fault_admitted").StructTag(`json:"kind"`),
		// 相关金额（分）：责任成立时取追偿金额，无追偿则为 0。
		field.Int64("amount_cents").Default(0).StructTag(`json:"amount_cents"`),
		field.String("note").MaxLen(255).Default("").StructTag(`json:"note"`),
		field.Enum("status").Values("recorded", "penalty_applied", "cleared").
			Default("recorded").StructTag(`json:"status"`),
		field.Int("recorded_by_admin_id").Optional().Nillable().StructTag(`json:"recorded_by_admin_id"`),
		field.Int("reviewed_by_admin_id").Optional().Nillable().StructTag(`json:"reviewed_by_admin_id"`),
		field.Time("reviewed_at").Optional().Nillable().StructTag(`json:"reviewed_at"`),
		field.String("review_note").MaxLen(255).Default("").StructTag(`json:"review_note"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsPlayerCreditEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("event_no").Unique(),
		// 同一售后单只自动记载一次，服务层在事务内查重保证。
		index.Fields("after_sale_id"),
		index.Fields("player_id", "status"),
		index.Fields("status", "created_at"),
	}
}
