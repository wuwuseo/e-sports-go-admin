package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsDispatchSeat 是订单派单席位。一个订单按 required_seats 生成对应条数席位
// （seat_index 从 1 开始），逐条记录从邀请、占位到释放/撤回的全过程。
// (order_id, seat_index) 唯一，保证席位生成的幂等。
type EsportsDispatchSeat struct{ ent.Schema }

func (EsportsDispatchSeat) Fields() []ent.Field {
	return []ent.Field{
		field.Int("order_id").Positive().StructTag(`json:"order_id"`),
		field.Int("seat_index").Positive().StructTag(`json:"seat_index"`),
		field.Int("player_id").Optional().Nillable().StructTag(`json:"player_id"`),
		// source 记录席位是通过指派、抢单、固排还是临时组队占用。
		field.Enum("source").Values("assign", "grab", "fixed_team", "temp_team").Default("assign").StructTag(`json:"source"`),
		field.Enum("status").Values("open", "invited", "accepted", "rejected", "expired", "revoked", "released").Default("open").StructTag(`json:"status"`),
		field.Int("invited_by_admin_id").Optional().Nillable().StructTag(`json:"invited_by_admin_id"`),
		field.Time("expires_at").Optional().Nillable().StructTag(`json:"expires_at"`),
		field.Time("responded_at").Optional().Nillable().StructTag(`json:"responded_at"`),
		// 接受或抢单时锁定该陪玩的分成规则；locked_at 非空且 rule_id 为空表示当时无规则。
		field.Int("commission_rule_id").Optional().Nillable().StructTag(`json:"commission_rule_id"`),
		field.Time("commission_rule_locked_at").Optional().Nillable().StructTag(`json:"commission_rule_locked_at"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsDispatchSeat) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("order_id", "seat_index").Unique(),
		index.Fields("player_id", "status"),
		index.Fields("status", "expires_at"),
	}
}
