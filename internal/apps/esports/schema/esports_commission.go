package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsCommissionRule 是陪玩分成规则。规则不可原地修改：改规则＝新增一条递增 version 的新行，
// 旧行转 history 且不再变更（§12.4）。同一 rule_no 的不同版本共享编号，version 逐版递增。
// §12.3 五级优先级：单笔手动指定（订单字段 manual_commission_rule_id，不落 scope）
// > scope=player > scope=level > scope=game > scope=global。
// 生效时间在未来的版本进入 scheduled，达到时间后解析器可立即使用，定时任务补齐状态转换。
type EsportsCommissionRule struct{ ent.Schema }

func (EsportsCommissionRule) Fields() []ent.Field {
	return []ent.Field{
		field.String("rule_no").NotEmpty().MaxLen(48).StructTag(`json:"rule_no"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Enum("status").Values("draft", "scheduled", "active", "history").Default("draft").StructTag(`json:"status"`),
		// scope 决定规则适用范围：全局 / 按游戏 / 按陪玩等级 / 按单个陪玩档案。
		field.Enum("scope").Values("global", "game", "level", "player").Default("global").StructTag(`json:"scope"`),
		// scope=game 时为游戏名；scope=level 时为陪玩等级（novice/skilled/senior/mentor）；
		// scope=player 时为陪玩档案 ID 字符串。
		field.String("scope_key").MaxLen(64).Default("").StructTag(`json:"scope_key"`),
		field.Enum("calc_mode").Values("percent", "fixed_per_order", "fixed_per_unit").Default("percent").StructTag(`json:"calc_mode"`),
		// percent 使用，万分比：100bp = 1%。
		field.Int("value_bp").Default(0).StructTag(`json:"value_bp"`),
		// fixed_per_order / fixed_per_unit 使用，单位分。
		field.Int64("value_cents").Default(0).StructTag(`json:"value_cents"`),
		// fixed_per_unit 的计量口径：按 SKU 购买份数或按承诺服务量。
		field.Enum("unit_basis").Values("sku_quantity", "promised_units").Default("sku_quantity").StructTag(`json:"unit_basis"`),
		field.String("note").MaxLen(255).Default("").StructTag(`json:"note"`),
		field.Time("effective_at").Optional().Nillable().StructTag(`json:"effective_at"`),
		field.Int("created_by_admin_id").Optional().Nillable().StructTag(`json:"created_by_admin_id"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsCommissionRule) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("rule_no", "version").Unique(),
		index.Fields("status", "scope"),
	}
}

// EsportsCommissionEntry 是分成明细。退款冲回不使用「修改原明细」表达，而是新增 kind=reversal 的
// 负数明细，使退款金额自然落在退款当期（§12.6）。待结算明细分批转入 settled 并记 settlement_id。
type EsportsCommissionEntry struct{ ent.Schema }

