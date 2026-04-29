package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// OperationLog holds the schema definition for the OperationLog entity.
type OperationLog struct {
	ent.Schema
}

// Fields of the OperationLog.
func (OperationLog) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").
			Immutable().
			Unique().
			StructTag(`json:"id,omitempty" autoincr:"true"`),
		field.Int("user_id").
			StructTag(`json:"user_id"`).
			Comment("操作用户ID"),
		field.String("username").
			MaxLen(255).
			NotEmpty().
			StructTag(`json:"username"`).
			Comment("用户名"),
		field.String("module").
			MaxLen(255).
			NotEmpty().
			StructTag(`json:"module"`).
			Comment("模块名"),
		field.String("action").
			MaxLen(255).
			NotEmpty().
			StructTag(`json:"action"`).
			Comment("操作"),
		field.String("description").
			Optional().
			MaxLen(1024).
			StructTag(`json:"description,omitempty"`).
			Comment("操作描述"),
		field.String("method").
			MaxLen(10).
			NotEmpty().
			StructTag(`json:"method"`).
			Comment("HTTP方法"),
		field.String("path").
			MaxLen(1024).
			NotEmpty().
			StructTag(`json:"path"`).
			Comment("请求路径"),
		field.String("ip").
			MaxLen(50).
			StructTag(`json:"ip"`).
			Comment("客户端IP"),
		field.String("user_agent").
			Optional().
			MaxLen(1024).
			StructTag(`json:"user_agent,omitempty"`).
			Comment("用户代理"),
		field.String("request_body").
			Optional().
			Sensitive().
			Comment("请求体"),
		field.Int("status_code").
			StructTag(`json:"status_code"`).
			Comment("响应状态码"),
		field.Int64("duration_ms").
			StructTag(`json:"duration_ms"`).
			Comment("耗时毫秒"),
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			StructTag(`json:"created_at"`).
			Comment("创建时间"),
	}
}

// Indexes of the OperationLog.
func (OperationLog) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id"),
		index.Fields("module"),
		index.Fields("action"),
		index.Fields("created_at"),
	}
}
