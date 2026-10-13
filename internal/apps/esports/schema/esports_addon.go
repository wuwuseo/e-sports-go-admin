package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsOrderAddon 是服务中订单的「加钟单」（§3.3 订单操作 / §6.2 陪玩端加钟确认 / §12.5 特殊场景）。
//
// 流程：客服对**服务中**订单发起加钟（追加金额与工时）→ 陪玩收到通知后确认或拒绝 →
// 确认时按**确认时点锁定的分成规则**为承接陪玩生成 `kind=addon` 的加钟分成明细，并把追加金额与
// 追加服务量累加到订单的 `addon_cents` / `addon_units`（只增不改）。
//
// 追加金额不改写原订单报价快照（`payable_cents` 保持原报价事实），加钟金额作为订单上的独立
// 应收追加，由后续收款流程处理。
// ponytail: 加钟收款（收单 / 支付意图）与加钟退款分摊未实现；同一订单同时只允许一条待确认加钟，
// 由 service 层在事务内查重保证（MySQL 不支持带条件的部分唯一索引）。
type EsportsOrderAddon struct{ ent.Schema }

func (EsportsOrderAddon) Fields() []ent.Field {
	return []ent.Field{
		field.String("addon_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"addon_no"`),
		field.Int("order_id").Positive().StructTag(`json:"order_id"`),
		// 追加金额（分）与追加服务量，单位与订单 SKU 的 service_unit_name 一致。
		field.Int64("added_cents").Default(0).StructTag(`json:"added_cents"`),
		field.Int64("added_units").Default(0).StructTag(`json:"added_units"`),
		// pending 待陪玩确认 → confirmed 已确认 / rejected 已拒绝；cancelled 客服取消。
		field.Enum("status").Values("pending", "confirmed", "rejected", "cancelled").
			Default("pending").StructTag(`json:"status"`),
		field.Int("requested_by_admin_id").Optional().Nillable().StructTag(`json:"requested_by_admin_id"`),
		// 确认时锁定的分成规则快照与按该规则算出的陪玩加钟分成（§12.5：按加钟确认时锁定的规则计算）。
		field.Int("rule_id").Optional().Nillable().StructTag(`json:"rule_id"`),
		field.String("rule_no").MaxLen(48).Default("").StructTag(`json:"rule_no"`),
		field.Int("rule_version").Default(0).StructTag(`json:"rule_version"`),
		field.Int64("commission_cents").Default(0).StructTag(`json:"commission_cents"`),
		// 确认的陪玩与时间；拒绝原因。
		field.Int("confirmed_player_id").Optional().Nillable().StructTag(`json:"confirmed_player_id"`),
		field.Time("confirmed_at").Optional().Nillable().StructTag(`json:"confirmed_at"`),
		field.String("reject_reason").MaxLen(255).Default("").StructTag(`json:"reject_reason"`),
		field.String("note").MaxLen(255).Default("").StructTag(`json:"note"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsOrderAddon) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("addon_no").Unique(),
		index.Fields("order_id", "status"),
		index.Fields("status", "created_at"),
	}
}
