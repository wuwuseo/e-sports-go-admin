package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsplayer"
)

// P0-9 多动作口径：带动作项的售后单从 execution_failed 可由人工续跑，执行失败时保留预占；
// 但人工撤回终结时必须释放「尚未成功」的退款动作项额度，不把额度永久占住。
func TestAfterSaleActionsFailedWithdrawReleasesHeldReservation(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_actions_withdraw_release")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	// 无余额核销流水的订单：退款动作项必然失败，补钟动作项成功 → 部分成功 execution_failed。
	order := seedPaidOrder(t, db, "as-actions-withdraw", 1)
	player := seedPlayer(t, db, 7501, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	planned := newMultiActionAfterSale(t, db, order.ID, "refund_request", 11, []afterSaleActionInput{
		{ActionType: "refund", AmountCents: 500, SourceRef: "order:" + order.OrderNo},
		{ActionType: "makeup", PlayerID: player.ID, SeatIndex: 1, Units: 1, CompanyPayCents: 200},
	}, now)
	if _, err := approveAfterSale(ctx, db, planned.ID, 12, now); err != nil {
		t.Fatalf("多动作方案审批失败：%v", err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundReservedCents != 500 {
		t.Fatalf("多动作方案审批未预占退款额度：%+v", got)
	}
	done, err := executeAfterSale(ctx, db, planned.ID, 12, now)
	if err != nil || done.Status != esportsaftersale.StatusExecutionFailed {
		t.Fatalf("部分成功的多动作执行应落 execution_failed：%v %+v", err, done)
	}
	// 可续跑，故失败项预占保留待人工处理。
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundReservedCents != 500 {
		t.Fatalf("多动作失败项不应在失败时释放预占：%+v", got)
	}
	if _, err := withdrawAfterSale(ctx, db, planned.ID, 11, now); err != nil {
		t.Fatalf("撤回多动作 execution_failed 售后失败：%v", err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundReservedCents != 0 || got.RefundedCents != 0 {
		t.Fatalf("撤回多动作售后未清退预占额度：reserved=%d refunded=%d",
			got.RefundReservedCents, got.RefundedCents)
	}
}
