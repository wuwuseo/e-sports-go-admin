package esports

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportscommissionentry"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsorderaddon"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportswallettx"
	"go-admin/internal/ent/migrate"
)

// r6OpenDB 打开 R6 用例专用内存库（唯一 DSN + busy timeout，避免共享内存库相互污染与并发写锁失败）。
func r6OpenDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	return enttest.Open(t, "sqlite3",
		"file:"+name+"?mode=memory&cache=shared&_fk=1&_busy_timeout=10000",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
}

// r6SeedAddonPaidOrder 构造 R6 资金口径用例订单（§12.5-02）：
// 服务应付 payableCents 由客户余额核销（本金 / 赠送构成按参数分列，承诺服务量 3、服务中、已占席），
// 并按**真实链路**确认 + 收款一笔加钟（addedCents / addedUnits），使「加钟已收款」只能由只增流水派生。
func r6SeedAddonPaidOrder(t *testing.T, db *ent.Client, suffix string, userID int,
	payableCents, principalCents, bonusCents, addedCents, addedUnits int64) *ent.EsportsOrder {
	t.Helper()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	customer := saveCustomerOrFail(t, db, 11)
	player := seedPlayer(t, db, userID, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := db.EsportsOrder.Create().
		SetOrderNo("R6-FUND-" + suffix).SetCustomerID(customer.ID).SetSkuID(1).SetQuantity(1).
		SetUnitPriceCents(payableCents).SetPayableCents(payableCents).
		SetPromisedServiceUnits(3).SetRequiredSeats(1).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusInService).SaveX(ctx)
	db.EsportsDispatchSeat.Create().SetOrderID(order.ID).SetSeatIndex(1).
		SetPlayerID(player.ID).SetStatus(esportsdispatchseat.StatusAccepted).SaveX(ctx)
	// 先按真实充值链路备足「服务核销 + 加钟收款」的余额，再写服务核销流水（余额由只增流水求和派生）。
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: principalCents + addedCents, BonusCents: bonusCents, Note: "R6 用例充值"},
		"r6-fund-recharge-"+suffix); err != nil {
		t.Fatalf("用例充值失败：%v", err)
	}
	db.EsportsWalletTx.Create().SetTxNo("R6-FUND-TX-" + suffix).
		SetAccountType(esportswallettx.AccountTypeCustomerBalance).SetOwnerID(customer.ID).
		SetEntryType(esportswallettx.EntryTypeConsume).
		SetPrincipalDeltaCents(-principalCents).SetBonusDeltaCents(-bonusCents).
		SetSourceType("order").SetSourceID(order.OrderNo).
		SetOperatorID(11).SetNote("订单余额核销").SaveX(ctx)
	if addedCents > 0 {
		seedAddonCommissionRule(t, db, "R-ADDON-R6")
		addon, err := requestOrderAddon(ctx, db, order.ID, 11,
			addonInput{AddedCents: addedCents, AddedUnits: addedUnits}, now)
		if err != nil {
			t.Fatalf("发起加钟失败：%v", err)
		}
		if _, err = confirmOrderAddon(ctx, db, player.ID, addon.ID, now); err != nil {
			t.Fatalf("确认加钟失败：%v", err)
		}
		if _, err = payOrderAddonByBalance(ctx, db, 11, addon.ID, now); err != nil {
			t.Fatalf("加钟收款失败：%v", err)
		}
	}
	return db.EsportsOrder.GetX(ctx, order.ID)
}

