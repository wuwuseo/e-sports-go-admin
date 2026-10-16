package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsAfterSaleAction 是售后方案的动作项（§13.3 / §13.6 多动作售后方案与逐项执行）。
//
// 背景：售后单原先把方案压成单个 plan_action + 一组金额 / 补履约字段 + 一个总状态，
// 无法表达「退款 + 补钟」这类多动作，也看不到部分成功后的逐项进度。动作项把方案拆成可逐项执行的粒度：
// 一张售后单可挂多条动作项（退款 / 补偿 / 补钟 / 换人），方案提交时一次性写入并清空上一轮动作；
// 执行按动作项逐项推进、逐项更新进度，允许「一条成功、一条失败」的部分成功结果。
//
// 与既有单动作字段的关系（向后兼容）：动作项是增量真相，售后单上的 plan_action / refund_cents /
// makeup_* 退化为「聚合摘要」——refund_cents 记资金类动作金额之和，供既有审批预占额度口径直接复用；
// 单条补履约动作的参数同时镜像到售后单 makeup_* 字段，供既有补履约读取方无感兼容。
//
// 状态机：pending（待执行）→ executing（执行中）→ succeeded（成功）| failed（失败）；skipped（跳过 / 不再适用）。
// 幂等键 idem_key = aftersale:<after_sale_id>:action:<seq>:<source>，唯一索引兜底同一动作项重复落账；
// 下游账本（钱包流水 / 分成冲回 / 成长值 / 补履约任务）各自的幂等键由「按项来源号」派生，避免双记。
type EsportsAfterSaleAction struct{ ent.Schema }

func (EsportsAfterSaleAction) Fields() []ent.Field {
	return []ent.Field{
		field.Int("after_sale_id").Positive().StructTag(`json:"after_sale_id"`),
		// 冗余订单 ID，便于按订单聚合；非订单来源为 0。
		field.Int("order_id").Default(0).StructTag(`json:"order_id"`),
		// 动作序号，方案内从 1 递增；构成按项幂等键与逐项执行的定位键。
		field.Int("seq").Positive().StructTag(`json:"seq"`),
		// 动作类型：refund 退原核销款 / compensate 发放补偿权益 / makeup 补钟 / replace 换人。
		field.Enum("action_type").Values("refund", "compensate", "makeup", "replace").
			StructTag(`json:"action_type"`),
		// 资金类动作（refund / compensate）金额（分）；补履约类（makeup / replace）恒为 0。
		field.Int64("amount_cents").Default(0).StructTag(`json:"amount_cents"`),
		// 补履约类动作参数（makeup / replace）：承接陪玩、席位占位、补足服务量、公司承担报酬。
		field.Int("player_id").Optional().Nillable().StructTag(`json:"player_id"`),
		field.Int("seat_index").Optional().Nillable().StructTag(`json:"seat_index"`),
		field.Int64("units").Default(0).StructTag(`json:"units"`),
		field.Int64("company_pay_cents").Default(0).StructTag(`json:"company_pay_cents"`),
		// 退款类动作定向冲回的争议陪玩（§13.3 部分退款成员明细冲回）；留空按订单整体比例冲回。
		field.Int("dispute_player_id").Optional().Nillable().StructTag(`json:"dispute_player_id"`),
		// 原核销明细引用：构成按项幂等键的 <source> 段，用于把同一动作项拆分到具体来源明细。
		field.String("source_ref").MaxLen(120).Default("").StructTag(`json:"source_ref"`),
		// 按项幂等键：aftersale:<after_sale_id>:action:<seq>:<source>；唯一索引兜底重复落账。
		field.String("idem_key").NotEmpty().MaxLen(191).Immutable().StructTag(`json:"idem_key"`),
		// pending → executing → succeeded | failed；skipped 为方案变更 / 不再适用而被跳过。
		field.Enum("status").Values("pending", "executing", "succeeded", "failed", "skipped").
			Default("pending").StructTag(`json:"status"`),
		// 执行结果摘要（成功说明 / 失败原因），供逐项进度展示与人工处理参考。
		field.String("result_summary").MaxLen(500).Default("").StructTag(`json:"result_summary"`),
		field.String("note").MaxLen(255).Default("").StructTag(`json:"note"`),
		field.Int("executor_admin_id").Optional().Nillable().StructTag(`json:"executor_admin_id"`),
		field.Time("executed_at").Optional().Nillable().StructTag(`json:"executed_at"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsAfterSaleAction) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("idem_key").Unique(),
		// 同一售后单内动作序号唯一，提交方案时按序整体重建。
		index.Fields("after_sale_id", "seq").Unique(),
		index.Fields("after_sale_id", "status"),
	}
}
