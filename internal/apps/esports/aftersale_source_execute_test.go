package esports

import (
	"errors"
	"strconv"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportscommissionentry"
	"go-admin/internal/ent/esportsgiftorder"
	"go-admin/internal/ent/esportsrechargerefundexecution"
	"go-admin/internal/ent/esportsstoredvaluecard"
	"go-admin/internal/ent/esportswallettx"
)

// newSourceRefundAfterSale 构造一张非订单来源、已进入待审批的退款售后（§13.3 全流程前半段）。
func newSourceRefundAfterSale(t *testing.T, db *ent.Client, sourceType string, sourceID int,
	refundCents int64, handler int, now time.Time) *ent.EsportsAfterSale {
	t.Helper()
	ctx := t.Context()
	created, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: sourceType, SourceID: sourceID, Kind: "refund_request", Channel: "customer",
		Demand: "客户申请退款", ExpectedPlan: "原路退回",
	}, handler, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acceptAfterSale(ctx, db, created.ID, handler, now); err != nil {
		t.Fatal(err)
	}
	if _, err := submitAfterSalePlan(ctx, db, created.ID, handler, afterSalePlanInput{
		FaultParty: "none", PlanAction: "refund_original", RefundCents: refundCents, Resolution: "原路退回",
	}, now); err != nil {
		t.Fatal(err)
	}
	planned, err := confirmAfterSaleByAdmin(ctx, db, created.ID, handler, now)
	if err != nil {
		t.Fatal(err)
	}
	return planned
}

// 礼物售后执行：按原付款来源退回（余额本金），冲回礼物分成（gift_reversal）与成长值。
func TestGiftAfterSaleExecuteRefund(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_gift_exec")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)

	customer := seedGiftCustomer(t, db, 101)
	player := seedPlayer(t, db, 202, "qualified", "online")
	gift := seedGift(t, db, giftInput{Code: "AS-GIFT", Name: "礼物", PriceCents: 1000, CommissionMode: "percent", CommissionBp: 5000})
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 5000, Note: "测试"}, "as-gift-recharge"); err != nil {
		t.Fatal(err)
	}
	order, err := sendGift(ctx, db, 11, giftSendInput{
		CustomerID: customer.ID, GiftID: gift.ID, PlayerID: player.ID, Quantity: 1, ClientKey: "as-gift-order-1",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if order.Status != esportsgiftorder.StatusPaid || order.AmountCents != 1000 {
		t.Fatalf("礼物单未支付：%+v", order)
	}

	item := newSourceRefundAfterSale(t, db, "gift", order.ID, 1000, 11, now)
	if _, err := approveAfterSale(ctx, db, item.ID, 12, now); err != nil {
		t.Fatalf("审批失败：%v", err)
	}
	done, err := executeAfterSale(ctx, db, item.ID, 12, now)
	if err != nil || done.Status != esportsaftersale.StatusCompleted {
		t.Fatalf("礼物售后执行失败：%v %+v", err, done)
	}
	// 幂等：重复执行被拦截。
	if _, err := executeAfterSale(ctx, db, item.ID, 12, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复执行未拦截：%v", err)
	}

	got := db.EsportsGiftOrder.GetX(ctx, order.ID)
	if got.Status != esportsgiftorder.StatusRefunded || got.RefundStatus != esportsgiftorder.RefundStatusFull ||
		got.RefundCents != 1000 {
		t.Fatalf("礼物退款状态不正确：%+v", got)
	}
	// 退款流水按原付款来源（客户余额本金）退回。
	refundTx := db.EsportsWalletTx.Query().Where(
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeRefundReturn),
		esportswallettx.SourceTypeEQ("gift")).OnlyX(ctx)
	if refundTx.PrincipalDeltaCents != 1000 || refundTx.OwnerID != customer.ID ||
		refundTx.AccountType != esportswallettx.AccountTypeCustomerBalance {
		t.Fatalf("礼物退款流水不正确：%+v", refundTx)
	}
	// 冲回礼物分成（kind=gift_reversal）。
	if !db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.KindEQ(esportscommissionentry.KindGiftReversal)).ExistX(ctx) {
		t.Fatal("缺少礼物分成冲回明细（gift_reversal）")
	}
	// 冲回成长值（负向流水）。
	var negative int
	for _, g := range db.EsportsGrowthLedger.Query().AllX(ctx) {
		if g.Delta < 0 {
			negative++
		}
	}
	if negative != 1 {
		t.Fatalf("应有一条负向成长值流水，got=%d", negative)
	}
}

