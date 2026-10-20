package esports

import (
	"errors"
	"strconv"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/apps/esports/schema"
	"go-admin/internal/ent/esportsgrowthledger"
	"go-admin/internal/ent/esportsledgerentry"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportswallettx"
)

// 第 4 轮 · 双钱包 / 会员 / 礼物桶（wallet-member）回归测试。
// 覆盖条目：10.1-02、10.2-03、10.4-04、11.2-01、11.3-02、11.6-01、11.6-02、
// 4.3-03（发起侧）、4.5-04、6.1-14、9.1-02、9.2-01。
// 辅助物一律 r4wm 前缀，避免与其他桶的同名测试辅助冲突。

func r4wmBool(value bool) *bool { return &value }

// 10.1-02：专属通道 / 线下活动优先两项权益可读写，且未传时不被静默清零。
func TestR4WMMemberLevelPrivilegeFieldsReadWrite(t *testing.T) {
	db := openMemberDB(t, "r4wm_member_privileges")
	defer db.Close()
	ctx := t.Context()

	level := seedMemberLevel(t, db, memberLevelInput{
		Code: "r4wm-gold", Name: "金卡", GrowthThreshold: 0, DiscountBps: 10000,
		DedicatedChannel: r4wmBool(true), OfflineEventPriority: r4wmBool(true),
	})
	if !level.DedicatedChannel || !level.OfflineEventPriority {
		t.Fatalf("新建等级未落专属通道/线下活动优先权益：%+v", level)
	}
	view := toMemberLevelView(level)
	if !view.DedicatedChannel || !view.OfflineEventPriority {
		t.Fatalf("等级视图未返回两项新权益：%+v", view)
	}
	if got, err := memberDedicatedChannel(ctx, db, "r4wm-gold"); err != nil || !got {
		t.Fatalf("专属通道权益读取异常：%v %v", got, err)
	}
	if got, err := memberOfflineEventPriority(ctx, db, "r4wm-gold"); err != nil || !got {
		t.Fatalf("线下活动优先权益读取异常：%v %v", got, err)
	}

	// 只显式关闭专属通道：另一项未传须保持原值（沿用 10.1-03 的「未传不清零」口径）。
	updated, err := updateMemberLevel(ctx, db, level.ID, level.Version, memberLevelInput{
		Code: "r4wm-gold", Name: "金卡", GrowthThreshold: 0, DiscountBps: 10000,
		DedicatedChannel: r4wmBool(false),
	})
	if err != nil {
		t.Fatalf("更新等级失败：%v", err)
	}
	if updated.DedicatedChannel {
		t.Fatalf("专属通道显式 false 未生效：%+v", updated)
	}
	if !updated.OfflineEventPriority {
		t.Fatalf("未传的线下活动优先被静默清零：%+v", updated)
	}
}

// 10.2-03：成长值流水在计提与冲回时都写入支付来源，明细每笔非 none。
func TestR4WMGrowthLedgerPaymentSourceOnAccrualAndReversal(t *testing.T) {
	db := openRound4MemberDB(t, "r4wm_growth_payment_source")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)

	seedMemberLevel(t, db, memberLevelInput{Code: "r4wm-ps-silver", Name: "银卡", GrowthThreshold: 0, DiscountBps: 10000})
	customer := saveCustomerOrFail(t, db, 11)
	order := seedCloseableMemberOrder(t, db, customer.ID, "r4wm-ps-order-1", 10000)
	// 核销构成：本金 4000 + 赠送 6000 → mixed。
	db.EsportsWalletTx.Create().SetTxNo("r4wm-ps-tx-1").
		SetAccountType(esportswallettx.AccountTypeCustomerBalance).SetOwnerID(customer.ID).
		SetEntryType(esportswallettx.EntryTypeConsume).
		SetPrincipalDeltaCents(-4000).SetBonusDeltaCents(-6000).
		SetSourceType("order").SetSourceID(order.OrderNo).
		SetOperatorID(11).SetNote("订单余额核销").SaveX(ctx)

	if _, err := closeOrder(ctx, db, order.ID, 11, closeOrderInput{Note: "支付来源口径"}, now); err != nil {
		t.Fatalf("结单失败：%v", err)
	}
	accrued := db.EsportsGrowthLedger.Query().Where(
		esportsgrowthledger.SourceTypeEQ(esportsgrowthledger.SourceTypeOrder),
		esportsgrowthledger.SourceIDEQ(order.OrderNo),
	).AllX(ctx)
	if len(accrued) != 1 || accrued[0].PaymentSource != esportsgrowthledger.PaymentSourceMixed {
		t.Fatalf("计提流水支付来源应为 mixed：%+v", accrued)
	}

	first := newRefundAfterSale(t, db, order.ID, "refund_request", 5000, 11, 12, now)
	if _, err := approveAfterSale(ctx, db, first.ID, 12, now); err != nil {
		t.Fatalf("退款审批失败：%v", err)
	}
	if _, err := executeAfterSale(ctx, db, first.ID, 12, now); err != nil {
		t.Fatalf("退款执行失败：%v", err)
	}
	rows := db.EsportsGrowthLedger.Query().Where(
		esportsgrowthledger.CustomerIDEQ(customer.ID),
	).AllX(ctx)
	for _, row := range rows {
		if row.PaymentSource == esportsgrowthledger.PaymentSourceNone {
			t.Fatalf("成长值明细存在未标注支付来源的流水：%+v", row)
		}
	}
}

