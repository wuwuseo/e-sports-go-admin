package esports

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsaftersaleaction"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
)

// ar4ActionStatus 取客户侧进度里某个动作项的状态文案。
func ar4ActionStatus(t *testing.T, progress *afterSaleCustomerProgress, seq int) afterSaleActionProgressItem {
	t.Helper()
	for _, action := range progress.Actions {
		if action.Seq == seq {
			return action
		}
	}
	t.Fatalf("进度里缺少动作项 #%d：%+v", seq, progress.Actions)
	return afterSaleActionProgressItem{}
}

// ：客户侧必须能按动作项逐项看到进度，且**不得把「待执行」显示成「已到账」**。
func TestCustomerProgressShowsPerActionStatus(t *testing.T) {
	db := openAfterSaleDB(t, "esports_r4w2_progress_actions")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

	order := seedBalancePaidOrder(t, db, "r4w2-progress")
	player := seedPlayer(t, db, 7401, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	planned := newMultiActionAfterSale(t, db, order.ID, "refund_request", 11, []afterSaleActionInput{
		{ActionType: afterSaleActionRefund, AmountCents: 500},
		{ActionType: afterSaleActionRefund, AmountCents: 300},
		{ActionType: afterSaleActionMakeup, PlayerID: player.ID, SeatIndex: 1, Units: 1},
	}, now)
	// #1 已成功到账；#2 待执行；#3 待执行（补钟类）。
	db.EsportsAfterSaleAction.Update().Where(
		esportsaftersaleaction.AfterSaleIDEQ(planned.ID),
		esportsaftersaleaction.SeqEQ(1),
	).SetStatus(esportsaftersaleaction.StatusSucceeded).
		SetResultSummary("已退回 500 分").
		SetExecutedAt(now.Add(time.Minute)).ExecX(ctx)

	if planned.CustomerID == nil {
		t.Fatal("测试售后单应绑定客户")
	}
	progress, err := afterSaleCustomerProgressOf(ctx, db, *planned.CustomerID, planned.ID)
	if err != nil {
		t.Fatalf("客户侧进度查询失败：%v", err)
	}
	if len(progress.Actions) != 3 {
		t.Fatalf("客户侧应看到 3 个动作项，实际 %d", len(progress.Actions))
	}
	first := ar4ActionStatus(t, progress, 1)
	if first.StatusLabel != "已执行" || first.ActionLabel != "退款" || first.AmountCents != 500 {
		t.Fatalf("动作项 #1 进度错误：%+v", first)
	}
	if first.ExecutedAt == nil || first.ResultSummary != "已退回 500 分" {
		t.Fatalf("动作项 #1 应带执行时间与结果摘要：%+v", first)
	}
	second := ar4ActionStatus(t, progress, 2)
	if second.StatusLabel != "待执行" {
		t.Fatalf("未执行的动作项必须显示「待执行」，实际 %q", second.StatusLabel)
	}
	third := ar4ActionStatus(t, progress, 3)
	if third.StatusLabel != "待执行" || third.ActionLabel != "补钟" {
		t.Fatalf("补钟动作项进度错误：%+v", third)
	}
	// 聚合口径：只把已成功的计入已到账，待执行的单独列示。
	if progress.RefundedCents != 500 || progress.PendingCents != 300 {
		t.Fatalf("已到账 / 待执行金额应分别为 500 / 300，实际 %d / %d",
			progress.RefundedCents, progress.PendingCents)
	}
	if progress.SettlementState != "partially_completed" {
		t.Fatalf("部分完成时聚合状态应为 partially_completed，实际 %q", progress.SettlementState)
	}
	if strings.Contains(progress.SettlementLabel, "已到账") || strings.Contains(progress.SettlementLabel, "已退款") {
		t.Fatalf("存在待执行项时不得显示为已到账 / 已退款，实际 %q", progress.SettlementLabel)
	}
	if progress.Progress["total"] != 3 || progress.Progress["succeeded"] != 1 || progress.Progress["pending"] != 2 {
		t.Fatalf("逐项进度汇总错误：%+v", progress.Progress)
	}
}

// ：全部待执行时聚合状态必须是 pending_execution（未到账），全部成功后才是 completed。
func TestCustomerProgressNeverClaimsSettlementBeforeExecution(t *testing.T) {
	db := openAfterSaleDB(t, "esports_r4w2_progress_pending")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	order := seedBalancePaidOrder(t, db, "r4w2-progress-pending")
	planned := newMultiActionAfterSale(t, db, order.ID, "refund_request", 11, []afterSaleActionInput{
		{ActionType: afterSaleActionRefund, AmountCents: 700},
	}, now)
	if planned.CustomerID == nil {
		t.Fatal("测试售后单应绑定客户")
	}
	pending, err := afterSaleCustomerProgressOf(ctx, db, *planned.CustomerID, planned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pending.SettlementState != "pending_execution" || pending.RefundedCents != 0 || pending.PendingCents != 700 {
		t.Fatalf("待执行阶段不得计入已到账：%+v", pending)
	}
	if !strings.Contains(pending.SettlementLabel, "待执行") {
		t.Fatalf("待执行阶段文案错误：%q", pending.SettlementLabel)
	}
	if ar4ActionStatus(t, pending, 1).StatusLabel != "待执行" {
		t.Fatalf("待执行动作项文案错误：%+v", pending.Actions)
	}

	// 执行失败 → 明示失败且金额仍不计入已到账。
	db.EsportsAfterSaleAction.Update().Where(
		esportsaftersaleaction.AfterSaleIDEQ(planned.ID),
		esportsaftersaleaction.SeqEQ(1),
	).SetStatus(esportsaftersaleaction.StatusFailed).SetResultSummary("渠道失败").ExecX(ctx)
	failed, err := afterSaleCustomerProgressOf(ctx, db, *planned.CustomerID, planned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.SettlementState != "failed" || failed.RefundedCents != 0 || failed.PendingCents != 700 {
		t.Fatalf("执行失败时不应计入已到账：%+v", failed)
	}
	if ar4ActionStatus(t, failed, 1).StatusLabel != "执行失败" {
		t.Fatalf("失败动作项文案错误：%+v", failed.Actions)
	}

	// 全部成功 → completed，金额计入已到账。
	db.EsportsAfterSaleAction.Update().Where(
		esportsaftersaleaction.AfterSaleIDEQ(planned.ID),
		esportsaftersaleaction.SeqEQ(1),
	).SetStatus(esportsaftersaleaction.StatusSucceeded).ExecX(ctx)
	done, err := afterSaleCustomerProgressOf(ctx, db, *planned.CustomerID, planned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.SettlementState != "completed" || done.RefundedCents != 700 || done.PendingCents != 0 {
		t.Fatalf("全部成功后才可显示已到账：%+v", done)
	}
}

// ：单动作（无动作项）售后单回落到既有字段口径，待执行同样不得显示为已到账。
func TestCustomerProgressSingleActionFallback(t *testing.T) {
	db := openAfterSaleDB(t, "esports_r4w2_progress_single")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	order := ar4SeedOrder(t, db, "PROG-SINGLE", 2000, 2000, 0, 0, 0, 1,
		esportsorder.FulfillmentStatusPendingDispatch, nil)
	item := ar4SeedAfterSale(t, db, order, order.CustomerID, "refund_request", "",
		esportsaftersale.StatusPendingExecution, now)
	db.EsportsAfterSale.UpdateOneID(item.ID).
		SetPlanAction(esportsaftersale.PlanActionRefundOriginal).SetRefundCents(600).ExecX(ctx)

	progress, err := afterSaleCustomerProgressOf(ctx, db, order.CustomerID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if progress.SettlementState != "pending_execution" || progress.PendingCents != 600 || progress.RefundedCents != 0 {
		t.Fatalf("单动作待执行阶段不得显示为已到账：%+v", progress)
	}
	db.EsportsAfterSale.UpdateOneID(item.ID).SetStatus(esportsaftersale.StatusCompleted).ExecX(ctx)
	progress, err = afterSaleCustomerProgressOf(ctx, db, order.CustomerID, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if progress.SettlementState != "completed" || progress.RefundedCents != 600 || progress.PendingCents != 0 {
		t.Fatalf("单动作完成后应计入已到账：%+v", progress)
	}

	// 越权访问明确拒绝。
	if _, err = afterSaleCustomerProgressOf(ctx, db, order.CustomerID+99, item.ID); !errors.Is(err, errForbidden) {
		t.Fatalf("非本人售后单查询进度应被拒，实际 err=%v", err)
	}
}
