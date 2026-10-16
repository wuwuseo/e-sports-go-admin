package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsSkuSuspension 是 SKU 停服（下架并停服）申请（§15.7）：一次下架动作的申请 → 异人审批 →
// 受影响订单逐单处置闭环。它与既有直接上下架接口（PUT /admin/skus/{id} 直接改 status）并存：
// 直接下架不做审批与订单处置，停服申请用于「需要审批、且必须逐单收敛在途订单」的下架场景。
//
// 同一 SKU 同一时刻只允许一张进行中申请（pending_key 唯一索引兜底），申请人不得自审。
type EsportsSkuSuspension struct{ ent.Schema }

func (EsportsSkuSuspension) Fields() []ent.Field {
	return []ent.Field{
		field.Int("sku_id").Positive().StructTag(`json:"sku_id"`),
		field.Int("requested_by_admin_id").Positive().StructTag(`json:"requested_by_admin_id"`),
		field.String("reason").NotEmpty().MaxLen(500).StructTag(`json:"reason"`),
		// 生效方式：immediate = 审批通过即下架；scheduled = 审批通过后等到 effective_at 由定时任务下架。
		field.Enum("effective_mode").Values("immediate", "scheduled").Default("immediate").StructTag(`json:"effective_mode"`),
		field.Time("effective_at").Optional().Nillable().StructTag(`json:"effective_at"`),
		field.Enum("status").Values("pending", "approved", "rejected", "applied", "cancelled").Default("pending").StructTag(`json:"status"`),
		// pending_key 在「进行中」（pending / approved 未生效）期间固定为 sku:<sku_id>，进入终结状态后清空；
		// 唯一索引保证同一 SKU 同一时刻只有一张进行中申请（重复申请直接被数据库拒绝）。
		field.String("pending_key").Optional().Nillable().MaxLen(80).StructTag(`json:"-"`),
		field.Int("reviewed_by_admin_id").Optional().Nillable().StructTag(`json:"reviewed_by_admin_id"`),
		field.String("review_note").MaxLen(500).Default("").StructTag(`json:"review_note"`),
		field.Time("reviewed_at").Optional().Nillable().StructTag(`json:"reviewed_at"`),
		// 申请时点的受影响订单统计快照（未终结订单口径），供审批人评估影响面与预估退款额。
		field.Int("affected_order_count").Default(0).StructTag(`json:"affected_order_count"`),
		field.JSON("affected_snapshot", map[string]interface{}{}).Optional().StructTag(`json:"affected_snapshot"`),
		// 实际下架（生效）时刻：逐单处置清单在生效的同一事务内生成。
		field.Time("applied_at").Optional().Nillable().StructTag(`json:"applied_at"`),
		field.Time("cancelled_at").Optional().Nillable().StructTag(`json:"cancelled_at"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsSkuSuspension) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("pending_key").Unique(),
		index.Fields("sku_id", "status"),
		index.Fields("status", "effective_at"),
	}
}

// EsportsSkuSuspensionItem 是停服申请的「受影响订单逐单处置清单」：一单一条，逐单可部分完成。
// 取消并退款 / 改期复用既有取消政策与改期提案闭环，本实体只记录处置结论与留痕。
type EsportsSkuSuspensionItem struct{ ent.Schema }

func (EsportsSkuSuspensionItem) Fields() []ent.Field {
	return []ent.Field{
		field.Int("suspension_id").Positive().StructTag(`json:"suspension_id"`),
		field.Int("order_id").Positive().StructTag(`json:"order_id"`),
		field.String("order_no").NotEmpty().MaxLen(64).StructTag(`json:"order_no"`),
		// 生成清单时的订单履约 / 支付状态快照；处置完成时刷新为处置后的状态。
		field.String("order_status").MaxLen(40).Default("").StructTag(`json:"order_status"`),
		field.String("payment_status").MaxLen(40).Default("").StructTag(`json:"payment_status"`),
		// 系统建议处置方式；实际处置方式由客服 / 管理员在 action 上填写。
		field.Enum("suggested_action").Values("cancel_refund", "reschedule", "keep").StructTag(`json:"suggested_action"`),
		field.String("action").MaxLen(40).Default("").StructTag(`json:"action"`),
		field.Enum("status").Values("pending", "done", "failed").Default("pending").StructTag(`json:"status"`),
		field.String("note").MaxLen(255).Default("").StructTag(`json:"note"`),
		field.Int("handled_by_admin_id").Optional().Nillable().StructTag(`json:"handled_by_admin_id"`),
		field.Time("handled_at").Optional().Nillable().StructTag(`json:"handled_at"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsSkuSuspensionItem) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("suspension_id", "order_id").Unique(),
		index.Fields("suspension_id", "status"),
	}
}
