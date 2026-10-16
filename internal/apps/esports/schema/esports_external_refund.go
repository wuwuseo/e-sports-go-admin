package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsExternalRefund 是外部渠道退款登记（§13 补全）：针对已核销的外部付款订单退款，
// 余额 / 储值卡原路退之外的场景，由财务登记外部退款凭证（渠道 + 流水号 + 金额 + 关联售后/订单），
// 真实通道退款仍走人工登记，登记后经财务核验执行。幂等：同一外部退款流水号只能登记一次。
type EsportsExternalRefund struct{ ent.Schema }

func (EsportsExternalRefund) Fields() []ent.Field {
	return []ent.Field{
		field.String("refund_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"refund_no"`),
		// 退款渠道：与外部收款渠道一致（wechat_work / merchant_qr / corporate_transfer 等）。
		field.String("channel").NotEmpty().MaxLen(48).StructTag(`json:"channel"`),
		// 外部退款流水号：幂等键，同一流水号不重复登记。
		field.String("transaction_no").NotEmpty().MaxLen(128).StructTag(`json:"transaction_no"`),
		field.Int64("amount_cents").Positive().StructTag(`json:"amount_cents"`),
		field.Int("order_id").Positive().StructTag(`json:"order_id"`),
		field.Int("aftersale_id").Optional().Nillable().StructTag(`json:"aftersale_id"`),
		field.Enum("status").Values("registered", "executed").Default("registered").StructTag(`json:"status"`),
		field.String("reason").MaxLen(255).Default("").StructTag(`json:"reason"`),
		field.Int("registered_by_admin_id").Optional().Nillable().StructTag(`json:"registered_by_admin_id"`),
		field.Int("executed_by_admin_id").Optional().Nillable().StructTag(`json:"executed_by_admin_id"`),
		field.Time("executed_at").Optional().Nillable().StructTag(`json:"executed_at"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsExternalRefund) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("refund_no").Unique(),
		// 同一外部退款流水号只能登记一次（幂等）。
		index.Fields("transaction_no").Unique(),
		index.Fields("order_id", "status"),
		index.Fields("status", "created_at"),
	}
}
