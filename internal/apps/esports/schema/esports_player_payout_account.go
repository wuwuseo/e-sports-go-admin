package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsPlayerPayoutAccount 是陪玩打款收款账户（云账户代发签约账户）：
// 每个陪玩按 provider + account_type 只保留一个账户，重复提交走更新而不是新增；
// 卡号 / 账号只存后四位脱敏串，明文由云账户侧持有，本站不落明文。
// status 由签约状态驱动：pending 待签约 / active 已生效 / disabled 已停用 / expired 签约过期。
type EsportsPlayerPayoutAccount struct{ ent.Schema }

func (EsportsPlayerPayoutAccount) Fields() []ent.Field {
	return []ent.Field{
		field.Int("player_id").Positive().StructTag(`json:"player_id"`),
		// 打款渠道：manual=人工打款 / yunzhanghu=云账户代发。
		field.String("provider").MaxLen(32).Default("yunzhanghu").StructTag(`json:"provider"`),
		// 收款账户类型：银行卡 / 支付宝（云账户代发只支持这两类）。
		field.Enum("account_type").Values("bank_card", "alipay").StructTag(`json:"account_type"`),
		field.String("account_name").MaxLen(64).Default("").StructTag(`json:"account_name"`),
		// 仅存后四位脱敏串，不落完整卡号 / 账号。
		field.String("account_no_masked").MaxLen(64).Default("").StructTag(`json:"account_no_masked"`),
		// 云账户签约号：签约成功后回填，代发下单时透传。
		field.String("provider_sign_no").MaxLen(128).Default("").StructTag(`json:"provider_sign_no"`),
		field.Enum("status").Values("pending", "active", "disabled", "expired").
			Default("pending").StructTag(`json:"status"`),
		field.Time("signed_at").Optional().Nillable().StructTag(`json:"signed_at"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsPlayerPayoutAccount) Indexes() []ent.Index {
	return []ent.Index{
		// 同一陪玩在同一渠道下的同类账户只有一条，重复提交按更新处理。
		index.Fields("player_id", "provider", "account_type").Unique(),
		index.Fields("status"),
	}
}
