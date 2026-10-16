package esports

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsaftersaleaction"
	"go-admin/internal/ent/esportsfulfillmentmakeup"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportswallettx"
)

// newMultiActionAfterSale 构造一张已受理、已提交多动作方案并进入待审批的售后单（§13.3 / §13.6）。
func newMultiActionAfterSale(t *testing.T, db *ent.Client, orderID int, kind string, handler int,
	actions []afterSaleActionInput, now time.Time) *ent.EsportsAfterSale {
	t.Helper()
	ctx := t.Context()
	created, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "order", SourceID: orderID, Kind: kind, Channel: "customer",
		Demand: "客户反馈服务问题，申请多动作处理", ExpectedPlan: "退款并补钟",
	}, handler, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acceptAfterSale(ctx, db, created.ID, handler, now); err != nil {
		t.Fatal(err)
	}
	if _, err := submitAfterSalePlan(ctx, db, created.ID, handler, afterSalePlanInput{
		FaultParty: "player", Resolution: "退款并补钟", Actions: actions,
	}, now); err != nil {
		t.Fatal(err)
	}
	planned, err := confirmAfterSaleByAdmin(ctx, db, created.ID, handler, now)
	if err != nil {
		t.Fatal(err)
	}
	return planned
}

// 多动作方案（退款 + 补钟）：提交即写入两条动作项并给出聚合摘要；审批预占总额；
// 执行逐项落账，退款写按项来源号的流水、补钟创建补履约任务，全部成功 → 既有终态 completed。
func TestAfterSaleActionsPlanSubmitAndExecute(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_actions_flow")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	order := seedBalancePaidOrder(t, db, "actions-flow")
	player := seedPlayer(t, db, 7301, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	planned := newMultiActionAfterSale(t, db, order.ID, "refund_request", 11, []afterSaleActionInput{
		{ActionType: "refund", AmountCents: 500, SourceRef: "order:" + order.OrderNo},
		{ActionType: "makeup", PlayerID: player.ID, SeatIndex: 1, Units: 2, CompanyPayCents: 300},
	}, now)
	if planned.Status != esportsaftersale.StatusPendingApproval {
		t.Fatalf("多动作方案代客户确认后状态不正确：%+v", planned)
	}
	// 聚合摘要：refund_cents = 资金类动作之和，plan_action 取主导类型。
	if planned.RefundCents != 500 || planned.PlanAction != esportsaftersale.PlanActionRefundOriginal {
		t.Fatalf("多动作方案聚合摘要不正确：%+v", planned)
	}

	actions, err := listAfterSaleActions(ctx, db, planned.ID, 100)
	if err != nil || len(actions) != 2 {
		t.Fatalf("动作项写入不正确：%v %+v", err, actions)
	}
	if actions[0].Seq != 1 || actions[0].ActionType != esportsaftersaleaction.ActionTypeRefund ||
		actions[0].AmountCents != 500 || actions[0].Status != esportsaftersaleaction.StatusPending {
		t.Fatalf("退款动作项不正确：%+v", actions[0])
	}
	wantIdem := fmt.Sprintf("aftersale:%d:action:1:order:%s", planned.ID, order.OrderNo)
	if actions[0].IdemKey != wantIdem {
		t.Fatalf("按项幂等键不正确：got %s want %s", actions[0].IdemKey, wantIdem)
	}
	if actions[1].ActionType != esportsaftersaleaction.ActionTypeMakeup ||
		actions[1].PlayerID == nil || *actions[1].PlayerID != player.ID || actions[1].Units != 2 {
		t.Fatalf("补钟动作项不正确：%+v", actions[1])
	}

	// 审批预占资金类动作总额。
	if _, err = approveAfterSale(ctx, db, planned.ID, 12, now); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundReservedCents != 500 {
		t.Fatalf("多动作方案审批预占不正确：%+v", got)
	}

	done, err := executeAfterSale(ctx, db, planned.ID, 12, now)
	if err != nil || done.Status != esportsaftersale.StatusCompleted {
		t.Fatalf("多动作执行失败：%v %+v", err, done)
	}
	gotOrder := db.EsportsOrder.GetX(ctx, order.ID)
	if gotOrder.RefundedCents != 500 || gotOrder.RefundReservedCents != 0 ||
		gotOrder.RefundStatus != esportsorder.RefundStatusPartial {
		t.Fatalf("订单退款额度落账不正确：%+v", gotOrder)
	}
	// 退款流水按项来源号写入（<after_sale_no>:a1），与既有单动作（原售后单号）互不覆盖。
	refundTx := db.EsportsWalletTx.Query().Where(
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeRefundReturn)).OnlyX(ctx)
	if refundTx.SourceID != planned.AfterSaleNo+":a1" {
		t.Fatalf("按项退款流水来源号不正确：%+v", refundTx)
	}
	task := db.EsportsFulfillmentMakeup.Query().OnlyX(ctx)
	if task.AfterSaleID != planned.ID || task.Kind != esportsfulfillmentmakeup.KindMakeupTime ||
		task.PlayerID != player.ID || task.Units != 2 || task.CompanyPayCents != 300 {
		t.Fatalf("补履约任务创建不正确：%+v", task)
	}
	actions = db.EsportsAfterSaleAction.Query().Order(ent.Asc(esportsaftersaleaction.FieldSeq)).AllX(ctx)
	progress := afterSaleActionProgressOf(actions)
	if progress["succeeded"] != 2 || progress["failed"] != 0 {
		t.Fatalf("逐项进度不正确：%+v", progress)
	}
	counts := countEventsByType(t, db)
	if counts["order.after_sale_action_executed"] != 2 || counts["order.after_sale_actions_completed"] != 1 {
		t.Fatalf("多动作事件留痕不正确：%+v", counts)
	}
}

