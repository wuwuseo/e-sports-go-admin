package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"go-admin/internal/apps/esports/schematier"
)

// EsportsCloseRule 是结单规则（§3.4 首版）：把「服务凭证不少于 N 张、服务时长不为零、
// 无未处理加钟 / 售后」等结单条件做成可配置规则，并按 全局 / 游戏分区 / SKU 绑定到订单。
// 规则不可原地修改：改规则＝新增一条递增 version 的新行，旧行转 history（§12.4 同款版本管理）。
//
// 首版为「结构化条件项 + 金额复核阈值」，覆盖 §3.4 结单条件默认项与「高额单管理员复核」的最小闭环。
// 结单表单（自定义字段库、按游戏分区 / SKU + 金额区间绑定模板、结单时校验并留痕）见 close_form.go。
// ponytail: 字段 + 运算符 + 值的任意 AND/OR 嵌套、结单审批流节点编排均未实现。
type EsportsCloseRule struct{ ent.Schema }

func (EsportsCloseRule) Fields() []ent.Field {
	return []ent.Field{
		field.String("code").NotEmpty().MaxLen(48).Immutable().StructTag(`json:"code"`),
		field.String("name").NotEmpty().MaxLen(64).StructTag(`json:"name"`),
		// scope 决定规则适用范围：global 全局 / game 按游戏分区 / sku 按具体 SKU。
		field.Enum("scope").Values("global", "game", "sku").Default("global").StructTag(`json:"scope"`),
		// scope=game 时为游戏分区名；scope=sku 时为 SKU 的 code。
		field.String("scope_key").MaxLen(64).Default("").StructTag(`json:"scope_key"`),
		// 结单条件（§3.4 默认项的结构化表达）。
		field.Int("min_evidence").Default(1).StructTag(`json:"min_evidence"`),
		field.Bool("require_duration").Default(true).StructTag(`json:"require_duration"`),
		field.Bool("require_no_pending_addon").Default(true).StructTag(`json:"require_no_pending_addon"`),
		field.Bool("require_no_open_aftersale").Default(true).StructTag(`json:"require_no_open_aftersale"`),
		// condition_tree 是通用 AND/OR 结单条件树（§3.4：字段 + 运算符 + 值，支持嵌套）。
		// 配置后优先于上面的结构化条件项；未配置时回落结构化条件项（二者语义等价）。
		// 零值（空 op 与空 field）表示未配置，回落结构化条件项。
		field.JSON("condition_tree", schematier.CloseConditionNode{}).Optional().
			StructTag(`json:"condition_tree"`),
		// 金额复核阈值（分）：>0 且订单应付达到该阈值时，结单须由非负责客服的管理员执行（§3.4 高额单管理员复核）。
		field.Int64("review_threshold_cents").Default(0).StructTag(`json:"review_threshold_cents"`),
		field.Enum("status").Values("draft", "active", "history").Default("draft").StructTag(`json:"status"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Int("created_by_admin_id").Optional().Nillable().StructTag(`json:"created_by_admin_id"`),
		field.String("note").MaxLen(255).Default("").StructTag(`json:"note"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsCloseRule) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("code", "version").Unique(),
		index.Fields("status", "scope"),
	}
}
