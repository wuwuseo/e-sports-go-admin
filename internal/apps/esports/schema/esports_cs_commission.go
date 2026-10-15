package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"go-admin/internal/apps/esports/schematier"
)

// CsCommissionTier 是客服提成的阶梯档：按「提成周期累计净基数」分档，取满足阈值且最高的一档。
// 定义在 schematier 包以避免与主 schema 包同名的生成代码导入冲突；此处以别名保留类型名。
type CsCommissionTier = schematier.CsCommissionTier

// EsportsCsCommissionRule 是客服提成规则（§12.2）。与陪玩分成规则一样不可原地修改：改规则＝
// 新增一条递增 version 的新行，旧行转 history（§12.4）。version 在支付核销/结单时随明细快照。
// ponytail: 「单笔手动指定」优先级未实现；effective_at 调度生效未做，仅以 status=active 判定。
type EsportsCsCommissionRule struct{ ent.Schema }

func (EsportsCsCommissionRule) Fields() []ent.Field {
	return []ent.Field{
		field.String("rule_no").NotEmpty().MaxLen(48).StructTag(`json:"rule_no"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Enum("status").Values("draft", "active", "history").Default("draft").StructTag(`json:"status"`),
		// scope 决定规则适用范围：全局 / 指定客服（业绩归属客服）。
		field.Enum("scope").Values("global", "admin").Default("global").StructTag(`json:"scope"`),
		// scope=admin 时为目标客服的管理员 ID 字符串。
		field.String("scope_key").MaxLen(64).Default("").StructTag(`json:"scope_key"`),
		// 提成基数：报单金额（折后应付）/ 订单贡献（扣陪玩分成后的可分余额）/ 单量（每单计 1 个基数单位）。
		field.Enum("base_mode").Values("order_amount", "order_contribution", "order_count").
			Default("order_amount").StructTag(`json:"base_mode"`),
		field.Enum("calc_mode").Values("percent", "fixed_per_order").Default("percent").StructTag(`json:"calc_mode"`),
		// percent 使用，万分比：100bp = 1%。有阶梯时作为兜底值（无命中阶梯时使用）。
		field.Int("value_bp").Default(0).StructTag(`json:"value_bp"`),
		// fixed_per_order 使用，单位分。
		field.Int64("value_cents").Default(0).StructTag(`json:"value_cents"`),
		// 阶梯档：按提成周期累计净基数分档，threshold 需严格递增；空表示不分档。
		field.JSON("tiers", []CsCommissionTier{}).Optional().StructTag(`json:"tiers"`),
		// 提成周期：决定阶梯累计窗口与独立结算单的周期。
		field.Enum("period").Values("daily", "weekly", "monthly").Default("monthly").StructTag(`json:"period"`),
		field.String("note").MaxLen(255).Default("").StructTag(`json:"note"`),
		field.Time("effective_at").Optional().Nillable().StructTag(`json:"effective_at"`),
		field.Int("created_by_admin_id").Optional().Nillable().StructTag(`json:"created_by_admin_id"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsCsCommissionRule) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("rule_no", "version").Unique(),
		index.Fields("status", "scope"),
	}
}

// EsportsCsCommissionEntry 是客服提成明细（§12.2）。退款冲回不使用「修改原明细」表达，而是新增
// kind=reversal 的负数明细，使退款金额落在退款当期（§12.6）。提成只在订单完成后生成。
type EsportsCsCommissionEntry struct{ ent.Schema }

func (EsportsCsCommissionEntry) Fields() []ent.Field {
	return []ent.Field{
		// 幂等键：service 为 cs_commission:service:<order_no>:<admin_id>，
		// reversal 为 cs_commission:reversal:<after_sale_no>:<admin_id>。
		field.String("idem_key").NotEmpty().MaxLen(120).StructTag(`json:"idem_key"`),
		field.Int("order_id").Positive().StructTag(`json:"order_id"`),
		// 业绩归属客服：客服代报单取经授权的业绩归属客服（本切片取订单负责客服）。
		field.Int("admin_id").Positive().StructTag(`json:"admin_id"`),
		field.Enum("kind").Values("service", "reversal").Default("service").StructTag(`json:"kind"`),
		// reversal 的来源售后单。
		field.Int("after_sale_id").Optional().Nillable().StructTag(`json:"after_sale_id"`),
		field.Int("rule_id").Optional().Nillable().StructTag(`json:"rule_id"`),
		field.String("rule_no").MaxLen(48).Default("").StructTag(`json:"rule_no"`),
		field.Int("rule_version").Default(0).StructTag(`json:"rule_version"`),
		field.Enum("calc_mode").Values("percent", "fixed_per_order").Default("percent").StructTag(`json:"calc_mode"`),
		field.Enum("base_mode").Values("order_amount", "order_contribution", "order_count").
			Default("order_amount").StructTag(`json:"base_mode"`),
		// 提成基数（分）；base_mode=order_count 时等于 1（单量）。
		field.Int64("base_cents").Default(0).StructTag(`json:"base_cents"`),
		// 命中的阶梯万分比（无阶梯或固定额时为 0）。
		field.Int("matched_tier_bp").Default(0).StructTag(`json:"matched_tier_bp"`),
		// 提成金额（分）；reversal 为负数。
		field.Int64("amount_cents").Default(0).StructTag(`json:"amount_cents"`),
		// 该笔计提时点的已知渠道成本快照（分，§12.1）；reversal 为 0。
		field.Int64("channel_cost_cents").Default(0).StructTag(`json:"channel_cost_cents"`),
		// 工作室剩余快照（分，§12.1 反算）：服务应付 − 结单前退款 − 陪玩分成 − 渠道成本 − 本笔客服提成。
		// 仅在 kind=service 时非零，reversal 为 0。
		field.Int64("studio_remainder_cents").Default(0).StructTag(`json:"studio_remainder_cents"`),
		field.Enum("status").Values("pending_settlement", "settled", "void").Default("pending_settlement").StructTag(`json:"status"`),
		field.Int("settlement_id").Optional().Nillable().StructTag(`json:"settlement_id"`),
		field.String("note").MaxLen(255).Default("").StructTag(`json:"note"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsCsCommissionEntry) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("idem_key").Unique(),
		index.Fields("order_id", "admin_id"),
		index.Fields("admin_id", "status"),
		index.Fields("settlement_id"),
	}
}
