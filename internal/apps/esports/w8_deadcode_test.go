package esports

import (
	"strings"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersaleaction"
	"go-admin/internal/ent/esportsdispatchseat"
)

// 本文件锁定「清零残留」批次（审核报告 §B.3-4）的清理结论：
//  1. 售后动作项状态不再含 skipped（全仓从未写入），枚举校验必须拒绝；
//  2. 派单席位来源不再含 temp_team（从未写入），枚举校验必须拒绝；
//  3. awaiting_execution（外部渠道退款已登记待执行）有中文标签，且在进度聚合里按「待执行」而不是「失败」处理。

// TestAfterSaleActionStatusRejectsSkipped 锁定动作项状态枚举：skipped 已删除，awaiting_execution 仍可写。
func TestAfterSaleActionStatusRejectsSkipped(t *testing.T) {
	db := openAfterSaleDB(t, "esports_deadcode_action_status")
	defer db.Close()
	ctx := t.Context()
	createWith := func(seq int, status string) error {
		_, err := db.EsportsAfterSaleAction.Create().
			SetAfterSaleID(1).SetSeq(seq).
			SetActionType(esportsaftersaleaction.ActionTypeRefund).
			SetIdemKey("w8-deadcode-action-" + status).
			SetStatus(esportsaftersaleaction.Status(status)).
			Save(ctx)
		return err
	}
	if err := createWith(1, "skipped"); !ent.IsValidationError(err) {
		t.Fatalf("skipped 已不是合法动作项状态，应被枚举校验拒绝，实际 err=%v", err)
	}
	if err := createWith(2, string(esportsaftersaleaction.StatusAwaitingExecution)); err != nil {
		t.Fatalf("awaiting_execution 应可正常写入：%v", err)
	}
}

// TestDispatchSeatSourceRejectsTempTeam 锁定席位来源枚举：temp_team 已删除，固定队伍来源仍可写。
func TestDispatchSeatSourceRejectsTempTeam(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_deadcode_seat_source")
	defer db.Close()
	ctx := t.Context()
	createWith := func(seatIndex int, source string) error {
		_, err := db.EsportsDispatchSeat.Create().
			SetOrderID(1).SetSeatIndex(seatIndex).
			SetSource(esportsdispatchseat.Source(source)).
			Save(ctx)
		return err
	}
	if err := createWith(1, "temp_team"); !ent.IsValidationError(err) {
		t.Fatalf("temp_team 已不是合法席位来源，应被枚举校验拒绝，实际 err=%v", err)
	}
	if err := createWith(2, string(esportsdispatchseat.SourceFixedTeam)); err != nil {
		t.Fatalf("fixed_team 应可正常写入：%v", err)
	}
}

// TestAwaitingExecutionStatusLabelAndProgress 锁定 awaiting_execution 的文案与进度口径：
// 中文标签为「待执行（外部退款）」，聚合状态为 pending_execution（不是 failed），金额只进待执行、不进已退款。
func TestAwaitingExecutionStatusLabelAndProgress(t *testing.T) {
	if got := afterSaleActionStatusLabel(string(esportsaftersaleaction.StatusAwaitingExecution)); got != "待执行（外部退款）" {
		t.Fatalf("awaiting_execution 中文标签应为「待执行（外部退款）」，实际 %q", got)
	}
	// 标签不能退化成英文原值或「已执行」。
	if got := afterSaleActionStatusLabel(string(esportsaftersaleaction.StatusAwaitingExecution)); got == "awaiting_execution" ||
		got == "已执行" {
		t.Fatalf("awaiting_execution 标签不得为英文原值或已执行，实际 %q", got)
	}

	awaiting := []*ent.EsportsAfterSaleAction{{
		Seq: 1, ActionType: esportsaftersaleaction.ActionTypeRefund,
		Status: esportsaftersaleaction.StatusAwaitingExecution, AmountCents: 3000,
	}}
	progress := afterSaleActionProgressOf(awaiting)
	if progress["awaiting_execution"] != 1 || progress["failed"] != 0 || progress["pending"] != 0 {
		t.Fatalf("awaiting_execution 应单独计数且不算失败，实际 %+v", progress)
	}
	state, label, refunded, pendingCents := afterSaleCustomerSettlement(&ent.EsportsAfterSale{}, awaiting)
	if state != "pending_execution" {
		t.Fatalf("仅含待执行外部退款时聚合状态应为 pending_execution（不得判失败），实际 %q", state)
	}
	if refunded != 0 || pendingCents != 3000 {
		t.Fatalf("未出账金额不得计入已退款：refunded=%d pending=%d", refunded, pendingCents)
	}
	if !strings.Contains(label, "待执行") || strings.Contains(label, "失败") {
		t.Fatalf("聚合文案应表达待执行且不含失败，实际 %q", label)
	}

	// 已成功 + 待执行 → 部分完成；不得因 awaiting_execution 落 failed。
	mixed := append(append([]*ent.EsportsAfterSaleAction{}, awaiting...), &ent.EsportsAfterSaleAction{
		Seq: 2, ActionType: esportsaftersaleaction.ActionTypeMakeup,
		Status: esportsaftersaleaction.StatusSucceeded,
	})
	state, _, refunded, pendingCents = afterSaleCustomerSettlement(&ent.EsportsAfterSale{}, mixed)
	if state != "partially_completed" || refunded != 0 || pendingCents != 3000 {
		t.Fatalf("成功 + 待执行应为 partially_completed 且金额口径正确，实际 state=%q refunded=%d pending=%d",
			state, refunded, pendingCents)
	}
}
