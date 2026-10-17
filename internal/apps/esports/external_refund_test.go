package esports

import (
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportscommissionentry"
	"go-admin/internal/ent/esportscscommissionentry"
	"go-admin/internal/ent/esportsexternalrefund"
	"go-admin/internal/ent/esportsgrowthledger"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/migrate"
)

// seedExternalRefundRow 直接写入一条已登记的外部退款（绕过登记额度校验），
// 用于验证执行阶段的并发 / 超退保护。
func seedExternalRefundRow(t *testing.T, db *ent.Client, txNo string, orderID int,
	amount int64, adminID int, afterSaleID *int) *ent.EsportsExternalRefund {
	t.Helper()
	create := db.EsportsExternalRefund.Create().
		SetRefundNo(externalRefundNo()).SetChannel("wechat_work").
		SetTransactionNo(txNo).SetAmountCents(amount).SetOrderID(orderID).
		SetStatus(esportsexternalrefund.StatusRegistered).
		SetReason("测试外部退款").SetRegisteredByAdminID(adminID)
	if afterSaleID != nil {
		create.SetAftersaleID(*afterSaleID)
	}
	return create.SaveX(t.Context())
}

// seedExternalRefundReversalBase 构造带分成 / 客服提成 / 成长值的余额核销订单，
// 便于验证外部退款执行时的三件套冲回。
func seedExternalRefundReversalBase(t *testing.T, db *ent.Client, suffix string,
	now time.Time) *ent.EsportsOrder {
	t.Helper()
	ctx := t.Context()
	orderID := seedMemberCommissionOrder(t, db, suffix, 200, 300, now)
	order := db.EsportsOrder.GetX(ctx, orderID)
	db.EsportsCsCommissionEntry.Create().SetIdemKey("cs_commission:service:" + suffix + ":21").
		SetOrderID(orderID).SetAdminID(21).
		SetKind(esportscscommissionentry.KindService).
		SetBaseCents(order.PayableCents).SetAmountCents(400).
		SetStatus(esportscscommissionentry.StatusPendingSettlement).SaveX(ctx)
	db.EsportsGrowthLedger.Create().SetCustomerID(order.CustomerID).
		SetDelta(order.PayableCents).
		SetSourceType(esportsgrowthledger.SourceTypeOrder).SetSourceID(order.OrderNo).
		SetNote("订单结单计提").SaveX(ctx)
	return order
}

