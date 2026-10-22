package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsAfterSaleRule 是售后自动建单规则（§13.2-05）：把「条件 + 动作」做成可配置规则，
// 覆盖结单后金额异常 / 时长严重不符等场景，命中后由任务在同一事务内建 channel=system 的售后单。
//
// 口径：
//   - trigger_event 决定在哪个业务时点判定（结单 / 服务段结束 / 礼物支付完成 / 仅手动触发）；
//   - metric + threshold_value 决定命中条件，单位随 metric 变化（基点 / 分 / 分钟）；
//   - window_minutes 是结单后的观察窗口，0 表示不限时；
//   - after_sale_kind 是命中后生成的售后单 kind（与 EsportsAfterSale.kind 同值集）；
//   - 规则不删除历史：改条件用 version 乐观锁原地编辑，停用改 status=disabled。
//
// 建议表名：esports_after_sale_rules。CRUD 路由与 PermAPI 由主控补，本 schema 不含业务逻辑。
type EsportsAfterSaleRule struct{ ent.Schema }

func (EsportsAfterSaleRule) Fields() []ent.Field {
	return []ent.Field{
		field.String("code").NotEmpty().MaxLen(48).Immutable().StructTag(`json:"code"`),
		field.String("name").NotEmpty().MaxLen(120).StructTag(`json:"name"`),
		// 来源单据类型，与 EsportsAfterSale.source_type 同口径。
		field.Enum("source_type").Values("order", "gift", "stored_value_card", "recharge", "assessment").
			Default("order").StructTag(`json:"source_type"`),
		// 判定时点：结单 / 服务段结束 / 礼物支付完成 / 仅人工触发。
		field.Enum("trigger_event").Values("order_closed", "service_segment_finished", "gift_paid", "manual").
			Default("order_closed").StructTag(`json:"trigger_event"`),
		// 判定口径：金额偏离基点 / 金额超阈值（分）/ 时长短缺（分钟）/ 时长短缺比例（基点）/ 不判定。
		field.Enum("metric").
			Values("amount_deviation_bp", "amount_over_cents", "duration_shortage_minutes", "duration_shortage_bp", "none").
			Default("none").StructTag(`json:"metric"`),
		// 阈值，单位随 metric：基点 / 分 / 分钟。0 表示该规则不设阈值（配合 metric=none 使用）。
		field.Int64("threshold_value").Default(0).StructTag(`json:"threshold_value"`),
		// 结单后观察窗口（分钟）：0 表示不限时；仅 trigger_event=order_closed 时有意义。
		field.Int("window_minutes").Default(0).StructTag(`json:"window_minutes"`),
		// 适用范围：为空表示不限；scope_sku_id 为空表示不限 SKU。两条同时命中才生效。
		field.String("scope_game").MaxLen(64).Default("").StructTag(`json:"scope_game"`),
		field.Int("scope_sku_id").Optional().Nillable().StorageKey("scope_product_id").StructTag(`json:"scope_product_id"`),
		// 命中后生成的售后单 kind（与 EsportsAfterSale.kind 同值集）。
		field.Enum("after_sale_kind").
			Values("service_mismatch", "duration_short", "no_show", "wrong_item", "overcharge", "player_appeal", "refund_request", "other").
			Default("other").StructTag(`json:"after_sale_kind"`),
		// 建单通道：system 自动建单 / admin 只推待办由客服确认后建单。
		field.Enum("channel").Values("system", "admin").Default("system").StructTag(`json:"channel"`),
		// 命中建单时写入售后单的默认责任方。
		field.Enum("fault_party").Values("undetermined", "player", "admin", "customer", "platform", "none").
			Default("undetermined").StructTag(`json:"fault_party"`),
		// 命中是否直接建单（false 表示只写命中留痕 + 待办，不建单）。
		field.Bool("auto_create").Default(true).StructTag(`json:"auto_create"`),
		// 多条规则同时命中时按 priority 从大到小取第一条。
		field.Int("priority").Default(0).StructTag(`json:"priority"`),
		field.String("note").MaxLen(255).Default("").StructTag(`json:"note"`),
		field.Enum("status").Values("draft", "active", "disabled").Default("draft").StructTag(`json:"status"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsAfterSaleRule) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("code").Unique(),
		index.Fields("status", "priority"),
		index.Fields("trigger_event", "metric"),
	}
}

// EsportsAfterSaleRuleHit 是规则命中留痕（§13.2-05，只增不改）：每条规则对每个来源单据至多命中一次，
// 由 dedup_key = <rule_code>:<source_type>:<source_id> 的唯一索引兜底，避免重复建单与重复提醒。
// 命中后若建单成功，回填 after_sale_id；未建单（auto_create=false）时该列为空。
//
// 建议表名：esports_after_sale_rule_hits。
type EsportsAfterSaleRuleHit struct{ ent.Schema }

func (EsportsAfterSaleRuleHit) Hooks() []ent.Hook { return appendOnlyHooks() }

func (EsportsAfterSaleRuleHit) Fields() []ent.Field {
	return []ent.Field{
		field.Int("rule_id").Positive().StructTag(`json:"rule_id"`),
		field.String("rule_code").NotEmpty().MaxLen(48).Immutable().StructTag(`json:"rule_code"`),
		field.String("source_type").NotEmpty().MaxLen(32).Immutable().StructTag(`json:"source_type"`),
		field.Int("source_id").Positive().Immutable().StructTag(`json:"source_id"`),
		// 命中时的实际度量值与阈值快照，便于事后解释为何命中。
		field.Int64("metric_value").Default(0).StructTag(`json:"metric_value"`),
		field.Int64("threshold_value").Default(0).StructTag(`json:"threshold_value"`),
		// 命中建单结果：建单成功回填售后单 ID；0 表示未建单（仅提醒 / 待办）。
		field.Int("after_sale_id").Default(0).StructTag(`json:"after_sale_id"`),
		// 幂等键 <rule_code>:<source_type>:<source_id>，唯一索引兜底同规则同来源只命中一次。
		field.String("dedup_key").NotEmpty().MaxLen(191).Immutable().StructTag(`json:"dedup_key"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

func (EsportsAfterSaleRuleHit) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("dedup_key").Unique(),
		index.Fields("rule_id", "created_at"),
		index.Fields("source_type", "source_id"),
	}
}
