package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsMemberLevelRule 是会员等级配置（§10.1）。等级按成长值门槛分层，门槛与权益均可配置。
// 成长值单位与订单金额一致（分），避免取整损失；折扣用基点表示（10000=原价，9500=95 折）。
// ponytail: 生日礼遇、专属通道、优先预约、线下活动优先等非核心权益未落字段；
// 首版只实现消费折扣、会员订单优先、储值卡赠送比例与身份标识四项可配置项。
type EsportsMemberLevelRule struct{ ent.Schema }

func (EsportsMemberLevelRule) Fields() []ent.Field {
	return []ent.Field{
		field.String("code").NotEmpty().MaxLen(32).Immutable().StructTag(`json:"code"`),
		field.String("name").NotEmpty().MaxLen(32).StructTag(`json:"name"`),
		// 达到该累计成长值（分）即进入本等级。
		field.Int64("growth_threshold").Default(0).StructTag(`json:"growth_threshold"`),
		// 消费折扣基点：10000=不打折，9500=95 折。
		field.Int("discount_bps").Default(10000).StructTag(`json:"discount_bps"`),
		// 会员订单在客服派单池置顶（§10.4）。
		field.Bool("priority_dispatch").Default(false).StructTag(`json:"priority_dispatch"`),
		// 储值卡开卡赠送比例基点（§10.4）；储值卡开卡流程尚未实现，此处仅预留配置位。
		field.Int("prepay_bonus_bps").Default(0).StructTag(`json:"prepay_bonus_bps"`),
		field.Int("sort_order").Default(0).StructTag(`json:"sort_order"`),
		field.Enum("status").Values("enabled", "disabled").Default("enabled").StructTag(`json:"status"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsMemberLevelRule) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("code").Unique(),
		index.Fields("status", "growth_threshold"),
	}
}

// EsportsGrowthLedger 只增不改的成长值流水（§10.2）。成长值不落库，一律由流水求和得出，
// 与钱包余额同构。来源：订单结单计提（order）、售后退款冲回（after_sale）、管理员手动调整（manual）。
// 同一来源只允许一条流水，靠唯一索引保证幂等。
type EsportsGrowthLedger struct{ ent.Schema }

func (EsportsGrowthLedger) Hooks() []ent.Hook { return appendOnlyHooks() }

func (EsportsGrowthLedger) Fields() []ent.Field {
	return []ent.Field{
		field.Int("customer_id").Positive().StructTag(`json:"customer_id"`),
		// 成长值增量（分），正为计提、负为冲回。
		field.Int64("delta").StructTag(`json:"delta"`),
		field.Enum("source_type").Values("order", "after_sale", "manual", "gift").StructTag(`json:"source_type"`),
		// 关联来源号：订单号 / 售后单号 / 礼物单号（退款冲回加 :refund 后缀）；manual 用管理员指定标识。
		field.String("source_id").NotEmpty().MaxLen(80).StructTag(`json:"source_id"`),
		field.Int("operator_id").Optional().Nillable().StructTag(`json:"operator_id"`),
		field.String("note").MaxLen(255).Default("").StructTag(`json:"note"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

func (EsportsGrowthLedger) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("source_type", "source_id").Unique(),
		index.Fields("customer_id", "created_at"),
	}
}

// EsportsMemberLevelOverride 是管理员手动调级的审计记录（§10.3）。手动调级只建覆盖记录、
// 不改成长值流水；记录操作人、原因与时间，可审计。
type EsportsMemberLevelOverride struct{ ent.Schema }

func (EsportsMemberLevelOverride) Hooks() []ent.Hook { return appendOnlyHooks() }

func (EsportsMemberLevelOverride) Fields() []ent.Field {
	return []ent.Field{
		field.Int("customer_id").Positive().StructTag(`json:"customer_id"`),
		field.String("level_code").NotEmpty().MaxLen(32).StructTag(`json:"level_code"`),
		field.String("reason").NotEmpty().MaxLen(255).StructTag(`json:"reason"`),
		field.Int("operator_id").Positive().StructTag(`json:"operator_id"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

func (EsportsMemberLevelOverride) Indexes() []ent.Index {
	return []ent.Index{index.Fields("customer_id", "created_at")}
}
