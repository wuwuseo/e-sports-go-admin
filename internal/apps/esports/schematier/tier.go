// Package schematier 存放 ent JSON 字段引用的自定义类型。
// 单独成包是为了避免与主 schema 包同名：ent 生成的代码会同时导入本包与 schema 包，
// 若两者同名会产生「schema redeclared」的导入冲突。
package schematier

// CampaignTier 是阶梯赠送的一档：实充金额达到 ThresholdCents 时赠送 BonusCents。
type CampaignTier struct {
	ThresholdCents int64 `json:"threshold_cents"`
	BonusCents     int64 `json:"bonus_cents"`
}

// CsCommissionTier 是客服提成的一档阶梯：提成周期累计净基数达到 ThresholdBaseCents 时按 ValueBp 计提。
type CsCommissionTier struct {
	ThresholdBaseCents int64 `json:"threshold_base_cents"`
	ValueBp            int   `json:"value_bp"`
}

// CloseFormFieldBinding 是结单表单模板引用的一条字段：字段库 FieldKey + 该模板下的必填覆盖与排序。
type CloseFormFieldBinding struct {
	FieldKey string `json:"field_key"`
	Required bool   `json:"required"`
	Sort     int    `json:"sort"`
}
