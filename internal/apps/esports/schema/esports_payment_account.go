package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsPaymentAccount 是工作室收款账户（§4.3）：企业微信收款 / 工作室商户码 / 对公转账。
// 收款账户必须挂在工作主体名下并经财务审核后启用，客服不能新增或更换收款主体；
// 订单外部到账只能核销到启用中的账户，停用 / 待审核 / 驳回账户一律不可用。
type EsportsPaymentAccount struct{ ent.Schema }

func (EsportsPaymentAccount) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("channel").Values("wechat_work", "merchant_qr", "corporate_transfer").
			StructTag(`json:"channel"`),
		// 收款主体名称（须为工作室主体，非个人）。
		field.String("subject_name").NotEmpty().MaxLen(120).StructTag(`json:"subject_name"`),
		// 商户号 / 银行账号；商户码类账户可为空。
		field.String("account_no").MaxLen(128).Default("").StructTag(`json:"account_no"`),
		// 收款码图片地址（merchant_qr / wechat_work 使用）。
		field.String("qr_url").MaxLen(255).Default("").StructTag(`json:"qr_url"`),
		field.String("bank_name").MaxLen(120).Default("").StructTag(`json:"bank_name"`),
		// 审核状态：待财务审核 / 启用 / 驳回 / 停用。
		field.Enum("status").Values("pending_review", "active", "rejected", "disabled").
			Default("pending_review").StructTag(`json:"status"`),
		field.String("submit_note").MaxLen(255).Default("").StructTag(`json:"submit_note"`),
		field.Int("submitted_by_admin_id").Optional().Nillable().StructTag(`json:"submitted_by_admin_id"`),
		field.Int("reviewed_by_admin_id").Optional().Nillable().StructTag(`json:"reviewed_by_admin_id"`),
		field.Time("reviewed_at").Optional().Nillable().StructTag(`json:"reviewed_at"`),
		field.String("review_resolution").MaxLen(255).Default("").StructTag(`json:"review_resolution"`),
		field.Time("disabled_at").Optional().Nillable().StructTag(`json:"disabled_at"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsPaymentAccount) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("channel", "subject_name", "account_no").Unique(),
		index.Fields("channel", "status"),
	}
}
