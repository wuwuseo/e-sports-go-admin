package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ThirdPartyUser 第三方用户信息
type ThirdPartyUser struct {
	ent.Schema
}

func (ThirdPartyUser) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").
			Immutable().
			Unique().
			StructTag(`json:"id,omitempty" autoincr:"true"`),
		field.Int("user_id").
			StructTag(`json:"user_id"`).
			Comment("关联系统用户ID"),
		field.String("platform").
			MaxLen(50).
			NotEmpty().
			StructTag(`json:"platform"`).
			Comment("平台标识：wechat/github/google/enterprise等"),
		field.String("platform_name").
			MaxLen(100).
			Optional().
			StructTag(`json:"platform_name,omitempty"`).
			Comment("平台显示名称"),
		field.String("openid").
			MaxLen(255).
			NotEmpty().
			StructTag(`json:"openid"`).
			Comment("第三方平台用户唯一标识"),
		field.String("unionid").
			MaxLen(255).
			Optional().
			StructTag(`json:"unionid,omitempty"`).
			Comment("UnionID（微信生态专用）"),
		field.String("access_token").
			MaxLen(2048).
			Sensitive().
			Optional().
			Comment("访问令牌"),
		field.String("refresh_token").
			MaxLen(2048).
			Sensitive().
			Optional().
			Comment("刷新令牌"),
		field.Time("token_expires_at").
			Optional().
			Nillable().
			StructTag(`json:"token_expires_at,omitempty"`).
			Comment("令牌过期时间"),
		field.String("nickname").
			MaxLen(255).
			Optional().
			StructTag(`json:"nickname,omitempty"`).
			Comment("第三方昵称"),
		field.String("avatar_url").
			MaxLen(1024).
			Optional().
			StructTag(`json:"avatar_url,omitempty"`).
			Comment("第三方头像URL"),
		field.JSON("extra_data", map[string]interface{}{}).
			Optional().
			StructTag(`json:"extra_data,omitempty"`).
			Comment("扩展数据（JSON格式）"),
		field.String("source").
			MaxLen(100).
			Optional().
			StructTag(`json:"source,omitempty"`).
			Comment("来源系统标识"),
		field.String("source_user_id").
			MaxLen(255).
			Optional().
			StructTag(`json:"source_user_id,omitempty"`).
			Comment("来源系统的用户ID"),
		field.String("company").
			MaxLen(255).
			Optional().
			StructTag(`json:"company,omitempty"`).
			Comment("所属公司（合作伙伴场景）"),
		field.Time("expires_at").
			Optional().
			Nillable().
			StructTag(`json:"expires_at,omitempty"`).
			Comment("合作有效期"),
		field.Enum("status").
			Values("active", "inactive").
			Default("active").
			StructTag(`json:"status"`).
			Comment("绑定状态"),
		field.Time("last_sync_at").
			Optional().
			Nillable().
			StructTag(`json:"last_sync_at,omitempty"`).
			Comment("最后同步时间"),
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			StructTag(`json:"created_at"`).
			Comment("创建时间"),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			StructTag(`json:"updated_at"`).
			Comment("更新时间"),
	}
}

func (ThirdPartyUser) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("user", User.Type).Unique().Required().Field("user_id"),
	}
}

func (ThirdPartyUser) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("platform", "openid").Unique(),
		index.Fields("user_id"),
		index.Fields("platform"),
	}
}
