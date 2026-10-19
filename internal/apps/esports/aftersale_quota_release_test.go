package esports

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsledgerentry"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsstoredvaluecard"
	"go-admin/internal/ent/esportswalletaccount"
	"go-admin/internal/ent/esportswallettx"
)

// seedCardPaidOrderForAfterSale 构造一张以储值卡核销的已付款订单（应付 1000，卡内本金 3000 / 赠送 1000），
// 返回订单与卡 ID；卡核销按先赠送后本金，故消费流水的构成为赠送 1000。
func seedCardPaidOrderForAfterSale(t *testing.T, db *ent.Client, suffix string) (*ent.EsportsOrder, int) {
	t.Helper()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	customer := saveCustomerOrFail(t, db, 11)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "AS-CARD-" + suffix, Title: "储值卡售后退款测试商品", ServiceType: "play",
		UnitPriceCents: 1000, ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1,
		QuotaTotal: 10, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	card, err := openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "merchant_qr",
		PaymentRef: "CARD-REF-" + suffix, PrincipalCents: 3000, BonusCents: 1000,
		ContractConfirmed: true, ValidityDays: 30, IdempotencyKey: "as-card-open-" + suffix,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"as-card-order-"+suffix, now)
	if err != nil {
		t.Fatal(err)
	}
	paid, err := payOrderWithStoredValueCard(ctx, db, 11, order.ID, card.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	return paid, card.ID
}

