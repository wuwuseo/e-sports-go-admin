package esports

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscommissionentry"
	"go-admin/internal/ent/esportscommissionrule"
	"go-admin/internal/ent/esportscscommissionentry"
	"go-admin/internal/ent/esportsordercollaborator"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsplayerrecovery"
	"go-admin/internal/ent/esportsstoredvaluecard"
	"go-admin/internal/ent/esportswallettx"
)

// holdAccountLockThenRun 断言 fn 在持有账户锁期间会被阻塞（即 fn 会获取同一把锁）。
// 返回 true 表示「持有锁时 fn 未完成」（正确地获取了锁）。
func holdAccountLockThenRun(t *testing.T, key string, fn func() error) {
	t.Helper()
	unlock := lockMoneyAccount(key)
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		unlock()
		if err != nil {
			t.Fatalf("持有账户锁 %q 时操作立即返回错误（属 fixture 问题，非加锁缺失）：%v", key, err)
		}
		t.Fatalf("操作未获取账户锁 %q：持有锁期间即完成写入，并发下可超扣", key)
	case <-time.After(150 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("释放账户锁 %q 后操作失败：%v", key, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("释放账户锁 %q 后操作仍未完成", key)
	}
}

// 加钟余额收款（D1）必须串行化「求和 → 写负数流水」，否则同客户并发收款可超扣余额。
func TestPayOrderAddonByBalanceAcquiresAccountLock(t *testing.T) {
	db := openAddonDB(t, "esports_addon_lock")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	customer := saveCustomerOrFail(t, db, 11)
	player := seedPlayer(t, db, 9401, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedInServiceOrder(t, db, "addon-lock-1", player.ID)
	db.EsportsCommissionRule.Create().SetRuleNo("R-ADDON-LOCK").
		SetStatus(esportscommissionrule.StatusActive).SetScope(esportscommissionrule.ScopeGlobal).
		SetCalcMode(esportscommissionrule.CalcModePercent).SetValueBp(5000).SaveX(ctx)

	addon, err := requestOrderAddon(ctx, db, order.ID, 11, addonInput{AddedCents: 4000}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = confirmOrderAddon(ctx, db, player.ID, addon.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 4000, Note: "测试充值"}, "addon-lock-seed"); err != nil {
		t.Fatal(err)
	}

	holdAccountLockThenRun(t, "balance:"+strconv.Itoa(customer.ID), func() error {
		_, err := payOrderAddonByBalance(ctx, db, 11, addon.ID, now)
		return err
	})
	if balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, customer.ID); err != nil || balance.total() != 0 {
		t.Fatalf("加锁路径收款后余额应为 0：%v %+v", err, balance)
	}
}

// 同一充值来源的第二张售后单部分退款（D2）必须落一条独立的外部退款执行明细：
// 幂等键不含售后单号时，第二次会撞首张单的唯一索引被静默跳过，
// 而内部余额已按 RefundCents 冲减（aftersale_source_execute.go）→ 账实不符、漏退。
func TestRechargeRefundExecSecondPartialRefundOnSameSource(t *testing.T) {
	db := openStoredValueDB(t, "esports_recharge_refund_second")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	customer := saveCustomerOrFail(t, db, 11)

	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 10000, Note: "首次充值"}, "rre-second-1"); err != nil {
		t.Fatal(err)
	}
	source := db.EsportsWalletTx.Query().Where(esportswallettx.SourceIDEQ("rre-second-1")).OnlyX(ctx)

	newItem := func(kind string, refundCents int64) int {
		item, err := createAfterSale(ctx, db, afterSaleInput{
			SourceType: "recharge", SourceID: source.ID, Kind: kind,
			Channel: "admin", Demand: "客户要求分批退款",
		}, 11, now)
		if err != nil {
			t.Fatal(err)
		}
		db.EsportsAfterSale.UpdateOneID(item.ID).SetRefundCents(refundCents).ExecX(ctx)
		return item.ID
	}

	first := newItem("refund_request", 3000)
	firstRows, err := executeRechargeRefundByBatch(ctx, db, first, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstRows) != 1 || firstRows[0].AmountCents != 3000 {
		t.Fatalf("首张售后单应落 3000 明细：%+v", firstRows)
	}

	// 该批次本金 10000、已占用 3000，尚余 7000：第二张售后单再退 3000 必须同样落明细。
	second := newItem("other", 3000)
	secondRows, err := executeRechargeRefundByBatch(ctx, db, second, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(secondRows) != 1 || secondRows[0].AmountCents != 3000 {
		t.Fatalf("同来源第二张售后单必须落独立退款明细（当前被幂等键撞号跳过）：%+v", secondRows)
	}
}