// 10.2-03（礼物链路）：礼物计提只动本金 → prepaid_principal；外部收款礼物 → cash。
func TestR4WMGiftGrowthPaymentSourceFollowsFunding(t *testing.T) {
	db := openGiftDB(t, "r4wm_gift_growth_payment_source")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)

	customer := seedGiftCustomer(t, db, 101)
	player := seedPlayer(t, db, 202, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	gift := seedGift(t, db, giftInput{Code: "R4WM-PS-GIFT", Name: "礼物", PriceCents: 1000, CommissionMode: "percent", CommissionBp: 5000})
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 5000, Note: "测试"}, "r4wm-gift-ps-recharge"); err != nil {
		t.Fatal(err)
	}
	order, err := sendGift(ctx, db, 11, giftSendInput{
		CustomerID: customer.ID, GiftID: gift.ID, PlayerID: player.ID, Quantity: 1, ClientKey: "r4wm-gift-ps-1",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	row := db.EsportsGrowthLedger.Query().Where(
		esportsgrowthledger.SourceTypeEQ(esportsgrowthledger.SourceTypeGift),
		esportsgrowthledger.SourceIDEQ(order.GiftNo),
	).OnlyX(ctx)
	if row.PaymentSource != esportsgrowthledger.PaymentSourcePrepaidPrincipal {
		t.Fatalf("余额本金支付的礼物成长值来源应为 prepaid_principal：%+v", row)
	}
}

// 10.4-04：陪玩可用时段查询把已被订单/排单占用的时段标为 occupied。
func TestR4WMPlayerAvailabilityMarksOccupiedSlots(t *testing.T) {
	db := openMemberDB(t, "r4wm_player_availability")
	defer db.Close()
	ctx := t.Context()

	customer := saveCustomerOrFail(t, db, 11)
	player := seedPlayer(t, db, 202, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedCloseableMemberOrder(t, db, customer.ID, "r4wm-avail-order-1", 5000)
	base := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	slotStart, slotEnd := base.Add(time.Hour), base.Add(2*time.Hour)
	// 排单占位 09:00–10:00。
	db.EsportsDispatchSeat.Create().SetOrderID(order.ID).SetSeatIndex(1).SetPlayerID(player.ID).
		SetSource("assign").SetStatus("accepted").
		SetNillableSlotStart(&slotStart).SetNillableSlotEnd(&slotEnd).SaveX(ctx)

	view, err := playerAvailableSlots(ctx, db, player.ID, base, base.Add(3*time.Hour), 60)
	if err != nil {
		t.Fatalf("可用时段查询失败：%v", err)
	}
	if len(view.Slots) != 3 {
		t.Fatalf("时段数应为 3：%+v", view.Slots)
	}
	for _, slot := range view.Slots {
		switch slot.Start {
		case base:
			if slot.Status != "available" || slot.Bookable == false {
				t.Fatalf("08:00–09:00 应为可预约：%+v", slot)
			}
		case base.Add(time.Hour):
			if slot.Status != "occupied" || slot.Bookable {
				t.Fatalf("09:00–10:00 已被排单占用：%+v", slot)
			}
		case base.Add(2 * time.Hour):
			if slot.Status != "available" {
				t.Fatalf("10:00–11:00 应为可预约：%+v", slot)
			}
		}
	}
	if view.OccupiedCount != 1 {
		t.Fatalf("已占用时段数应为 1：%d", view.OccupiedCount)
	}
}

// 11.2-01：活动奖励可入陪玩猫粮（新增目标账户 + 陪玩受众）。
func TestR4WMCampaignRewardsPlayerCatfood(t *testing.T) {
	db := openCampaignDB(t)
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)

	player := seedPlayer(t, db, 202, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	other := seedPlayer(t, db, 203, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	created, err := createCampaign(ctx, db, campaignInput{
		Code: "r4wm-catfood", Name: "陪玩活动奖励", ActivityType: "limited",
		Tiers:             []schema.CampaignTier{{ThresholdCents: 1000, BonusCents: 500}},
		Audience:          "player",
		AudiencePlayerIDs: []int{player.ID},
		TargetAccountType: "player_catfood",
		StackRule:         "stack",
		StartsAt:          now.Add(-time.Hour),
		EndsAt:            now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("创建陪玩猫粮活动失败：%v", err)
	}
	if created.TargetAccountType != "player_catfood" || created.Audience != "player" {
		t.Fatalf("活动目标账户/受众未落库：%+v", created)
	}
	db.EsportsRechargeCampaign.UpdateOneID(created.ID).SetStatus("active").SaveX(ctx)

	if _, err := rechargeWithCampaigns(ctx, db, 11, walletPlayerCatfood, player.ID,
		1000, "活动奖励入猫粮", "r4wm-catfood-1", now); err != nil {
		t.Fatalf("活动奖励入猫粮失败：%v", err)
	}
	balance, err := walletBalanceOf(ctx, db, walletPlayerCatfood, player.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance.PrincipalCents != 1000 || balance.BonusCents != 500 {
		t.Fatalf("猫粮活动奖励金额不符：%+v", balance)
	}
	row := db.EsportsWalletTx.Query().Where(
		esportswallettx.AccountTypeEQ(esportswallettx.AccountTypePlayerCatfood),
		esportswallettx.OwnerIDEQ(player.ID),
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeRecharge),
	).OnlyX(ctx)
	if row.SourceType != campaignSourceType {
		t.Fatalf("猫粮活动奖励来源应为 recharge_campaign：%+v", row)
	}
	// 受众外陪玩不得命中该活动。
	if _, err := rechargeWithCampaigns(ctx, db, 11, walletPlayerCatfood, other.ID,
		1000, "非受众", "r4wm-catfood-2", now); err != nil {
		t.Fatalf("非受众充值不应报错：%v", err)
	}
	otherBalance, err := walletBalanceOf(ctx, db, walletPlayerCatfood, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if otherBalance.BonusCents != 0 {
		t.Fatalf("非受众陪玩命中活动：%+v", otherBalance)
	}
}

// 11.6-01：新增「钱包流水求和 == 批次账本求和」维度，并把异常落到管理端可查列表。
func TestR4WMLedgerReconcileDetectsWalletBatchSumMismatchAndListable(t *testing.T) {
	db := openWalletTestDB(t, "r4wm_ledger_sum")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC)

	customer := saveCustomerOrFail(t, db, 11)
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 1000, Note: "测试"}, "r4wm-ledger-sum-1"); err != nil {
		t.Fatal(err)
	}
	clean, err := reconcileLedgers(ctx, db, 100)
	if err != nil {
		t.Fatalf("核验失败：%v", err)
	}
	if len(clean.Anomalies) != 0 {
		t.Fatalf("正常数据不应报异常：%+v", clean.Anomalies)
	}
	if clean.ScannedWalletSums == 0 {
		t.Fatal("核验未扫描钱包流水求和维度")
	}

	// 制造不一致：批次账本多出一条无对应钱包流水的正向条目。
	db.EsportsLedgerEntry.Create().
		SetAccountType(walletCustomerBalance).SetAccountID(strconv.Itoa(customer.ID)).
		SetSourceType("r4wm_tamper").SetSourceID("r4wm-orphan-batch").
		SetEntryRole("recharge_batch").SetDeltaCents(500).
		SetPrincipalDeltaCents(500).SetFundBatch("r4wm-orphan-batch").SaveX(ctx)

	dirty, err := reconcileLedgers(ctx, db, 100)
	if err != nil {
		t.Fatalf("核验失败：%v", err)
	}
	found := false
	for _, item := range dirty.Anomalies {
		if item.Kind == ledgerAnomalyWalletBatchMismatch {
			found = true
		}
	}
	if !found {
		t.Fatalf("未报出钱包流水求和与批次账本求和不一致：%+v", dirty.Anomalies)
	}
	if err = publishLedgerReconcileAnomalies(ctx, db, dirty, now); err != nil {
		t.Fatalf("写核验异常外盒失败：%v", err)
	}
	list, err := listLedgerReconcileAnomalies(ctx, db, 10)
	if err != nil {
		t.Fatalf("读取核验异常列表失败：%v", err)
	}
	if len(list) == 0 || list[0].AnomalyCount == 0 {
		t.Fatalf("管理端可查列表为空：%+v", list)
	}
	if list[0].ScannedWalletSums == 0 {
		t.Fatalf("列表未带上新增维度扫描量：%+v", list[0])
	}
}

// 11.6-02：钱包流水记录归属人、资金批次与变动前后余额，且历史行返回时可派生。
func TestR4WMWalletTxSelfProvingFields(t *testing.T) {
	db := openWalletTestDB(t, "r4wm_wallet_tx_self_proving")
	defer db.Close()
	ctx := t.Context()

	customer := saveCustomerOrFail(t, db, 11)
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 1000, Note: "首充"}, "r4wm-wt-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 500, Note: "次充"}, "r4wm-wt-2"); err != nil {
		t.Fatal(err)
	}
	rows, err := listWalletTransactions(ctx, db, walletCustomerBalance, customer.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("流水条数应为 2：%d", len(rows))
	}
	latest := rows[0]
	if latest.BeneficiaryType != esportswallettx.BeneficiaryTypeCustomer || latest.BeneficiaryID != customer.ID {
		t.Fatalf("流水未显式记录归属人：%+v", latest)
	}
	if latest.FundBatch != "r4wm-wt-2" {
		t.Fatalf("流水未记录资金批次：%q", latest.FundBatch)
	}
	if latest.BalanceBeforeCents != 1000 || latest.BalanceAfterCents != 1500 {
		t.Fatalf("流水未记录变动前后余额：%d → %d", latest.BalanceBeforeCents, latest.BalanceAfterCents)
	}
	oldest := rows[1]
	if oldest.BalanceBeforeCents != 0 || oldest.BalanceAfterCents != 1000 {
		t.Fatalf("首笔流水前后余额不符：%d → %d", oldest.BalanceBeforeCents, oldest.BalanceAfterCents)
	}
}

