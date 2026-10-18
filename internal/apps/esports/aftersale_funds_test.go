package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportscommissionentry"
	"go-admin/internal/ent/esportsgrowthledger"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportswallettx"
)

// seedAfterSaleCompositionOrder 构造一张已付款订单及其原余额核销流水（本金 / 赠送按参数分列），
// 用于验证分次退款必须沿用原 consume 构成，而不是每次重新「先本金」。
func seedAfterSaleCompositionOrder(t *testing.T, db *ent.Client, suffix string,
	principalCents, bonusCents, payableCents int64) *ent.EsportsOrder {
	t.Helper()
	ctx := t.Context()
	customer := saveCustomerOrFail(t, db, 11)
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	order := db.EsportsOrder.Create().
		SetOrderNo("AS-FUND-" + suffix).
		SetCustomerID(customer.ID).SetSkuID(1).SetQuantity(1).
		SetUnitPriceCents(payableCents).SetPayableCents(payableCents).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).
		SaveX(ctx)
	db.EsportsWalletTx.Create().SetTxNo("AS-FUND-TX-" + suffix).
		SetAccountType(esportswallettx.AccountTypeCustomerBalance).SetOwnerID(customer.ID).
		SetEntryType(esportswallettx.EntryTypeConsume).
		SetPrincipalDeltaCents(-principalCents).SetBonusDeltaCents(-bonusCents).
		SetSourceType("order").SetSourceID(order.OrderNo).
		SetOperatorID(11).SetNote("订单余额核销").SaveX(ctx)
	return order
}

// aftersaleRefundTotals 汇总订单已写出的退款流水本金 / 赠送合计与条数（按写入顺序）。
func aftersaleRefundTotals(t *testing.T, db *ent.Client) (principal, bonus int64, rows []*ent.EsportsWalletTx) {
	t.Helper()
	rows = db.EsportsWalletTx.Query().Where(
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeRefundReturn),
		esportswallettx.SourceTypeEQ("after_sale"),
	).Order(ent.Asc(esportswallettx.FieldID)).AllX(t.Context())
	for _, row := range rows {
		principal += row.PrincipalDeltaCents
		bonus += row.BonusDeltaCents
	}
	return principal, bonus, rows
}