// §12.5-02：加钟已收款须并入订单可退额度（可退上界 = 服务应付 + 加钟已收）。
// 本用例的加钟 4000 经真实余额收款写入只增流水：服务 1000 + 加钟 4000 = 5000，
// 因此两张各退 1000 的售后单都必须能预占额度（旧口径只按 payable_cents=1000 封顶，第二张必冲突）。
func TestAddonReceivedExtendsRefundableUpperBound(t *testing.T) {
	db := r6OpenDB(t, "round6_addon_refund_bound")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

	order := r6SeedAddonPaidOrder(t, db, "BOUND", 9601, 1000, 1000, 0, 4000, 2)
	if order.AddonCents != 4000 || order.AddonUnits != 2 {
		t.Fatalf("加钟收款后订单应累加加钟金额 / 服务量：%+v", order)
	}

	first := newRefundAfterSale(t, db, order.ID, "refund_request", 1000, 11, 22, now)
	second := newRefundAfterSale(t, db, order.ID, "duration_short", 1000, 11, 22, now)
	if _, err := approveAfterSale(ctx, db, first.ID, 22, now); err != nil {
		t.Fatalf("第一张 1000 分退款审批失败：%v", err)
	}
	if _, err := approveAfterSale(ctx, db, second.ID, 22, now); err != nil {
		t.Fatalf("加钟已收款（4000）应并入可退额度，第二张 1000 分退款应可预占：%v", err)
	}
	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.RefundReservedCents != 2000 || got.RefundedCents != 0 {
		t.Fatalf("两张各 1000 分预占后额度不正确：%+v", got)
	}
}

// §12.5-02：「加钟款可原路退回」——服务应付 1000（本金全额核销）+ 加钟已收 500（余额收款），
// 一次退 1500 必须可审批 / 可执行，且退款流水按原路分别退回：
// 服务部分回原订单核销账户（source_type=after_sale），加钟部分回加钟收款账户（source_type=after_sale_addon）。
func TestAddonRefundReturnsViaOriginalBalanceAccount(t *testing.T) {
	db := r6OpenDB(t, "round6_addon_refund_origin")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

	order := r6SeedAddonPaidOrder(t, db, "ORIGIN", 9602, 1000, 1000, 0, 500, 1)
	item := newRefundAfterSale(t, db, order.ID, "refund_request", 1500, 11, 22, now)
	if _, err := approveAfterSale(ctx, db, item.ID, 22, now); err != nil {
		t.Fatalf("加钟已收款应并入可退额度（1000 + 500 = 1500）：%v", err)
	}
	if _, err := executeAfterSale(ctx, db, item.ID, 22, now); err != nil {
		t.Fatalf("含加钟款的原路退款应可执行：%v", err)
	}

	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.RefundedCents != 1500 || got.RefundReservedCents != 0 ||
		got.RefundStatus != esportsorder.RefundStatusFull {
		t.Fatalf("退满可退上界（1000 + 500）后订单额度 / 退款状态不正确：%+v", got)
	}

	var serviceBack, addonBack int64
	rows := db.EsportsWalletTx.Query().Where(
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeRefundReturn),
	).AllX(ctx)
	for _, row := range rows {
		switch row.SourceType {
		case "after_sale":
			serviceBack += row.PrincipalDeltaCents + row.BonusDeltaCents
		case "after_sale_addon":
			addonBack += row.PrincipalDeltaCents + row.BonusDeltaCents
		default:
			t.Fatalf("退款流水来源类型非预期：%+v", row)
		}
		if row.AccountType != esportswallettx.AccountTypeCustomerBalance || row.OwnerID != order.CustomerID {
			t.Fatalf("退款未原路退回原余额账户：%+v", row)
		}
	}
	if serviceBack != 1000 {
		t.Fatalf("服务部分应原路退回 1000 分，实际 %d（流水 %d 条）", serviceBack, len(rows))
	}
	if addonBack != 500 {
		t.Fatalf("加钟已收款 500 分应经原路退回（after_sale_addon），实际 %d（流水 %d 条）", addonBack, len(rows))
	}
}

