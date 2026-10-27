package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsRealNameVerification 统一实名档案：客户与陪玩共用，一个主体只保留一条记录。
// 状态机 pending → verified / rejected / expired；rejected / expired 可原地回到 pending。
// 身份证号只保存脱敏值（保留前 6 后 4），原始证件号仅在请求生命周期内使用、不落库不写日志。
type EsportsRealNameVerification struct{ ent.Schema }

func (EsportsRealNameVerification) Fields() []ent.Field {
	return []ent.Field{
		// 主体类型：customer 客户 / player 陪玩。同一 ID 在两个主体下互不影响。
		field.Enum("subject_type").Values("customer", "player").StructTag(`json:"subject_type"`),
		field.Int("subject_id").Positive().StructTag(`json:"subject_id"`),
		field.String("real_name").NotEmpty().MaxLen(120).StructTag(`json:"real_name"`),
		// 脱敏证件号（保留前 6 后 4），原号不落库。
		field.String("id_number_masked").NotEmpty().MaxLen(32).StructTag(`json:"id_number_masked"`),
		// 核验渠道：wechat / alipay / third_party 为自动核验，manual 为人工审核。
		field.Enum("channel").Values("wechat", "alipay", "third_party", "manual").StructTag(`json:"channel"`),
		// 核验方式：two_factor 二要素同步 / face 人脸异步 / manual 人工。
		field.Enum("method").Values("two_factor", "face", "manual").StructTag(`json:"method"`),
		field.Enum("status").Values("pending", "verified", "rejected", "expired").
			Default("pending").StructTag(`json:"status"`),
		// 渠道侧流水号：异步核验（人脸 / 回调 / 轮询）据此回查与幂等。
		field.String("provider_ref").MaxLen(255).Optional().Nillable().StructTag(`json:"provider_ref"`),
		field.String("fail_reason").MaxLen(255).Default("").StructTag(`json:"fail_reason"`),
		field.Time("verified_at").Optional().Nillable().StructTag(`json:"verified_at"`),
		field.Int("reviewed_by_admin_id").Optional().Nillable().StructTag(`json:"reviewed_by_admin_id"`),
		// requested_by 提交人：主体本人自助提交为 0，管理端代提为 adminID。
		field.Int("requested_by").Default(0).StructTag(`json:"requested_by"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsRealNameVerification) Indexes() []ent.Index {
	return []ent.Index{
		// 一主体一条档案：重复提交原地复用，不再新增行。
		index.Fields("subject_type", "subject_id").Unique(),
		// 超时扫描（pending 到期）与列表筛选。
		index.Fields("status", "updated_at"),
	}
}

// EsportsRealNameAttempt 实名核验流水：每次提交留痕，用于审计与每日次数限制。
// ponytail: 流水不存证件号，落库的只有脱敏值；真实核验发生在 provider 请求生命周期内。
type EsportsRealNameAttempt struct{ ent.Schema }

func (EsportsRealNameAttempt) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("subject_type").Values("customer", "player").StructTag(`json:"subject_type"`),
		field.Int("subject_id").Positive().StructTag(`json:"subject_id"`),
		field.Enum("channel").Values("wechat", "alipay", "third_party", "manual").StructTag(`json:"channel"`),
		field.Enum("method").Values("two_factor", "face", "manual").StructTag(`json:"method"`),
		field.Enum("status").Values("pending", "passed", "failed", "abandoned").
			Default("pending").StructTag(`json:"status"`),
		// 渠道侧流水号：为空时走 NULL，NULL 不参与唯一索引冲突（与客户企微标识同手法）。
		field.String("provider_ref").MaxLen(255).Optional().Nillable().StructTag(`json:"provider_ref"`),
		field.String("fail_reason").MaxLen(255).Default("").StructTag(`json:"fail_reason"`),
		field.Int("requested_by").Default(0).StructTag(`json:"requested_by"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsRealNameAttempt) Indexes() []ent.Index {
	return []ent.Index{
		// 每日次数统计与主体流水列表。
		index.Fields("subject_type", "subject_id", "created_at"),
		// 回调幂等：同一渠道的同一流水号只能有一条流水。
		index.Fields("channel", "provider_ref").Unique(),
	}
}