// P0-9 回归：执行失败终结（execution_failed）的售后单撤回时必须清退审批预占的退款额度，
// 否则该订单可退额度被永久占住、后续退款全部冲突；预占释放后应能再次发起并执行退款。
func TestAfterSaleExecutionFailedWithdrawReleasesRefundReservation(t *testing.T) {
	db := openStoredValueDB(t, "esports_aftersale_failed_release")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	order, cardID := seedCardPaidOrderForAfterSale(t, db, "release")

	// 卡已退卡 + 客户余额账户被冻结 → 退原核销款既不能回卡也不能回余额，只能人工处置。
	db.EsportsStoredValueCard.UpdateOneID(cardID).
		SetStatus(esportsstoredvaluecard.StatusRefunded).ExecX(ctx)
	frozen := db.EsportsWalletAccount.Create().
		SetAccountType(esportswalletaccount.AccountTypeCustomerBalance).
		SetOwnerID(order.CustomerID).
		SetStatus(esportswalletaccount.StatusFrozen).SaveX(ctx)

	item := newRefundAfterSale(t, db, order.ID, "refund_request", 800, 11, 12, now)
	if _, err := approveAfterSale(ctx, db, item.ID, 12, now); err != nil {
		t.Fatalf("审批失败：%v", err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundReservedCents != 800 {
		t.Fatalf("审批未预占退款额度：%+v", got)
	}
	if _, err := executeAfterSale(ctx, db, item.ID, 12, now); !errors.Is(err, errExecutionFailed) {
		t.Fatalf("原卡作废且余额冻结时执行应失败（交人工处置），err=%v", err)
	}
	failed := db.EsportsAfterSale.GetX(ctx, item.ID)
	if failed.Status != esportsaftersale.StatusExecutionFailed {
		t.Fatalf("执行失败应落 execution_failed：%+v", failed)
	}
	if !strings.Contains(failed.Resolution, "原支付储值卡已不可用") {
		t.Fatalf("执行失败原因应说明原卡不可用：%q", failed.Resolution)
	}

	// P0-9 核心断言：执行失败终结即须清退预占额度（旧实现停在非零 → 后续退款全部冲突）。
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundReservedCents != 0 || got.RefundedCents != 0 {
		t.Fatalf("执行失败终结未清退预占额度：reserved=%d refunded=%d",
			got.RefundReservedCents, got.RefundedCents)
	}
	if _, err := withdrawAfterSale(ctx, db, item.ID, 11, now); err != nil {
		t.Fatalf("撤回 execution_failed 售后失败：%v", err)
	}
	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.RefundReservedCents != 0 || got.RefundedCents != 0 {
		t.Fatalf("撤回未清退预占额度：reserved=%d refunded=%d",
			got.RefundReservedCents, got.RefundedCents)
	}
	if gotItem := db.EsportsAfterSale.GetX(ctx, item.ID); gotItem.Status != esportsaftersale.StatusWithdrawn {
		t.Fatalf("撤回后状态不正确：%+v", gotItem)
	}

	// 兼容历史数据：老版本留在 execution_failed 且预占未释放的单，撤回时同样必须清退。
	legacy := newRefundAfterSale(t, db, order.ID, "duration_short", 800, 11, 12, now)
	if _, err := approveAfterSale(ctx, db, legacy.ID, 12, now); err != nil {
		t.Fatalf("历史单审批失败：%v", err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundReservedCents != 800 {
		t.Fatalf("历史单审批未预占额度：%+v", got)
	}
	// 模拟修复前的存量数据：状态已是 execution_failed、预占仍挂着。
	db.EsportsAfterSale.UpdateOneID(legacy.ID).
		SetStatus(esportsaftersale.StatusExecutionFailed).ExecX(ctx)
	if _, err := withdrawAfterSale(ctx, db, legacy.ID, 11, now); err != nil {
		t.Fatalf("撤回历史 execution_failed 售后失败：%v", err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundReservedCents != 0 {
		t.Fatalf("撤回历史 execution_failed 售后未清退预占额度：reserved=%d", got.RefundReservedCents)
	}

	// 额度释放后再次发起退款应能审批并执行（本次放开余额账户，走客户余额回退）。
	db.EsportsWalletAccount.UpdateOneID(frozen.ID).
		SetStatus(esportswalletaccount.StatusActive).ExecX(ctx)
	second := newRefundAfterSale(t, db, order.ID, "service_mismatch", 800, 11, 12, now)
	if _, err := approveAfterSale(ctx, db, second.ID, 12, now); err != nil {
		t.Fatalf("预占额度释放后再次审批退款失败：%v", err)
	}
	done, err := executeAfterSale(ctx, db, second.ID, 12, now)
	if err != nil || done.Status != esportsaftersale.StatusCompleted {
		t.Fatalf("再次执行退款失败：%v %+v", err, done)
	}
	final := db.EsportsOrder.GetX(ctx, order.ID)
	if final.RefundedCents != 800 || final.RefundReservedCents != 0 {
		t.Fatalf("再次退款落账不正确：%+v", final)
	}
}

// P0-9 多动作口径：带动作项的售后单从 execution_failed 可由人工续跑，执行失败时保留预占；
// 但人工撤回终结时必须释放「尚未成功」的退款动作项额度，不把额度永久占住。
func TestAfterSaleActionsFailedWithdrawReleasesHeldReservation(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_actions_withdraw_release")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	// 无余额 / 储值卡核销流水的订单：退款动作项必然失败，补钟动作项成功 → 部分成功 execution_failed。
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

// P0-4 回归：原支付储值卡已退卡 / 作废时，退原核销款不得把钱写回该卡，
// 改为退回客户余额（并写明原因），同时不产生原卡的预付款批次冲回。
func TestAfterSaleRefundToVoidedCardFallsBackToCustomerBalance(t *testing.T) {
	db := openStoredValueDB(t, "esports_aftersale_voided_card")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	order, cardID := seedCardPaidOrderForAfterSale(t, db, "voided")
	db.EsportsStoredValueCard.UpdateOneID(cardID).
		SetStatus(esportsstoredvaluecard.StatusRefunded).ExecX(ctx)

	item := newRefundAfterSale(t, db, order.ID, "refund_request", 600, 11, 12, now)
	if _, err := approveAfterSale(ctx, db, item.ID, 12, now); err != nil {
		t.Fatalf("审批失败：%v", err)
	}
	done, err := executeAfterSale(ctx, db, item.ID, 12, now)
	if err != nil || done.Status != esportsaftersale.StatusCompleted {
		t.Fatalf("原卡作废但客户余额可用时应退回余额并成功：%v %+v", err, done)
	}

	// 不得写回已作废的卡（卡账户上不得出现任何退款冲回流水）。
	if n := db.EsportsWalletTx.Query().Where(
		esportswallettx.AccountTypeEQ(esportswallettx.AccountTypeStoredValueCard),
		esportswallettx.OwnerIDEQ(cardID),
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeRefundReturn),
	).CountX(ctx); n != 0 {
		t.Fatalf("退款不得写回已作废的储值卡，got=%d 条卡退款流水", n)
	}
	// 不得冲回原卡的预付款批次（卡已退卡，批次账本已终结）。
	if n := db.EsportsLedgerEntry.Query().Where(
		esportsledgerentry.EntryRoleEQ("card_refund_return"),
	).CountX(ctx); n != 0 {
		t.Fatalf("卡已作废时不应再写卡批次冲回，got=%d", n)
	}
	refundTx := db.EsportsWalletTx.Query().Where(
		esportswallettx.AccountTypeEQ(esportswallettx.AccountTypeCustomerBalance),
		esportswallettx.OwnerIDEQ(order.CustomerID),
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeRefundReturn),
		esportswallettx.SourceTypeEQ("after_sale"),
	).OnlyX(ctx)
	if total := refundTx.PrincipalDeltaCents + refundTx.BonusDeltaCents; total != 600 {
		t.Fatalf("退回客户余额金额不正确：%+v", refundTx)
	}
	if !strings.Contains(refundTx.Note, "原储值卡已不可用") {
		t.Fatalf("退回余额的流水应说明原因：%q", refundTx.Note)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundedCents != 600 || got.RefundReservedCents != 0 {
		t.Fatalf("退款额度落账不正确：%+v", got)
	}
}