// 部分成功：一条资金动作因缺少原核销流水执行失败、一条补钟成功，
// 售后单落为「部分执行完成 / 需人工处理」（execution_failed）而非静默全成功或全失败，并写事件与外盒留痕。
func TestAfterSaleActionsPartialSuccess(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_actions_partial")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	// 未走余额核销的订单：退款动作执行时缺少 consume 流水（确定性失败）。
	order := seedPaidOrder(t, db, "as-actions-partial", 1)
	player := seedPlayer(t, db, 7302, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	planned := newMultiActionAfterSale(t, db, order.ID, "duration_short", 11, []afterSaleActionInput{
		{ActionType: "refund", AmountCents: 400},
		{ActionType: "makeup", PlayerID: player.ID, SeatIndex: 1, Units: 1},
	}, now)
	if _, err := approveAfterSale(ctx, db, planned.ID, 12, now); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundReservedCents != 400 {
		t.Fatalf("审批未预占退款额度：%+v", got)
	}

	done, err := executeAfterSale(ctx, db, planned.ID, 12, now)
	if err != nil {
		t.Fatalf("部分成功不应返回执行错误：%v", err)
	}
	if done.Status != esportsaftersale.StatusExecutionFailed {
		t.Fatalf("部分成功总状态不正确：%+v", done)
	}
	if !strings.Contains(done.Resolution, "部分动作项执行失败") ||
		!strings.Contains(done.Resolution, "需人工处理") {
		t.Fatalf("部分成功未给出人工处理说明：%s", done.Resolution)
	}
	// 失败项不落账：退款额度仍预占（待人工处理），补钟项成功创建任务。
	gotOrder := db.EsportsOrder.GetX(ctx, order.ID)
	if gotOrder.RefundedCents != 0 || gotOrder.RefundReservedCents != 400 {
		t.Fatalf("失败项不应落账：%+v", gotOrder)
	}
	actions := db.EsportsAfterSaleAction.Query().Order(ent.Asc(esportsaftersaleaction.FieldSeq)).AllX(ctx)
	if actions[0].Status != esportsaftersaleaction.StatusFailed || actions[0].ResultSummary == "" {
		t.Fatalf("失败动作项未记录结果摘要：%+v", actions[0])
	}
	if actions[1].Status != esportsaftersaleaction.StatusSucceeded {
		t.Fatalf("成功动作项状态不正确：%+v", actions[1])
	}
	progress := afterSaleActionProgressOf(actions)
	if progress["succeeded"] != 1 || progress["failed"] != 1 {
		t.Fatalf("部分成功进度不正确：%+v", progress)
	}
	if db.EsportsFulfillmentMakeup.Query().CountX(ctx) != 1 {
		t.Fatal("补钟动作项未创建补履约任务")
	}
	counts := countEventsByType(t, db)
	if counts["order.after_sale_action_failed"] != 1 || counts["order.after_sale_action_executed"] != 1 ||
		counts["order.after_sale_actions_partially_executed"] != 1 {
		t.Fatalf("部分成功事件留痕不正确：%+v", counts)
	}
	if !db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("aftersale.actions_partially_executed")).ExistX(ctx) {
		t.Fatal("部分成功缺少外盒留痕")
	}
}

