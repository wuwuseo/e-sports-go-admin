package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"go-admin/internal/apps/esports/schematier"
)

// CloseApprovalNode / CloseApprovalStep 定义在 schematier 包以避免生成代码的导入冲突；此处以别名保留类型名。
type CloseApprovalNode = schematier.CloseApprovalNode

type CloseApprovalStep = schematier.CloseApprovalStep

// EsportsCloseApprovalFlow 是结单审批流：节点串行可增删，每个节点配审批角色与超时策略；
// 涉及资金 / 争议的节点禁止自动通过。按 全局 / 游戏分区 / SKU 绑定，并可按金额阈值触发。
// 流不可原地修改：改流＝新增一条递增 version 的新行，旧行转 history（同款版本管理）。
type EsportsCloseApprovalFlow struct{ ent.Schema }

func (EsportsCloseApprovalFlow) Fields() []ent.Field {
	return []ent.Field{
		field.String("code").NotEmpty().MaxLen(48).Immutable().StructTag(`json:"code"`),
		field.String("name").NotEmpty().MaxLen(64).StructTag(`json:"name"`),
		// scope 决定适用范围：global 全局 / game 按游戏分区 / sku 按具体 SKU。
		field.Enum("scope").Values("global", "game", "sku").Default("global").StructTag(`json:"scope"`),
		// scope=game 时为游戏分区名；scope=sku 时为 SKU 的 code。
		field.String("scope_key").MaxLen(64).Default("").StructTag(`json:"scope_key"`),
		// min_cents 为触发阈值（分）：订单应付 ≥ 该值才走本流；0 表示所有结单都需审批。
		field.Int64("min_cents").Default(0).StructTag(`json:"min_cents"`),
		// nodes 是串行节点列表（seq 从 1 连续递增）。
		field.JSON("nodes", []CloseApprovalNode{}).Optional().StructTag(`json:"nodes"`),
		field.Enum("status").Values("draft", "active", "history").Default("draft").StructTag(`json:"status"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Int("created_by_admin_id").Optional().Nillable().StructTag(`json:"created_by_admin_id"`),
		field.String("note").MaxLen(255).Default("").StructTag(`json:"note"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsCloseApprovalFlow) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("code", "version").Unique(),
		index.Fields("status", "scope"),
	}
}

// EsportsCloseApproval 是订单的结单审批进度：一条订单一行，记录当前节点与逐步结果。
// flow_id = 0 表示由结单规则的金额复核阈值触发的内置单节点「管理员复核」。
type EsportsCloseApproval struct{ ent.Schema }

func (EsportsCloseApproval) Fields() []ent.Field {
	return []ent.Field{
		field.Int("order_id").Positive().StructTag(`json:"order_id"`),
		field.Int("flow_id").Default(0).StructTag(`json:"flow_id"`),
		field.String("flow_code").MaxLen(48).Default("").StructTag(`json:"flow_code"`),
		field.Int("flow_version").Default(0).StructTag(`json:"flow_version"`),
		// current_seq 为当前待审批节点序号（1 起）。
		field.Int("current_seq").Default(1).StructTag(`json:"current_seq"`),
		field.Enum("status").Values("pending", "approved", "rejected").Default("pending").StructTag(`json:"status"`),
		// steps 记录每个节点的处理结果（含自动通过）。
		field.JSON("steps", []CloseApprovalStep{}).Optional().StructTag(`json:"steps"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsCloseApproval) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("order_id").Unique(),
		index.Fields("status", "current_seq"),
	}
}
