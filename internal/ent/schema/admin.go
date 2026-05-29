package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"entgo.io/ent/schema/mixin"
)

// Admin 后台管理员表
type Admin struct {
	ent.Schema
}

// Fields of the Admin.
func (Admin) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").
			Immutable().
			Unique().
			StructTag(`json:"id,omitempty" autoincr:"true"`),
		field.String("email").
			Unique().
			MaxLen(255).
			Match(emailRegex).
			Comment("管理员邮箱"),
		field.String("password").
			MaxLen(255).
			Sensitive().
			NotEmpty().
			Comment("管理员密码"),
		field.String("password_salt").
			MaxLen(255).
			Sensitive().
			Optional().
			Comment("密码盐值"),
		field.String("uid").
			Unique().
			MaxLen(255).
			Comment("管理员唯一标识"),
		field.String("username").
			Unique().
			MaxLen(255).
			Comment("登录用户名"),
		field.String("nickname").
			MaxLen(255).
			Optional().
			Comment("管理员昵称"),
		field.String("name").
			MaxLen(255).
			Optional().
			Comment("真实姓名"),
		field.String("avatar").
			MaxLen(1024).
			Optional().
			Comment("头像"),
		field.String("phone").
			MaxLen(20).
			Optional().
			Comment("手机号码"),
		field.String("department").
			MaxLen(255).
			Optional().
			Comment("所属部门"),
		field.String("position").
			MaxLen(255).
			Optional().
			Comment("职位"),
		field.Enum("status").
			Values("active", "inactive", "suspended").
			Default("inactive").
			Comment("管理员状态"),
		field.Time("last_login").
			Optional().
			Comment("最后登录时间"),
		field.String("last_ip").
			MaxLen(50).
			Optional().
			Comment("最后登录IP"),
		field.Bool("is_super_admin").
			Default(false).
			Comment("是否超级管理员"),
	}
}

// Edges of the Admin.
func (Admin) Edges() []ent.Edge {
	return nil
}

// Indexes of the Admin.
func (Admin) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("email"),
		index.Fields("username"),
		index.Fields("uid"),
	}
}

func (Admin) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.Time{},
	}
}
