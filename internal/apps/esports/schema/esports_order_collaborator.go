package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsOrderCollaborator 是订单的「授权协作」关系：订单负责客服可把订单授权给其他客服
// 查看（功能列表口径为「查看负责或获授权协作的订单」）。
//
// 协作只扩大**读取**边界，不代表转移负责关系：订单的 responsible_admin_id 不变，
// 结单、退款、派单等写操作仍只允许负责人或超级管理员执行。
// 移除协作文档后立即失去访问权（每次访问都按当前文档判定，不做缓存）。
type EsportsOrderCollaborator struct{ ent.Schema }

func (EsportsOrderCollaborator) Fields() []ent.Field {
	return []ent.Field{
		field.Int("order_id").Positive().StructTag(`json:"order_id"`),
		// 被授权查看该订单的客服。
		field.Int("admin_id").Positive().StructTag(`json:"admin_id"`),
		// 授予人（订单负责人或超级管理员）；历史数据可能为空。
		field.Int("granted_by_admin_id").Optional().Nillable().StructTag(`json:"granted_by_admin_id"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

func (EsportsOrderCollaborator) Edges() []ent.Edge {
	return []ent.Edge{
		// 复用 order_id 外键列，避免 Ent 另建隐式关联列。
		edge.From("order", EsportsOrder.Type).Ref("collaborators").Field("order_id").Unique().Required(),
	}
}

func (EsportsOrderCollaborator) Indexes() []ent.Index {
	return []ent.Index{
		// 同一客服对同一订单只允许一条协作文档；重复授予按幂等处理。
		index.Fields("order_id", "admin_id").Unique(),
		index.Fields("admin_id"),
	}
}
