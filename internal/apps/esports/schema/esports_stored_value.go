package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsStoredValueCard 是储值卡（预付账户，§4.4 / §11.3）。每张卡独立账户：登记合同、有效期、
// 适用范围与开卡本金 / 赠送权益。卡内余额不落库，由 EsportsWalletTx 中 account_type=stored_value_card、
// owner_id=卡 ID 的流水求和派生，本金与赠送分列，结构上保证「卡余额等于对应流水之和」。
type EsportsStoredValueCard struct{ ent.Schema }

func (EsportsStoredValueCard) Fields() []ent.Field {
	return []ent.Field{
		field.String("card_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"card_no"`),
		field.Int("customer_id").Positive().StructTag(`json:"customer_id"`),
		// 开卡来源：客服代办 / 客户自助。
		field.Enum("source").Values("staff", "self").Default("staff").StructTag(`json:"source"`),
		// 开卡来源支付：企微收款 / 工作室商户码 / 对公转账 / 用客户余额购卡（账户间划转）。
		field.Enum("payment_channel").Values("wechat_work", "merchant_qr", "corporate_transfer", "customer_balance").
			StructTag(`json:"payment_channel"`),
		// 来源支付凭证号（对公 / 商户码截图单号）；余额划转时为空。
		field.String("payment_ref").MaxLen(128).Default("").StructTag(`json:"payment_ref"`),
		// 开卡本金与赠送权益分列记账，赠送不参与退卡可退金额。
		field.Int64("principal_cents").Positive().StructTag(`json:"principal_cents"`),
		field.Int64("bonus_cents").Default(0).StructTag(`json:"bonus_cents"`),
		// 适用商品（SKU ID 列表）；空表示全场通用。
		field.JSON("applicable_scope", []int{}).Optional().StructTag(`json:"applicable_scope"`),
		// 合同确认：开卡必须确认合同，未确认不得写卡。
		field.Bool("contract_confirmed").Default(false).StructTag(`json:"contract_confirmed"`),
		field.Time("valid_from").StructTag(`json:"valid_from"`),
		field.Time("valid_until").StructTag(`json:"valid_until"`),
		// 卡状态：active 可用 / exhausted 本金赠送均用尽 / refund_pending 退卡待审 /
		// refunded 已退卡 / expired 已过期（不再可消费，仍可走退卡）/ frozen 冻结。
		field.Enum("status").Values("active", "exhausted", "refund_pending", "refunded", "expired", "frozen").
			Default("active").StructTag(`json:"status"`),
		field.Int("opened_by_admin_id").Optional().Nillable().StructTag(`json:"opened_by_admin_id"`),
		// 退卡试算明细（可退本金 / 未消费赠送作废 / 计算过程），申请时写入并快照。
		field.JSON("refund_quote", map[string]any{}).Optional().StructTag(`json:"refund_quote"`),
		// 退卡申请人与审核人：审批不得自审。
		field.Int("refund_requested_by_admin_id").Optional().Nillable().StructTag(`json:"refund_requested_by_admin_id"`),
		field.Time("refund_requested_at").Optional().Nillable().StructTag(`json:"refund_requested_at"`),
		field.Int("refund_decided_by_admin_id").Optional().Nillable().StructTag(`json:"refund_decided_by_admin_id"`),
		field.Int64("refunded_cents").Default(0).StructTag(`json:"refunded_cents"`),
		field.Time("refunded_at").Optional().Nillable().StructTag(`json:"refunded_at"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsStoredValueCard) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("card_no").Unique(),
		index.Fields("customer_id", "status"),
		// 到期提醒按「有效期」扫描。
		index.Fields("status", "valid_until"),
	}
}
