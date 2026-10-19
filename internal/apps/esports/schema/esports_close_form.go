package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"

	"go-admin/internal/apps/esports/schematier"
)

// CloseFormFieldBinding 定义在 schematier 包以避免与主 schema 包同名的生成代码导入冲突；此处以别名保留类型名。
type CloseFormFieldBinding = schematier.CloseFormFieldBinding

// EsportsCloseFormField 是结单表单的字段库（§3.4 结单表单设计器首版）。
// 字段可被多个模板复用；字段库原地编辑（不做版本化），用 enabled 控制是否可被新模板引用。
// ponytail: 拖拽画布与任意坐标、字段级联与条件显示、字段值的历史修订均未实现。
type EsportsCloseFormField struct{ ent.Schema }

func (EsportsCloseFormField) Fields() []ent.Field {
	return []ent.Field{
		field.String("key").NotEmpty().MaxLen(48).Immutable().StructTag(`json:"key"`),
		field.String("label").NotEmpty().MaxLen(64).StructTag(`json:"label"`),
		// field_type 覆盖 §3.4 自定义字段类型：文本 / 数字 / 金额只读 / 单选 / 多选 / 日期时间 / 图片 / 开关 / 关联。
		field.Enum("field_type").
			Values("text", "number", "money", "select", "multiselect", "datetime", "image", "switch", "reference").
			Default("text").StructTag(`json:"field_type"`),
		field.Bool("required").Default(false).StructTag(`json:"required"`),
		// options 仅 select / multiselect 使用（可选值列表）。
		field.JSON("options", []string{}).Optional().StructTag(`json:"options"`),
		// max_length 仅 text 使用，0 = 不限。
		field.Int("max_length").Default(0).StructTag(`json:"max_length"`),
		field.Bool("enabled").Default(true).StructTag(`json:"enabled"`),
		field.Int("sort_order").Default(0).StructTag(`json:"sort_order"`),
		field.String("note").MaxLen(255).Default("").StructTag(`json:"note"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsCloseFormField) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("key").Unique(),
	}
}

// EsportsCloseFormTemplate 是结单表单模板（§3.4）：按 全局 / 游戏分区 / SKU + 金额区间绑定一组字段。
// 模板不可原地修改：改模板＝新增一条递增 version 的新行，旧行转 history（§12.4 同款版本管理）。
type EsportsCloseFormTemplate struct{ ent.Schema }

// CloseFormFieldDefinition 存放在独立包，避免 ent 生成代码的 schema 包名冲突。
type CloseFormFieldDefinition = schematier.CloseFormFieldDefinition

func (EsportsCloseFormTemplate) Fields() []ent.Field {
	return []ent.Field{
		field.String("code").NotEmpty().MaxLen(48).Immutable().StructTag(`json:"code"`),
		field.String("name").NotEmpty().MaxLen(64).StructTag(`json:"name"`),
		// scope 决定适用范围：global 全局 / game 按游戏分区 / sku 按具体 SKU。
		// scope 末尾追加 order_type（§3.4-07）：按订单类型（即时/预约）绑定模板，
		// 此时 scope_key 取 appointment_status 的取值（instant / request / confirmed）；既有取值保留。
		field.Enum("scope").Values("global", "game", "sku", "order_type").Default("global").StructTag(`json:"scope"`),
		// scope=game 时为游戏分区名；scope=sku 时为 SKU 的 code。
		field.String("scope_key").MaxLen(64).Default("").StructTag(`json:"scope_key"`),
		// 金额区间（分）：min_cents ≤ 订单应付 < max_cents；任一侧为 0 表示该侧不限。
		field.Int64("min_cents").Default(0).StructTag(`json:"min_cents"`),
		field.Int64("max_cents").Default(0).StructTag(`json:"max_cents"`),
		// fields 是模板字段绑定列表（字段库 key + 必填覆盖 + 排序）。
		field.JSON("fields", []CloseFormFieldBinding{}).Optional().StructTag(`json:"fields"`),
		field.JSON("field_definitions", []CloseFormFieldDefinition{}).Optional().StructTag(`json:"field_definitions"`),
		field.Enum("status").Values("draft", "active", "history").Default("draft").StructTag(`json:"status"`),
		field.Int("version").Default(1).StructTag(`json:"version"`),
		field.Int("created_by_admin_id").Optional().Nillable().StructTag(`json:"created_by_admin_id"`),
		field.String("note").MaxLen(255).Default("").StructTag(`json:"note"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).StructTag(`json:"updated_at"`),
	}
}

func (EsportsCloseFormTemplate) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("code", "version").Unique(),
		index.Fields("status", "scope"),
	}
}

// EsportsCloseFormSnapshot 记录一次结单按模板填写的字段值（§3.4）：模板版本与字段值一并留痕，
// 便于结单审核与事后追溯；同订单可能有多条（复核驳回后可重新结单）。
type EsportsCloseFormSnapshot struct{ ent.Schema }

func (EsportsCloseFormSnapshot) Fields() []ent.Field {
	return []ent.Field{
		field.Int("order_id").StructTag(`json:"order_id"`),
		// template_id=0 表示该订单无适用模板（未配置或未匹配到）。
		field.Int("template_id").Default(0).StructTag(`json:"template_id"`),
		field.String("template_code").MaxLen(48).Default("").StructTag(`json:"template_code"`),
		field.Int("template_version").Default(0).StructTag(`json:"template_version"`),
		field.JSON("values", map[string]any{}).Optional().StructTag(`json:"values"`),
		field.String("note").MaxLen(255).Default("").StructTag(`json:"note"`),
		field.Int("submitted_by_admin_id").Optional().Nillable().StructTag(`json:"submitted_by_admin_id"`),
		field.Time("created_at").Immutable().Default(time.Now).StructTag(`json:"created_at"`),
	}
}

func (EsportsCloseFormSnapshot) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("order_id"),
	}
}
