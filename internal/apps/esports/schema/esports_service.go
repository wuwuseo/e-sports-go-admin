package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsServiceSegment 记录陪玩在某一订单上的一个服务段：从开始服务到结束服务的一段计时。
// 同时保留两种服务量来源——陪玩上报的 reported_units（单位同 SKU 的 service_unit_name）与
// 系统按开始/结束点击间隔扣除暂停时间记的 system_seconds。差异过大时提醒负责客服核实。
type EsportsServiceSegment struct{ ent.Schema }

func (EsportsServiceSegment) Fields() []ent.Field {
	return []ent.Field{
		field.Int("order_id").Positive().StructTag(`json:"order_id"`),
		field.Int("player_id").Positive().StructTag(`json:"player_id"`),
		field.Int("seat_index").Positive().StructTag(`json:"seat_index"`),
		field.Enum("status").Values("in_service", "paused", "finished").Default("in_service").StructTag(`json:"status"`),
		field.Time("started_at").StructTag(`json:"started_at"`),
		field.Time("paused_at").Optional().Nillable().StructTag(`json:"paused_at"`),
		field.Time("ended_at").Optional().Nillable().StructTag(`json:"ended_at"`),
		field.Int64("paused_seconds").Default(0).StructTag(`json:"paused_seconds"`),
		// reported_units 为陪玩上报的服务量，单位与订单 SKU 的 service_unit_name 一致。
		field.Int64("reported_units").Default(0).StructTag(`json:"reported_units"`),
		// reported_seconds 为陪玩填写的实际服务时长，独立于 SKU 的局数/课时等计费单位。
		field.Int64("reported_seconds").Default(0).StructTag(`json:"reported_seconds"`),
		// system_seconds 为系统按开始/结束点击间隔记录的秒数。
		field.Int64("system_seconds").Default(0).StructTag(`json:"system_seconds"`),
		// 服务段保留接单时规则快照，席位改派后原服务成员仍按原规则结算。
		field.Int("commission_rule_id").Optional().Nillable().StructTag(`json:"commission_rule_id"`),
		field.Time("commission_rule_locked_at").Optional().Nillable().StructTag(`json:"commission_rule_locked_at"`),
		field.String("evidence_ref").MaxLen(255).Default("").StructTag(`json:"evidence_ref"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsServiceSegment) Indexes() []ent.Index {
	return []ent.Index{
		// 同一陪玩在同一订单上只允许一个服务段，重复开始靠唯一索引兜底。
		index.Fields("order_id", "player_id").Unique(),
		index.Fields("player_id", "status"),
	}
}
