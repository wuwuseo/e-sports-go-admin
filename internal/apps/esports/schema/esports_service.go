package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsServiceSegment 记录陪玩在某一订单上的一个服务段：从开始服务到结束服务的一段计时。
// 同时保留两种服务量来源——陪玩上报的 reported_units（单位同 SKU 的 service_unit_name）与
// 系统按开始/结束点击间隔记的 system_seconds。两者差异核对待接入客服流程后处理。
// ponytail: 上报与系统两个来源的差异核对、服务量单位换算与"差异过大提醒客服"未实现，留待 §12 与客服流程。
type EsportsServiceSegment struct{ ent.Schema }

func (EsportsServiceSegment) Fields() []ent.Field {
	return []ent.Field{
		field.Int("order_id").Positive().StructTag(`json:"order_id"`),
		field.Int("player_id").Positive().StructTag(`json:"player_id"`),
		field.Int("seat_index").Positive().StructTag(`json:"seat_index"`),
		field.Enum("status").Values("in_service", "paused", "finished").Default("in_service").StructTag(`json:"status"`),
		field.Time("started_at").StructTag(`json:"started_at"`),
		field.Time("ended_at").Optional().Nillable().StructTag(`json:"ended_at"`),
		field.Int64("paused_seconds").Default(0).StructTag(`json:"paused_seconds"`),
		// reported_units 为陪玩上报的服务量，单位与订单 SKU 的 service_unit_name 一致。
		field.Int64("reported_units").Default(0).StructTag(`json:"reported_units"`),
		// system_seconds 为系统按开始/结束点击间隔记录的秒数。
		field.Int64("system_seconds").Default(0).StructTag(`json:"system_seconds"`),
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
