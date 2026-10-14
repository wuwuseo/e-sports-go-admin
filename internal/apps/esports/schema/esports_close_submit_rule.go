package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// EsportsCloseSubmitRule 是结单提交规则（§3.4）：规定订单结单前「陪玩个人服务段上报」（服务量 + 凭证）
// 需要多少成员完成、是否允许客服代提交、未达标如何处理。按 全局 / 游戏分区 / SKU 绑定。
// 规则不可原地修改：改规则＝新增一条递增 version 的新行，旧行转 history（§12.4 同款版本管理）。
type EsportsCloseSubmitRule struct{ ent.Schema }

func (EsportsCloseSubmitRule) Fields() []ent.Field {
	return []ent.Field{
		field.String("code").NotEmpty().MaxLen(48).Immutable().StructTag(`json:"code"`),
		field.String("name").NotEmpty().MaxLen(64).StructTag(`json:"name"`),
		// scope 决定适用范围：global 全局 / game 按游戏分区 / sku 按具体 SKU。
		field.Enum("scope").Values("global", "game", "sku").Default("global").StructTag(`json:"scope"`),
		// scope=game 时为游戏分区名；scope=sku 时为 SKU 的 code。
		field.String("scope_key").MaxLen(64).Default("").StructTag(`json:"scope_key"`),
		// mode：single 任一成员提交即可 / multi 所有实际服务成员逐人提交 / custom 按 min_submit_ratio_bp。
		field.Enum("mode").Values("single", "multi", "custom").Default("multi").StructTag(`json:"mode"`),
		// min_submit_ratio_bp 仅 custom 模式使用：最低提交比例（万分比），0 = 不限制。
		field.Int("min_submit_ratio_bp").Default(0).StructTag(`json:"min_submit_ratio_bp"`),
		// allow_substitute 为 true 时，客服可在结单时代未上报成员补填服务段上报。
		field.Bool("allow_substitute").Default(false).StructTag(`json:"allow_substitute"`),
		// on_missing：block 未达标禁止结单 / remind 放行并写催办提醒 / auto_accept 未提交成员视为已提交。
		field.Enum("on_missing").Values("block", "remind", "auto_accept").Default("block").StructTag(`json:"on_missing"`),
		field.Enum("status").Values("draft", "active", "history").Default("draft").StructTag(`json:"status"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Int("created_by_admin_id").Optional().Nillable().StructTag(`json:"created_by_admin_id"`),
		field.String("note").MaxLen(255).Default("").StructTag(`json:"note"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsCloseSubmitRule) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("code", "version").Unique(),
		index.Fields("status", "scope"),
	}
}