// 4.3-03（发起侧）：未开通渠道不得发起充值。
func TestR4WMInitiatedPaymentsRejectDisabledChannel(t *testing.T) {
	db := openWalletTestDB(t, "r4wm_channel_gate")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	customer := saveCustomerOrFail(t, db, 11)
	if err := setPaymentChannelEnabled(ctx, db, "corporate_transfer", false); err != nil {
		t.Fatalf("停用渠道失败：%v", err)
	}
	if _, err := createRechargeIntent(ctx, db, customer, rechargeIntentInput{
		PrincipalCents: 1000, PaymentChannel: "corporate_transfer", IdempotencyKey: "r4wm-ch-1",
	}, now); err == nil {
		t.Fatal("停用渠道仍可发起充值")
	}
	if err := setPaymentChannelEnabled(ctx, db, "corporate_transfer", true); err != nil {
		t.Fatalf("开通渠道失败：%v", err)
	}
	if _, err := createRechargeIntent(ctx, db, customer, rechargeIntentInput{
		PrincipalCents: 1000, PaymentChannel: "corporate_transfer", IdempotencyKey: "r4wm-ch-3",
	}, now); err != nil {
		t.Fatalf("开通渠道后充值发起失败：%v", err)
	}
}

// 4.5-04：活动充值批次账本分列本金/赠送；余额核销分摊明细分列本金/赠送。
func TestR4WMCampaignBatchAndAllocationSplitPrincipalBonus(t *testing.T) {
	db := openCampaignDB(t)
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)

	customer := saveCustomerOrFail(t, db, 11)
	created, err := createCampaign(ctx, db, campaignInput{
		Code: "r4wm-split", Name: "分摊拆分", ActivityType: "full_gift",
		Tiers:    []schema.CampaignTier{{ThresholdCents: 1000, BonusCents: 500}},
		Audience: "all", TargetAccountType: "customer_balance", StackRule: "stack",
		StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	db.EsportsRechargeCampaign.UpdateOneID(created.ID).SetStatus("active").SaveX(ctx)
	if _, err := rechargeWithCampaigns(ctx, db, 11, walletCustomerBalance, customer.ID,
		1000, "活动充值", "r4wm-split-1", now); err != nil {
		t.Fatalf("活动充值失败：%v", err)
	}
	entry := db.EsportsLedgerEntry.Query().Where(
		esportsledgerentry.AccountTypeEQ(walletCustomerBalance),
		esportsledgerentry.AccountIDEQ(strconv.Itoa(customer.ID)),
		esportsledgerentry.EntryRoleEQ("recharge_batch"),
	).OnlyX(ctx)
	if entry.PrincipalDeltaCents != 1000 || entry.BonusDeltaCents != 500 || entry.DeltaCents != 1500 {
		t.Fatalf("批次账本未分列本金/赠送：%+v", entry)
	}

	// 余额核销：本金 1000 + 赠送 500 = 1500 全部消耗，分摊明细须给出两侧金额。
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("支付时限").
		SetValue("60").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "R4WM-SPLIT-SKU", Title: "分摊测试一单", ServiceType: "play", UnitPriceCents: 1500,
		ServiceUnits: 1, ServiceUnitName: "小时", RequiredSeats: 1, QuotaTotal: 1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"r4wm-split-order-1", now)
	if err != nil {
		t.Fatalf("下单失败：%v", err)
	}
	if order.PayableCents != 1500 {
		t.Fatalf("应付额不符：%d", order.PayableCents)
	}
	if _, err := payOrderWithBalance(ctx, db, 11, order.ID, now); err != nil {
		t.Fatalf("余额核销失败：%v", err)
	}
	allocs, err := listLedgerAllocations(ctx, db, walletCustomerBalance, strconv.Itoa(customer.ID), 10)
	if err != nil {
		t.Fatal(err)
	}
	var principalAllocated, bonusAllocated int64
	for _, item := range allocs {
		principalAllocated += item.PrincipalCents
		bonusAllocated += item.BonusCents
	}
	if principalAllocated != 1000 || bonusAllocated != 500 {
		t.Fatalf("核销分摊未分列本金/赠送：本金 %d / 赠送 %d", principalAllocated, bonusAllocated)
	}
}

