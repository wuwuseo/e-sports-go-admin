package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsCsPresence 是客服（管理员）在线状态（§1.1-08）：派单系统的「客服不在线」此前无法判定，
// 现按客服维度登记在线状态与最近操作时间，供「客服离线 / 订单积压自动转池」任务与排班看板使用。
//
// 口径：
//   - presence=online 由客服端登录 / 操作打点维持，busy 表示已知忙碌（仍计入在线，但不接新单转池判定）；
//   - last_active_at 是最近一次操作时间，任务据它与配置的离线阈值判定自动离线；
//   - auto_offline_at 仅由任务写入，用于区分「人工下线」与「超时自动离线」；
//   - 与陪玩的 EsportsPlayer.presence 分开：客服是 admin 身份，不复用陪玩档案。
//
// 建议表名：esports_cs_presences。在线打点 / 列表路由与 PermAPI 由主控补。
type EsportsCsPresence struct{ ent.Schema }

func (EsportsCsPresence) Fields() []ent.Field {
	return []ent.Field{
		field.Int("admin_id").Positive().StructTag(`json:"admin_id"`),
		field.Enum("presence").Values("online", "busy", "offline").Default("offline").StructTag(`json:"presence"`),
		// 最近一次操作时间（心跳 / 页面操作打点）；空表示从未打点（存量客服默认不判离线）。
		field.Time("last_active_at").Optional().Nillable().StructTag(`json:"last_active_at"`),
		field.Time("last_online_at").Optional().Nillable().StructTag(`json:"last_online_at"`),
		field.Time("last_offline_at").Optional().Nillable().StructTag(`json:"last_offline_at"`),
		// 由超时任务判定离线的时刻；人工下线不写该字段。
		field.Time("auto_offline_at").Optional().Nillable().StructTag(`json:"auto_offline_at"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsCsPresence) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("admin_id").Unique(),
		index.Fields("presence", "last_active_at"),
	}
}
