package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsGiftTier 是礼物价格档位：同一礼物可配置多档价格，每档独立设置
// 高额二次确认（high_value）与年龄限制（require_age），使 require_age 真正按档生效
// （此前 require_age 只写入、无读取点，且礼物只有一个 price_cents）。
//
// 口径：
//   - 同一礼物下 (gift_id, price_cents) 唯一，避免配置两档完全相同的价格；
//   - is_default=true 的档位用于「未指定档位」的旧客户端下单，每个礼物至多一档默认（由业务侧校验）；
//   - 送礼意图 / 礼物单须快照 gift_tier_id 与 gift_tier_code，改档不追溯改写历史单；
//   - 礼物自身的 price_cents / high_value / require_age 保留为不分档时的回落口径。
//
// 建议表名：esports_gift_tiers。CRUD 路由与 PermAPI 由主控补。
type EsportsGiftTier struct{ ent.Schema }

func (EsportsGiftTier) Fields() []ent.Field {
	return []ent.Field{
		field.Int("gift_id").Positive().StructTag(`json:"gift_id"`),
		field.String("tier_code").NotEmpty().MaxLen(48).StructTag(`json:"tier_code"`),
		field.String("name").NotEmpty().MaxLen(120).StructTag(`json:"name"`),
		field.Int64("price_cents").Positive().StructTag(`json:"price_cents"`),
		// 该档是否需送礼二次确认（大额打赏）。
		field.Bool("high_value").Default(false).StructTag(`json:"high_value"`),
		// 该档是否仅成年客户可送（未成年人打赏拦截）；核销时按此拦截。
		field.Bool("require_age").Default(false).StructTag(`json:"require_age"`),
		// 未指定档位时使用的默认档（每个礼物至多一档）。
		field.Bool("is_default").Default(false).StructTag(`json:"is_default"`),
		field.Int("sort_order").Default(0).StructTag(`json:"sort_order"`),
		field.Enum("status").Values("enabled", "disabled").Default("enabled").StructTag(`json:"status"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsGiftTier) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("gift_id", "price_cents").Unique(),
		index.Fields("gift_id", "status", "sort_order"),
	}
}
