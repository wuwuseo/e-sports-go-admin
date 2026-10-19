package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsGiftIntent records the exact gift terms a customer must approve before
// a staff member can debit their balance. Confirmation is consumed with payment.
type EsportsGiftIntent struct{ ent.Schema }

func (EsportsGiftIntent) Fields() []ent.Field {
	return []ent.Field{
		field.Int("customer_id").Positive().StructTag(`json:"customer_id"`),
		field.Int("gift_id").Positive().StructTag(`json:"gift_id"`),
		field.String("gift_name").NotEmpty().MaxLen(120).StructTag(`json:"gift_name"`),
		field.Int("gift_version").Positive().StructTag(`json:"gift_version"`),
		field.Int("player_id").Positive().StructTag(`json:"player_id"`),
		field.String("player_name").NotEmpty().MaxLen(120).StructTag(`json:"player_name"`),
		field.Int("order_id").Optional().Nillable().StructTag(`json:"order_id"`),
		field.Int("quantity").Positive().StructTag(`json:"quantity"`),
		field.Int64("unit_price_cents").Positive().StructTag(`json:"unit_price_cents"`),
		field.Int64("amount_cents").Positive().StructTag(`json:"amount_cents"`),
		field.String("payment_source").Default("customer_balance_principal").StructTag(`json:"payment_source"`),
		field.Int("stored_value_card_id").Positive().Optional().Nillable().StructTag(`json:"stored_value_card_id"`),
		field.String("stored_value_card_no").MaxLen(64).Default("").StructTag(`json:"stored_value_card_no"`),
		// 价格档位快照（§9.1-02）：同一礼物多档价格时记录所选档；未分档的礼物为 0 / 空串。
		field.Int("gift_tier_id").Default(0).StructTag(`json:"gift_tier_id"`),
		field.String("gift_tier_code").MaxLen(48).Default("").StructTag(`json:"gift_tier_code"`),
		// 该档 / 该礼物的限制快照（§9.1-02）：在高额二次确认与年龄拦截时读取。
		field.Bool("high_value").Default(false).StructTag(`json:"high_value"`),
		field.Bool("require_age").Default(false).StructTag(`json:"require_age"`),
		// 收款通道（§9.2-01）：none 未发起收款 / customer_balance 余额本金 / stored_value_card 卡本金 /
		// wechat_work / merchant_qr / corporate_transfer 走在线渠道或人工核销。余额与卡本金都不足时，
		// 业务侧改走外部渠道收款，而不是直接报本金不足。
		field.Enum("payment_channel").
			Values("none", "customer_balance", "stored_value_card", "wechat_work", "merchant_qr", "corporate_transfer").
			Default("none").StructTag(`json:"payment_channel"`),
		// 收款状态（§9.2-01）：not_required 余额直扣 / awaiting_payment 待客户支付 /
		// pending_verification 待财务核验 / verified 已到账 / failed 渠道明确失败（不伪成功）。
		field.Enum("payment_status").
			Values("not_required", "awaiting_payment", "pending_verification", "verified", "failed").
			Default("not_required").StructTag(`json:"payment_status"`),
		// 收款意图号（§9.2-01）：在线渠道发起收款后写入，回调按号定位；未发起为空（NULL 不参与唯一冲突）。
		field.String("payment_intent_no").Optional().Nillable().MaxLen(64).StructTag(`json:"payment_intent_no"`),
		// 渠道流水号 / 人工核销凭证号。
		field.String("payment_ref").MaxLen(128).Default("").StructTag(`json:"payment_ref"`),
		field.Time("payment_expires_at").Optional().Nillable().StructTag(`json:"payment_expires_at"`),
		field.Time("payment_verified_at").Optional().Nillable().StructTag(`json:"payment_verified_at"`),
		field.Int("payment_verified_by_admin_id").Optional().Nillable().StructTag(`json:"payment_verified_by_admin_id"`),
		field.Int("created_by_admin_id").Positive().StructTag(`json:"created_by_admin_id"`),
		field.String("client_key").NotEmpty().MaxLen(128).StructTag(`json:"client_key"`),
		field.Enum("status").Values("pending", "confirmed", "consumed", "cancelled").Default("pending").StructTag(`json:"status"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.String("confirmation_ref").MaxLen(255).Default("").StructTag(`json:"confirmation_ref"`),
		field.Int("gift_order_id").Positive().Optional().Nillable().StructTag(`json:"gift_order_id"`),
		field.Time("expires_at").StructTag(`json:"expires_at"`),
		field.Time("confirmed_at").Optional().Nillable().StructTag(`json:"confirmed_at"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsGiftIntent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("created_by_admin_id", "client_key").Unique(),
		index.Fields("customer_id", "created_at"),
		index.Fields("status", "expires_at"),
		// 收款意图号唯一（§9.2-01）；未发起收款为 NULL，不参与冲突。
		index.Fields("payment_intent_no").Unique(),
	}
}
