package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsAfterSale 是售后 / 退款单。首版只支持来源为订单（source_type=order），
// 保留 gift/deposit 供后续扩展。状态机：
// pending_accept → processing → pending_customer_confirm → pending_approval → pending_execution → completed | execution_failed；
// 方案提交后须经「待客户确认」（客户自助确认或客服代确认，§13.3）才进入待审批；
// 客户拒绝则退回 processing，客服可改方案后重新提交；
// pending_approval --reject--> rejected；非终态 --withdraw--> withdrawn。
// "同来源单据同问题同一时刻仅保留一张进行中售后" 由 service 层在事务内查重保证。
// 限时与超时升级（§13.5）由 `esports_check_aftersale_sla` 任务按各阶段可配时限判定并写 `aftersale.sla_breach` 外盒。
// ponytail: 未用部分唯一索引表达该规则（MySQL 不支持带条件的部分唯一索引），并发下依赖事务内查询 + 串行化更新兜底；
// 超时只升级提醒、不自动流转或升级角色，超时也不影响流转。
type EsportsAfterSale struct{ ent.Schema }

func (EsportsAfterSale) Fields() []ent.Field {
	return []ent.Field{
		field.String("after_sale_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"after_sale_no"`),
		// 来源单据类型；首版仅 order，gift/deposit 预留。
		field.Enum("source_type").Values("order").Default("order").StructTag(`json:"source_type"`),
		// 来源单据 ID；首版即订单 ID。
		field.Int("source_id").Positive().StructTag(`json:"source_id"`),
		field.Enum("kind").Values("service_mismatch", "duration_short", "no_show", "wrong_item", "overcharge", "player_appeal", "refund_request", "other").StructTag(`json:"kind"`),
		// 发起入口：客户 / 客服 / 陪玩 / 系统。
		field.Enum("channel").Values("customer", "admin", "player", "system").StructTag(`json:"channel"`),
		field.String("demand").NotEmpty().MaxLen(500).StructTag(`json:"demand"`),
		field.String("expected_plan").MaxLen(255).Default("").StructTag(`json:"expected_plan"`),
		field.String("evidence_ref").MaxLen(255).Default("").StructTag(`json:"evidence_ref"`),
		field.String("contact").MaxLen(120).Default("").StructTag(`json:"contact"`),
		field.Enum("status").Values("pending_accept", "processing", "pending_customer_confirm", "pending_approval", "pending_execution", "execution_failed", "completed", "rejected", "withdrawn").Default("pending_accept").StructTag(`json:"status"`),
		field.Enum("fault_party").Values("undetermined", "player", "admin", "customer", "platform", "none").Default("undetermined").StructTag(`json:"fault_party"`),
		field.Enum("plan_action").Values("none", "refund_original", "compensate", "makeup_service").Default("none").StructTag(`json:"plan_action"`),
		// 本次赔付金额（分）。
		field.Int64("refund_cents").Default(0).StructTag(`json:"refund_cents"`),
		// 补履约方案参数（§13.6）：plan_action=makeup_service 时使用，执行时据此创建补履约任务。
		// makeup_kind 为 none 表示非补履约方案。
		field.Enum("makeup_kind").Values("none", "makeup_time", "replace_player").Default("none").StructTag(`json:"makeup_kind"`),
		field.Int("makeup_player_id").Optional().Nillable().StructTag(`json:"makeup_player_id"`),
		field.Int("makeup_seat_index").Optional().Nillable().StructTag(`json:"makeup_seat_index"`),
		field.Int64("makeup_units").Default(0).StructTag(`json:"makeup_units"`),
		// 公司承担报酬（分）：补履约由公司出钱，不增加原订单实收与成交额。
		field.Int64("makeup_company_pay_cents").Default(0).StructTag(`json:"makeup_company_pay_cents"`),
		field.Int("customer_id").Optional().Nillable().StructTag(`json:"customer_id"`),
		field.Int("player_id").Optional().Nillable().StructTag(`json:"player_id"`),
		// 待客户确认（§13.3）：方案提交后由客户确认；确认渠道区分自助与客服代确认。
		field.Time("customer_confirmed_at").Optional().Nillable().StructTag(`json:"customer_confirmed_at"`),
		field.Enum("customer_confirm_channel").Values("none", "self_service", "admin_proxy").
			Default("none").StructTag(`json:"customer_confirm_channel"`),
		field.Int("customer_confirmed_by_admin_id").Optional().Nillable().StructTag(`json:"customer_confirmed_by_admin_id"`),
		field.Time("customer_declined_at").Optional().Nillable().StructTag(`json:"customer_declined_at"`),
		field.String("customer_decline_reason").MaxLen(255).Default("").StructTag(`json:"customer_decline_reason"`),
		// 追偿（§13.4）：赔付由陪玩承担时，执行售后按此生成陪玩追偿明细（待审批）。
		field.Int("recovery_player_id").Optional().Nillable().StructTag(`json:"recovery_player_id"`),
		field.Int64("recovery_cents").Default(0).StructTag(`json:"recovery_cents"`),
		// 阶段时间戳（§13.5 限时）：受理 / 提交方案 / 审批各自锚定，用于计算当前阶段的超时。
		field.Time("accepted_at").Optional().Nillable().StructTag(`json:"accepted_at"`),
		field.Time("planned_at").Optional().Nillable().StructTag(`json:"planned_at"`),
		field.Time("approved_at").Optional().Nillable().StructTag(`json:"approved_at"`),
		field.Int("handler_admin_id").Optional().Nillable().StructTag(`json:"handler_admin_id"`),
		field.Int("approver_admin_id").Optional().Nillable().StructTag(`json:"approver_admin_id"`),
		field.Int("executor_admin_id").Optional().Nillable().StructTag(`json:"executor_admin_id"`),
		// 处理 / 执行结果说明。
		field.String("resolution").MaxLen(255).Default("").StructTag(`json:"resolution"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("resolved_at").Optional().Nillable().StructTag(`json:"resolved_at"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsAfterSale) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("after_sale_no").Unique(),
		index.Fields("source_type", "source_id", "status"),
		index.Fields("status", "created_at"),
	}
}
