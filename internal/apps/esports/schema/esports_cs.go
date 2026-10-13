package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsCustomerFollowUp 是客服对客户的跟进记录（§8.3）。只增不改：离职交接时接手人靠它快速了解客户。
// 记录写入门槛为「归属客服本人或超级管理员」，明细权限在服务层校验。
type EsportsCustomerFollowUp struct{ ent.Schema }

func (EsportsCustomerFollowUp) Hooks() []ent.Hook { return appendOnlyHooks() }

func (EsportsCustomerFollowUp) Fields() []ent.Field {
	return []ent.Field{
		field.Int("customer_id").Positive().StructTag(`json:"customer_id"`),
		// 记录人（归属客服）。历史归属不变，转交后旧记录仍指向原客服。
		field.Int("admin_id").Positive().StructTag(`json:"admin_id"`),
		// 沟通渠道：三个客服入口（PC / 移动 / 企微侧边栏）与电话、其他。
		field.Enum("channel").Values("pc", "mobile", "wecom", "phone", "other").Default("pc").StructTag(`json:"channel"`),
		field.String("summary").NotEmpty().MaxLen(500).StructTag(`json:"summary"`),
		// 下次跟进时间：空表示无需回访；到达后计入客服待办提醒。
		field.Time("next_follow_at").Optional().Nillable().StructTag(`json:"next_follow_at"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

func (EsportsCustomerFollowUp) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("customer_id", "created_at"),
		// 待办提醒按「下次跟进时间」扫描。
		index.Fields("admin_id", "next_follow_at"),
	}
}

// EsportsScript 是客服话术库（§8.2）。三个入口共用；支持分类与排序，禁用后不再下发。
type EsportsScript struct{ ent.Schema }

func (EsportsScript) Fields() []ent.Field {
	return []ent.Field{
		field.String("title").NotEmpty().MaxLen(120).StructTag(`json:"title"`),
		field.String("category").MaxLen(60).Default("").StructTag(`json:"category"`),
		field.String("content").NotEmpty().MaxLen(2000).StructTag(`json:"content"`),
		field.Int("sort_order").Default(0).StructTag(`json:"sort_order"`),
		field.Enum("status").Values("enabled", "disabled").Default("enabled").StructTag(`json:"status"`),
		field.Int("created_by").Optional().Nillable().StructTag(`json:"created_by"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsScript) Indexes() []ent.Index {
	return []ent.Index{index.Fields("status", "category", "sort_order")}
}
