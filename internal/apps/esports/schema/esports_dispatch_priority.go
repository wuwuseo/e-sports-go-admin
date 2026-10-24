package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsDispatchPriorityGrant 是「优先派单权」名单条目。它不是独占权：只对人工指派与
// 派单排序加权，仍受在线、接单上限、游戏分区等硬性条件约束，且**不影响抢单大厅**（先到先得）。
// 授予维度是「游戏分区 + 周期」：同一游戏同一期的名额有限，层级仅次于「客户指定陪玩」。
type EsportsDispatchPriorityGrant struct{ ent.Schema }

func (EsportsDispatchPriorityGrant) Fields() []ent.Field {
	return []ent.Field{
		// 游戏分区（与商品分类的 game 同口径）。
		field.String("game").NotEmpty().MaxLen(64).StructTag(`json:"game"`),
		field.Int("player_id").Positive().StructTag(`json:"player_id"`),
		// 周期：日 / 周 / 半月 / 月，决定名单有效期与重算切换点。
		field.Enum("period").Values("daily", "weekly", "biweekly", "monthly").Default("weekly").
			StructTag(`json:"period"`),
		field.Time("period_start").StructTag(`json:"period_start"`),
		field.Time("period_end").StructTag(`json:"period_end"`),
		// 名单内排名，越小越靠前；用于同持权者之间的排序加权。0 表示未排名。
		field.Int("rank").Default(0).StructTag(`json:"rank"`),
		// 获得方式：排名制 / 积分制 / 达标制 / 轮换制 / 人工指定。
		field.Enum("source").Values("ranking", "points", "threshold", "rotation", "manual").
			Default("manual").StructTag(`json:"source"`),
		// pending 为切换点前预生成的下一期名单；已过期的周期由重算任务转 expired。
		field.Enum("status").Values("pending", "active", "revoked", "expired").Default("pending").
			StructTag(`json:"status"`),
		field.Int("granted_by_admin_id").Optional().Nillable().StructTag(`json:"granted_by_admin_id"`),
		field.String("note").MaxLen(255).Default("").StructTag(`json:"note"`),
		field.String("revoke_reason").MaxLen(255).Default("").StructTag(`json:"revoke_reason"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsDispatchPriorityGrant) Indexes() []ent.Index {
	return []ent.Index{
		// 同一游戏同一期同一陪玩只允许一条名单条目。
		index.Fields("game", "period_start", "player_id").Unique(),
		index.Fields("game", "status"),
		index.Fields("status", "period_start"),
	}
}

// EsportsDispatchPriorityNotice 是「优先通知」明细（业务口径）：订单进入待派单时，
// 按游戏分区名单逐人建一条通知记录。窗口内只有被通知的人能看到并接单；全部拒绝或窗口超时后
// 订单释放到抢单大厅。(order_id, player_id) 唯一，保证重复开窗不重复通知。
type EsportsDispatchPriorityNotice struct{ ent.Schema }

func (EsportsDispatchPriorityNotice) Fields() []ent.Field {
	return []ent.Field{
		field.Int("order_id").Positive().StructTag(`json:"order_id"`),
		field.Int("player_id").Positive().StructTag(`json:"player_id"`),
		// 开窗时快照的游戏分区。
		field.String("game").MaxLen(64).Default("").StructTag(`json:"game"`),
		field.Enum("status").Values("notified", "accepted", "declined", "expired").
			Default("notified").StructTag(`json:"status"`),
		field.Time("notified_at").StructTag(`json:"notified_at"`),
		field.Time("responded_at").Optional().Nillable().StructTag(`json:"responded_at"`),
		field.String("decline_reason").MaxLen(255).Default("").StructTag(`json:"decline_reason"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsDispatchPriorityNotice) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("order_id", "player_id").Unique(),
		index.Fields("player_id", "status"),
		index.Fields("status", "notified_at"),
	}
}
