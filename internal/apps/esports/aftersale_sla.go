package esports

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/systemconfig"
)

// 售后限时与超时升级（§13.5）：受理 / 给出方案 / 客户确认 / 执行四段时限均可配置（小时，`0` = 不限），
// 超时由 `esports_check_aftersale_sla` 任务写 `aftersale.sla_breach` 事务外盒做升级提醒。
// 超时只提醒，不自动流转状态、不改角色。

const (
	afterSaleAcceptHoursKey          = "app.esports.aftersale_accept_hours"
	afterSalePlanHoursKey            = "app.esports.aftersale_plan_hours"
	afterSaleCustomerConfirmHoursKey = "app.esports.aftersale_customer_confirm_hours"
	afterSaleExecuteHoursKey         = "app.esports.aftersale_execute_hours"
)

// 阶段标识（用于升级事件与前端展示）。
const (
	afterSaleStageAccept          = "accept"
	afterSaleStagePlan            = "plan"
	afterSaleStageCustomerConfirm = "customer_confirm"
	afterSaleStageExecute         = "execute"
)

// afterSaleSLAPolicy 售后限时策略快照（单位：小时，0 = 该阶段不限时）。
type afterSaleSLAPolicy struct {
	AcceptHours          int
	PlanHours            int
	CustomerConfirmHours int
	ExecuteHours         int
}

// afterSaleSLAPolicyOf 读取售后限时配置；缺失或非法回落 §13.5 默认值（2 / 24 / 48 / 24 小时）。
func afterSaleSLAPolicyOf(ctx context.Context, db *ent.Client) afterSaleSLAPolicy {
	items, err := db.SystemConfig.Query().Where(systemconfig.KeyIn(
		afterSaleAcceptHoursKey, afterSalePlanHoursKey,
		afterSaleCustomerConfirmHoursKey, afterSaleExecuteHoursKey,
	)).Select(systemconfig.FieldKey, systemconfig.FieldValue).All(ctx)
	if err != nil {
		return afterSaleSLAPolicy{}
	}
	values := map[string]string{}
	for _, item := range items {
		values[item.Key] = strings.TrimSpace(item.Value)
	}
	parse := func(key string, fallback int) int {
		raw, ok := values[key]
		if !ok {
			return fallback
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 || value > 24*90 {
			return fallback
		}
		return value
	}
	return afterSaleSLAPolicy{
		AcceptHours:          parse(afterSaleAcceptHoursKey, 2),
		PlanHours:            parse(afterSalePlanHoursKey, 24),
		CustomerConfirmHours: parse(afterSaleCustomerConfirmHoursKey, 48),
		ExecuteHours:         parse(afterSaleExecuteHoursKey, 24),
	}
}

// afterSaleSLAStageOf 返回该状态下所处阶段与对应时限小时数；阶段不适用时返回空串。
func afterSaleSLAStageOf(item *ent.EsportsAfterSale, policy afterSaleSLAPolicy) (string, int) {
	switch item.Status {
	case esportsaftersale.StatusPendingAccept:
		return afterSaleStageAccept, policy.AcceptHours
	case esportsaftersale.StatusProcessing:
		return afterSaleStagePlan, policy.PlanHours
	case esportsaftersale.StatusPendingCustomerConfirm:
		return afterSaleStageCustomerConfirm, policy.CustomerConfirmHours
	case esportsaftersale.StatusPendingApproval, esportsaftersale.StatusPendingExecution:
		return afterSaleStageExecute, policy.ExecuteHours
	default:
		return "", 0
	}
}

// afterSaleSLAAnchor 返回当前阶段计时的起点（阶段时间戳缺失时逐级回落到 created_at）。
func afterSaleSLAAnchor(item *ent.EsportsAfterSale, stage string) time.Time {
	switch stage {
	case afterSaleStagePlan:
		if item.AcceptedAt != nil {
			return item.AcceptedAt.UTC()
		}
	case afterSaleStageCustomerConfirm:
		if item.PlannedAt != nil {
			return item.PlannedAt.UTC()
		}
	case afterSaleStageExecute:
		if item.ApprovedAt != nil {
			return item.ApprovedAt.UTC()
		}
		if item.PlannedAt != nil {
			return item.PlannedAt.UTC()
		}
	}
	return item.CreatedAt.UTC()
}

// afterSaleSLADeadline 计算当前阶段的时限与截止时间；阶段不适用或时限为 0（不限）时 ok=false。
func afterSaleSLADeadline(item *ent.EsportsAfterSale, policy afterSaleSLAPolicy) (stage string, hours int, deadline time.Time, ok bool) {
	stage, hours = afterSaleSLAStageOf(item, policy)
	if stage == "" || hours <= 0 {
		return stage, 0, time.Time{}, false
	}
	return stage, hours, afterSaleSLAAnchor(item, stage).Add(time.Duration(hours) * time.Hour), true
}

// checkAfterSaleSLABreaches 扫描进行中的售后单，对已超出当前阶段时限的写升级外盒，返回升级条数。
// 幂等：event_key = `aftersale_sla:<after_sale_no>:<stage>:<hours>`，同阶段同阈值只升级一次；
// 调整时限后会以新阈值再升级一次。
func checkAfterSaleSLABreaches(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	items, err := db.EsportsAfterSale.Query().Where(
		esportsaftersale.StatusIn(activeAfterSaleStatuses()...),
	).Order(ent.Asc(esportsaftersale.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return 0, err
	}
	if len(items) == 0 {
		return 0, nil
	}
	policy := afterSaleSLAPolicyOf(ctx, db)
	breached := 0
	for _, item := range items {
		stage, hours, deadline, ok := afterSaleSLADeadline(item, policy)
		if !ok || now.UTC().Before(deadline) {
			continue
		}
		if _, err = db.EsportsOutbox.Create().
			SetEventKey(fmt.Sprintf("aftersale_sla:%s:%s:%d", item.AfterSaleNo, stage, hours)).
			SetEventType("aftersale.sla_breach").SetAggregateRef(item.AfterSaleNo).
			SetPayload(map[string]interface{}{
				"after_sale_no": item.AfterSaleNo, "stage": stage, "limit_hours": hours,
				"deadline": deadline.Format(time.RFC3339), "status": string(item.Status),
				"source_type": string(item.SourceType), "source_id": item.SourceID,
			}).Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				continue
			}
			return breached, err
		}
		breached++
	}
	return breached, nil
}

