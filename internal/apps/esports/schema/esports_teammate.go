package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsTeammate 是陪玩之间的「固排」关系：按游戏分区互加形成的固定队友关系，
// 双方确认后建立，任一方可解除。关系无方向，存为规范化对（player_id < teammate_player_id），
// 仅用 initiator_player_id 记录是哪一方发起的邀请。组队派单时主陪玩可从固排列表邀请队友。
type EsportsTeammate struct{ ent.Schema }

func (EsportsTeammate) Fields() []ent.Field {
	return []ent.Field{
		// 规范化后较小的陪玩档案 ID（关系无方向）。
		field.Int("player_id").Positive().StructTag(`json:"player_id"`),
		// 规范化后较大的陪玩档案 ID。
		field.Int("teammate_player_id").Positive().StructTag(`json:"teammate_player_id"`),
		// 发起邀请的一方（用于待确认列表里区分「谁邀请我」）。
		field.Int("initiator_player_id").Positive().StructTag(`json:"initiator_player_id"`),
		// 游戏分区（与商品分类的 game 同口径）。
		field.String("game").NotEmpty().MaxLen(64).StructTag(`json:"game"`),
		// pending 待确认 / confirmed 已建立 / dissolved 已解除（含拒绝）。
		field.Enum("status").Values("pending", "confirmed", "dissolved").Default("pending").
			StructTag(`json:"status"`),
		// 幂等键：陪玩端重复发起只生成一条邀请。
		field.String("idem_key").NotEmpty().MaxLen(120).StructTag(`json:"idem_key"`),
		field.Time("confirmed_at").Optional().Nillable().StructTag(`json:"confirmed_at"`),
		field.Int("dissolved_by_player_id").Optional().Nillable().StructTag(`json:"dissolved_by_player_id"`),
		field.Time("dissolved_at").Optional().Nillable().StructTag(`json:"dissolved_at"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsTeammate) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("idem_key").Unique(),
		index.Fields("player_id", "status"),
		index.Fields("teammate_player_id", "status"),
		index.Fields("status", "created_at"),
	}
}
