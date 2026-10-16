package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsGift 是礼物目录（§9.1）。礼物分成与服务订单分成分开配置：按比例（commission_bp 万分比）
// 或固定金额（commission_cents）。高额档 high_value 与年龄限制 require_age 用于送礼二次确认与拦截。
type EsportsGift struct{ ent.Schema }

func (EsportsGift) Fields() []ent.Field {
	return []ent.Field{
		field.String("code").NotEmpty().MaxLen(80).Immutable().StructTag(`json:"code"`),
		field.String("name").NotEmpty().MaxLen(120).StructTag(`json:"name"`),
		field.String("icon").MaxLen(255).Default("").StructTag(`json:"icon"`),
		field.Int64("price_cents").Positive().StructTag(`json:"price_cents"`),
		field.String("category").MaxLen(80).Default("").StructTag(`json:"category"`),
		// 礼物默认分成：percent 用 commission_bp，fixed 用 commission_cents。
		field.Enum("commission_mode").Values("percent", "fixed").Default("percent").StructTag(`json:"commission_mode"`),
		field.Int("commission_bp").Default(0).StructTag(`json:"commission_bp"`),
		field.Int64("commission_cents").Default(0).StructTag(`json:"commission_cents"`),
		// 高额档：送礼需二次确认（§9.5 大额打赏）。
		field.Bool("high_value").Default(false).StructTag(`json:"high_value"`),
		// 年龄限制：仅成年客户可送（§9.5 未成年人打赏拦截）。
		field.Bool("require_age").Default(false).StructTag(`json:"require_age"`),
		field.Int("sort_order").Default(0).StructTag(`json:"sort_order"`),
		field.Enum("status").Values("draft", "on_sale", "off_sale").Default("draft").StructTag(`json:"status"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsGift) Indexes() []ent.Index {
	return []ent.Index{index.Fields("code").Unique(), index.Fields("status", "sort_order")}
}

// EsportsGiftOrder 是送礼单据（§9.2）。只有支付足额核销后才落库；收礼陪玩在支付前确认并快照
// （player_id + player_name），支付后不得静默改人。关联 order_id 仅用于追溯，不并入订单应付，
// 也不重复计提服务分成。review_status 承载防刷榜标记：疑似关联交易先标记，复核通过前不生成
// 可提现的礼物分成（§9.4）。
type EsportsGiftOrder struct{ ent.Schema }

func (EsportsGiftOrder) Fields() []ent.Field {
	return []ent.Field{
		field.String("gift_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"gift_no"`),
		field.Int("gift_id").Positive().StructTag(`json:"gift_id"`),
		field.Int("gift_intent_id").Positive().Optional().Nillable().StructTag(`json:"gift_intent_id"`),
		field.Int("customer_id").Positive().StructTag(`json:"customer_id"`),
		field.Int("player_id").Positive().StructTag(`json:"player_id"`),
		// 送礼时的业绩归属客服快照，后续客户转移不改变已计提的客服提成。
		field.Int("responsible_admin_id").Positive().Optional().Nillable().StructTag(`json:"responsible_admin_id"`),
		// 支付时命中的客服提成规则快照；关闭礼物提成或无规则时为空。
		field.Int("cs_commission_rule_id").Positive().Optional().Nillable().StructTag(`json:"cs_commission_rule_id"`),
		// 收礼对象快照：支付前确认后写入，支付后不改。
		field.String("player_name").MaxLen(120).Default("").StructTag(`json:"player_name"`),
		// 关联服务订单，仅追溯用。
		field.Int("order_id").Optional().Nillable().StructTag(`json:"order_id"`),
		field.Int("quantity").Positive().Default(1).StructTag(`json:"quantity"`),
		field.Int64("unit_price_cents").Positive().StructTag(`json:"unit_price_cents"`),
		field.Int64("amount_cents").Positive().StructTag(`json:"amount_cents"`),
		// 支付构成：首版仅本金，拒绝赠送权益支付礼物（§9.5）。
		field.Int64("principal_cents").Default(0).StructTag(`json:"principal_cents"`),
		field.Int64("bonus_cents").Default(0).StructTag(`json:"bonus_cents"`),
		field.String("payment_source").Default("customer_balance_principal").StructTag(`json:"payment_source"`),
		field.Int("stored_value_card_id").Positive().Optional().Nillable().StructTag(`json:"stored_value_card_id"`),
		// 礼物分成快照（与服务订单分开配置）。
		field.Enum("commission_mode").Values("percent", "fixed").Default("percent").StructTag(`json:"commission_mode"`),
		field.Int("commission_bp").Default(0).StructTag(`json:"commission_bp"`),
		field.Int64("commission_cents").Default(0).StructTag(`json:"commission_cents"`),
		// 陪玩礼物收入（分成后，分）。
		field.Int64("income_cents").Default(0).StructTag(`json:"income_cents"`),
		field.Int64("refund_cents").Default(0).StructTag(`json:"refund_cents"`),
		field.Enum("refund_status").Values("none", "partial", "full").Default("none").StructTag(`json:"refund_status"`),
		// 防刷榜复核状态：normal 正常 / flagged 待复核 / approved 复核通过（释放分成）/ rejected 复核驳回。
		field.Enum("review_status").Values("normal", "flagged", "approved", "rejected").Default("normal").StructTag(`json:"review_status"`),
		field.Enum("status").Values("paid", "refunded").Default("paid").StructTag(`json:"status"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsGiftOrder) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("gift_no").Unique(),
		index.Fields("gift_intent_id").Unique(),
		index.Fields("customer_id", "created_at"),
		index.Fields("player_id", "created_at"),
	}
}
