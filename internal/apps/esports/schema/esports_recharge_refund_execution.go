package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsRechargeRefundExecution 是按充值批次还原的外部退款执行明细（执行落账编排）。
//
// 背景：退款 / 退卡试算（rechargeRefundQuoteFor）已能按充值批次给出可退本金与作废赠送，
// 但真实外部打款仍缺一条「按批次生成执行单」的落账链——退卡走 stored_value.go 的申请 / 审核，
// 充值还原这条链路此前没有独立的执行明细可审计。
//
// 本实体只做执行编排：按每个可退批次生成一条明细（引用批次来源 + 退款金额 + 状态），
// 不接银行通道；真实外部打款由财务登记后把明细置为 executed 并回填外部凭证号。
// 幂等键 idem_key = refund:<after_sale_id>:<source_type>:<source_id>:<batch_source_type>:<batch_source_id>，
// 按「售后单 + 来源 + 批次」拆分；必须含售后单号，否则同一充值来源的第二张售后单会撞唯一索引
// 被静默跳过而漏生成外部打款明细。唯一索引兜底重复执行不重复落账。
type EsportsRechargeRefundExecution struct{ ent.Schema }

func (EsportsRechargeRefundExecution) Fields() []ent.Field {
	return []ent.Field{
		field.String("execution_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"execution_no"`),
		// 幂等键：refund:<after_sale_id>:<source_type>:<source_id>:<batch_source_type>:<batch_source_id>。
		field.String("idem_key").NotEmpty().MaxLen(191).Immutable().StructTag(`json:"idem_key"`),
		field.Int("after_sale_id").Positive().StructTag(`json:"after_sale_id"`),
		// 退款账户（账本身份）：customer_balance / stored_value_prepay。
		field.String("account_type").NotEmpty().MaxLen(48).StructTag(`json:"account_type"`),
		field.String("account_id").NotEmpty().MaxLen(80).StructTag(`json:"account_id"`),
		// 被还原的充值批次来源（对应一条正向账本条目）。
		field.String("batch_source_type").NotEmpty().MaxLen(48).StructTag(`json:"batch_source_type"`),
		field.String("batch_source_id").NotEmpty().MaxLen(80).StructTag(`json:"batch_source_id"`),
		// 该批次可退本金（分），即外部应打款金额。
		field.Int64("amount_cents").Positive().StructTag(`json:"amount_cents"`),
		// pending 待财务打款 / executed 已打款。
		field.Enum("status").Values("pending", "executed").Default("pending").StructTag(`json:"status"`),
		field.Int("executed_by_admin_id").Optional().Nillable().StructTag(`json:"executed_by_admin_id"`),
		field.Time("executed_at").Optional().Nillable().StructTag(`json:"executed_at"`),
		// 财务登记的外部打款凭证号，仅 executed 后非空。
		field.String("finance_ref").MaxLen(128).Default("").StructTag(`json:"finance_ref"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsRechargeRefundExecution) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("execution_no").Unique(),
		index.Fields("idem_key").Unique(),
		index.Fields("after_sale_id", "status"),
	}
}
