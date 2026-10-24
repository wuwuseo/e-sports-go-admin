package esports

import (
	"context"
	"fmt"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsaftersaleaction"
)

// 客户侧逐项进度：一张售后单可挂多条动作项（退款 / 补偿 / 补钟 / 换人），
// 客户必须能看到「每一项各自走到哪一步」，而不是只看售后单的笼统状态。
//
// 关键口径：**未执行完成的动作项一律显示为「待执行」，绝不显示成「已到账」**——
// 聚合状态 settlement_state 只有在没有任何待执行 / 失败项时才可能为 completed，
// 且 refunded_cents 只累计已成功（succeeded）的资金类动作项，待执行金额单独放在 pending_cents。

// afterSaleActionProgressItem 是给客户看的单个动作项进度。
type afterSaleActionProgressItem struct {
	Seq           int        `json:"seq"`
	ActionType    string     `json:"action_type"`
	ActionLabel   string     `json:"action_label"`
	Status        string     `json:"status"`
	StatusLabel   string     `json:"status_label"`
	AmountCents   int64      `json:"amount_cents"`
	ResultSummary string     `json:"result_summary"`
	ExecutedAt    *time.Time `json:"executed_at"`
}

// afterSaleCustomerProgress 是客户侧售后进度视图。
type afterSaleCustomerProgress struct {
	AfterSaleID int    `json:"after_sale_id"`
	AfterSaleNo string `json:"after_sale_no"`
	Status      string `json:"status"`
	// SettlementState：not_started / pending_execution / partially_completed / completed /
	// failed / rejected / withdrawn。
	SettlementState string `json:"settlement_state"`
	SettlementLabel string `json:"settlement_label"`
	// RefundedCents 只统计已成功执行的动作项金额；PendingCents 是待执行金额（明确区分「待执行」与「已到账」）。
	RefundedCents int64                         `json:"refunded_cents"`
	PendingCents  int64                         `json:"pending_cents"`
	Progress      map[string]int                `json:"progress"`
	Actions       []afterSaleActionProgressItem `json:"actions"`
}

// afterSaleCustomerProgressOf 组装某售后单的客户侧逐项进度；非本人售后单返回 errForbidden。
func afterSaleCustomerProgressOf(ctx context.Context, db *ent.Client, customerID, afterSaleID int) (*afterSaleCustomerProgress, error) {
	if customerID < 1 || afterSaleID < 1 {
		return nil, errInvalidInput
	}
	item, err := db.EsportsAfterSale.Get(ctx, afterSaleID)
	if err != nil {
		return nil, err
	}
	if item.CustomerID == nil || *item.CustomerID != customerID {
		return nil, errForbidden
	}
	actions, err := listAfterSaleActions(ctx, db, afterSaleID, 200)
	if err != nil {
		return nil, err
	}
	progress := afterSaleCustomerProgress{
		AfterSaleID: item.ID, AfterSaleNo: item.AfterSaleNo, Status: item.Status.String(),
		Progress: afterSaleActionProgressOf(actions),
		Actions:  make([]afterSaleActionProgressItem, 0, len(actions)),
	}
	for _, action := range actions {
		entry := afterSaleActionProgressItem{
			Seq: action.Seq, ActionType: action.ActionType.String(),
			ActionLabel: afterSaleActionLabel(action.ActionType.String()),
			Status:      action.Status.String(), StatusLabel: afterSaleActionStatusLabel(action.Status.String()),
			AmountCents: action.AmountCents, ResultSummary: action.ResultSummary,
		}
		if action.ExecutedAt != nil {
			at := action.ExecutedAt.UTC()
			entry.ExecutedAt = &at
		}
		progress.Actions = append(progress.Actions, entry)
	}
	progress.SettlementState, progress.SettlementLabel, progress.RefundedCents, progress.PendingCents =
		afterSaleCustomerSettlement(item, actions)
	return &progress, nil
}

