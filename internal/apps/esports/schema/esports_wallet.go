package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsWalletAccount 是钱包账户登记表。余额不落库，一律由 EsportsWalletTx 流水求和得出，
// 从结构上保证"账户余额等于对应流水之和"，不存在两处金额可以对不上的情况。
// account_type=stored_value_card 的账户号为储值卡 ID（§11.3 每张卡独立账户），
// 该类型不为卡建登记行——EsportsStoredValueCard 本身就是登记信息，避免卡状态出现两处真相。
type EsportsWalletAccount struct{ ent.Schema }

func (EsportsWalletAccount) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("account_type").Values("customer_balance", "player_catfood", "stored_value_card").StructTag(`json:"account_type"`),
		field.Int("owner_id").Positive().StructTag(`json:"owner_id"`),
		field.String("display_name").MaxLen(120).Default("").StructTag(`json:"display_name"`),
		field.Enum("status").Values("active", "frozen").Default("active").StructTag(`json:"status"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsWalletAccount) Indexes() []ent.Index {
	return []ent.Index{index.Fields("account_type", "owner_id").Unique()}
}

// EsportsWalletTx 只增不改的钱包流水。本金与赠送权益分列记录，退款、更正一律新增冲正流水，
// 不改历史行。customer_balance / player_catfood 的 (account_type, owner_id) 对应 EsportsWalletAccount；
// account_type=stored_value_card 时 owner_id 为储值卡 ID（每张卡独立账户，归属人由卡上的 customer_id 得出）。
type EsportsWalletTx struct{ ent.Schema }

func (EsportsWalletTx) Hooks() []ent.Hook { return appendOnlyHooks() }

func (EsportsWalletTx) Fields() []ent.Field {
	return []ent.Field{
		field.String("tx_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"tx_no"`),
		field.Enum("account_type").Values("customer_balance", "player_catfood", "stored_value_card").StructTag(`json:"account_type"`),
		field.Int("owner_id").Positive().StructTag(`json:"owner_id"`),
		// withdraw：陪玩提现申请时扣减猫粮（§6.3）；驳回用 refund_return 退回。
		field.Enum("entry_type").Values("recharge", "consume", "refund_return", "adjust", "expire", "withdraw").StructTag(`json:"entry_type"`),
		field.Int64("principal_delta_cents").Default(0).StructTag(`json:"principal_delta_cents"`),
		field.Int64("bonus_delta_cents").Default(0).StructTag(`json:"bonus_delta_cents"`),
		field.String("source_type").NotEmpty().MaxLen(48).StructTag(`json:"source_type"`),
		field.String("source_id").NotEmpty().MaxLen(80).StructTag(`json:"source_id"`),
		field.Int("operator_id").Optional().Nillable().StructTag(`json:"operator_id"`),
		field.String("note").MaxLen(255).Default("").StructTag(`json:"note"`),
		// 充值活动快照：活动资格 / 赠送规则 / 应付 / 赠送额 / 赠送有效期；手动充值为空。
		field.JSON("rule_snapshot", map[string]any{}).Optional().StructTag(`json:"rule_snapshot"`),
		// 该笔赠送权益的到期时间；bonus_valid_days=0 时为空表示永久。
		field.Time("bonus_expires_at").Optional().Nillable().StructTag(`json:"bonus_expires_at"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

func (EsportsWalletTx) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("tx_no").Unique(),
		// 同一资金来源只允许一张同类流水，重复提交靠这条唯一索引兜底。
		index.Fields("source_type", "source_id", "entry_type").Unique(),
		index.Fields("account_type", "owner_id", "created_at"),
	}
}
