package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsPayoutBatch 是一次陪玩批量代发（按周一键打款）的执行批次：
// 一个批次包含若干提现单（通过 EsportsWithdrawalPayout.batch_id 关联），
// idem_key 防连点（同一幂等键只生成一个批次），batch_no 为本站批次号。
// status：submitting 提交中 / submitted 已提交服务商 / partial 部分成功 /
// settled 全部到账 / failed 提交失败。
type EsportsPayoutBatch struct{ ent.Schema }

func (EsportsPayoutBatch) Fields() []ent.Field {
	return []ent.Field{
		field.String("batch_no").NotEmpty().MaxLen(64).Immutable().StructTag(`json:"batch_no"`),
		// 幂等键：防连点，同一 key 重复提交返回既有批次。
		field.String("idem_key").NotEmpty().MaxLen(120).StructTag(`json:"idem_key"`),
		field.String("provider").MaxLen(32).Default("yunzhanghu").StructTag(`json:"provider"`),
		field.Enum("status").Values("submitting", "submitted", "partial", "settled", "failed").
			Default("submitting").StructTag(`json:"status"`),
		field.Int("total_count").Default(0).StructTag(`json:"total_count"`),
		// 批次金额合计（分）。
		field.Int64("total_cents").Default(0).StructTag(`json:"total_cents"`),
		// 服务商批次号：只在提交成功后写入；未提交为 NULL，可空唯一索引不会互相冲突
		//（与 EsportsCheckoutAttempt.provider_trade_no 同一模式，MySQL / SQLite 唯一索引均允许多个 NULL）。
		field.String("provider_batch_no").Optional().Nillable().MaxLen(128).StructTag(`json:"provider_batch_no,omitempty"`),
		field.String("fail_reason").MaxLen(255).Default("").StructTag(`json:"fail_reason"`),
		field.Int("submitted_by_admin_id").Optional().Nillable().StructTag(`json:"submitted_by_admin_id"`),
		field.Time("submitted_at").Optional().Nillable().StructTag(`json:"submitted_at"`),
		field.Time("settled_at").Optional().Nillable().StructTag(`json:"settled_at"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsPayoutBatch) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("batch_no").Unique(),
		index.Fields("idem_key").Unique(),
		// 服务商批次号唯一；未回填为 NULL，不参与冲突。
		index.Fields("provider_batch_no").Unique(),
	}
}
