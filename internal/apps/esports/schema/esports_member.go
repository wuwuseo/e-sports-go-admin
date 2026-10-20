package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsMemberLevelRule 是会员等级配置（§10.1）。等级按成长值门槛分层，门槛与权益均可配置。
// 成长值单位与订单金额一致（分），避免取整损失；折扣用基点表示（10000=原价，9500=95 折）。
// ponytail: 生日自动发券、线下活动优先等非核心权益未落字段（P2）；专属通道（dedicated_cs）与
// 优先预约（priority_booking）已作为开关落字段，身份标识由前端按 level_code/level_name 渲染。
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
		// prepay_bonus_bps 曾用于储值卡开卡赠送比例（§10.4），已废弃（2026-10-20 储值卡功能移除）。
		// 列保留仅为历史配置可读，无任何生产消费点、不再由接口写入。
		field.Int("prepay_bonus_bps").Default(0).StructTag(`json:"prepay_bonus_bps"`),
		// 专属客服权益（§10.4）：客户绑定固定负责客服、不参与轮转。首版无轮转（客户已有 owner_admin_id），
		// 该开关仅用于客服端 / 企微侧边栏客户卡片展示「专属客服」标识。
		field.Bool("dedicated_cs").Default(false).StructTag(`json:"dedicated_cs"`),
		// 优先预约权益（§10.4）：启用该等级的客户可在自助端提交预约意向（陪玩 + 期望时段 + 备注），
		// 客服工作台待办出现「会员优先预约」并按等级置顶处理。
		field.Bool("priority_booking").Default(false).StructTag(`json:"priority_booking"`),
		// 专属通道权益（§10.1-02）：启用该等级的客户在客服工作台与自助端走「专属通道」优先接入
		// （与 dedicated_cs 的固定负责客服是两件事：后者是人不轮转，前者是通道优先）。
		field.Bool("dedicated_channel").Default(false).StructTag(`json:"dedicated_channel"`),
		// 线下活动优先权益（§10.1-02）：线下活动报名 / 名额分配时该等级客户优先。
		field.Bool("offline_event_priority").Default(false).StructTag(`json:"offline_event_priority"`),
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
		// 成长值来源的支付构成（§10.2-03）：现金 / 已核销预付本金 / 赠送权益 / 混合 / 未标注。
		// 计提与冲回都须写入，使单条成长值流水可自证「这笔成长值来自哪种钱」；none 仅供历史数据回落。
		field.Enum("payment_source").Values("none", "cash", "prepaid_principal", "bonus_grant", "mixed").
			Default("none").StructTag(`json:"payment_source"`),
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
