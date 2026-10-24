package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsWithdrawalPayout 是提现审核通过后的打款执行单（补全）：
// 提现申请 → 财务审核通过（approved）后，落账拆成明确阶段：财务提交打款执行（pay_requested），
// 随后标记打款成功（paid，猫粮已出账 + 到账时间 + 打款人）或失败（pay_failed，退回可重试）。
// 提现单自身状态保持 approved，直到打款成功才置为 paid。
// 幂等：同一外部流水号（transaction_no）只能登记一次，防止重复落账。
type EsportsWithdrawalPayout struct{ ent.Schema }

func (EsportsWithdrawalPayout) Fields() []ent.Field {
	return []ent.Field{
		field.String("payout_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"payout_no"`),
		field.Int("withdrawal_id").Positive().StructTag(`json:"withdrawal_id"`),
		// 打款渠道：银行卡 / 微信 / 支付宝，与提现申请渠道一致。
		field.Enum("channel").Values("bank_card", "wechat", "alipay").StructTag(`json:"channel"`),
		// 外部打款流水号：幂等键，同一流水号不重复落账。
		field.String("transaction_no").NotEmpty().MaxLen(128).StructTag(`json:"transaction_no"`),
		// 实际打款金额（分），须等于提现单 net_cents。
		field.Int64("amount_cents").Positive().StructTag(`json:"amount_cents"`),
		// 完税凭证，打款成功后回填。
		field.String("tax_ref").MaxLen(128).Default("").StructTag(`json:"tax_ref"`),
		field.Enum("status").Values("pay_requested", "paid", "pay_failed").
			Default("pay_requested").StructTag(`json:"status"`),
		field.String("fail_reason").MaxLen(255).Default("").StructTag(`json:"fail_reason"`),
		field.Int("submitted_by_admin_id").Optional().Nillable().StructTag(`json:"submitted_by_admin_id"`),
		field.Int("paid_by_admin_id").Optional().Nillable().StructTag(`json:"paid_by_admin_id"`),
		field.Time("paid_at").Optional().Nillable().StructTag(`json:"paid_at"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsWithdrawalPayout) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("payout_no").Unique(),
		// 同一外部流水号只能登记一次，重复登记靠唯一索引兜底（幂等）。
		index.Fields("transaction_no").Unique(),
		index.Fields("withdrawal_id", "status"),
		index.Fields("status", "created_at"),
	}
}