// 储值卡售后执行：退卡退款按原路冲减卡内本金 / 赠送、冲回批次账本并把卡置 refunded。
func TestStoredValueCardAfterSaleExecuteRefund(t *testing.T) {
	db := openStoredValueDB(t, "esports_aftersale_card_exec")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()

	customer := saveCustomerOrFail(t, db, 11)
	card, err := openVerifiedStoredValueCard(t, ctx, db, 11, storedValueOpenInput{
		CustomerID: customer.ID, Source: "staff", PaymentChannel: "merchant_qr",
		PaymentRef: "AS-CARD-1", PrincipalCents: 10000, BonusCents: 2000,
		ContractConfirmed: true, ValidityDays: 30, IdempotencyKey: "as-card-open-1",
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	item := newSourceRefundAfterSale(t, db, "stored_value_card", card.ID, 10000, 11, now)
	if _, err := approveAfterSale(ctx, db, item.ID, 12, now); err != nil {
		t.Fatalf("审批失败：%v", err)
	}
	done, err := executeAfterSale(ctx, db, item.ID, 12, now)
	if err != nil || done.Status != esportsaftersale.StatusCompleted {
		t.Fatalf("储值卡售后执行失败：%v %+v", err, done)
	}
	if _, err := executeAfterSale(ctx, db, item.ID, 12, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复执行未拦截：%v", err)
	}

	got := db.EsportsStoredValueCard.GetX(ctx, card.ID)
	if got.Status != esportsstoredvaluecard.StatusRefunded || got.RefundedCents != 10000 {
		t.Fatalf("卡退款状态不正确：%+v", got)
	}
	// 退款流水：本金退回、赠送作废（负向）。
	refundTx := db.EsportsWalletTx.Query().Where(
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeRefundReturn),
		esportswallettx.SourceTypeEQ("after_sale")).OnlyX(ctx)
	if refundTx.AccountType != esportswallettx.AccountTypeStoredValueCard ||
		refundTx.OwnerID != card.ID || refundTx.PrincipalDeltaCents != -10000 ||
		refundTx.BonusDeltaCents != -2000 {
		t.Fatalf("退卡退款流水不正确：%+v", refundTx)
	}
	// 卡内余额归零。
	balance, err := storedValueCardBalance(ctx, db, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance.PrincipalCents != 0 || balance.BonusCents != 0 {
		t.Fatalf("退卡后余额应归零：%+v", balance)
	}
}

// 充值售后部分退款：按累计已退计算剩余可返本金，允许退款额 <= 可退本金；
// 本金按实际退款额冲减、赠送按同比例作废，执行明细同样按退款额封顶。
func TestRechargeAfterSalePartialRefund(t *testing.T) {
	db := openStoredValueDB(t, "esports_aftersale_recharge_partial")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()

	customer := saveCustomerOrFail(t, db, 11)
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 10000, BonusCents: 2000, Note: "测试充值"}, "as-recharge-partial-1"); err != nil {
		t.Fatal(err)
	}
	rechargeTx := db.EsportsWalletTx.Query().Where(
		esportswallettx.SourceIDEQ("as-recharge-partial-1")).OnlyX(ctx)

	item := newSourceRefundAfterSale(t, db, "recharge", rechargeTx.ID, 4000, 11, now)
	if _, err := approveAfterSale(ctx, db, item.ID, 12, now); err != nil {
		t.Fatalf("审批失败：%v", err)
	}
	done, err := executeAfterSale(ctx, db, item.ID, 12, now)
	if err != nil || done.Status != esportsaftersale.StatusCompleted {
		t.Fatalf("充值部分退款执行失败：%v %+v", err, done)
	}

	// 本金按实际退款额冲减：10000 - 4000 = 6000；赠送按 40% 比例作废：2000 * 4000/10000 = 800。
	balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance.PrincipalCents != 6000 || balance.BonusCents != 1200 {
		t.Fatalf("部分退款后余额不正确：%+v", balance)
	}
	refundTx := db.EsportsWalletTx.Query().Where(
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeRefundReturn),
		esportswallettx.SourceTypeEQ("after_sale")).OnlyX(ctx)
	if refundTx.PrincipalDeltaCents != -4000 || refundTx.BonusDeltaCents != -800 {
		t.Fatalf("部分退款流水不正确：%+v", refundTx)
	}
	execs := db.EsportsRechargeRefundExecution.Query().AllX(ctx)
	if len(execs) != 1 || execs[0].AmountCents != 4000 {
		t.Fatalf("外部退款执行明细应按退款额封顶：%+v", execs)
	}
}