func (EsportsCommissionEntry) Fields() []ent.Field {
	return []ent.Field{
		// 幂等键：service 为 commission:service:<order_no>:<player_id>，reversal 为 commission:reversal:<after_sale_no>:<player_id>，
		// gift 为 commission:gift:<gift_no>，gift_reversal 为 commission:gift_reversal:<gift_no>。
		field.String("idem_key").NotEmpty().MaxLen(120).StructTag(`json:"idem_key"`),
		// 礼物收入与订单无关，order_id 可空（§9.3 礼物收入单独记账、与订单收入一起结算）。
		field.Int("order_id").Optional().Nillable().StructTag(`json:"order_id"`),
		field.Int("player_id").Positive().StructTag(`json:"player_id"`),
		field.Enum("kind").Values("service", "reversal", "gift", "gift_reversal", "addon").Default("service").StructTag(`json:"kind"`),
		field.Int("seat_index").Default(0).StructTag(`json:"seat_index"`),
		// reversal 的来源售后单。
		field.Int("after_sale_id").Optional().Nillable().StructTag(`json:"after_sale_id"`),
		field.Int("rule_id").Optional().Nillable().StructTag(`json:"rule_id"`),
		field.String("rule_no").MaxLen(48).Default("").StructTag(`json:"rule_no"`),
		field.Int("rule_version").Default(0).StructTag(`json:"rule_version"`),
		field.Enum("calc_mode").Values("percent", "fixed_per_order", "fixed_per_unit").Default("percent").StructTag(`json:"calc_mode"`),
		// 分成基数＝订单 payable_cents。
		field.Int64("base_cents").Default(0).StructTag(`json:"base_cents"`),
		// 分成金额（分）；reversal 为负数。
		field.Int64("amount_cents").Default(0).StructTag(`json:"amount_cents"`),
		field.Enum("status").Values("pending_settlement", "settled", "void").Default("pending_settlement").StructTag(`json:"status"`),
		field.Int("settlement_id").Optional().Nillable().StructTag(`json:"settlement_id"`),
		// 争议冻结（§12.6）：被争议的明细冻结待结算金额，不参与当期结算；
		// 冻结与状态正交（仍为 pending_settlement），解冻后恢复可结算。
		field.Time("frozen_at").Optional().Nillable().StructTag(`json:"frozen_at"`),
		field.String("frozen_reason").MaxLen(255).Default("").StructTag(`json:"frozen_reason"`),
		field.Int("frozen_by_admin_id").Optional().Nillable().StructTag(`json:"frozen_by_admin_id"`),
		field.String("note").MaxLen(255).Default("").StructTag(`json:"note"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsCommissionEntry) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("idem_key").Unique(),
		index.Fields("order_id", "player_id"),
		index.Fields("player_id", "status"),
		index.Fields("settlement_id"),
	}
}

// EsportsSettlement 是结算单。汇总某主体一周期内的待结算明细（含正向分成与负数冲回）。
// owner_type=player 时 net=实结额同时写入一条猫粮钱包流水（幂等靠 account_type+source_type+source_id+entry_type
// 唯一索引兜底）；owner_type=cs 时是客服提成结算单，不写钱包，发放由财务打款并留外部凭证（§12.2）。
// ponytail: 按游戏/等级/单个陪玩逐级覆盖结算周期、自定义间隔、争议冻结均未实现；结算周期仅支持
// daily/weekly/monthly 三种预设。
type EsportsSettlement struct{ ent.Schema }

func (EsportsSettlement) Fields() []ent.Field {
	return []ent.Field{
		field.String("settlement_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"settlement_no"`),
		field.Enum("owner_type").Values("player", "cs").Default("player").StructTag(`json:"owner_type"`),
		field.Int("owner_id").Positive().StructTag(`json:"owner_id"`),
		field.Time("period_start").StructTag(`json:"period_start"`),
		field.Time("period_end").StructTag(`json:"period_end"`),
		// 本期正向分成合计。
		field.Int64("gross_cents").Default(0).StructTag(`json:"gross_cents"`),
		// 本期负向冲回合计（负数）。
		field.Int64("adjustment_cents").Default(0).StructTag(`json:"adjustment_cents"`),
		// 实结＝gross + adjustment。
		field.Int64("net_cents").Default(0).StructTag(`json:"net_cents"`),
		field.Int("entry_count").Default(0).StructTag(`json:"entry_count"`),
		field.Enum("status").Values("draft", "locked", "paid").Default("draft").StructTag(`json:"status"`),
		field.Int("locked_by_admin_id").Optional().Nillable().StructTag(`json:"locked_by_admin_id"`),
		field.Time("locked_at").Optional().Nillable().StructTag(`json:"locked_at"`),
		// 发放（打款）留痕：外部凭证号与打款人，仅 status=paid 时非空（§12.2 客服提成发放记录关联外部凭证）。
		field.String("payout_ref").MaxLen(128).Default("").StructTag(`json:"payout_ref"`),
		field.Int("paid_by_admin_id").Optional().Nillable().StructTag(`json:"paid_by_admin_id"`),
		field.Time("paid_at").Optional().Nillable().StructTag(`json:"paid_at"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsSettlement) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("settlement_no").Unique(),
		index.Fields("owner_id", "period_start"),
	}
}
