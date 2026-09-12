package schema

import (
	"regexp"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"entgo.io/ent/schema/mixin"
)

var (
	emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)
	phoneRegex = regexp.MustCompile(`^[0-9]+$`)
)

// User holds the schema definition for the User entity.
type User struct {
	ent.Schema
}

// Fields of the User.
func (User) Fields() []ent.Field {
	return []ent.Field{
		field.Int("id").
			Immutable().
			Unique().
			StructTag(`json:"id,omitempty" autoincr:"true"`),
		field.String("email").
			Unique().
			MaxLen(255).
			Match(emailRegex).
			Comment("用户邮箱"),
		field.String("password").
			MaxLen(255).
			Sensitive().
			Optional().
			Comment("用户密码"),
		field.String("name").
			Optional().
			MaxLen(255).
			Comment("用户姓名"),
		field.String("username").
			MaxLen(255).
			Optional().
			Comment("用户名"),
		field.String("nickname").
			MaxLen(255).
			Optional().
			Comment("用户昵称"),
		field.String("uid").
			MaxLen(255).
			Optional().
			Comment("用户唯一标识"),
		field.String("password_salt").
			MaxLen(255).
			Sensitive().
			Optional().
			Comment("密码盐值"),
		field.String("avatar").
			Optional().
			MaxLen(1024).
			Comment("用户头像"),
		field.String("phone").
			Optional().
			MaxLen(20).
			Match(phoneRegex).
			Comment("手机号码"),
		field.Enum("status").
			Values("active", "inactive", "suspended").
			Default("inactive").
			Comment("用户状态"),
		field.Time("last_login").
			Optional().
			Comment("最后登录时间"),
		field.String("last_ip").
			Optional().
			MaxLen(50).
			Comment("最后登录IP"),
		field.Int("token_version").
			Default(0).
			StructTag(`json:"-"`).
			Comment("密码变更时递增，吊销此前签发的令牌"),
	}
}

// Edges of the User.
func (User) Edges() []ent.Edge {
	return nil
}

// Indexes of the User.
func (User) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("email"),
		index.Fields("phone"),
		index.Fields("username"),
	}
}

func (User) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixin.Time{},
	}
}
