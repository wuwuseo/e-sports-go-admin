package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsPaymentReversal 是已核销收款的渠道撤销 / 拒付风险事件（§3.2）。
// 背景：订单核销后钱可能被渠道或客户事后撤回（商户撤销、客户拒付），此前系统只记录「到账」，
// 核销后没有任何独立风险事件与资金异常处置入口，账面上订单仍是已收款。
// 流程：财务登记撤销/拒付（pending，绑定来源支付回执，一笔回执只能登记一次）→
// 由非登记人的管理员处置（handled）或认定为误报（rejected）。
type EsportsPaymentReversal struct{ ent.Schema }

func (EsportsPaymentReversal) Fields() []ent.Field {
	return []ent.Field{
		field.String("reversal_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"reversal_no"`),
		// 来源支付回执与订单：撤销必须指向一笔已核销的收款。
		field.Int("receipt_id").Positive().StructTag(`json:"receipt_id"`),
		field.Int("order_id").Positive().StructTag(`json:"order_id"`),
		field.String("channel").NotEmpty().MaxLen(48).StructTag(`json:"channel"`),
		field.String("transaction_no").NotEmpty().MaxLen(128).StructTag(`json:"transaction_no"`),
		// 被撤销 / 拒付的金额（分），不得超过该回执实际到账金额。
		field.Int64("amount_cents").Positive().StructTag(`json:"amount_cents"`),
		// revoked 渠道撤销 / chargeback 客户拒付。
		field.Enum("kind").Values("revoked", "chargeback").Default("revoked").StructTag(`json:"kind"`),
		field.Enum("status").Values("pending", "handled", "rejected").Default("pending").StructTag(`json:"status"`),
		field.String("reason").NotEmpty().MaxLen(255).StructTag(`json:"reason"`),
		// 渠道通知 / 银行回单等凭证引用。
		field.String("evidence_ref").MaxLen(255).Default("").StructTag(`json:"evidence_ref"`),
		field.Int("submitted_by_admin_id").Optional().Nillable().StructTag(`json:"submitted_by_admin_id"`),
		field.Int("handled_by_admin_id").Optional().Nillable().StructTag(`json:"handled_by_admin_id"`),
		field.Time("handled_at").Optional().Nillable().StructTag(`json:"handled_at"`),
		field.String("resolution").MaxLen(255).Default("").StructTag(`json:"resolution"`),
		// 处置动作：mark_exception 订单资金异常转人工 / suspend_customer 同时限制客户自助下单 /
		// ignore 认定为误报不处置 / none 未处置。
		field.Enum("action").Values("none", "mark_exception", "suspend_customer", "ignore").
			Default("none").StructTag(`json:"action"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsPaymentReversal) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("reversal_no").Unique(),
		// 一笔已核销收款只允许登记一次撤销 / 拒付，重复登记靠唯一索引兜底。
		index.Fields("receipt_id").Unique(),
		index.Fields("order_id", "status"),
		index.Fields("status", "created_at"),
	}
}