// 充值售后执行：按充值批次还原可退本金、生成外部退款执行明细，内部余额归零。
func TestRechargeAfterSaleExecuteRefund(t *testing.T) {
	db := openStoredValueDB(t, "esports_aftersale_recharge_exec")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()

	customer := saveCustomerOrFail(t, db, 11)
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 10000, Note: "测试充值"}, "as-recharge-1"); err != nil {
		t.Fatal(err)
	}
	rechargeTx := db.EsportsWalletTx.Query().Where(
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeRecharge)).OnlyX(ctx)

	item := newSourceRefundAfterSale(t, db, "recharge", rechargeTx.ID, 10000, 11, now)
	if _, err := approveAfterSale(ctx, db, item.ID, 12, now); err != nil {
		t.Fatalf("审批失败：%v", err)
	}
	done, err := executeAfterSale(ctx, db, item.ID, 12, now)
	if err != nil || done.Status != esportsaftersale.StatusCompleted {
		t.Fatalf("充值售后执行失败：%v %+v", err, done)
	}
	if _, err := executeAfterSale(ctx, db, item.ID, 12, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复执行未拦截：%v", err)
	}

	// 内部退款流水：本金冲回，余额归零。
	refundTx := db.EsportsWalletTx.Query().Where(
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeRefundReturn),
		esportswallettx.SourceTypeEQ("after_sale")).OnlyX(ctx)
	if refundTx.AccountType != esportswallettx.AccountTypeCustomerBalance ||
		refundTx.OwnerID != customer.ID || refundTx.PrincipalDeltaCents != -10000 {
		t.Fatalf("充值退款流水不正确：%+v", refundTx)
	}
	balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance.PrincipalCents != 0 || balance.BonusCents != 0 {
		t.Fatalf("充值售后执行后余额应归零：%+v", balance)
	}
	// 外部退款执行明细：按充值批次生成一条可退本金。
	execs := db.EsportsRechargeRefundExecution.Query().AllX(ctx)
	if len(execs) != 1 {
		t.Fatalf("应按批次生成一条外部退款执行明细，got=%d", len(execs))
	}
	if execs[0].AfterSaleID != item.ID || execs[0].AmountCents != 10000 ||
		execs[0].Status != esportsrechargerefundexecution.StatusPending {
		t.Fatalf("外部退款执行明细不正确：%+v", execs[0])
	}
	wantIdem := "refund:" + strconv.Itoa(item.ID) + ":recharge:" + strconv.Itoa(rechargeTx.ID) +
		":" + execs[0].BatchSourceType + ":" + execs[0].BatchSourceID
	if execs[0].IdemKey != wantIdem {
		t.Fatalf("外部退款幂等键不正确：got=%s want=%s", execs[0].IdemKey, wantIdem)
	}
}
