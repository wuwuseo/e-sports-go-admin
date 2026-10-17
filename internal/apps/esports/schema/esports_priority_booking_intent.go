package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsPriorityBookingIntent 是会员优先预约意向（§10.4）。启用 priority_booking 等级的客户
// 在自助端提交「陪玩 + 期望时段 + 备注」的预约意向；客服工作台据此出现「会员优先预约」待办，
// 并按会员等级置顶处理。本实体只增不改，处理结果通过 status 落 pending / processed / cancelled。
// 与正式订单解耦：预约意向不含 SKU 与金额，客服确认后再走既有报单链路，避免污染订单 / 排单 / 报表。
type EsportsPriorityBookingIntent struct{ ent.Schema }

func (EsportsPriorityBookingIntent) Fields() []ent.Field {
	return []ent.Field{
		field.Int("customer_id").Positive().StructTag(`json:"customer_id"`),
		// 指定陪玩（§10.4）：客户选择的热门 / 指定陪玩；player_name 为提交时快照，供待办展示。
		field.Int("player_id").Positive().StructTag(`json:"player_id"`),
		field.String("player_name").MaxLen(120).Default("").StructTag(`json:"player_name"`),
		// 期望时段（§10.4）：客户期望的开始时间；提交时校验为未来时间。
		field.Time("requested_start_at").Optional().Nillable().StructTag(`json:"requested_start_at"`),
		// 备注（§10.4）：客户自由填写，最多 500 字。
		field.String("note").MaxLen(500).Default("").StructTag(`json:"note"`),
		// 处理状态：pending 待处理 / processed 已处理 / cancelled 已取消。
		field.Enum("status").Values("pending", "processed", "cancelled").Default("pending").StructTag(`json:"status"`),
		field.Int("processed_by_admin_id").Optional().Nillable().StructTag(`json:"processed_by_admin_id"`),
		field.Time("processed_at").Optional().Nillable().StructTag(`json:"processed_at"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsPriorityBookingIntent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("customer_id", "created_at"),
		index.Fields("status", "created_at"),
	}
}
