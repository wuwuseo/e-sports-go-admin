package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsRescheduleProposal 是已付款订单的限时改期提案（§3.3）。
// 已付款订单不再由客服直接改写 requested_start_at，而是先建立一张带时限的提案：
// pending（待客户与已接单成员逐人确认）→ confirmed（全部通过后生效，档期在同一事务内原子切换并占用）
// | rejected（任一确认人拒绝）| expired（提案超时，由定时任务收敛）| withdrawn（客服撤回）。
// **提案本身不改订单**：订单的 requested_start_at / service_duration_minutes 只在 confirmed 时切换。
// 订单快照取自发起时刻（order_version / from_start_at / from_service_duration_minutes），
// 生效时按 order_version 做版本条件更新，避免把基于旧档期的提案套到已被其它流程改过的订单上。
//
// 「同一订单同一时刻只允许一张进行中的提案」由 active_order_id 唯一索引兜底：
// pending 时写入 order_id，进入任一终态时置 NULL；MySQL / SQLite 的唯一索引不约束 NULL，
// 因此同一订单可以留下多张历史提案（MySQL 不支持带条件的部分唯一索引，故不直接约束 (order_id, status)）。
type EsportsRescheduleProposal struct{ ent.Schema }

func (EsportsRescheduleProposal) Fields() []ent.Field {
	return []ent.Field{
		field.String("proposal_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"proposal_no"`),
		field.Int("order_id").Positive().StructTag(`json:"order_id"`),
		// 发起提案时的订单版本：生效时以此为条件更新版本，防止旧提案覆盖新档期。
		field.Int("order_version").Positive().StructTag(`json:"order_version"`),
		// 原档期快照（即时单可能没有预约开始时间，故可空）。
		field.Time("from_start_at").Optional().Nillable().StructTag(`json:"from_start_at"`),
		field.Int("from_service_duration_minutes").Default(0).StructTag(`json:"from_service_duration_minutes"`),
		// 目标档期：生效后写入订单的 requested_start_at / service_duration_minutes。
		field.Time("to_start_at").StructTag(`json:"to_start_at"`),
		field.Int("to_service_duration_minutes").Positive().StructTag(`json:"to_service_duration_minutes"`),
		field.Enum("status").Values("pending", "confirmed", "rejected", "expired", "withdrawn").
			Default("pending").StructTag(`json:"status"`),
		// 提案时限：不晚于目标开始时间，超时由 esports_expire_reschedule_proposals 任务转 expired。
		field.Time("expires_at").StructTag(`json:"expires_at"`),
		field.Int("proposed_by_admin_id").Positive().StructTag(`json:"proposed_by_admin_id"`),
		field.String("reason").NotEmpty().MaxLen(255).StructTag(`json:"reason"`),
		// 进行中提案的唯一键：pending 时为 order_id，终态时为 NULL（NULL 不参与唯一冲突）。
		field.Int("active_order_id").Optional().Nillable().StructTag(`json:"active_order_id"`),
		// 进入终态（confirmed / rejected / expired / withdrawn）的时刻。
		field.Time("decided_at").Optional().Nillable().StructTag(`json:"decided_at"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsRescheduleProposal) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("proposal_no").Unique(),
		// 唯一约束兜底：同一订单同一时刻只允许一张进行中的提案。
		index.Fields("active_order_id").Unique(),
		index.Fields("order_id", "status"),
		index.Fields("status", "expires_at"),
	}
}

// EsportsRescheduleConfirmation 是改期提案的逐人确认明细（§3.3），只增不改。
// 需要确认的两类人各记一条：客户（confirmer_type=customer，confirmer_id=客户 ID）与
// 订单上每名已接单成员（confirmer_type=player，confirmer_id=陪玩 ID，seat_index 记录所在席位）。
// 全部为 confirmed 且无 rejected 时提案才生效；任一人 rejected 即刻把提案置 rejected。
// (proposal_id, confirmer_type, confirmer_id) 唯一，保证同一人只留一条确认、重复确认幂等。
type EsportsRescheduleConfirmation struct{ ent.Schema }

func (EsportsRescheduleConfirmation) Fields() []ent.Field {
	return []ent.Field{
		field.Int("proposal_id").Positive().StructTag(`json:"proposal_id"`),
		field.Int("order_id").Positive().StructTag(`json:"order_id"`),
		// 确认人类型：客户 / 已接单成员。
		field.Enum("confirmer_type").Values("customer", "player").StructTag(`json:"confirmer_type"`),
		// 确认人 ID：confirmer_type=customer 时为客户 ID，=player 时为陪玩 ID。
		field.Int("confirmer_id").Positive().StructTag(`json:"confirmer_id"`),
		// 成员确认所在的派单席位序号（客户确认为 0），仅用于审计展示。
		field.Int("seat_index").Default(0).StructTag(`json:"seat_index"`),
		field.Enum("decision").Values("confirmed", "rejected").Default("confirmed").StructTag(`json:"decision"`),
		field.String("note").MaxLen(255).Default("").StructTag(`json:"note"`),
		field.Time("decided_at").StructTag(`json:"decided_at"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

func (EsportsRescheduleConfirmation) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("proposal_id", "confirmer_type", "confirmer_id").Unique(),
		index.Fields("order_id", "confirmer_type"),
		index.Fields("confirmer_type", "confirmer_id"),
	}
}
