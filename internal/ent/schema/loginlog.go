package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// LoginLog holds the schema definition for the LoginLog entity.
type LoginLog struct {
	ent.Schema
}

// Fields of the LoginLog.
func (LoginLog) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").
			Immutable().
			Unique().
			StructTag(`json:"id,omitempty" autoincr:"true"`),
		field.String("username").
			MaxLen(255).
			NotEmpty().
			StructTag(`json:"username"`).
			Comment("登录账号"),
		field.Int("user_id").
			Default(0).
			StructTag(`json:"user_id"`).
			Comment("登录用户ID（未知登录账号时为0）"),
		field.Enum("login_type").
			Values("user", "admin").
			StructTag(`json:"login_type"`).
			Comment("登录类型：前台用户/后台管理员"),
		field.Enum("status").
			Values("success", "fail").
			StructTag(`json:"status"`).
			Comment("登录状态"),
		field.String("message").
			Optional().
			MaxLen(255).
			StructTag(`json:"message,omitempty"`).
			Comment("提示信息"),
		field.String("ip").
			MaxLen(50).
			StructTag(`json:"ip"`).
			Comment("客户端IP"),
		field.String("user_agent").
			Optional().
			MaxLen(1024).
			StructTag(`json:"user_agent,omitempty"`).
			Comment("用户代理"),
		field.String("browser").
			Optional().
			MaxLen(100).
			StructTag(`json:"browser,omitempty"`).
			Comment("浏览器"),
		field.String("os").
			Optional().
			MaxLen(100).
			StructTag(`json:"os,omitempty"`).
			Comment("操作系统"),
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			StructTag(`json:"created_at"`).
			Comment("创建时间"),
	}
}

// Indexes of the LoginLog.
func (LoginLog) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("username"),
		index.Fields("user_id"),
		index.Fields("login_type"),
		index.Fields("status"),
		index.Fields("created_at"),
	}
}
