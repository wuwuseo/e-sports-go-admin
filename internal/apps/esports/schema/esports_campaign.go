package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"go-admin/internal/apps/esports/schematier"
)

// CampaignTier 是阶梯赠送的一档：实充金额达到 threshold_cents 时赠送 bonus_cents。
// 定义在 schematier 包以避免与主 schema 包同名的生成代码导入冲突；此处以别名保留类型名。
type CampaignTier = schematier.CampaignTier

// EsportsRechargeCampaign 是充值活动配置（§4.5）。与分成规则不同，活动不做版本化不可变：
// 活动本身允许直接编辑（靠 version 乐观锁），活动资格与赠送规则在充值时快照进钱包流水；
// code 创建后不可变。
// ponytail: 退款/退卡按充值批次还原（§4.4 储值卡范畴）、外部支付网关的独立支付意图、
// 扣减顺序可配置、礼物与打赏限制均未实现。
type EsportsRechargeCampaign struct{ ent.Schema }

func (EsportsRechargeCampaign) Fields() []ent.Field {
	return []ent.Field{
		field.String("code").NotEmpty().MaxLen(48).StructTag(`json:"code"`),
		field.String("name").NotEmpty().MaxLen(120).StructTag(`json:"name"`),
		// 满赠 / 阶梯 / 限时。
		field.Enum("activity_type").Values("full_gift", "ladder", "limited").Default("ladder").StructTag(`json:"activity_type"`),
		field.JSON("tiers", []CampaignTier{}).StructTag(`json:"tiers"`),
		// 适用客户：全部 / 新客 / 指定客户。
		field.Enum("audience").Values("all", "new_customer", "specified").Default("all").StructTag(`json:"audience"`),
		// 仅 audience=specified 时有意义。
		field.JSON("audience_customer_ids", []int{}).Optional().StructTag(`json:"audience_customer_ids"`),
		// 叠加规则：互斥 / 可叠加。
		field.Enum("stack_rule").Values("exclusive", "stack").Default("stack").StructTag(`json:"stack_rule"`),
		// 赠送有效期天数，0=永久。
		field.Int("bonus_valid_days").Default(0).StructTag(`json:"bonus_valid_days"`),
		field.Time("starts_at").StructTag(`json:"starts_at"`),
		field.Time("ends_at").StructTag(`json:"ends_at"`),
		field.Enum("status").Values("draft", "active", "paused", "ended").Default("draft").StructTag(`json:"status"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsRechargeCampaign) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("code").Unique(),
		index.Fields("status", "starts_at", "ends_at"),
	}
}
