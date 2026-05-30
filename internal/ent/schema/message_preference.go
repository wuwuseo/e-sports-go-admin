package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"entgo.io/ent/schema/mixin"
)

// MessagePreference 用户消息偏好实体（对应表 admin_message_preferences）
type MessagePreference struct {
	ent.Schema
}

// Fields 偏好字段定义
func (MessagePreference) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").
			Immutable().
			Unique().
			StructTag(`json:"id,omitempty" autoincr:"true"`),
		field.String("user_uid").
			Unique().
			MaxLen(64).
			NotEmpty().
			StructTag(`json:"user_uid"`).
			Comment("用户UID，唯一"),
		field.Bool("desktop_notify").
			Default(true).
			StructTag(`json:"desktop_notify"`).
			Comment("是否启用桌面通知"),
		field.Bool("sound_notify").
			Default(false).
			StructTag(`json:"sound_notify"`).
			Comment("是否启用声音提醒"),
		field.Bool("subscribe_system").
			Default(true).
			StructTag(`json:"subscribe_system"`).
			Comment("是否订阅系统消息"),
		field.Bool("subscribe_business").
			Default(true).
			StructTag(`json:"subscribe_business"`).
			Comment("是否订阅业务消息"),
		field.Bool("subscribe_task").
			Default(true).
			StructTag(`json:"subscribe_task"`).
			Comment("是否订阅任务消息"),
		field.String("do_not_disturb_start").
			MaxLen(5).
			Default("").
			Optional().
			StructTag(`json:"do_not_disturb_start"`).
			Comment("免打扰开始时间 HH:mm"),
		field.String("do_not_disturb_end").
			MaxLen(5).
			Default("").
			Optional().
			StructTag(`json:"do_not_disturb_end"`).
			Comment("免打扰结束时间 HH:mm"),
	}
}

// Indexes 偏好索引
func (MessagePreference) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_uid").Unique(),
	}
}

// Mixin 偏好混入（含 created_at / updated_at）
func (MessagePreference) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.Time{},
	}
}
