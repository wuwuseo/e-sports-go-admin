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
		// 客服端客户档案：手机号、消费偏好、客户标签、建档来源、跟进状态与最近联系时间。
		field.String("phone").MaxLen(32).Default("").StructTag(`json:"phone"`),
		// 生日（月-日，如 03-15），用于生日礼遇待办；空表示未填。
		field.String("birthday").MaxLen(5).Default("").StructTag(`json:"birthday"`),
		field.String("consumption_preference").MaxLen(255).Default("").StructTag(`json:"consumption_preference"`),
		// 客户时区与坐标：坐标用微度整数（1e-6 度）避免浮点；
		// 与陪玩的同名字段共同支撑派单排序的距离 / 时区分量。空 / 0 表示未采集。
		field.String("timezone").MaxLen(64).Default("").StructTag(`json:"timezone"`),
		field.Int("latitude_e6").Default(0).StructTag(`json:"latitude_e6"`),
		field.Int("longitude_e6").Default(0).StructTag(`json:"longitude_e6"`),
		field.JSON("tags", []string{}).Optional().StructTag(`json:"tags"`),
		field.Enum("source").Values("manual", "wecom", "self", "import").Default("manual").StructTag(`json:"source"`),
		field.Enum("follow_status").Values("new", "following", "pending", "dormant", "closed").Default("new").StructTag(`json:"follow_status"`),
		// 风险标记：normal 正常 / watch 疑似滥用复核（可自动标记，**不自动处罚**）/
		// restricted 限制自助下单（仅人工设置）。自助下单入口按该字段拦截。
		field.Enum("risk_status").Values("normal", "watch", "restricted").Default("normal").StructTag(`json:"risk_status"`),
		field.String("risk_reason").MaxLen(255).Default("").StructTag(`json:"risk_reason"`),
		field.Time("risk_marked_at").Optional().Nillable().StructTag(`json:"risk_marked_at"`),
		field.Time("last_contact_at").Optional().Nillable().StructTag(`json:"last_contact_at"`),
		// 企微侧边栏自动建档：以「企业主体 + 外部联系人标识」组合幂等。
		// 非企微来源两字段为空（SQL NULL），NULL 在唯一索引中互不冲突，故手建客户不受影响。
		field.String("wecom_corp_id").MaxLen(120).Optional().Nillable().StructTag(`json:"wecom_corp_id"`),
		field.String("wecom_external_id").MaxLen(255).Optional().Nillable().StructTag(`json:"wecom_external_id"`),
		// 会员等级：成长值不落库，由 EsportsGrowthLedger 求和得出；当前等级由重算或手动调级写入。
		// member_level_code 为空表示未定级（未配置等级或客户无成长值）。
		field.String("member_level_code").MaxLen(32).Default("").StructTag(`json:"member_level_code"`),
		// 应降等级与等级保护期到期时间：成长值跌破门槛时先挂起，保护期到期由定时任务降级。
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
		// 考核等级：由考核结论写入，与接单权限联动。取值为系统配置
		// app.esports.skill_levels 的等级编码（配置顺序即高低序），显示名随配置。
		field.String("level").MaxLen(32).Default("novice").StructTag(`json:"level"`),
		field.Time("level_appraised_at").Optional().Nillable().StructTag(`json:"level_appraised_at"`),
		// 接单上限（可配置项）：0 = 不限。
		// daily_limit 每日可履约席位数（按接单自然日）；concurrent_limit 同一时段并发可履约席位数。
		field.Int("daily_limit").Default(0).StructTag(`json:"daily_limit"`),
		field.Int("concurrent_limit").Default(0).StructTag(`json:"concurrent_limit"`),
		// 接单游戏分区：空表示不限；否则只接这些游戏分区的订单。
		field.JSON("games", []string{}).Optional().StructTag(`json:"games"`),
		// 心跳时戳：陪玩端保活打点；上线时也会写入基线，
		// 超时任务据此把长时间无心跳的在线陪玩自动离线，在途订单则转 protected_abnormal 并提醒客服。
		field.Time("last_heartbeat_at").Optional().Nillable().StructTag(`json:"last_heartbeat_at"`),
		// 期望报价：陪玩在资料页自填的期望单价（分），提交后进入待运营审核；0 表示未填写。
		field.Int64("expected_price_cents").Default(0).StructTag(`json:"expected_price_cents"`),
		// 期望报价审核状态：none 未提交 / pending 待运营审核 / approved 通过 / rejected 驳回。
		field.Enum("expected_price_status").Values("none", "pending", "approved", "rejected").
			Default("none").StructTag(`json:"expected_price_status"`),
		field.Time("expected_price_submitted_at").Optional().Nillable().StructTag(`json:"expected_price_submitted_at"`),
		field.Int("expected_price_reviewed_by_admin_id").Optional().Nillable().StructTag(`json:"expected_price_reviewed_by_admin_id"`),
		field.Time("expected_price_reviewed_at").Optional().Nillable().StructTag(`json:"expected_price_reviewed_at"`),
		field.String("expected_price_review_note").MaxLen(255).Default("").StructTag(`json:"expected_price_review_note"`),
		// 服务时段：按 UTC+8 的「当日分钟数」（0~1440）表示每日可接单窗口，须 start < end；
		// 两者都为 0 表示不限时段（默认，存量档案不受影响）。时段外不被派单。
		field.Int("service_slot_start_minute").Default(0).StructTag(`json:"service_slot_start_minute"`),
		field.Int("service_slot_end_minute").Default(0).StructTag(`json:"service_slot_end_minute"`),
		// 自动上线：开启后由陪玩端心跳 / 登录自动置为 online，无需手动上线。
		field.Bool("auto_online").Default(false).StructTag(`json:"auto_online"`),
		// 可接单价范围（分，）：0 表示该侧不限；超出范围的订单不参与派单。
		field.Int64("accept_price_min_cents").Default(0).StructTag(`json:"accept_price_min_cents"`),
		field.Int64("accept_price_max_cents").Default(0).StructTag(`json:"accept_price_max_cents"`),
		// 固排开关与固排时段：仅开关开启且处于固排时段内，才允许以固排身份被组队。
		field.Bool("fixed_team_enabled").Default(false).StructTag(`json:"fixed_team_enabled"`),
		field.Int("fixed_team_slot_start_minute").Default(0).StructTag(`json:"fixed_team_slot_start_minute"`),
		field.Int("fixed_team_slot_end_minute").Default(0).StructTag(`json:"fixed_team_slot_end_minute"`),
		// 时区与坐标：坐标用微度整数（1e-6 度）避免浮点；
		// 用于派单排序的距离 / 时区分量（此前 dispatchRankFactors 恒缺该分量）。空 / 0 表示未采集。
		field.String("timezone").MaxLen(64).Default("").StructTag(`json:"timezone"`),
		field.Int("latitude_e6").Default(0).StructTag(`json:"latitude_e6"`),
		field.Int("longitude_e6").Default(0).StructTag(`json:"longitude_e6"`),
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

// EsportsPlayerIdentity 是旧版陪玩实名认证记录（历史实体）。
// Deprecated: 已由统一实名（EsportsRealNameVerification）取代（2026-10）。
// 表保留只读、不再写入；仅 realname_migrate.go 一次性迁移时只读访问。
// 实名通过前陪玩 qualification 不得为 qualified，判断改读统一档案。
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
