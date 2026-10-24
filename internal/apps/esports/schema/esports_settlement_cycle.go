package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsSettlementCycle 是结算周期配置。周期支持日结 / 周结 / 月结 / 自定义间隔，
// 配置维度按「全局默认 → 按游戏 → 按等级 → 单个陪玩」逐级覆盖：同一维度同一 scope_key 只保留一条配置，
// 解析时取优先级最高且启用的那条；全部缺失时回落到系统配置项 app.esports.settlement_period。
// 自定义间隔以 UTC 自然日从 epoch 对齐切分（与日/周/月的自然周期口径一致，无需额外锚点配置）。
type EsportsSettlementCycle struct{ ent.Schema }

func (EsportsSettlementCycle) Fields() []ent.Field {
	return []ent.Field{
		// 配置维度：global 全局默认；game 按游戏分区；level 按考核等级；player 按单个陪玩档案。
		field.Enum("scope").Values("global", "game", "level", "player").Default("global").
			StructTag(`json:"scope"`),
		// scope=game 为游戏分区名；scope=level 为 novice/skilled/senior/mentor；scope=player 为陪玩档案 ID；global 为空。
		field.String("scope_key").MaxLen(64).Default("").StructTag(`json:"scope_key"`),
		field.Enum("period").Values("daily", "weekly", "monthly", "custom").Default("weekly").
			StructTag(`json:"period"`),
		// period=custom 时的间隔天数（1–365）；其余周期必须为 0。
		field.Int("interval_days").Default(0).StructTag(`json:"interval_days"`),
		field.Bool("enabled").Default(true).StructTag(`json:"enabled"`),
		field.String("note").MaxLen(255).Default("").StructTag(`json:"note"`),
		field.Int("created_by_admin_id").Optional().Nillable().StructTag(`json:"created_by_admin_id"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsSettlementCycle) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("scope", "scope_key").Unique(),
	}
}