// afterSaleCustomerSettlement 汇总客户侧结算口径。
// 逐项进度为空时回落到既有单动作字段（plan_action + refund_cents + 售后单状态）。
func afterSaleCustomerSettlement(item *ent.EsportsAfterSale, actions []*ent.EsportsAfterSaleAction) (string, string, int64, int64) {
	if len(actions) == 0 {
		funding := item.PlanAction == esportsaftersale.PlanActionRefundOriginal ||
			item.PlanAction == esportsaftersale.PlanActionCompensate
		switch item.Status {
		case esportsaftersale.StatusCompleted:
			var refunded int64
			if funding {
				refunded = item.RefundCents
			}
			return "completed", "已全部完成", refunded, 0
		case esportsaftersale.StatusExecutionFailed:
			var pending int64
			if funding {
				pending = item.RefundCents
			}
			return "failed", "执行失败，客服正在处理", 0, pending
		case esportsaftersale.StatusRejected:
			return "rejected", "已驳回", 0, 0
		case esportsaftersale.StatusWithdrawn:
			return "withdrawn", "已撤回", 0, 0
		case esportsaftersale.StatusPendingApproval, esportsaftersale.StatusPendingExecution:
			var pending int64
			if funding {
				pending = item.RefundCents
			}
			return "pending_execution", "待执行（尚未到账）", 0, pending
		default:
			return "not_started", "处理中（方案尚未开始执行）", 0, 0
		}
	}
	succeeded, failed, pendingCount := 0, 0, 0
	var refunded, pending int64
	for _, action := range actions {
		funding := afterSaleActionIsFunding(action.ActionType.String())
		switch action.Status {
		case esportsaftersaleaction.StatusSucceeded:
			succeeded++
			if funding {
				refunded += action.AmountCents
			}
		case esportsaftersaleaction.StatusFailed:
			failed++
			if funding {
				pending += action.AmountCents
			}
		default:
			// pending / executing / awaiting_execution（外部渠道退款已登记待财务执行）都算「待执行」：
			// 钱未到账，既不能显示成已到账，也不能算失败。
			pendingCount++
			if funding {
				pending += action.AmountCents
			}
		}
	}
	total := succeeded + failed + pendingCount
	switch {
	case failed > 0 && succeeded > 0:
		return "partially_completed",
			fmt.Sprintf("部分完成（已完成 %d / 共 %d 项，%d 项失败）", succeeded, total, failed), refunded, pending
	case failed > 0:
		return "failed", fmt.Sprintf("执行失败（%d 项失败，客服正在处理）", failed), refunded, pending
	case pendingCount > 0 && succeeded > 0:
		return "partially_completed",
			fmt.Sprintf("部分完成（已完成 %d / 共 %d 项，%d 项待执行）", succeeded, total, pendingCount), refunded, pending
	case pendingCount > 0:
		return "pending_execution", "待执行（尚未到账）", refunded, pending
	default:
		return "completed", "已全部完成", refunded, pending
	}
}

// afterSaleActionStatusLabel 动作项状态的中文文案。
func afterSaleActionStatusLabel(status string) string {
	switch status {
	case string(esportsaftersaleaction.StatusPending):
		return "待执行"
	case string(esportsaftersaleaction.StatusExecuting):
		return "执行中"
	case string(esportsaftersaleaction.StatusAwaitingExecution):
		// 外部渠道退款已登记、钱未出账：语义是「已登记待执行」，不能显示成「已执行」。
		return "待执行（外部退款）"
	case string(esportsaftersaleaction.StatusSucceeded):
		return "已执行"
	case string(esportsaftersaleaction.StatusFailed):
		return "执行失败"
	}
	return status
}

// afterSaleActionLabel 动作类型的中文文案。
func afterSaleActionLabel(actionType string) string {
	switch actionType {
	case afterSaleActionRefund:
		return "退款"
	case afterSaleActionCompensate:
		return "补偿"
	case afterSaleActionMakeup:
		return "补钟"
	case afterSaleActionReplace:
		return "换人"
	}
	return actionType
}
