package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsWithdrawal 是陪玩提现单。只有猫粮（已结算收入）可提现：申请时同事务
// 扣减猫粮本金的 withdraw 流水（避免重复申请超额），驳回时写一条 refund_return 退回原额，
// 打款不再动钱包——钱已离开猫粮账户，由工作室按外部凭证打款。
// 首版一律由财务审核；审核与打款分两步，到账时间与外部打款 / 完税凭证分别留痕。
type EsportsWithdrawal struct{ ent.Schema }

func (EsportsWithdrawal) Fields() []ent.Field {
	return []ent.Field{
		field.String("withdrawal_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"withdrawal_no"`),
		field.Int("player_id").Positive().StructTag(`json:"player_id"`),
		// 幂等键：同一 key 重复提交只生成一张提现单。
		field.String("idem_key").NotEmpty().MaxLen(120).StructTag(`json:"idem_key"`),
		// 申请金额（从猫粮扣减的额）。
		field.Int64("amount_cents").Positive().StructTag(`json:"amount_cents"`),
		// 手续费与代扣税费：从申请金额内扣除，实际到账 = 申请金额 − 手续费 − 税费。
		field.Int64("fee_cents").Default(0).StructTag(`json:"fee_cents"`),
		field.Int64("tax_cents").Default(0).StructTag(`json:"tax_cents"`),
		field.Int64("net_cents").Positive().StructTag(`json:"net_cents"`),
		// 提现方式：银行卡 / 微信 / 支付宝，按工作室已开通能力。
		field.Enum("channel").Values("bank_card", "wechat", "alipay").StructTag(`json:"channel"`),
		// 收款账号或收款标识。
		field.String("account_ref").NotEmpty().MaxLen(128).StructTag(`json:"account_ref"`),
		field.Enum("status").Values("pending_review", "approved", "paid", "rejected").
			Default("pending_review").StructTag(`json:"status"`),
		field.String("reject_reason").MaxLen(255).Default("").StructTag(`json:"reject_reason"`),
		field.Int("reviewed_by_admin_id").Optional().Nillable().StructTag(`json:"reviewed_by_admin_id"`),
		field.Time("reviewed_at").Optional().Nillable().StructTag(`json:"reviewed_at"`),
		// 外部打款凭证与完税凭证，仅打款后非空。
		field.String("payout_ref").MaxLen(128).Default("").StructTag(`json:"payout_ref"`),
		field.String("tax_ref").MaxLen(128).Default("").StructTag(`json:"tax_ref"`),
		field.Int("paid_by_admin_id").Optional().Nillable().StructTag(`json:"paid_by_admin_id"`),
		field.Time("paid_at").Optional().Nillable().StructTag(`json:"paid_at"`),
		field.String("note").MaxLen(255).Default("").StructTag(`json:"note"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsWithdrawal) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("withdrawal_no").Unique(),
		index.Fields("idem_key").Unique(),
		// 提现频率校验按「本人最近一次申请时间」扫描，财务待审列表按状态扫描。
		index.Fields("player_id", "created_at"),
		index.Fields("status", "created_at"),
	}
}
