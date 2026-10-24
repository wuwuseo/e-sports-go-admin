package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsAfterSaleReview 是终态售后的新证据复核申请。
// 已驳回 / 已完成的售后单此前只能另建一张不同问题单，无法在有新证据时复核原结论。状态机：
// pending（待审查）→ upheld（维持原结论）| reopened（发回重新处理）| withdrawn（申请人撤回）。
// 复核结论须由「非申请人且非原审批人」的异人审查产生；reopened 时售后单由终态回到 processing，
// 允许重新提交方案、重新走客户确认与审批（退款上限仍受订单已退 / 已预占额度约束）。
type EsportsAfterSaleReview struct{ ent.Schema }

func (EsportsAfterSaleReview) Fields() []ent.Field {
	return []ent.Field{
		field.String("review_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"review_no"`),
		field.Int("after_sale_id").Positive().StructTag(`json:"after_sale_id"`),
		// 冗余订单 ID，便于按订单聚合复核历史。
		field.Int("order_id").Default(0).StructTag(`json:"order_id"`),
		field.Int("applicant_admin_id").Positive().StructTag(`json:"applicant_admin_id"`),
		field.String("reason").NotEmpty().MaxLen(500).StructTag(`json:"reason"`),
		// 新证据引用：凭证所有权校验在附件模块，这里只存引用。
		field.String("evidence_ref").MaxLen(255).Default("").StructTag(`json:"evidence_ref"`),
		field.Enum("status").Values("pending", "upheld", "reopened", "withdrawn").
			Default("pending").StructTag(`json:"status"`),
		field.Int("reviewer_admin_id").Optional().Nillable().StructTag(`json:"reviewer_admin_id"`),
		field.Time("reviewed_at").Optional().Nillable().StructTag(`json:"reviewed_at"`),
		field.String("decision_note").MaxLen(255).Default("").StructTag(`json:"decision_note"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsAfterSaleReview) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("review_no").Unique(),
		// 「同一售后单同一时刻仅一张进行中复核」由 service 层在事务内查重保证。
		index.Fields("after_sale_id", "status"),
		index.Fields("status", "created_at"),
	}
}
