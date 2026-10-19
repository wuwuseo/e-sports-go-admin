package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsAssessmentRule 是考核维度权重配置（§7.2）：按考核类型分组，每个维度的权重以基点表示，
// 同一类型下各维度权重合计须为 10000（即 100%）。权重可配置，改分流程按当前配置快照。
type EsportsAssessmentRule struct{ ent.Schema }

func (EsportsAssessmentRule) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("assessment_type").Values("entry", "upgrade", "spot_check", "retry").StructTag(`json:"assessment_type"`),
		field.Enum("dimension").Values("game_skill", "teaching", "attitude", "communication", "compliance", "stability").StructTag(`json:"dimension"`),
		field.Int("weight_bp").Positive().StructTag(`json:"weight_bp"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsAssessmentRule) Indexes() []ent.Index {
	return []ent.Index{index.Fields("assessment_type", "dimension").Unique()}
}

// EsportsAssessment 是考核任务（§7.1/§7.2）：入职 / 升级 / 抽查复核 / 复考四类，状态从待分配考官
// 推进到已约考核，打分后落 passed / failed。结论通过则定级并开通接单权限，不通过则可安排复考。
// appeal_status 承载结果申诉状态（§7.4），申诉成立时由管理员复核改判并重放定级动作。
type EsportsAssessment struct{ ent.Schema }

func (EsportsAssessment) Fields() []ent.Field {
	return []ent.Field{
		field.String("task_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"task_no"`),
		field.Int("player_id").Positive().StructTag(`json:"player_id"`),
		field.Enum("assessment_type").Values("entry", "upgrade", "spot_check", "retry").StructTag(`json:"assessment_type"`),
		// 目标等级：升级 / 复考时陪玩申请的等级，入职默认 novice；空串表示未指定。
		field.String("target_level").MaxLen(32).Default("").StructTag(`json:"target_level"`),
		field.Enum("status").Values("pending", "scheduled", "passed", "failed").Default("pending").StructTag(`json:"status"`),
		field.Enum("appeal_status").Values("none", "pending", "approved", "rejected").Default("none").StructTag(`json:"appeal_status"`),
		field.Enum("source").Values("apply", "system").Default("apply").StructTag(`json:"source"`),
		field.Int("examiner_admin_id").Optional().Nillable().StructTag(`json:"examiner_admin_id"`),
		field.Time("scheduled_at").Optional().Nillable().StructTag(`json:"scheduled_at"`),
		field.Time("contacted_at").Optional().Nillable().StructTag(`json:"contacted_at"`),
		field.Time("scored_at").Optional().Nillable().StructTag(`json:"scored_at"`),
		// 按维度加权计算的总分（0~100 分），打分前为 0。
		field.Int("total_score").Default(0).StructTag(`json:"total_score"`),
		field.String("note").MaxLen(500).Default("").StructTag(`json:"note"`),
		field.Int("created_by_admin_id").Optional().Nillable().StructTag(`json:"created_by_admin_id"`),
		// active_dedup_key 是「同一陪玩至多一条进行中单」的唯一索引兜底键（可空）：
		// 升级考核建单时写入 upgrade:<playerID>，落终态（结论已出且无待复核申诉）时清空为 NULL。
		// MySQL / SQLite 的唯一索引都允许多个 NULL，因此终态单据与其它考核类型不冲突；
		// 不用部分索引（partial index），MySQL 5.7 不支持。
		field.String("active_dedup_key").Optional().Nillable().MaxLen(80).StructTag(`json:"active_dedup_key,omitempty"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsAssessment) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("task_no").Unique(),
		index.Fields("active_dedup_key").Unique(),
		index.Fields("player_id", "created_at"),
		index.Fields("status", "assessment_type"),
	}
}

// EsportsAssessmentScore 是逐维度打分（§7.2）：分值 0~100，权重在打分时快照，评语与依据必填。
// 与考核任务一并写入，构成完整考核记录（§7.4）。
type EsportsAssessmentScore struct{ ent.Schema }

func (EsportsAssessmentScore) Fields() []ent.Field {
	return []ent.Field{
		field.Int("assessment_id").Positive().StructTag(`json:"assessment_id"`),
		field.Enum("dimension").Values("game_skill", "teaching", "attitude", "communication", "compliance", "stability").StructTag(`json:"dimension"`),
		field.Int("weight_bp").Positive().StructTag(`json:"weight_bp"`),
		// 维度分值 0~100（允许 0 分，故不加 Positive）。
		field.Int("score").StructTag(`json:"score"`),
		field.String("comment").MaxLen(500).StructTag(`json:"comment"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

func (EsportsAssessmentScore) Indexes() []ent.Index {
	return []ent.Index{index.Fields("assessment_id", "dimension").Unique()}
}

// EsportsAssessmentAppeal 是考核结果申诉（§7.4）：陪玩发起，管理员复核。每个考核任务仅一条申诉记录。
type EsportsAssessmentAppeal struct{ ent.Schema }

func (EsportsAssessmentAppeal) Fields() []ent.Field {
	return []ent.Field{
		field.Int("assessment_id").Positive().StructTag(`json:"assessment_id"`),
		field.Int("player_id").Positive().StructTag(`json:"player_id"`),
		field.String("reason").MaxLen(500).StructTag(`json:"reason"`),
		field.Enum("status").Values("pending", "approved", "rejected").Default("pending").StructTag(`json:"status"`),
		field.Int("reviewer_admin_id").Optional().Nillable().StructTag(`json:"reviewer_admin_id"`),
		field.String("review_note").MaxLen(500).Default("").StructTag(`json:"review_note"`),
		field.Time("reviewed_at").Optional().Nillable().StructTag(`json:"reviewed_at"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

func (EsportsAssessmentAppeal) Indexes() []ent.Index {
	return []ent.Index{index.Fields("assessment_id").Unique()}
}

// EsportsPlayerReview 是客户对实际服务陪玩的评价（§7.5）。同客户同订单同服务成员仅保留一份
// 当前有效评价（唯一索引）；修改时递增 version 并追加一条修订历史。客服代录的反馈 verified=false，
// 不直接计入综合评分。status 承载异常刷评 / 评价申诉的管理员复核结论。
type EsportsPlayerReview struct{ ent.Schema }

func (EsportsPlayerReview) Fields() []ent.Field {
	return []ent.Field{
		field.Int("order_id").Positive().StructTag(`json:"order_id"`),
		field.Int("customer_id").Positive().StructTag(`json:"customer_id"`),
		field.Int("player_id").Positive().StructTag(`json:"player_id"`),
		field.Int("rating").Positive().StructTag(`json:"rating"`),
		field.String("comment").MaxLen(500).Default("").StructTag(`json:"comment"`),
		// 是否来自已核验身份的客户自助评价；客服代录为 false。
		field.Bool("verified").Default(false).StructTag(`json:"verified"`),
		field.Enum("status").Values("normal", "disputed", "invalid", "appeal_pending").Default("normal").StructTag(`json:"status"`),
		// 异常刷评自动识别留痕（§7.5-05）：命中规则即置 status=disputed 并写入命中规则码与明细；
		// auto_disputed=true 表示由规则自动标记（与管理员手工标记区分，便于事后复核与申诉）。
		field.Bool("auto_disputed").Default(false).StructTag(`json:"auto_disputed"`),
		field.String("dispute_rule_code").MaxLen(48).Default("").StructTag(`json:"dispute_rule_code"`),
		field.JSON("dispute_detail", map[string]any{}).Optional().StructTag(`json:"dispute_detail"`),
		field.Time("disputed_at").Optional().Nillable().StructTag(`json:"disputed_at"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsPlayerReview) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("order_id", "customer_id", "player_id").Unique(),
		index.Fields("player_id", "status"),
	}
}

// EsportsPlayerReviewRevision 是评价修订历史（append-only）：每次新建 / 修改追加一条，保留版本与时间（§7.5）。
type EsportsPlayerReviewRevision struct{ ent.Schema }

func (EsportsPlayerReviewRevision) Hooks() []ent.Hook { return appendOnlyHooks() }

func (EsportsPlayerReviewRevision) Fields() []ent.Field {
	return []ent.Field{
		field.Int("review_id").Positive().StructTag(`json:"review_id"`),
		field.Int("version").Positive().StructTag(`json:"version"`),
		field.Int("rating").Positive().StructTag(`json:"rating"`),
		field.String("comment").MaxLen(500).Default("").StructTag(`json:"comment"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

func (EsportsPlayerReviewRevision) Indexes() []ent.Index {
	return []ent.Index{index.Fields("review_id", "version").Unique()}
}

// EsportsPlayerReviewAppeal 是评价申诉流水（§7.5-05）：客户 / 陪玩对评价（含被自动判定的异常刷评）
// 发起申诉，管理员复核后改判。与考核申诉同构，每条评价仅一条申诉记录（唯一索引兜底）。
// 建议表名：esports_player_review_appeals。
type EsportsPlayerReviewAppeal struct{ ent.Schema }

func (EsportsPlayerReviewAppeal) Fields() []ent.Field {
	return []ent.Field{
		field.Int("review_id").Positive().StructTag(`json:"review_id"`),
		// 申诉发起方：客户 / 陪玩。
		field.Enum("appellant_type").Values("customer", "player").StructTag(`json:"appellant_type"`),
		field.Int("appellant_id").Positive().StructTag(`json:"appellant_id"`),
		field.Int("order_id").Default(0).StructTag(`json:"order_id"`),
		field.String("reason").NotEmpty().MaxLen(500).StructTag(`json:"reason"`),
		field.String("evidence_ref").MaxLen(255).Default("").StructTag(`json:"evidence_ref"`),
		// pending 进入管理员复核队列；approved 申诉成立（评价改为 invalid 或复原 normal，由复核结论决定）；
		// rejected 申诉驳回（维持 disputed / invalid）。
		field.Enum("status").Values("pending", "approved", "rejected").Default("pending").StructTag(`json:"status"`),
		field.Int("reviewer_admin_id").Optional().Nillable().StructTag(`json:"reviewer_admin_id"`),
		field.String("review_note").MaxLen(500).Default("").StructTag(`json:"review_note"`),
		field.Time("reviewed_at").Optional().Nillable().StructTag(`json:"reviewed_at"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

func (EsportsPlayerReviewAppeal) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("review_id").Unique(),
		index.Fields("status", "created_at"),
	}
}
