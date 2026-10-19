package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsStoredValueException 是储值卡不一致订单的异常队列（§4.6-06）：结单 / 结算时
// storedValueOrderConsistent 判定「卡标记 / 关联卡 / 核销流水」三者不符即自动入队一条待跟进记录，
// 由客服指派跟进、财务处理后置为 resolved；同一（卡 + 订单 + 不一致类型）至多一条待跟进。
//
// 口径：
//   - 不替代结单硬拦截（既有 400 仍然生效），异常队列只是「可跟进的落地记录 + 待办入口」；
//   - dedup_key = <card_id>:<order_id>:<kind>，仅待跟进（open / following）时写入，落终态清空为 NULL，
//     靠可空唯一索引兜底（MySQL / SQLite 都允许多个 NULL），不使用部分索引。
//
// 建议表名：esports_stored_value_exceptions。列表 / 指派 / 处理路由与 PermAPI 由主控补。
type EsportsStoredValueException struct{ ent.Schema }

func (EsportsStoredValueException) Fields() []ent.Field {
	return []ent.Field{
		field.String("exception_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"exception_no"`),
		field.Int("card_id").Positive().StructTag(`json:"card_id"`),
		// 触发不一致的订单；非订单场景（如开卡核验）为 0。
		field.Int("order_id").Default(0).StructTag(`json:"order_id"`),
		field.Int("customer_id").Default(0).StructTag(`json:"customer_id"`),
		// 不一致类型：核销流水不符 / 卡标记不符 / 关联卡不符 / 结算被拦截。
		field.Enum("kind").
			Values("write_off_mismatch", "card_flag_mismatch", "card_link_mismatch", "settlement_blocked").
			Default("write_off_mismatch").StructTag(`json:"kind"`),
		// 三处「真相」的对比快照与判定依据，供跟进人不必重放流程即可看清差异。
		field.JSON("detail", map[string]any{}).Optional().StructTag(`json:"detail"`),
		field.String("reason").MaxLen(255).Default("").StructTag(`json:"reason"`),
		// open 待指派 / following 跟进中 / resolved 已处理 / ignored 已忽略（无需处理）。
		field.Enum("status").Values("open", "following", "resolved", "ignored").Default("open").StructTag(`json:"status"`),
		field.Int("assignee_admin_id").Optional().Nillable().StructTag(`json:"assignee_admin_id"`),
		field.Time("assigned_at").Optional().Nillable().StructTag(`json:"assigned_at"`),
		// 重复触发次数（同一卡同一订单同一类型再次命中时累加，不新增行）。
		field.Int("occurrences").Default(1).StructTag(`json:"occurrences"`),
		field.Time("last_hit_at").Optional().Nillable().StructTag(`json:"last_hit_at"`),
		field.Int("resolved_by_admin_id").Optional().Nillable().StructTag(`json:"resolved_by_admin_id"`),
		field.Time("resolved_at").Optional().Nillable().StructTag(`json:"resolved_at"`),
		field.String("resolution").MaxLen(500).Default("").StructTag(`json:"resolution"`),
		// 待跟进去重键 <card_id>:<order_id>:<kind>；落终态时由业务侧清空为 NULL，从而允许同一组合再次入队。
		field.String("dedup_key").Optional().Nillable().MaxLen(191).StructTag(`json:"dedup_key,omitempty"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsStoredValueException) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("exception_no").Unique(),
		// 待跟进唯一：空值 NULL 不参与冲突（§4.6-06）。
		index.Fields("dedup_key").Unique(),
		index.Fields("status", "created_at"),
		index.Fields("assignee_admin_id", "status"),
		index.Fields("card_id", "status"),
	}
}