// §12.5-02 并发红线：可退上界 = 服务 1000 + 加钟已收 500 = 1500。
// 三张各退 750 的售后单**并发审批**：只能成功两张（恰好到上界），第三张必须 errConflict，
// 且最终预占额不得越过上界——这是 D34 不变量「冲回不得超过原计提」的额度前提。
func TestAddonRefundUpperBoundBlocksConcurrentOverRefund(t *testing.T) {
	db := r6OpenDB(t, "round6_addon_refund_concurrent")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

	order := r6SeedAddonPaidOrder(t, db, "CONCUR", 9603, 1000, 1000, 0, 500, 1)
	kinds := []string{"refund_request", "duration_short", "other"}
	items := make([]*ent.EsportsAfterSale, 0, len(kinds))
	for _, kind := range kinds {
		items = append(items, newRefundAfterSale(t, db, order.ID, kind, 750, 11, 22, now))
	}

	var wg sync.WaitGroup
	errs := make([]error, len(items))
	for i, item := range items {
		wg.Add(1)
		go func(index int, afterSaleID int) {
			defer wg.Done()
			// 并发写 SQLite 共享内存库会短暂表锁：锁冲突重试，保证每张都拿到确定结论（成功或业务冲突）。
			for attempt := 0; attempt < 20; attempt++ {
				_, err := approveAfterSale(ctx, db, afterSaleID, 22, now)
				if err == nil || errors.Is(err, errConflict) || errors.Is(err, errForbidden) {
					errs[index] = err
					return
				}
				time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
			}
			errs[index] = errors.New("重试后仍无法得出审批结论")
		}(i, item.ID)
	}
	wg.Wait()

	success, conflicts := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			success++
		case errors.Is(err, errConflict):
			conflicts++
		}
	}
	if success != 2 || conflicts != 1 {
		t.Fatalf("并发退款只能成功到上界 1500（每张 750）：期望 2 成功 1 冲突，实际 %d 成功 %d 冲突：%v",
			success, conflicts, errs)
	}
	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.RefundReservedCents != 1500 {
		t.Fatalf("并发审批后预占额必须恰好停在上界 1500，实际 %d", got.RefundReservedCents)
	}
}