// afterSaleView 在售后单上附加当前阶段的限时（§13.5）信息，供管理端列表 / 详情展示。
type afterSaleView struct {
	*ent.EsportsAfterSale
	SLALimitHours int        `json:"sla_limit_hours"`
	SLADeadline   *time.Time `json:"sla_deadline"`
	SLAOverdue    bool       `json:"sla_overdue"`
	// SourceTypeLabel 来源类型中文名：来源类型枚举里的 stored_value_card 已随储值卡功能下线，
	// 由后端统一给出「储值卡（已停用）」，两端无需再维护这个废弃值的中文映射。
	SourceTypeLabel string `json:"source_type_label"`
}

// afterSaleSourceTypeLabels 来源类型的中文名（含已废弃但仍需可读的 stored_value_card）。
var afterSaleSourceTypeLabels = map[string]string{
	"order":             "服务订单",
	"gift":              "礼物单",
	"recharge":          "钱包充值",
	"assessment":        "考核记录",
	"stored_value_card": "储值卡（已停用）",
}

// afterSaleSourceTypeLabel 返回来源类型中文名；未知类型回落原始值，不隐藏数据。
func afterSaleSourceTypeLabel(sourceType string) string {
	if label, ok := afterSaleSourceTypeLabels[sourceType]; ok {
		return label
	}
	return sourceType
}

// afterSaleViewsOf 批量包装售后单并计算限时信息（一次读取策略，避免逐条查配置）。
func afterSaleViewsOf(ctx context.Context, db *ent.Client, items []*ent.EsportsAfterSale, now time.Time) []afterSaleView {
	policy := afterSaleSLAPolicyOf(ctx, db)
	views := make([]afterSaleView, 0, len(items))
	for _, item := range items {
		views = append(views, afterSaleViewOf(item, policy, now))
	}
	return views
}

// afterSaleViewOf 包装单条售后单并计算限时信息。
func afterSaleViewOf(item *ent.EsportsAfterSale, policy afterSaleSLAPolicy, now time.Time) afterSaleView {
	view := afterSaleView{EsportsAfterSale: item, SourceTypeLabel: afterSaleSourceTypeLabel(item.SourceType.String())}
	if _, hours, deadline, ok := afterSaleSLADeadline(item, policy); ok {
		at := deadline
		view.SLALimitHours = hours
		view.SLADeadline = &at
		view.SLAOverdue = now.UTC().After(deadline)
	}
	return view
}