// 6.1-14：陪玩端收礼记录（礼物名 / 送礼人 / 金额）与本人榜单排名。
func TestR4WMPlayerGiftRecordsAndOwnRank(t *testing.T) {
	db := openGiftDB(t, "r4wm_player_gift_records")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)

	_, customer := seedVerifiedCustomer(t, db, "r4wm-gift-customer-uid")
	playerUser := db.User.Create().SetEmail("r4wm-player@example.com").SetUID("r4wm-gift-player-uid").
		SetStatus("active").SaveX(ctx)
	player := seedPlayer(t, db, playerUser.ID, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	rival := seedPlayer(t, db, 909, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	gift := seedGift(t, db, giftInput{Code: "R4WM-RECORD-GIFT", Name: "火箭", PriceCents: 2000, CommissionMode: "percent", CommissionBp: 5000})
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 10000, Note: "测试"}, "r4wm-gift-records-recharge"); err != nil {
		t.Fatal(err)
	}
	if _, err := sendGift(ctx, db, 11, giftSendInput{
		CustomerID: customer.ID, GiftID: gift.ID, PlayerID: player.ID, Quantity: 2, ClientKey: "r4wm-record-1",
	}, now); err != nil {
		t.Fatal(err)
	}
	// 对手收礼更多，陪玩本人排名应为第 2。
	if _, err := sendGift(ctx, db, 11, giftSendInput{
		CustomerID: customer.ID, GiftID: gift.ID, PlayerID: rival.ID, Quantity: 3, ClientKey: "r4wm-record-2",
	}, now); err != nil {
		t.Fatal(err)
	}

	view, err := listPlayerGiftRecords(ctx, db, "r4wm-gift-player-uid", "daily", now, 20)
	if err != nil {
		t.Fatalf("陪玩收礼记录查询失败：%v", err)
	}
	if view.PlayerID != player.ID || len(view.Records) != 1 {
		t.Fatalf("收礼记录不符：%+v", view)
	}
	record := view.Records[0]
	if record.GiftName != "火箭" || record.AmountCents != 4000 || record.SenderName == "" {
		t.Fatalf("收礼记录字段不全：%+v", record)
	}
	if view.Rank != 2 || view.BoardSize != 2 {
		t.Fatalf("本人榜单排名不符：rank=%d size=%d", view.Rank, view.BoardSize)
	}
}

