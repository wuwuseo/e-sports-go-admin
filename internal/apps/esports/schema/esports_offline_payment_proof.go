package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsOfflinePaymentProof 是线下 / 外部到账凭证（待核验收款单，§4.2 / §4.3）：
// 企微收款无回调、商户码截图、对公转账凭证都由客服提交凭证生成「待核验收款」，
// 财务核验通过后才调用收款核销边界把订单置为已付款并放行派单；提交人不得自审。
// 唯一键 (channel, transaction_no) 保证同一笔渠道流水不会被核销两次。
type EsportsOfflinePaymentProof struct{ ent.Schema }

func (EsportsOfflinePaymentProof) Fields() []ent.Field {
	return []ent.Field{
		field.String("proof_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"proof_no"`),
		field.Int("order_id").Positive().StructTag(`json:"order_id"`),
		field.Int("intent_id").Positive().StructTag(`json:"intent_id"`),
		field.Int("account_id").Positive().StructTag(`json:"account_id"`),
		field.Enum("channel").Values("wechat_work", "merchant_qr", "corporate_transfer").
			StructTag(`json:"channel"`),
		// 渠道流水号 / 转账单号 / 商户订单号，与 channel 组成唯一键。
		field.String("transaction_no").NotEmpty().MaxLen(128).StructTag(`json:"transaction_no"`),
		field.Int64("amount_cents").Positive().StructTag(`json:"amount_cents"`),
		// 凭证地址 / 编号（转账截图、对公回单等）。
		field.String("proof_ref").NotEmpty().MaxLen(255).StructTag(`json:"proof_ref"`),
		field.String("note").MaxLen(500).Default("").StructTag(`json:"note"`),
		field.Enum("status").Values("pending_verification", "verified", "rejected").
			Default("pending_verification").StructTag(`json:"status"`),
		field.Int("submitted_by_admin_id").Optional().Nillable().StructTag(`json:"submitted_by_admin_id"`),
		field.Int("verified_by_admin_id").Optional().Nillable().StructTag(`json:"verified_by_admin_id"`),
		field.Time("verified_at").Optional().Nillable().StructTag(`json:"verified_at"`),
		field.String("resolution").MaxLen(255).Default("").StructTag(`json:"resolution"`),
		// 核销成功后回填的收款收据 ID。
		field.Int("receipt_id").Optional().Nillable().StructTag(`json:"receipt_id"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsOfflinePaymentProof) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("proof_no").Unique(),
		index.Fields("channel", "transaction_no").Unique(),
		index.Fields("order_id", "status"),
		index.Fields("status", "created_at"),
	}
}
