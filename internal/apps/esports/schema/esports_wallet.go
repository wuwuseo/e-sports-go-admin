package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsWalletAccount 是钱包账户登记表。余额不落库，一律由 EsportsWalletTx 流水求和得出，
// 从结构上保证"账户余额等于对应流水之和"，不存在两处金额可以对不上的情况。
// account_type 里的 stored_value_card 为已废弃值（2026-10-20 储值卡功能移除）：该类型的账户号曾是
// 储值卡 ID，且不为卡建登记行。历史流水仍可能带该值，保留枚举仅为历史台账可读，禁止新写入。
type EsportsWalletAccount struct{ ent.Schema }

func (EsportsWalletAccount) Fields() []ent.Field {
	return []ent.Field{
		// stored_value_card 为已废弃值（2026-10-20 储值卡功能移除），保留仅为历史流水/历史台账可读，禁止新写入。
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
// account_type 里的 stored_value_card 为已废弃值（2026-10-20 储值卡功能移除），保留仅为历史流水可读，
// 禁止新写入。
type EsportsWalletTx struct{ ent.Schema }

func (EsportsWalletTx) Hooks() []ent.Hook { return appendOnlyHooks() }

func (EsportsWalletTx) Fields() []ent.Field {
	return []ent.Field{
		field.String("tx_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"tx_no"`),
		// stored_value_card 为已废弃值（2026-10-20 储值卡功能移除），保留仅为历史流水/历史台账可读，禁止新写入。
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
		// 归属人（§11.6-02）：归属人须显式落库，使单条流水可独立自证「这笔钱是谁的」
		// （历史上 stored_value_card 账户的 owner_id 是卡 ID，归属人须由卡上 customer_id 得出）。
		// customer=客户 / player=陪玩 / platform=平台自有；历史数据默认 customer，可按下方 SQL 回填。
		field.Enum("beneficiary_type").Values("customer", "player", "platform").Default("customer").StructTag(`json:"beneficiary_type"`),
		field.Int("beneficiary_id").Default(0).StructTag(`json:"beneficiary_id"`),
		// 变动前后余额（§11.6-02，按「本金 + 赠送」合计口径，单位分）：写入时由账本在同一事务内计算并快照，
		// 历史流水默认 0（读取方须按 0 判定为「未快照」而不是「余额为 0」）。
		field.Int64("balance_before_cents").Default(0).StructTag(`json:"balance_before_cents"`),
		field.Int64("balance_after_cents").Default(0).StructTag(`json:"balance_after_cents"`),
		// 该笔资金所属批次键（§11.6-02）：与 EsportsLedgerEntry.fund_batch 同口径，
		// 使单条钱包流水可回溯到批次账本；手动调整等无批次场景为空串。
		field.String("fund_batch").MaxLen(80).Default("").StructTag(`json:"fund_batch"`),
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
