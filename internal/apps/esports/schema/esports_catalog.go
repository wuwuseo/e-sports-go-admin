package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	entschema "entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsProductCategory groups sellable SKUs by game or business line.
type EsportsProductCategory struct{ ent.Schema }

func (EsportsProductCategory) Fields() []ent.Field {
	return []ent.Field{
		field.String("code").NotEmpty().MaxLen(80).Immutable().StructTag(`json:"code"`),
		field.String("name").NotEmpty().MaxLen(120).StructTag(`json:"name"`),
		field.String("game").MaxLen(120).Default("").StructTag(`json:"game"`),
		field.Int("sort_order").Default(0).StructTag(`json:"sort_order"`),
		field.Enum("status").Values("enabled", "disabled").Default("enabled").StructTag(`json:"status"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsProductCategory) Indexes() []ent.Index {
	return []ent.Index{index.Fields("code").Unique(), index.Fields("status", "sort_order")}
}

// EsportsSku is a fixed-price first-release service unit.
type EsportsSku struct{ ent.Schema }

func (EsportsSku) Annotations() []entschema.Annotation {
	return []entschema.Annotation{entsql.Annotation{Table: "esports_products"}}
}

func (EsportsSku) Fields() []ent.Field {
	return []ent.Field{
		field.String("code").NotEmpty().MaxLen(80).Immutable().StructTag(`json:"code"`),
		field.String("title").NotEmpty().MaxLen(160).StructTag(`json:"title"`),
		// service_type 枚举仅在末尾追加 boosting 预留值（§3.1-04）：代练首版未开放，
		// 业务侧须对 boosting 显式返回「代练首版未开放」参数错误；既有取值一个都不删除。
		field.Enum("service_type").Values("play", "teaching", "entertainment", "boosting").StructTag(`json:"service_type"`),
		field.Int("category_id").Optional().Nillable().StructTag(`json:"category_id"`),
		field.String("description").MaxLen(500).Default("").StructTag(`json:"description"`),
		field.Int64("unit_price_cents").Positive().StructTag(`json:"unit_price_cents"`),
		// 人工议价的每份最低价；会员权益折扣另行计算。旧客户端省略时服务层回落到展示价。
		field.Int64("min_unit_price_cents").Default(0).StructTag(`json:"min_unit_price_cents"`),
		// 单笔人工调整绝对值超过展示价小计的该比例时，须异人审批。
		field.Int("adjustment_approval_bp").Default(2000).StructTag(`json:"adjustment_approval_bp"`),
		field.Int64("service_units").Positive().StructTag(`json:"service_units"`),
		field.String("service_unit_name").NotEmpty().MaxLen(32).StructTag(`json:"service_unit_name"`),
		// 每席承诺服务量（§15.3-01 / §15.5-01）：多人 SKU（required_seats>1）必须 >0，
		// 下单校验 promised_service_units = per_seat_service_units × required_seats；0=未配置（下单报参数错误）。
		field.Int64("per_seat_service_units").Default(0).StructTag(`json:"-"`),
		// SKU 成本（分，§15.8-03）：可选，仅财务角色只读可见，供订单毛利口径；
		// 未填写为 NULL（历史 SKU 不回填），读取方按 nil 处理。
		field.Int64("cost_cents").Optional().Nillable().StructTag(`json:"cost_cents"`),
		// 单次服务的标准时长（分钟，§5.5）：用于预约时段重叠与服务开始自然日的容量判定；0 表示未配置，
		// 此时容量判定回落到「未结束即占位」的保守口径。
		field.Int("service_duration_minutes").Default(0).StructTag(`json:"service_duration_minutes"`),
		field.Int("required_seats").Positive().Default(1).StructTag(`json:"-"`),
		// 所需技能等级（§5.5）：陪玩等级不得低于该值（novice 最低 = 无门槛）。
		field.Enum("required_level").Values("novice", "skilled", "senior", "mentor").Default("novice").StructTag(`json:"required_level"`),
		// 结单要求（§15.3）：引用生效结单规则的 code；下单时随 SKU 快照，结单按该规则的条件与审批流执行。
		// 为空表示按 游戏分区 / 全局 规则逐级回落。
		field.String("close_rule_code").MaxLen(48).Default("").StructTag(`json:"close_rule_code"`),
		field.Int64("quota_total").Default(-1).StructTag(`json:"quota_total"`), // -1 means unlimited; quota counts purchased SKU units.
		field.Int64("quota_reserved").Default(0).StructTag(`json:"quota_reserved"`),
		field.Int64("quota_sold").Default(0).StructTag(`json:"quota_sold"`),
		field.Int("sort_order").Default(0).StructTag(`json:"sort_order"`),
		field.Enum("status").Values("draft", "on_sale", "off_sale").Default("draft").StructTag(`json:"status"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsSku) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("code").Unique(),
		index.Fields("status"),
		index.Fields("category_id", "sort_order"),
	}
}

// EsportsQuotaHold separates temporary and sold quantities from the order count.
type EsportsQuotaHold struct{ ent.Schema }

func (EsportsQuotaHold) Fields() []ent.Field {
	return []ent.Field{
		field.Int("order_id").Positive().StructTag(`json:"order_id"`),
		field.Int("sku_id").Positive().StorageKey("product_id").StructTag(`json:"product_id"`),
		field.Int64("quantity").Positive().StructTag(`json:"quantity"`),
		field.Enum("status").Values("reserved", "sold", "released", "returned").StructTag(`json:"status"`),
		field.Time("expires_at").StructTag(`json:"expires_at"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsQuotaHold) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("order_id").Unique(),
		index.Fields("sku_id", "status", "expires_at"),
	}
}