// 按项幂等：重复执行同一动作项不重复落账；已成功项在整体仍可执行时被按项幂等拦截。
func TestAfterSaleActionsPerItemIdempotency(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_actions_idem")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	order := seedBalancePaidOrder(t, db, "actions-idem")
	// 两条退款动作项：先执行第 1 项，整体因第 2 项未完成而处于 execution_failed，
	// 此时仍可单独重试第 1 项，验证「按项」幂等而非仅整体状态拦截。
	planned := newMultiActionAfterSale(t, db, order.ID, "refund_request", 11, []afterSaleActionInput{
		{ActionType: "refund", AmountCents: 300},
		{ActionType: "refund", AmountCents: 200},
	}, now)
	if _, err := approveAfterSale(ctx, db, planned.ID, 12, now); err != nil {
		t.Fatal(err)
	}

	first, err := executeAfterSaleActionSeq(ctx, db, planned.ID, 1, 12, now)
	if err != nil || first.Status != esportsaftersaleaction.StatusSucceeded {
		t.Fatalf("逐项执行第 1 项失败：%v %+v", err, first)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundedCents != 300 || got.RefundReservedCents != 200 {
		t.Fatalf("第 1 项落账不正确：%+v", got)
	}
	if got := db.EsportsAfterSale.GetX(ctx, planned.ID); got.Status != esportsaftersale.StatusExecutionFailed {
		t.Fatalf("部分执行后总状态应为异常态：%+v", got)
	}
	refundTxCount := func() int {
		return db.EsportsWalletTx.Query().Where(
			esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeRefundReturn)).CountX(ctx)
	}
	if refundTxCount() != 1 {
		t.Fatalf("退款流水条数不正确：%d", refundTxCount())
	}

	// 重复执行第 1 项：被按项幂等拦截，不重复落账。
	if _, err := executeAfterSaleActionSeq(ctx, db, planned.ID, 1, 12, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复执行同一动作项未被拦截：%v", err)
	}
	if refundTxCount() != 1 {
		t.Fatalf("重复执行产生了重复落账：%d", refundTxCount())
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundedCents != 300 {
		t.Fatalf("重复执行改变了已退金额：%+v", got)
	}

	// 执行剩余项后整体完成；再对已完成项重复执行仍被拦截。
	if _, err := executeAfterSaleActionSeq(ctx, db, planned.ID, 2, 12, now); err != nil {
		t.Fatalf("逐项执行第 2 项失败：%v", err)
	}
	if got := db.EsportsAfterSale.GetX(ctx, planned.ID); got.Status != esportsaftersale.StatusCompleted {
		t.Fatalf("全部动作项完成后总状态不正确：%+v", got)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundedCents != 500 || got.RefundReservedCents != 0 {
		t.Fatalf("逐项退款累计不正确：%+v", got)
	}
	if refundTxCount() != 2 {
		t.Fatalf("两条动作项应各写一条流水：%d", refundTxCount())
	}
	if _, err := executeAfterSaleActionSeq(ctx, db, planned.ID, 2, 12, now); !errors.Is(err, errConflict) {
		t.Fatalf("已完成项被重复执行：%v", err)
	}
	if refundTxCount() != 2 {
		t.Fatalf("已完成项重复执行产生重复落账：%d", refundTxCount())
	}
}

// 审批口径：含资金动作的多动作方案沿用「审批与执行分人、不得自审」；纯补履约方案不占退款额度。
func TestAfterSaleActionsApprovalSeparation(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_actions_approval")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	order := seedBalancePaidOrder(t, db, "actions-approval")
	player := seedPlayer(t, db, 7303, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	// 含资金动作：自审被拦截，异人审批通过并预占总额。
	funding := newMultiActionAfterSale(t, db, order.ID, "refund_request", 11, []afterSaleActionInput{
		{ActionType: "refund", AmountCents: 600},
		{ActionType: "makeup", PlayerID: player.ID, SeatIndex: 1, Units: 1},
	}, now)
	if _, err := approveAfterSale(ctx, db, funding.ID, 11, now); !errors.Is(err, errForbidden) {
		t.Fatalf("含资金动作的多动作方案自审未被拦截：%v", err)
	}
	if _, err := approveAfterSale(ctx, db, funding.ID, 12, now); err != nil {
		t.Fatalf("异人审批失败：%v", err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundReservedCents != 600 {
		t.Fatalf("含资金动作方案审批未预占总额：%+v", got)
	}
}
