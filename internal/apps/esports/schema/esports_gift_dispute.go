package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsGiftDispute 是礼物争议单（§9.5）。礼物退款走「客户申请 → 客服审核 → 执行」闭环，
// 支持部分退款：审核确认额不得超过申请额与礼物可退余额，执行时按比例冲回礼物分成、客服提成与成长值。
// 状态机：pending_review --approve--> approved --execute--> completed；pending_review --reject--> rejected；
// 非终态 --withdraw--> withdrawn。同一礼物订单同一时刻只允许一张进行中争议单（服务层在事务内查重）。
type EsportsGiftDispute struct{ ent.Schema }

func (EsportsGiftDispute) Fields() []ent.Field {
	return []ent.Field{
		field.String("dispute_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"dispute_no"`),
		field.Int("gift_order_id").Positive().StructTag(`json:"gift_order_id"`),
		field.String("gift_no").MaxLen(64).Default("").StructTag(`json:"gift_no"`),
		field.Int("customer_id").Positive().StructTag(`json:"customer_id"`),
		// 业绩归属客服快照（客户建档归属），用于争议列表归属过滤。
		field.Int("owner_admin_id").Optional().Nillable().StructTag(`json:"owner_admin_id"`),
		// 客服代客户建单时的申请人；客户自助申请为空。
		field.Int("created_by_admin_id").Optional().Nillable().StructTag(`json:"created_by_admin_id"`),
		// 争议类型：未收到 / 送错对象 / 礼物与说明不符 / 非本人授权送礼 / 其他。
		field.Enum("kind").Values("not_received", "wrong_gift", "quality", "unauthorized", "other").
			StructTag(`json:"kind"`),
		field.Enum("channel").Values("customer", "admin").Default("customer").StructTag(`json:"channel"`),
		field.String("demand").NotEmpty().MaxLen(500).StructTag(`json:"demand"`),
		field.String("evidence_ref").MaxLen(255).Default("").StructTag(`json:"evidence_ref"`),
		// 客户申请退款额与客服审核确认额（分）；确认额不得超过申请额与可退余额。
		field.Int64("requested_refund_cents").Positive().StructTag(`json:"requested_refund_cents"`),
		field.Int64("approved_refund_cents").Default(0).StructTag(`json:"approved_refund_cents"`),
		field.Enum("status").Values("pending_review", "approved", "rejected", "completed", "withdrawn").
			Default("pending_review").StructTag(`json:"status"`),
		field.String("resolution").MaxLen(255).Default("").StructTag(`json:"resolution"`),
		field.Int("reviewed_by_admin_id").Optional().Nillable().StructTag(`json:"reviewed_by_admin_id"`),
		field.Time("reviewed_at").Optional().Nillable().StructTag(`json:"reviewed_at"`),
		field.Int("executed_by_admin_id").Optional().Nillable().StructTag(`json:"executed_by_admin_id"`),
		field.Time("executed_at").Optional().Nillable().StructTag(`json:"executed_at"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsGiftDispute) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("dispute_no").Unique(),
		index.Fields("gift_order_id", "status"),
		index.Fields("customer_id", "status"),
	}
}