// 9.1-02：礼物多档价格，每档独立 high_value / require_age，且按档生效。
func TestR4WMGiftTiersDriveHighValueAndRequireAge(t *testing.T) {
	db := openGiftDB(t, "r4wm_gift_tiers")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)

	customer := seedGiftCustomer(t, db, 101)
	player := seedPlayer(t, db, 202, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	gift := seedGift(t, db, giftInput{Code: "R4WM-TIER-GIFT", Name: "花束", PriceCents: 1000, CommissionMode: "percent", CommissionBp: 5000})

	small, err := createGiftTier(ctx, db, giftTierInput{
		GiftID: gift.ID, TierCode: "small", Name: "小花束", PriceCents: 1000,
	})
	if err != nil {
		t.Fatalf("创建低价档失败：%v", err)
	}
	big, err := createGiftTier(ctx, db, giftTierInput{
		GiftID: gift.ID, TierCode: "big", Name: "大花束", PriceCents: 20000,
		HighValue: true, RequireAge: true, IsDefault: true,
	})
	if err != nil {
		t.Fatalf("创建高价档失败：%v", err)
	}
	tiers, err := listGiftTiers(ctx, db, gift.ID, "")
	if err != nil || len(tiers) != 2 {
		t.Fatalf("档位列表不符：%d %v", len(tiers), err)
	}

	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 100000, Note: "测试"}, "r4wm-tier-recharge"); err != nil {
		t.Fatal(err)
	}
	// 同一礼物两档价格各自成单，单价与档位快照随单落库。
	low, err := sendGift(ctx, db, 11, giftSendInput{
		CustomerID: customer.ID, GiftID: gift.ID, PlayerID: player.ID, Quantity: 1,
		GiftTierID: small.ID, ClientKey: "r4wm-tier-low-1",
	}, now)
	if err != nil {
		t.Fatalf("低价档送礼失败：%v", err)
	}
	if low.UnitPriceCents != 1000 || low.GiftTierID != small.ID || low.GiftTierCode != "small" {
		t.Fatalf("低价档未按档定价：%+v", low)
	}
	// 高价档需二次确认，且未成年客户被年龄限制拦住。
	if _, err := sendGift(ctx, db, 11, giftSendInput{
		CustomerID: customer.ID, GiftID: gift.ID, PlayerID: player.ID, Quantity: 1,
		GiftTierID: big.ID, ClientKey: "r4wm-tier-high-1",
	}, now); err == nil {
		t.Fatal("高价档未要求二次确认")
	}
	high, err := sendGift(ctx, db, 11, giftSendInput{
		CustomerID: customer.ID, GiftID: gift.ID, PlayerID: player.ID, Quantity: 1,
		GiftTierID: big.ID, Confirmed: true, ClientKey: "r4wm-tier-high-2",
	}, now)
	if err != nil {
		t.Fatalf("高价档确认后送礼失败：%v", err)
	}
	if high.UnitPriceCents != 20000 || high.GiftTierCode != "big" {
		t.Fatalf("高价档未按档定价：%+v", high)
	}

	db.EsportsCustomer.UpdateOneID(customer.ID).SetAgeStatus("minor").ExecX(ctx)
	if _, err := sendGift(ctx, db, 11, giftSendInput{
		CustomerID: customer.ID, GiftID: gift.ID, PlayerID: player.ID, Quantity: 1,
		GiftTierID: big.ID, Confirmed: true, ClientKey: "r4wm-tier-minor-1",
	}, now); !errors.Is(err, errAgeUnverified) {
		t.Fatalf("require_age=true 的档位未拦截未成年客户：%v", err)
	}
	if _, err := sendGift(ctx, db, 11, giftSendInput{
		CustomerID: customer.ID, GiftID: gift.ID, PlayerID: player.ID, Quantity: 1,
		GiftTierID: small.ID, ClientKey: "r4wm-tier-minor-2",
	}, now); err != nil {
		t.Fatalf("require_age=false 的档位不应拦截未成年客户：%v", err)
	}
}