// 外部退款登记人不得自行核验执行（与售后「不得自审」同口径）。
func TestExternalRefundRejectsSelfExecution(t *testing.T) {
	db := openAfterSaleDB(t, "esports_external_refund_self")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	order := seedBalancePaidOrder(t, db, "er-self")

	refund, err := registerExternalRefund(ctx, db, 11, registerExternalRefundInput{
		Channel: "wechat_work", TransactionNo: "ER-SELF-1", AmountCents: 500,
		OrderID: order.ID, Reason: "外部渠道退款",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executeExternalRefund(ctx, db, 11, refund.ID, now); !errors.Is(err, errForbidden) {
		t.Fatalf("登记人自执行未被拦截：%v", err)
	}
	// 被拦截后状态与额度都不应变化。
	if got := db.EsportsExternalRefund.GetX(ctx, refund.ID); got.Status != esportsexternalrefund.StatusRegistered {
		t.Fatalf("自执行被拦截后状态不应变化：%+v", got)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundedCents != 0 {
		t.Fatalf("自执行被拦截后不应累加已退额：%+v", got)
	}
	if _, err := executeExternalRefund(ctx, db, 12, refund.ID, now); err != nil {
		t.Fatalf("换人执行失败：%v", err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundedCents != 500 {
		t.Fatalf("换人执行后已退额不正确：%+v", got)
	}
}

// 未关联售后单的外部退款：登记与执行都不得侵占售后预占额度，超退必须拦截。
func TestExternalRefundWithoutAfterSaleRespectsReservation(t *testing.T) {
	db := openAfterSaleDB(t, "esports_external_refund_guard")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	order := seedBalancePaidOrder(t, db, "er-guard")

	// 售后审批预占 800，外部退款登记时可见但仍只能使用剩余额度 1200。
	item := newRefundAfterSale(t, db, order.ID, "refund_request", 800, 11, 12, now)
	if _, err := approveAfterSale(ctx, db, item.ID, 12, now); err != nil {
		t.Fatal(err)
	}
	if _, err := registerExternalRefund(ctx, db, 11, registerExternalRefundInput{
		Channel: "wechat_work", TransactionNo: "ER-GUARD-OVER", AmountCents: 1300,
		OrderID: order.ID, Reason: "超退登记",
	}, now); !errors.Is(err, errConflict) {
		t.Fatalf("登记阶段超退未被拦截：%v", err)
	}
	first, err := registerExternalRefund(ctx, db, 11, registerExternalRefundInput{
		Channel: "wechat_work", TransactionNo: "ER-GUARD-1", AmountCents: 900,
		OrderID: order.ID, Reason: "外部渠道退款一",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	// 同订单第二笔待执行登记 300 仍可登记，累计不超过剩余额度 1200。
	if _, err := registerExternalRefund(ctx, db, 11, registerExternalRefundInput{
		Channel: "wechat_work", TransactionNo: "ER-GUARD-2", AmountCents: 300,
		OrderID: order.ID, Reason: "外部渠道退款二",
	}, now); err != nil {
		t.Fatalf("剩余额度内登记失败：%v", err)
	}
	// 第三笔 1 分即透支，登记阶段拦截。
	if _, err := registerExternalRefund(ctx, db, 11, registerExternalRefundInput{
		Channel: "wechat_work", TransactionNo: "ER-GUARD-3", AmountCents: 1,
		OrderID: order.ID, Reason: "再登记一分钱",
	}, now); !errors.Is(err, errConflict) {
		t.Fatalf("多笔待执行登记透支未被拦截：%v", err)
	}

	// 伪造一条绕过登记的 1201 外部退款，执行时条件表达式仍须拦截（预占 800 + 1201 > 应付 2000）。
	forged := seedExternalRefundRow(t, db, "ER-GUARD-FORGED", order.ID, 1201, 11, nil)
	if _, err := executeExternalRefund(ctx, db, 12, forged.ID, now); !errors.Is(err, errConflict) {
		t.Fatalf("执行阶段超退未被拦截：%v", err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundedCents != 0 {
		t.Fatalf("超退被拦截后已退额不应变化：%+v", got)
	}
	if _, err := executeExternalRefund(ctx, db, 12, first.ID, now); err != nil {
		t.Fatalf("额度内执行失败：%v", err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundedCents != 900 || got.RefundReservedCents != 800 {
		t.Fatalf("外部退款不应侵占售后预占：%+v", got)
	}
}

// 关联售后单的外部退款：核销预占、累加已退额，并同事务冲回分成 / 客服提成 / 成长值；
// 重复执行返回 errConflict 且不新增冲回明细（幂等）。
func TestExternalRefundLinkedAfterSaleReversesEntitlements(t *testing.T) {
	db := openAfterSaleDB(t, "esports_external_refund_linked")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	order := seedExternalRefundReversalBase(t, db, "er-linked", now)

	item := newRefundAfterSale(t, db, order.ID, "refund_request", 1000, 11, 12, now)
	if _, err := approveAfterSale(ctx, db, item.ID, 12, now); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundReservedCents != 1000 {
		t.Fatalf("审批未预占额度：%+v", got)
	}
	refund, err := registerExternalRefund(ctx, db, 11, registerExternalRefundInput{
		Channel: "wechat_work", TransactionNo: "ER-LINKED-1", AmountCents: 1000,
		OrderID: order.ID, AfterSaleID: &item.ID, Reason: "按售后方案外部退款",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executeExternalRefund(ctx, db, 12, refund.ID, now); err != nil {
		t.Fatalf("外部退款执行失败：%v", err)
	}

	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.RefundedCents != 1000 || got.RefundReservedCents != 0 ||
		got.RefundStatus != esportsorder.RefundStatusPartial {
		t.Fatalf("关联售后的外部退款额度落账不正确：%+v", got)
	}
	// 分成冲回：1000/2000，总目标 250，按 200:300 分摊 → 100 / 150。
	commissionSums := map[int]int64{}
	for _, row := range db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.KindEQ(esportscommissionentry.KindReversal)).AllX(ctx) {
		commissionSums[row.PlayerID] += row.AmountCents
		if row.Note != "外部退款冲回" {
			t.Fatalf("外部退款分成冲回备注不正确：%+v", row)
		}
		if row.AfterSaleID == nil || *row.AfterSaleID != item.ID {
			t.Fatalf("外部退款冲回应保留关联售后 ID：%+v", row)
		}
	}
	if commissionSums[1] != -100 || commissionSums[2] != -150 {
		t.Fatalf("分成冲回金额不正确：%v", commissionSums)
	}
	csRows := db.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.KindEQ(esportscscommissionentry.KindReversal)).AllX(ctx)
	if len(csRows) != 1 || csRows[0].AmountCents != -200 ||
		csRows[0].Note != "外部退款冲回客服提成" {
		t.Fatalf("客服提成冲回不正确：%+v", csRows)
	}
	growthRows := db.EsportsGrowthLedger.Query().Where(
		esportsgrowthledger.SourceTypeEQ(esportsgrowthledger.SourceTypeAfterSale)).AllX(ctx)
	if len(growthRows) != 1 || growthRows[0].Delta != -1000 ||
		growthRows[0].SourceID != refund.RefundNo ||
		growthRows[0].Note != "外部退款冲回成长值" {
		t.Fatalf("成长值冲回不正确：%+v", growthRows)
	}

	// 幂等：重复执行被拦截，且不新增任何冲回明细。
	if _, err := executeExternalRefund(ctx, db, 12, refund.ID, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复执行未拦截：%v", err)
	}
	if total := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.KindEQ(esportscommissionentry.KindReversal)).CountX(ctx); total != 2 {
		t.Fatalf("重复执行新增了分成冲回明细：%d", total)
	}
	if total := db.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.KindEQ(esportscscommissionentry.KindReversal)).CountX(ctx); total != 1 {
		t.Fatalf("重复执行新增了客服提成冲回明细：%d", total)
	}
	if total := db.EsportsGrowthLedger.Query().CountX(ctx); total != 2 {
		t.Fatalf("重复执行新增了成长值流水：%d", total)
	}
}

// 并发执行同订单两笔外部退款：允许一方失败，但数据库不得出现累计超退。
func TestExternalRefundConcurrentExecutionNeverOverRefunds(t *testing.T) {
	// 并发写 SQLite 共享内存库会短暂表锁，这里给足 busy timeout；仍失败时由下面的重试兜底。
	db := enttest.Open(t, "sqlite3",
		"file:esports_external_refund_concurrent?mode=memory&cache=shared&_fk=1&_busy_timeout=10000",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	order := seedBalancePaidOrder(t, db, "er-concurrent")
	first := seedExternalRefundRow(t, db, "ER-CONCURRENT-1", order.ID, 1500, 11, nil)
	second := seedExternalRefundRow(t, db, "ER-CONCURRENT-2", order.ID, 1500, 11, nil)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, id := range []int{first.ID, second.ID} {
		wg.Add(1)
		go func(index, refundID int) {
			defer wg.Done()
			// 表锁失败时重试，确保两笔都能拿到确定结论（成功或业务冲突）。
			for attempt := 0; attempt < 10; attempt++ {
				_, err := executeExternalRefund(ctx, db, 12, refundID, now)
				if err == nil || errors.Is(err, errConflict) || errors.Is(err, errForbidden) {
					errs[index] = err
					return
				}
				time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
			}
			errs[index] = errors.New("重试后仍无法执行")
		}(i, id)
	}
	wg.Wait()
	success := 0
	for _, err := range errs {
		if err == nil {
			success++
		}
	}
	if success < 1 {
		t.Fatalf("并发执行应至少成功一笔：%v", errs)
	}
	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.RefundedCents > got.PayableCents {
		t.Fatalf("并发执行出现超退：refunded=%d payable=%d", got.RefundedCents, got.PayableCents)
	}
	if success == 1 && got.RefundedCents != 1500 {
		t.Fatalf("单笔成功时已退额应为 1500：%+v", got)
	}
}

// 同一订单连续三笔部分外部退款：累计已退额正确，分成冲回累计不超过原计提，成长值不透支为负。
func TestExternalRefundRepeatedPartialRefunds(t *testing.T) {
	db := openAfterSaleDB(t, "esports_external_refund_partial")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	order := seedExternalRefundReversalBase(t, db, "er-partial", now)

	for i := 0; i < 3; i++ {
		refund, err := registerExternalRefund(ctx, db, 11, registerExternalRefundInput{
			Channel: "wechat_work", TransactionNo: "ER-PARTIAL-" + strconv.Itoa(i),
			AmountCents: 500, OrderID: order.ID, Reason: "分次外部退款",
		}, now)
		if err != nil {
			t.Fatalf("第 %d 笔登记失败：%v", i+1, err)
		}
		if _, err := executeExternalRefund(ctx, db, 12, refund.ID, now); err != nil {
			t.Fatalf("第 %d 笔执行失败：%v", i+1, err)
		}
		if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundedCents != int64(500*(i+1)) {
			t.Fatalf("第 %d 笔后累计已退额不正确：%+v", i+1, got)
		}
	}

	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.RefundedCents != 1500 || got.RefundedCents > got.PayableCents {
		t.Fatalf("三笔部分退款累计不正确：%+v", got)
	}
	var commissionReversed int64
	for _, row := range db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.KindEQ(esportscommissionentry.KindReversal)).AllX(ctx) {
		commissionReversed -= row.AmountCents
	}
	// 每笔 500/2000 冲回 125，三笔合计 375，不超过原计提 500。
	if commissionReversed != 375 {
		t.Fatalf("分成冲回累计不正确：%d", commissionReversed)
	}
	var growth int64
	for _, row := range db.EsportsGrowthLedger.Query().AllX(ctx) {
		growth += row.Delta
	}
	if growth != 500 {
		t.Fatalf("成长值不应透支：累计=%d（期望 2000-1500=500）", growth)
	}
	var csReversed int64
	for _, row := range db.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.KindEQ(esportscscommissionentry.KindReversal)).AllX(ctx) {
		csReversed -= row.AmountCents
	}
	if csReversed > 400 {
		t.Fatalf("客服提成冲回累计超过原计提：%d", csReversed)
	}
}