// §12.1-05 / §12.5-02（D34）：统一可退基数含加钟已收款后，若「先退款、后收加钟款」使分母中途扩容，
// 早期已冲满的类别不得被再次冲回——同一类别累计冲回必须始终 ≤ 原计提。
// 本用例：应付 1000 先全额退掉（服务分成 500 已 100% 冲回）；之后加钟收款 4000（加钟分成 2000），
// 再退剩余的 4000：服务侧累计仍为 500（不得变成 900），加钟侧按统一基数冲回 1600。
func TestReversalStaysWithinAccrualWhenAddonExtendsBase(t *testing.T) {
	db := r6OpenDB(t, "round6_reversal_accrual_cap")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

	order := db.EsportsOrder.Create().SetOrderNo("R6-CAP-1").SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusCompleted).SaveX(ctx)
	db.EsportsCommissionEntry.Create().SetIdemKey("r6cap:service:1").
		SetOrderID(order.ID).SetPlayerID(7).
		SetKind(esportscommissionentry.KindService).SetSeatIndex(1).
		SetBaseCents(1000).SetAmountCents(500).
		SetStatus(esportscommissionentry.StatusPendingSettlement).SaveX(ctx)

	// 第一次退款 1000（此时还没有加钟收款）：服务分成全额冲回 500。
	first := &ent.EsportsAfterSale{AfterSaleNo: "AS-R6-CAP-1", RefundCents: 1000}
	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = reverseOrderCommission(ctx, tx, order, first, now); err != nil {
		_ = tx.Rollback()
		t.Fatalf("第一次退款冲回失败：%v", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	db.EsportsOrder.UpdateOneID(order.ID).SetRefundedCents(1000).ExecX(ctx)

	// 之后加钟收款 4000（只增流水）并计提加钟分成 2000。
	addon := db.EsportsOrderAddon.Create().SetAddonNo("AD-R6-CAP-1").SetOrderID(order.ID).
		SetAddedCents(4000).SetAddedUnits(1).
		SetStatus(esportsorderaddon.StatusConfirmed).
		SetPaymentStatus(esportsorderaddon.PaymentStatusPaid).SetPaidCents(4000).
		SetPaidAt(now).SaveX(ctx)
	db.EsportsWalletTx.Create().SetTxNo("R6-CAP-ADDON-TX").
		SetAccountType(esportswallettx.AccountTypeCustomerBalance).SetOwnerID(order.CustomerID).
		SetEntryType(esportswallettx.EntryTypeConsume).
		SetPrincipalDeltaCents(-4000).SetSourceType(addonWalletSourceType).
		SetSourceID(addon.AddonNo).SetOperatorID(11).SetNote("加钟余额收款").SaveX(ctx)
	db.EsportsCommissionEntry.Create().SetIdemKey("r6cap:addon:1").
		SetOrderID(order.ID).SetPlayerID(7).
		SetKind(esportscommissionentry.KindAddon).SetSeatIndex(1).
		SetBaseCents(4000).SetAmountCents(2000).
		SetStatus(esportscommissionentry.StatusPendingSettlement).SaveX(ctx)
	db.EsportsOrder.UpdateOneID(order.ID).AddAddonCents(4000).AddAddonUnits(1).ExecX(ctx)

	// 第二次退款 4000（可退上界 5000 − 已退 1000）：分母扩容到 5000，但服务侧已冲满，不得再冲。
	current := db.EsportsOrder.GetX(ctx, order.ID)
	second := &ent.EsportsAfterSale{AfterSaleNo: "AS-R6-CAP-2", RefundCents: 4000}
	tx, err = db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = reverseOrderCommission(ctx, tx, current, second, now); err != nil {
		_ = tx.Rollback()
		t.Fatalf("第二次退款冲回失败：%v", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var serviceReversed, addonReversed int64
	for _, entry := range db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.OrderIDEQ(order.ID),
		esportscommissionentry.KindEQ(esportscommissionentry.KindReversal),
	).AllX(ctx) {
		if strings.Contains(entry.Note, addonReversalNote) {
			addonReversed -= entry.AmountCents
			continue
		}
		serviceReversed -= entry.AmountCents
	}
	if serviceReversed != 500 {
		t.Fatalf("服务冲回累计不得超过原计提 500（分母扩容后不得二次冲回）：%d", serviceReversed)
	}
	if addonReversed != 1600 {
		t.Fatalf("加钟冲回应按统一基数 5000 的比例 4000/5000 冲回 1600：%d", addonReversed)
	}
}

// §15.5-04：单位级分摊须留痕可查（单位级明细），冲回明细按最小服务单位给出逐单位冲回额，
// 尾差并入最后一单位（§18.2 裁决）；承诺量 3、冲回 100 分 → [33,33,34] 且合计恒等于 100。
func TestCommissionEntryViewExposesUnitLevelAmounts(t *testing.T) {
	db := r6OpenDB(t, "round6_unit_level_view")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

	order := db.EsportsOrder.Create().SetOrderNo("R6-UNIT-1").SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(100).SetPayableCents(100).
		SetPromisedServiceUnits(3).SetRequiredSeats(1).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusCompleted).SaveX(ctx)
	db.EsportsCommissionEntry.Create().SetIdemKey("r6unit:service:1").
		SetOrderID(order.ID).SetPlayerID(7).
		SetKind(esportscommissionentry.KindService).SetSeatIndex(1).
		SetBaseCents(100).SetAmountCents(100).
		SetStatus(esportscommissionentry.StatusPendingSettlement).SaveX(ctx)

	item := &ent.EsportsAfterSale{AfterSaleNo: "AS-R6-UNIT-1", RefundCents: 100}
	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = reverseOrderCommission(ctx, tx, order, item, now); err != nil {
		_ = tx.Rollback()
		t.Fatalf("退款冲回失败：%v", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	views, err := listCommissionEntryViews(ctx, db, 0, order.ID, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	var serviceView, reversalView *commissionEntryView
	for i := range views {
		switch views[i].Kind {
		case esportscommissionentry.KindService:
			serviceView = &views[i]
		case esportscommissionentry.KindReversal:
			reversalView = &views[i]
		}
	}
	if serviceView == nil || reversalView == nil {
		t.Fatalf("缺少服务分成 / 冲回明细读模型：%+v", views)
	}
	if len(serviceView.ServiceUnitAmounts) != 3 ||
		serviceView.ServiceUnitAmounts[0] != 33 || serviceView.ServiceUnitAmounts[1] != 33 ||
		serviceView.ServiceUnitAmounts[2] != 34 {
		t.Fatalf("服务分成的逐单位金额应为 33/33/34（尾差在最后一单位）：%v", serviceView.ServiceUnitAmounts)
	}
	if len(reversalView.ServiceUnitAmounts) != 3 ||
		reversalView.ServiceUnitAmounts[0] != 33 || reversalView.ServiceUnitAmounts[1] != 33 ||
		reversalView.ServiceUnitAmounts[2] != 34 {
		t.Fatalf("退款冲回的逐单位金额应为 33/33/34（尾差在最后一单位，§18.2 裁决）：%v",
			reversalView.ServiceUnitAmounts)
	}
}