// 9.2-01：余额与卡本金都不足的客户可对礼物单发起收款并核销成单；停用渠道明确失败。
func TestR4WMGiftExternalPaymentCollectAndVerify(t *testing.T) {
	db := openGiftDB(t, "r4wm_gift_external_payment")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

	_, customer := seedVerifiedCustomer(t, db, "r4wm-gp-customer-uid")
	player := seedPlayer(t, db, 707, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	gift := seedGift(t, db, giftInput{Code: "R4WM-GP-GIFT", Name: "游艇", PriceCents: 5000, CommissionMode: "percent", CommissionBp: 5000})

	// 本金为 0 也能创建确认单（旧口径直接报本金不足）。
	intent, err := createCustomerGiftIntent(ctx, db, "r4wm-gp-customer-uid", customerGiftIntentInput{
		GiftID: gift.ID, PlayerID: player.ID, Quantity: 1, ClientKey: "r4wm-gp-intent-1",
	}, now)
	if err != nil {
		t.Fatalf("本金不足时应可创建礼物确认单并发起收款：%v", err)
	}
	if intent.PaymentChannel != "none" {
		t.Fatalf("未发起收款时通道应为 none：%+v", intent)
	}
	if _, err := confirmGiftIntent(ctx, db, "r4wm-gp-customer-uid", intent.ID, intent.Version, true, now); err != nil {
		t.Fatalf("确认礼物单失败：%v", err)
	}

	// 停用渠道 → 明确失败，不伪成功。
	if err := setPaymentChannelEnabled(ctx, db, "merchant_qr", false); err != nil {
		t.Fatalf("停用渠道失败：%v", err)
	}
	if _, err := startGiftPayment(ctx, db, 11, false, intent.ID, giftPaymentStartInput{
		Channel: "merchant_qr", ClientKey: "r4wm-gp-start-0",
	}, now); err == nil {
		t.Fatal("停用渠道仍可发起礼物收款")
	}
	if err := setPaymentChannelEnabled(ctx, db, "merchant_qr", true); err != nil {
		t.Fatalf("开通渠道失败：%v", err)
	}

	started, err := startGiftPayment(ctx, db, 11, false, intent.ID, giftPaymentStartInput{
		Channel: "merchant_qr", ClientKey: "r4wm-gp-start-1",
	}, now)
	if err != nil {
		t.Fatalf("发起礼物收款失败：%v", err)
	}
	if started.PaymentIntentNo == "" || started.PaymentStatus != "awaiting_payment" || started.PayableCents != 5000 {
		t.Fatalf("礼物收款意图不符：%+v", started)
	}
	got := db.EsportsGiftIntent.GetX(ctx, intent.ID)
	if got.PaymentChannel == "none" || got.PaymentIntentNo == nil || *got.PaymentIntentNo != started.PaymentIntentNo {
		t.Fatalf("礼物确认单未落收款通道/意图号：%+v", got)
	}

	order, err := verifyGiftPayment(ctx, db, 12, true, intent.ID, giftPaymentVerifyInput{
		Approve: true, PaymentRef: started.PaymentIntentNo, ReceivedCents: 5000,
		EvidenceNote: "对私商户码到账核验",
	}, now)
	if err != nil {
		t.Fatalf("核销礼物收款失败：%v", err)
	}
	if order.Status != "paid" || order.AmountCents != 5000 || order.PaymentSource != giftPaymentSourceExternal {
		t.Fatalf("核销后礼物单不符：%+v", order)
	}
	after := db.EsportsGiftIntent.GetX(ctx, intent.ID)
	if after.PaymentStatus != "verified" || after.PaymentRef == "" || after.Status != "consumed" {
		t.Fatalf("礼物收款状态未落终态：%+v", after)
	}
	// 外部收款不产生余额流水。
	balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance.total() != 0 {
		t.Fatalf("外部收款礼物不应产生余额流水：%+v", balance)
	}
	// 外部收款礼物的成长值来源为现金。
	row := db.EsportsGrowthLedger.Query().Where(
		esportsgrowthledger.SourceTypeEQ(esportsgrowthledger.SourceTypeGift),
		esportsgrowthledger.SourceIDEQ(order.GiftNo),
	).OnlyX(ctx)
	if row.PaymentSource != esportsgrowthledger.PaymentSourceCash {
		t.Fatalf("外部收款礼物成长值来源应为 cash：%+v", row)
	}
}