// D34 的判定依据：退款冲回累计不得超过原计提（§12.1-05 / §12.5-02）。
// 覆盖「结单前已退款导致 serviceBase < payable」的场景，分多次部分退款后逐类校验。
// 该不变量依赖调用方已按 refunded + reserved + 本次 <= payable 预占额度
// （下例只按剩余可退额退款，与生产调用方同口径）。
func TestRefundReversalNeverExceedsAccrual(t *testing.T) {
	db := openAddonDB(t, "esports_reversal_bound")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	player := seedPlayer(t, db, 9402, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedInServiceOrder(t, db, "rev-bound-1", player.ID)
	// 应付 10000、结单前已退 2000 → 结单净服务额 8000（serviceBase < payable 的最不利口径）。
	db.EsportsOrder.UpdateOneID(order.ID).
		SetPayableCents(10000).SetRefundedCents(2000).SetCloseNetServiceCents(8000).ExecX(ctx)
	const serviceAccrued, addonAccrued = int64(2400), int64(600)
	db.EsportsCommissionEntry.Create().
		SetIdemKey("commission:service:rev-bound-1:1").SetOrderID(order.ID).SetPlayerID(player.ID).
		SetKind(esportscommissionentry.KindService).SetSeatIndex(1).
		SetBaseCents(8000).SetAmountCents(serviceAccrued).
		SetStatus(esportscommissionentry.StatusPendingSettlement).SaveX(ctx)
	db.EsportsCommissionEntry.Create().
		SetIdemKey("commission:addon:REV-BOUND-1:1").SetOrderID(order.ID).SetPlayerID(player.ID).
		SetKind(esportscommissionentry.KindAddon).SetSeatIndex(1).
		SetBaseCents(2000).SetAmountCents(addonAccrued).
		SetStatus(esportscommissionentry.StatusPendingSettlement).SaveX(ctx)

	// 剩余可退 = 10000 - 2000 = 8000，分两次退完。
	for i, amount := range []int64{4000, 4000} {
		current := db.EsportsOrder.GetX(ctx, order.ID)
		item := &ent.EsportsAfterSale{AfterSaleNo: fmt.Sprintf("AS-BOUND-%d", i+1), RefundCents: amount}
		tx, err := db.Tx(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = reverseOrderCommission(ctx, tx, current, item, now); err != nil {
			_ = tx.Rollback()
			t.Fatalf("第 %d 次冲回应成功：%v", i+1, err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		db.EsportsOrder.UpdateOneID(order.ID).AddRefundedCents(amount).ExecX(ctx)
	}

	var serviceReversed, addonReversed int64
	reversals := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.OrderIDEQ(order.ID),
		esportscommissionentry.KindEQ(esportscommissionentry.KindReversal),
	).AllX(ctx)
	for _, entry := range reversals {
		if strings.HasPrefix(entry.Note, addonReversalNote) {
			addonReversed += -entry.AmountCents
			continue
		}
		serviceReversed += -entry.AmountCents
	}
	if serviceReversed > serviceAccrued {
		t.Fatalf("服务冲回累计 %d 超过原计提 %d", serviceReversed, serviceAccrued)
	}
	if addonReversed > addonAccrued {
		t.Fatalf("加钟冲回累计 %d 超过原计提 %d", addonReversed, addonAccrued)
	}
	if serviceReversed != serviceAccrued {
		t.Fatalf("退完剩余可退额后服务冲回应等于原计提 %d，实际 %d", serviceAccrued, serviceReversed)
	}
}

// 存单冻结期间禁止一切写操作（§3.3-11）：释放到公共池、授予与撤销协作客服都是写操作，
// 必须同样被冻结守卫拦截，否则冻结期可改归属/协作、解冻后回不到冻结前状态。
func TestFrozenOrderBlocksPoolReleaseAndCollaboratorWrites(t *testing.T) {
	db := openDepositDB(t, "esports_freeze_guards")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	order := seedPaidOrder(t, db, "FREEZE-GUARD-1", 1)
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(11).ExecX(ctx)
	// 冻结前先建立一条协作关系，供撤销用例使用。
	if _, err := addOrderCollaborator(ctx, db, 11, false, order.ID, 22, 11, now); err != nil {
		t.Fatal(err)
	}
	if _, err := depositOrder(ctx, db, order.ID, 99, now); err != nil {
		t.Fatal(err)
	}

	if _, err := releaseOrderToPublicPool(ctx, db, 11, false, order.ID, "释放", now); !errors.Is(err, errOrderFrozen) {
		t.Fatalf("冻结期释放到公共池应被拒：%v", err)
	}
	if _, err := addOrderCollaborator(ctx, db, 11, false, order.ID, 33, 11, now); !errors.Is(err, errOrderFrozen) {
		t.Fatalf("冻结期新增协作客服应被拒：%v", err)
	}
	if err := removeOrderCollaborator(ctx, db, 11, false, order.ID, 22); !errors.Is(err, errOrderFrozen) {
		t.Fatalf("冻结期撤销协作客服应被拒：%v", err)
	}

	// 解冻后回到冻结前状态：归属客服与原有协作关系仍在，写操作恢复可用。
	frozen := db.EsportsOrder.GetX(ctx, order.ID)
	if frozen.ResponsibleAdminID == nil || *frozen.ResponsibleAdminID != 11 {
		t.Fatalf("冻结期间不得改变归属客服：%+v", frozen.ResponsibleAdminID)
	}
	if collaborators := db.EsportsOrderCollaborator.Query().Where(
		esportsordercollaborator.OrderIDEQ(order.ID)).CountX(ctx); collaborators != 1 {
		t.Fatalf("冻结期间不得增删协作客服，实际 %d 条", collaborators)
	}
	if _, err := restoreOrder(ctx, db, order.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	if _, err := releaseOrderToPublicPool(ctx, db, 11, false, order.ID, "解冻后释放", now); err != nil {
		t.Fatalf("解冻后释放公共池应成功：%v", err)
	}
}

// D4：其余「求和 → 写负数流水」路径也必须取账户锁，否则与订单核销 / 提现并发时可超扣。
// 覆盖：用余额购卡（客户余额）、储值卡到期作废赠送（卡）、赠送到期作废（客户余额）、追偿扣猫粮（猫粮）。
func TestFundingPathsAcquireAccountLock(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC()

	t.Run("用余额购卡取客户余额锁", func(t *testing.T) {
		db := openStoredValueDB(t, "esports_lock_open_card")
		defer db.Close()
		customer := saveCustomerOrFail(t, db, 11)
		if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
			walletInput{PrincipalCents: 10000, Note: "测试充值"}, "lock-open-card-seed"); err != nil {
			t.Fatal(err)
		}
		in := storedValueOpenInput{CustomerID: customer.ID, Source: "staff",
			PaymentChannel: "customer_balance", PrincipalCents: 5000,
			ContractConfirmed: true, ValidityDays: 30, IdempotencyKey: "lock-open-card"}
		holdAccountLockThenRun(t, "balance:"+strconv.Itoa(customer.ID), func() error {
			_, err := openStoredValueCard(ctx, db, 11, in, now)
			return err
		})
	})

	t.Run("储值卡到期作废赠送取卡锁", func(t *testing.T) {
		db := openStoredValueDB(t, "esports_lock_card_expire")
		defer db.Close()
		customer := saveCustomerOrFail(t, db, 11)
		card := db.EsportsStoredValueCard.Create().SetCardNo("CARD-LOCK-1").
			SetCustomerID(customer.ID).SetPaymentChannel("merchant_qr").
			SetStatus(esportsstoredvaluecard.StatusActive).
			SetPrincipalCents(10000).SetBonusCents(1000).
			SetValidFrom(now.AddDate(0, 0, -60)).SetValidUntil(now.AddDate(0, 0, -1)).SaveX(ctx)
		db.EsportsWalletTx.Create().SetTxNo("TX-LOCK-CARD").
			SetAccountType(esportswallettx.AccountTypeStoredValueCard).SetOwnerID(card.ID).
			SetEntryType(esportswallettx.EntryTypeRecharge).
			SetPrincipalDeltaCents(10000).SetBonusDeltaCents(1000).
			SetSourceType("test").SetSourceID("lock-card-1").SaveX(ctx)
		holdAccountLockThenRun(t, "card:"+strconv.Itoa(card.ID), func() error {
			_, err := expireStoredValueCard(ctx, db, card.ID, now)
			return err
		})
	})

	t.Run("赠送到期作废取客户余额锁", func(t *testing.T) {
		db := openCampaignDB(t)
		defer db.Close()
		customer := saveCustomerOrFail(t, db, 11)
		if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
			walletInput{PrincipalCents: 10000, BonusCents: 1000, Note: "活动充值"}, "lock-campaign-seed"); err != nil {
			t.Fatal(err)
		}
		holdAccountLockThenRun(t, "balance:"+strconv.Itoa(customer.ID), func() error {
			_, err := expireOwnerBonus(ctx, db, customer.ID, now.AddDate(0, 0, 3650))
			return err
		})
	})

	t.Run("追偿扣猫粮取猫粮锁", func(t *testing.T) {
		db := openAfterSaleDB(t, "esports_lock_recovery")
		defer db.Close()
		player := seedPlayer(t, db, 4402, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
		if _, err := walletRecharge(ctx, db, 11, walletPlayerCatfood, player.ID,
			walletInput{PrincipalCents: 5000, Note: "结算入账"}, "lock-recovery-seed"); err != nil {
			t.Fatal(err)
		}
		recovery := db.EsportsPlayerRecovery.Create().SetRecoveryNo("REC-LOCK-1").
			SetPlayerID(player.ID).SetAmountCents(500).
			SetStatus(esportsplayerrecovery.StatusApproved).
			SetReason("测试追偿").SetRequestedByAdminID(11).SaveX(ctx)
		holdAccountLockThenRun(t, "catfood:"+strconv.Itoa(player.ID), func() error {
			_, err := executePlayerRecovery(ctx, db, recovery.ID, 12, now)
			return err
		})
	})
}

// D35（业务口径已确认「加钟款并入可分基数」）：含加钟的订单，客服提成基数与工作室剩余
// 必须包含加钟追加金额。此前 netPayable 不含加钟款，剩余被钳 0，工作室毛利凭空消失。
func TestAddonRevenueJoinsDistributableBase(t *testing.T) {
	db := openCsCommissionDB(t, "esports_addon_base")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	admin := seedAdmin(t, db, "addon-base-admin")

	// 陪玩分成 60%、客服提成 10%，两者均按各自口径计提。
	if err := createAndActivatePlayerRule(t, db, admin.ID, 6000, now); err != nil {
		t.Fatal(err)
	}
	csRule, err := createCsCommissionRule(ctx, db, csCommissionRuleInput{
		RuleNo: "CC-ADDON-BASE", Scope: "global", BaseMode: "order_amount", CalcMode: "percent",
		ValueBp: 1000, Period: "monthly",
	}, admin.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = activateCsCommissionRule(ctx, db, csRule.ID, admin.ID, now); err != nil {
		t.Fatal(err)
	}

	order := seedCloseableOrder(t, db, "CC-ADDON-BASE-1", 10000,
		[]segSeed{{playerID: 1, seatIndex: 1, seconds: 600}})
	db.EsportsOrder.UpdateOneID(order.ID).SetResponsibleAdminID(admin.ID).SetAddonCents(20000).ExecX(ctx)
	// 加钟 20000 已收款、按 50% 计提 10000（先于结单计提）。
	db.EsportsCommissionEntry.Create().SetIdemKey("commission:addon:ADDON-BASE-1:1").
		SetOrderID(order.ID).SetPlayerID(1).SetKind(esportscommissionentry.KindAddon).SetSeatIndex(1).
		SetBaseCents(20000).SetAmountCents(10000).
		SetStatus(esportscommissionentry.StatusPendingSettlement).SaveX(ctx)

	if _, err = closeOrder(ctx, db, order.ID, admin.ID, closeOrderInput{Note: "结单"}, now); err != nil {
		t.Fatalf("结单应成功：%v", err)
	}

	csEntry, err := db.EsportsCsCommissionEntry.Query().Where(
		esportscscommissionentry.OrderIDEQ(order.ID)).Only(ctx)
	if err != nil {
		t.Fatalf("含加钟订单结单后应有客服提成明细（加钟款未并入可分基数时会被钳 0 而不计提）：%v", err)
	}
	// 可分基数 = 服务应付 10000 + 加钟 20000 = 30000；10% = 3000。
	if csEntry.BaseCents != 30000 || csEntry.AmountCents != 3000 {
		t.Fatalf("客服提成基数应含加钟款：base=%d amount=%d（期望 30000 / 3000）",
			csEntry.BaseCents, csEntry.AmountCents)
	}
	// §12.1-04：陪玩分成 + 客服提成 + 渠道成本 ≤ 可分基数；工作室剩余由可分基数反算。
	var playerTotal int64
	entries := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.OrderIDEQ(order.ID)).AllX(ctx)
	for _, entry := range entries {
		playerTotal += entry.AmountCents
	}
	if csEntry.StudioRemainderCents != csEntry.BaseCents-playerTotal-csEntry.AmountCents {
		t.Fatalf("工作室剩余应 = 可分基数 − 陪玩分成 − 客服提成：got=%d want=%d（分成合计 %d）",
			csEntry.StudioRemainderCents, csEntry.BaseCents-playerTotal-csEntry.AmountCents, playerTotal)
	}
	if committed := playerTotal + csEntry.AmountCents; committed > csEntry.BaseCents {
		t.Fatalf("分成 + 提成 %d 超过可分基数 %d", committed, csEntry.BaseCents)
	}
}
