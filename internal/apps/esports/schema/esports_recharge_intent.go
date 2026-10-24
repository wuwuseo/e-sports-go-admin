package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsRechargeIntent 记录一次「客户充值」在发起时锁定的活动资格与金额快照。
// 与支付意图 EsportsPaymentIntent 一对一（intent_no 唯一），核验到账后按本快照入账，
// 避免核验时点活动已改导致赠送漂移。本实体只增不改。
type EsportsRechargeIntent struct{ ent.Schema }

func (EsportsRechargeIntent) Fields() []ent.Field {
	return []ent.Field{
		field.String("intent_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"intent_no"`),
		field.Int("customer_id").Positive().StructTag(`json:"customer_id"`),
		// 实充本金（客户实付额）。
		field.Int64("principal_cents").Positive().StructTag(`json:"principal_cents"`),
		// 赠送合计（从 rule_snapshot 冗余，便于核验入账与报表不解析 JSON）。
		field.Int64("bonus_cents").Default(0).StructTag(`json:"bonus_cents"`),
		// 活动资格与赠送快照：命中活动列表、赠送合计、各活动赠送有效期与本金/赠送拆分。
		// 与充值流水 EsportsWalletTx.rule_snapshot 同构，核验入账时原样写回流水。
		field.JSON("rule_snapshot", map[string]any{}).Optional().StructTag(`json:"rule_snapshot"`),
		// 赠送到期时间（从 rule_snapshot 冗余）；永久赠送为空。
		field.Time("bonus_expires_at").Optional().Nillable().StructTag(`json:"bonus_expires_at"`),
		// 活动资格判定时刻（快照锁定时点）。
		field.Time("snapshot_at").StructTag(`json:"snapshot_at"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

func (EsportsRechargeIntent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("intent_no").Unique(),
		index.Fields("customer_id", "created_at"),
	}
}