// 分次部分退款构成：原核销本金 50 / 赠送 50，两次各退 40 应各返本金 20、赠送 20，
// 最后一笔 20 补齐为累计本金 50 / 赠送 50，不能出现「每次都先本金」导致本金退 80。
func TestAfterSalePartialRefundCompositionFollowsConsume(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_fund_split")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	order := seedAfterSaleCompositionOrder(t, db, "SPLIT", 50, 50, 100)

	for i, refund := range []struct {
		kind   string
		amount int64
	}{
		{"refund_request", 40},
		{"duration_short", 40},
	} {
		item := newRefundAfterSale(t, db, order.ID, refund.kind, refund.amount, 11, 12, now)
		if _, err := approveAfterSale(ctx, db, item.ID, 12, now); err != nil {
			t.Fatalf("第 %d 次部分退款审批失败：%v", i+1, err)
		}
		if _, err := executeAfterSale(ctx, db, item.ID, 12, now); err != nil {
			t.Fatalf("第 %d 次部分退款执行失败：%v", i+1, err)
		}
	}
	principal, bonus, rows := aftersaleRefundTotals(t, db)
	if len(rows) != 2 {
		t.Fatalf("两次部分退款应各写一条流水，got=%d", len(rows))
	}
	if rows[0].PrincipalDeltaCents != 20 || rows[0].BonusDeltaCents != 20 ||
		rows[1].PrincipalDeltaCents != 20 || rows[1].BonusDeltaCents != 20 {
		t.Fatalf("两次各退 40 应各返本金 20 / 赠送 20，got=%+v %+v", rows[0], rows[1])
	}
	if principal != 40 || bonus != 40 {
		t.Fatalf("两次部分退款累计构成本金 40 / 赠送 40，got=%d/%d", principal, bonus)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundedCents != 80 || got.RefundReservedCents != 0 {
		t.Fatalf("两次部分退款后订单退款额度不正确：%+v", got)
	}

	// 最后一笔退剩余 20：累计本金 / 赠送自然补齐到原构成 50 / 50。
	last := newRefundAfterSale(t, db, order.ID, "other", 20, 11, 12, now)
	if _, err := approveAfterSale(ctx, db, last.ID, 12, now); err != nil {
		t.Fatal(err)
	}
	if _, err := executeAfterSale(ctx, db, last.ID, 12, now); err != nil {
		t.Fatal(err)
	}
	principal, bonus, rows = aftersaleRefundTotals(t, db)
	if len(rows) != 3 || rows[2].PrincipalDeltaCents != 10 || rows[2].BonusDeltaCents != 10 {
		t.Fatalf("最后一笔应补齐本金 10 / 赠送 10：%+v", rows)
	}
	if principal != 50 || bonus != 50 {
		t.Fatalf("全额退款后累计构成本金 50 / 赠送 50，got=%d/%d", principal, bonus)
	}
	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.RefundedCents != 100 || got.RefundReservedCents != 0 ||
		got.RefundStatus != esportsorder.RefundStatusFull {
		t.Fatalf("全额退款后订单额度 / 状态不正确：%+v", got)
	}
}

// 全额退款尾差：333 / 667 分两次各退 500，本金按累计取整分别为 166 / 167，
// 最后一笔补齐 333，赠送合计 667，不能本金单边优先。
func TestAfterSaleFullRefundCompositionTail(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_fund_tail")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	order := seedAfterSaleCompositionOrder(t, db, "TAIL", 333, 667, 1000)
	for i, kind := range []string{"refund_request", "duration_short"} {
		item := newRefundAfterSale(t, db, order.ID, kind, 500, 11, 12, now)
		if _, err := approveAfterSale(ctx, db, item.ID, 12, now); err != nil {
			t.Fatalf("第 %d 次退款审批失败：%v", i+1, err)
		}
		if _, err := executeAfterSale(ctx, db, item.ID, 12, now); err != nil {
			t.Fatalf("第 %d 次退款执行失败：%v", i+1, err)
		}
	}
	principal, bonus, rows := aftersaleRefundTotals(t, db)
	if len(rows) != 2 ||
		rows[0].PrincipalDeltaCents != 166 || rows[0].BonusDeltaCents != 334 ||
		rows[1].PrincipalDeltaCents != 167 || rows[1].BonusDeltaCents != 333 {
		t.Fatalf("全额退款尾差未补齐：%+v", rows)
	}
	if principal != 333 || bonus != 667 {
		t.Fatalf("全额退款累计构成本金 333 / 赠送 667，got=%d/%d", principal, bonus)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundedCents != 1000 ||
		got.RefundStatus != esportsorder.RefundStatusFull {
		t.Fatalf("全额退款订单状态不正确：%+v", got)
	}
}

// 补偿方案：只发权益、写追偿（如适用），不占原单退款额度、不改 refunded_cents /
// refund_status，也不产生负向分成 / 成长值冲回。
func TestAfterSaleCompensateDoesNotConsumeRefundQuota(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_compensate")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	order := seedAfterSaleCompositionOrder(t, db, "COMP", 600, 400, 1000)
	// 预置原单待结算分成与结单成长的流水，用于验证补偿不会走退款冲回。
	db.EsportsCommissionEntry.Create().
		SetIdemKey("commission:service:" + order.OrderNo + ":9001").
		SetOrderID(order.ID).SetPlayerID(9001).
		SetKind(esportscommissionentry.KindService).SetSeatIndex(1).
		SetBaseCents(1000).SetAmountCents(300).
		SetStatus(esportscommissionentry.StatusPendingSettlement).
		SetNote("订单结单服务分成").SaveX(ctx)
	db.EsportsGrowthLedger.Create().SetCustomerID(order.CustomerID).
		SetDelta(1000).SetSourceType(esportsgrowthledger.SourceTypeOrder).
		SetSourceID(order.OrderNo).SetNote("订单结单计提").SaveX(ctx)

	created, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "order", SourceID: order.ID, Kind: "refund_request", Channel: "customer",
		Demand: "服务体验不佳，申请补偿", ExpectedPlan: "发放补偿权益",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = acceptAfterSale(ctx, db, created.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	if _, err = submitAfterSalePlan(ctx, db, created.ID, 11, afterSalePlanInput{
		FaultParty: "platform", PlanAction: "compensate", RefundCents: 500, Resolution: "平台补偿 500 分权益",
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err = confirmAfterSaleByAdmin(ctx, db, created.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	approved, err := approveAfterSale(ctx, db, created.ID, 12, now)
	if err != nil || approved.Status != esportsaftersale.StatusPendingExecution {
		t.Fatalf("补偿方案审批失败：%v %+v", err, approved)
	}
	// 审批不得占用退款额度。
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundReservedCents != 0 || got.RefundedCents != 0 {
		t.Fatalf("补偿审批占用了退款额度：%+v", got)
	}
	done, err := executeAfterSale(ctx, db, created.ID, 12, now)
	if err != nil || done.Status != esportsaftersale.StatusCompleted {
		t.Fatalf("补偿执行失败：%v %+v", err, done)
	}

	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.RefundedCents != 0 || got.RefundReservedCents != 0 ||
		got.RefundStatus != esportsorder.RefundStatusNone {
		t.Fatalf("补偿改变了订单退款状态：%+v", got)
	}
	compensateTx := db.EsportsWalletTx.Query().Where(
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeAdjust),
		esportswallettx.SourceTypeEQ("after_sale_compensate"),
	).OnlyX(ctx)
	if compensateTx.PrincipalDeltaCents != 0 || compensateTx.BonusDeltaCents != 500 {
		t.Fatalf("补偿权益流水不正确：%+v", compensateTx)
	}
	// 不产生负向分成，原待结算分成保持完整并解冻。
	reversals := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.OrderIDEQ(order.ID),
		esportscommissionentry.KindEQ(esportscommissionentry.KindReversal),
	).CountX(ctx)
	if reversals != 0 {
		t.Fatalf("补偿不应冲回分成，got reversal=%d", reversals)
	}
	service := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.OrderIDEQ(order.ID),
		esportscommissionentry.KindEQ(esportscommissionentry.KindService),
	).OnlyX(ctx)
	if service.AmountCents != 300 || service.FrozenAt != nil {
		t.Fatalf("补偿不应改变原分成明细：%+v", service)
	}
	// 不产生负向成长值流水。
	if n := db.EsportsGrowthLedger.Query().Where(
		esportsgrowthledger.SourceTypeEQ(esportsgrowthledger.SourceTypeAfterSale),
	).CountX(ctx); n != 0 {
		t.Fatalf("补偿不应冲回成长值，got after_sale ledger=%d", n)
	}
	counts := countEventsByType(t, db)
	if counts["order.compensate_executed"] != 1 || counts["order.refund_executed"] != 0 {
		t.Fatalf("补偿事件留痕不正确：%+v", counts)
	}
}

// 冻结订单：受理与退原核销款执行都应返回 errOrderFrozen（HTTP 层映射 409），
// 且不预占 / 不落账；恢复存单后可继续执行。
func TestAfterSaleFrozenOrderBlocksRefundExecution(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_frozen")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	order := seedAfterSaleCompositionOrder(t, db, "FROZEN", 100, 100, 200)
	item := newRefundAfterSale(t, db, order.ID, "refund_request", 80, 11, 12, now)
	if _, err := approveAfterSale(ctx, db, item.ID, 12, now); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundReservedCents != 80 {
		t.Fatalf("审批未预占退款额度：%+v", got)
	}
	db.EsportsOrder.UpdateOneID(order.ID).
		SetDepositStatus(esportsorder.DepositStatusFrozen).ExecX(ctx)

	if _, err := executeAfterSale(ctx, db, item.ID, 12, now); !errors.Is(err, errOrderFrozen) {
		t.Fatalf("冻结单执行退款未返回存单冻结错误：%v", err)
	}
	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.RefundedCents != 0 || got.RefundReservedCents != 80 {
		t.Fatalf("冻结单执行失败后不应落账：%+v", got)
	}
	if gotItem := db.EsportsAfterSale.GetX(ctx, item.ID); gotItem.Status != esportsaftersale.StatusPendingExecution {
		t.Fatalf("冻结单执行失败不应改变售后状态：%+v", gotItem)
	}
	if n := db.EsportsWalletTx.Query().Where(
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeRefundReturn)).CountX(ctx); n != 0 {
		t.Fatalf("冻结单执行不应写退款流水，got=%d", n)
	}

	// 受理入口同样被冻结守卫拦截（新建售后单本身不改资金，受理会冻结分成）。
	pending, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "order", SourceID: order.ID, Kind: "duration_short", Channel: "customer",
		Demand: "冻结期间再次申请售后", ExpectedPlan: "待恢复后处理",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acceptAfterSale(ctx, db, pending.ID, 11, now); !errors.Is(err, errOrderFrozen) {
		t.Fatalf("冻结单受理未返回存单冻结错误：%v", err)
	}

	// 恢复存单后原退款可正常执行。
	db.EsportsOrder.UpdateOneID(order.ID).
		SetDepositStatus(esportsorder.DepositStatusNone).ExecX(ctx)
	done, err := executeAfterSale(ctx, db, item.ID, 12, now)
	if err != nil || done.Status != esportsaftersale.StatusCompleted {
		t.Fatalf("恢复存单后执行失败：%v %+v", err, done)
	}
	got = db.EsportsOrder.GetX(ctx, order.ID)
	if got.RefundedCents != 80 || got.RefundReservedCents != 0 {
		t.Fatalf("恢复存单后退款落账不正确：%+v", got)
	}
}
