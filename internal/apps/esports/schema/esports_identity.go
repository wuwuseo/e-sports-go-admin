package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsCustomer is the business profile. Login identities are verified separately.
type EsportsCustomer struct{ ent.Schema }

func (EsportsCustomer) Fields() []ent.Field {
	return []ent.Field{
		field.Int("user_id").Optional().Nillable().StructTag(`json:"user_id"`),
		field.Int("owner_admin_id").Optional().Nillable().StructTag(`json:"owner_admin_id"`),
		field.String("display_name").MaxLen(120).StructTag(`json:"display_name"`),
		field.Enum("age_status").Values("unknown", "adult", "minor").Default("unknown").StructTag(`json:"age_status"`),
		field.Bool("self_order_enabled").Default(false).StructTag(`json:"self_order_enabled"`),
		// 客服端客户档案（§8.3）：手机号、消费偏好、客户标签、建档来源、跟进状态与最近联系时间。
		field.String("phone").MaxLen(32).Default("").StructTag(`json:"phone"`),
		// 生日（月-日，如 03-15），用于生日礼遇待办（§10.4）；空表示未填。
		field.String("birthday").MaxLen(5).Default("").StructTag(`json:"birthday"`),
		field.String("consumption_preference").MaxLen(255).Default("").StructTag(`json:"consumption_preference"`),
		field.JSON("tags", []string{}).Optional().StructTag(`json:"tags"`),
		field.Enum("source").Values("manual", "wecom", "self", "import").Default("manual").StructTag(`json:"source"`),
		field.Enum("follow_status").Values("new", "following", "pending", "dormant", "closed").Default("new").StructTag(`json:"follow_status"`),
		// 风险标记（§13.7）：normal 正常 / watch 疑似滥用复核（可自动标记，**不自动处罚**）/
		// restricted 限制自助下单（仅人工设置）。自助下单入口按该字段拦截。
		field.Enum("risk_status").Values("normal", "watch", "restricted").Default("normal").StructTag(`json:"risk_status"`),
		field.String("risk_reason").MaxLen(255).Default("").StructTag(`json:"risk_reason"`),
		field.Time("risk_marked_at").Optional().Nillable().StructTag(`json:"risk_marked_at"`),
		field.Time("last_contact_at").Optional().Nillable().StructTag(`json:"last_contact_at"`),
		// 企微侧边栏自动建档（§8.4）：以「企业主体 + 外部联系人标识」组合幂等。
		// 非企微来源两字段为空（SQL NULL），NULL 在唯一索引中互不冲突，故手建客户不受影响。
		field.String("wecom_corp_id").MaxLen(120).Optional().Nillable().StructTag(`json:"wecom_corp_id"`),
		field.String("wecom_external_id").MaxLen(255).Optional().Nillable().StructTag(`json:"wecom_external_id"`),
		// 会员等级（§10）：成长值不落库，由 EsportsGrowthLedger 求和得出；当前等级由重算或手动调级写入。
		// member_level_code 为空表示未定级（未配置等级或客户无成长值）。
		field.String("member_level_code").MaxLen(32).Default("").StructTag(`json:"member_level_code"`),
		// 应降等级与等级保护期到期时间：成长值跌破门槛时先挂起，保护期到期由定时任务降级（§10.3）。
		field.String("member_pending_level_code").MaxLen(32).Default("").StructTag(`json:"member_pending_level_code"`),
		field.Time("member_downgrade_after").Optional().Nillable().StructTag(`json:"member_downgrade_after"`),
		field.Time("member_level_changed_at").Optional().Nillable().StructTag(`json:"member_level_changed_at"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsCustomer) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id").Unique(),
		index.Fields("owner_admin_id"),
		// 企微自动建档幂等：企业主体 + 外部联系人标识唯一（空值 NULL 不参与冲突）。
		index.Fields("wecom_corp_id", "wecom_external_id").Unique(),
	}
}

// EsportsPlayer is deliberately separate from User so a verified person can also be a customer.
type EsportsPlayer struct{ ent.Schema }

func (EsportsPlayer) Fields() []ent.Field {
	return []ent.Field{
		field.Int("user_id").Positive().StructTag(`json:"user_id"`),
		field.String("display_name").MaxLen(120).StructTag(`json:"display_name"`),
		field.Enum("qualification").Values("pending", "qualified", "suspended").Default("pending").StructTag(`json:"qualification"`),
		field.Enum("presence").Values("offline", "online", "protected_abnormal").Default("offline").StructTag(`json:"presence"`),
		// 考核等级（§7.3）：新手 / 熟练 / 资深 / 导师，由考核结论写入，与接单权限联动。
		field.Enum("level").Values("novice", "skilled", "senior", "mentor").Default("novice").StructTag(`json:"level"`),
		field.Time("level_appraised_at").Optional().Nillable().StructTag(`json:"level_appraised_at"`),
		// 接单上限（§5.5 / §6.4 可配置项）：0 = 不限。
		// daily_limit 每日可履约席位数（按接单自然日）；concurrent_limit 同一时段并发可履约席位数。
		field.Int("daily_limit").Default(0).StructTag(`json:"daily_limit"`),
		field.Int("concurrent_limit").Default(0).StructTag(`json:"concurrent_limit"`),
		// 接单游戏分区（§5.5 / §6.4）：空表示不限；否则只接这些游戏分区的订单。
		field.JSON("games", []string{}).Optional().StructTag(`json:"games"`),
		// 心跳时戳（§6.4）：陪玩端保活打点；上线时也会写入基线，
		// 超时任务据此把长时间无心跳的在线陪玩自动离线，在途订单则转 protected_abnormal 并提醒客服。
		field.Time("last_heartbeat_at").Optional().Nillable().StructTag(`json:"last_heartbeat_at"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsPlayer) Indexes() []ent.Index {
	return []ent.Index{index.Fields("user_id").Unique(), index.Fields("qualification", "presence")}
}

// EsportsIdentityBinding records verified links without merging customer funds on a name or phone match.
type EsportsIdentityBinding struct{ ent.Schema }

func (EsportsIdentityBinding) Fields() []ent.Field {
	return []ent.Field{
		field.Int("customer_id").Positive().StructTag(`json:"customer_id"`),
		field.String("provider").MaxLen(40).StructTag(`json:"provider"`),
		field.String("subject_scope").MaxLen(120).Default("").StructTag(`json:"subject_scope"`),
		field.String("external_subject").MaxLen(255).StructTag(`json:"external_subject"`),
		field.String("verification_method").MaxLen(80).StructTag(`json:"verification_method"`),
		field.Enum("status").Values("active", "revoked").Default("active").StructTag(`json:"status"`),
		field.Time("verified_at").StructTag(`json:"verified_at"`),
		field.Time("revoked_at").Optional().Nillable().StructTag(`json:"revoked_at"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

func (EsportsIdentityBinding) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("provider", "subject_scope", "external_subject").Unique(),
		index.Fields("customer_id", "status"),
	}
}

// EsportsPlayerIdentity 是陪玩实名认证记录（§6.1）：陪玩提交真实姓名与身份证号后进入
// pending_verification，由管理员审核后置 verified / rejected；实名通过前陪玩 qualification
// 不得为 qualified（由 player_identity.go 把关）。身份证号只存脱敏值，原始证件号不落库。
// ponytail: 该实体由主代理统一 go generate 后生效；生成前 player_identity.go 暂用
// EsportsIdentityBinding 承载认证状态（见该文件注释），生成后应切换到此实体。
type EsportsPlayerIdentity struct{ ent.Schema }

func (EsportsPlayerIdentity) Fields() []ent.Field {
	return []ent.Field{
		field.Int("player_id").Positive().StructTag(`json:"player_id"`),
		field.String("real_name").NotEmpty().MaxLen(120).StructTag(`json:"real_name"`),
		// 身份证号脱敏存储（保留前 6 后 4），原始证件号不落库。
		field.String("id_number_masked").NotEmpty().MaxLen(32).StructTag(`json:"id_number_masked"`),
		field.Enum("status").Values("pending_verification", "verified", "rejected").
			Default("pending_verification").StructTag(`json:"status"`),
		// requested_by 提交人：陪玩本人自助提交为 0，管理端代提为 adminID。
		field.Int("requested_by").Default(0).StructTag(`json:"requested_by"`),
		field.Int("reviewed_by_admin_id").Optional().Nillable().StructTag(`json:"reviewed_by_admin_id"`),
		field.String("reject_reason").MaxLen(255).Default("").StructTag(`json:"reject_reason"`),
		field.Time("reviewed_at").Optional().Nillable().StructTag(`json:"reviewed_at"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

func (EsportsPlayerIdentity) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("player_id", "status").Unique(),
		index.Fields("status", "created_at"),
	}
}
