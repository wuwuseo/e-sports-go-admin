// Package schematier 存放 ent JSON 字段引用的自定义类型。
// 单独成包是为了避免与主 schema 包同名：ent 生成的代码会同时导入本包与 schema 包，
// 若两者同名会产生「schema redeclared」的导入冲突。
package schematier

import "time"

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

// CloseApprovalNode 是结单审批流的一个串行节点（§3.4）：配审批角色与超时策略。
// InvolvesFunds 为 true 表示涉及资金 / 争议的节点，禁止自动通过。
type CloseApprovalNode struct {
	Seq               int    `json:"seq"`
	Name              string `json:"name"`
	Role              string `json:"role"`
	TimeoutHours      int    `json:"timeout_hours"`
	AutoPassOnTimeout bool   `json:"auto_pass_on_timeout"`
	InvolvesFunds     bool   `json:"involves_funds"`
}

// CloseApprovalStep 是某订单审批进度中的一步：由节点派生，并记录处理结果。
type CloseApprovalStep struct {
	Seq        int        `json:"seq"`
	Name       string     `json:"name"`
	Role       string     `json:"role"`
	Status     string     `json:"status"`
	AdminID    int        `json:"admin_id"`
	At         *time.Time `json:"at"`
	Reason     string     `json:"reason"`
	AutoPassed bool       `json:"auto_passed"`
}
