package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportscommissionentry"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportssettlement"
	"go-admin/internal/ent/esportswithdrawal"
	"go-admin/internal/ent/migrate"
)

// TestE2EEndToEndBusinessFlow 用同一批真实角色与测试数据串联完整业务闭环：
// 下单 → 付款 → 派单（抢单）→ 服务 → 结单 → 结算 → 提现 → 售后，
// 并贯穿校验权限（越权下单 / 越权抢单 / 自审 / 越权看他人售后）、
// 失败重试与幂等（重复下单 / 重复结单 / 重复提现 / 重复退款）、对账（reconcileLedgers 无异常）。
//
// 角色分派体现「不得自审 / 越权」：
//   - 客服 adminID=11：下单、付款、发布抢单、受理售后（处理人）
//   - 陪玩 playerID（user 1001）：抢单、服务、提现
//   - 财务 adminID=12：结算锁定、提现审核、售后执行
//   - 审核人 adminID=13：售后审批（异人，不得为处理人）
//   - 无关客服 adminID=99：越权下单 / 越权看售后（被拒）
//   - 未合格陪玩（user 1002）：越权抢单（被拒）
func TestE2EEndToEndBusinessFlow(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_e2e_flow?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()

	const (
		csAdmin     = 11 // 客服：下单 / 付款 / 发布抢单 / 受理售后
		finance     = 12 // 财务：结算锁定 / 提现审核 / 售后执行
		reviewAdmin = 13 // 审核人：售后审批（异人）
		otherAdmin  = 99 // 无关客服：越权场景
	)

	// 支付时限配置。
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)

	// 角色与数据。
	customer := saveCustomerOrFail(t, db, csAdmin)
	player := seedPlayer(t, db, 1001, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	pendingPlayer := seedPlayer(t, db, 1002, esportsplayer.QualificationPending, esportsplayer.PresenceOnline)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "E2E-GAME", Title: "E2E游戏陪玩", ServiceType: "play",
		UnitPriceCents: 1000, ServiceUnits: 1, ServiceUnitName: "局",
		RequiredSeats: 1, QuotaTotal: 10, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	// 分成规则：percent 1000bp（10%），结单后陪玩分成 = 应付额 * 10%。
	seedActiveRule(t, db, ruleInput{
		RuleNo: "E2E", Scope: "global", CalcMode: "percent", ValueBp: 1000, UnitBasis: "sku_quantity",
	})

	// ========== 1. 下单（客服 + 在售 SKU）==========
	in := orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1}
	order, err := placeOrder(ctx, db, csAdmin, in, "e2e-order-key-0001", now)
	if err != nil {
		t.Fatal(err)
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingPayment ||
		order.PaymentStatus != esportsorder.PaymentStatusUnpaid || order.PayableCents != 1000 {
		t.Fatalf("下单初始状态不正确：%+v", order)
	}
	// 越权下单：无关客服对他人客户下单被拒。
	if _, err := placeOrder(ctx, db, otherAdmin, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 1},
		"e2e-forbidden-0001", now); !errors.Is(err, errForbidden) {
		t.Fatalf("无关客服越权下单未被拦截：%v", err)
	}
	// 幂等：同一幂等键返回同一张订单，不重复占用名额。
	replay, err := placeOrder(ctx, db, csAdmin, in, "e2e-order-key-0001", now)
	if err != nil || replay.ID != order.ID {
		t.Fatalf("同幂等键重放应返回同一单：%+v, %v", replay, err)
	}

	// ========== 2. 付款（余额充值后核销 → 待派单）==========
	if _, err := walletRecharge(ctx, db, csAdmin, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 2000, Note: "E2E客户充值"}, "e2e-recharge-0001"); err != nil {
		t.Fatal(err)
	}
	paid, err := payOrderWithBalance(ctx, db, csAdmin, order.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if paid.FulfillmentStatus != esportsorder.FulfillmentStatusPendingDispatch ||
		paid.PaymentStatus != esportsorder.PaymentStatusPaid {
		t.Fatalf("付款后订单未转待派单：%+v", paid)
	}

	// ========== 3. 派单（发布抢单大厅 → 陪玩抢单 → 已接单）==========
	published, err := publishOrderToGrabHall(ctx, db, order.ID, csAdmin, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if published.FulfillmentStatus != esportsorder.FulfillmentStatusGrabbing {
		t.Fatalf("发布抢单大厅后订单未转抢单中：%+v", published)
	}
	// 越权抢单：未合格陪玩被拒。
	if _, err := grabPlayerOrder(ctx, db, pendingPlayer.ID, order.ID, now); !errors.Is(err, errForbidden) {
		t.Fatalf("未合格陪玩越权抢单未被拦截：%v", err)
	}
	grabbed, err := grabPlayerOrder(ctx, db, player.ID, order.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if grabbed.FulfillmentStatus != esportsorder.FulfillmentStatusAccepted {
		t.Fatalf("抢单后订单未转已接单：%+v", grabbed)
	}

	// ========== 4. 服务（开始 → 结束 → 待结单）==========
	started, err := startPlayerService(ctx, db, player.ID, order.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if started.FulfillmentStatus != esportsorder.FulfillmentStatusInService {
		t.Fatalf("开工后订单未转服务中：%+v", started)
	}
	finished, err := finishPlayerService(ctx, db, player.ID, order.ID, 1, 600, "ev.png", now.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if finished.FulfillmentStatus != esportsorder.FulfillmentStatusPendingClose {
		t.Fatalf("服务结束后订单未转待结单：%+v", finished)
	}

	// ========== 5. 结单（completed + 计提分成/提成/成长值）==========
	closed, err := closeOrder(ctx, db, order.ID, csAdmin, closeOrderInput{Note: "E2E结单"}, now.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if closed.FulfillmentStatus != esportsorder.FulfillmentStatusCompleted {
		t.Fatalf("结单后订单未转已完成：%+v", closed)
	}
	// 幂等：重复结单失败，不重复计提。
	if _, err := closeOrder(ctx, db, order.ID, csAdmin, closeOrderInput{Note: "重复结单"},
		now.Add(10*time.Minute)); !errors.Is(err, errConflict) {
		t.Fatalf("重复结单未被拦截：%v", err)
	}
	serviceEntry := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.OrderIDEQ(order.ID),
		esportscommissionentry.KindEQ(esportscommissionentry.KindService),
	).OnlyX(ctx)
	if serviceEntry.AmountCents != 100 || serviceEntry.PlayerID != player.ID ||
		serviceEntry.Status != esportscommissionentry.StatusPendingSettlement {
		t.Fatalf("陪玩分成计提不正确：%+v", serviceEntry)
	}

	// ========== 6. 结算（生成结算单并锁定，陪玩分成转 settled，猫粮入账）==========
	settlement, err := settlePlayerCommission(ctx, db, player.ID,
		now.Add(-time.Hour), now.Add(time.Hour), finance, now.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if settlement.Status != esportssettlement.StatusDraft || settlement.GrossCents != 100 ||
		settlement.NetCents != 100 || settlement.EntryCount != 1 {
		t.Fatalf("结算单汇总不正确：%+v", settlement)
	}
	locked, err := lockSettlement(ctx, db, settlement.ID, finance, now.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if locked.Status != esportssettlement.StatusLocked {
		t.Fatalf("结算单锁定失败：%+v", locked)
	}
	catfood, err := walletBalanceOf(ctx, db, walletPlayerCatfood, player.ID)
	if err != nil {
		t.Fatal(err)
	}
	if catfood.total() != 100 {
		t.Fatalf("锁定后猫粮应入账 100，got=%d", catfood.total())
	}

	// ========== 7. 提现（申请 → 财务审核通过 → 打款）==========
	wd, err := applyWithdrawal(ctx, db, player.ID, validApply("e2e-wd-key-0001", 40), now.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if wd.Status != esportswithdrawal.StatusPendingReview {
		t.Fatalf("提现单初始状态不正确：%+v", wd)
	}
	// 幂等：同一幂等键重放返回同一张提现单，不重复扣钱。
	wdReplay, err := applyWithdrawal(ctx, db, player.ID, validApply("e2e-wd-key-0001", 40), now.Add(10*time.Minute))
	if err != nil || wdReplay.ID != wd.ID {
		t.Fatalf("提现幂等重放应返回同一单：%+v, %v", wdReplay, err)
	}
	if catfood, err = walletBalanceOf(ctx, db, walletPlayerCatfood, player.ID); err != nil {
		t.Fatal(err)
	} else if catfood.total() != 60 {
		t.Fatalf("提现幂等重放不应重复扣减，猫粮应为 60，got=%d", catfood.total())
	}
	approvedWd, err := reviewWithdrawal(ctx, db, finance, wd.ID, withdrawalReviewInput{Approve: true}, now.Add(11*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if approvedWd.Status != esportswithdrawal.StatusApproved {
		t.Fatalf("提现审核未通过：%+v", approvedWd)
	}
	paidWd, err := payWithdrawal(ctx, db, finance+1, wd.ID, "BANK-E2E-0001", "", now.Add(11*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if paidWd.Status != esportswithdrawal.StatusPaid {
		t.Fatalf("提现打款未完成：%+v", paidWd)
	}

	// ========== 8. 售后（创建 → 审批异人 → 执行退款并冲回分成/成长值）==========
	created, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "order", SourceID: order.ID, Kind: "refund_request", Channel: "customer",
		Demand: "客户反馈服务时长不足，申请部分退款", ExpectedPlan: "按差额退款",
	}, csAdmin, now.Add(12*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != esportsaftersale.StatusPendingAccept {
		t.Fatalf("新建售后状态不正确：%+v", created)
	}
	// 越权看售后：无关客服无法读取他人订单售后。
	if _, err := loadOwnedAfterSale(ctx, db, otherAdmin, false, created.ID); !errors.Is(err, errForbidden) {
		t.Fatalf("无关客服越权读取售后未被拦截：%v", err)
	}
	if _, err = acceptAfterSale(ctx, db, created.ID, csAdmin, now.Add(12*time.Minute)); err != nil {
		t.Fatal(err)
	}
	planned, err := submitAfterSalePlan(ctx, db, created.ID, csAdmin, afterSalePlanInput{
		FaultParty: "player", PlanAction: "refund_original", RefundCents: 500, Resolution: "陪玩服务不足，按差额退款 500",
	}, now.Add(12*time.Minute))
	if err != nil || planned.Status != esportsaftersale.StatusPendingCustomerConfirm {
		t.Fatalf("提交售后方案失败：%v %+v", err, planned)
	}
	confirmed, err := confirmAfterSaleByAdmin(ctx, db, created.ID, csAdmin, now.Add(12*time.Minute))
	if err != nil || confirmed.Status != esportsaftersale.StatusPendingApproval {
		t.Fatalf("代客户确认售后方案失败：%v %+v", err, confirmed)
	}
	// 自审被禁：处理客服（11）不能审批自己受理的售后。
	if _, err := approveAfterSale(ctx, db, created.ID, csAdmin, now.Add(12*time.Minute)); !errors.Is(err, errForbidden) {
		t.Fatalf("售后自审未被拦截：%v", err)
	}
	approvedAs, err := approveAfterSale(ctx, db, created.ID, reviewAdmin, now.Add(12*time.Minute))
	if err != nil || approvedAs.Status != esportsaftersale.StatusPendingExecution {
		t.Fatalf("异人审批售后失败：%v %+v", err, approvedAs)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundReservedCents != 500 {
		t.Fatalf("审批未预占退款额度：%+v", got)
	}
	executed, err := executeAfterSale(ctx, db, created.ID, finance, now.Add(12*time.Minute))
	if err != nil || executed.Status != esportsaftersale.StatusCompleted {
		t.Fatalf("售后执行失败：%v %+v", err, executed)
	}
	// 幂等：重复执行退款被拦截，不重复冲回、不重复退款。
	if _, err := executeAfterSale(ctx, db, created.ID, finance, now.Add(12*time.Minute)); !errors.Is(err, errConflict) {
		t.Fatalf("重复执行售后未被拦截：%v", err)
	}
	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.RefundedCents != 500 || got.RefundReservedCents != 0 || got.RefundStatus != esportsorder.RefundStatusPartial {
		t.Fatalf("订单退款额度落账不正确：%+v", got)
	}
	reversal := db.EsportsCommissionEntry.Query().Where(
		esportscommissionentry.OrderIDEQ(order.ID),
		esportscommissionentry.KindEQ(esportscommissionentry.KindReversal),
	).OnlyX(ctx)
	if reversal.AmountCents != -50 || reversal.PlayerID != player.ID {
		t.Fatalf("退款未按比例冲回分成：%+v", reversal)
	}
	// 客户余额原路退回 500。
	customerBalance, err := walletBalanceOf(ctx, db, walletCustomerBalance, customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if customerBalance.total() != 1500 {
		t.Fatalf("售后退款后客户余额应为 1500，got=%d", customerBalance.total())
	}

	// 冲回分成进入下一结算周期：结算 -50 冲回并锁定，猫粮从 60 → 10。
	reversalSettlement, err := settlePlayerCommission(ctx, db, player.ID,
		now.Add(-time.Hour), now.Add(time.Hour), finance, now.Add(12*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if reversalSettlement.AdjustmentCents != -50 || reversalSettlement.NetCents != -50 {
		t.Fatalf("冲回结算单汇总不正确：%+v", reversalSettlement)
	}
	if _, err := lockSettlement(ctx, db, reversalSettlement.ID, finance, now.Add(12*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if catfood, err = walletBalanceOf(ctx, db, walletPlayerCatfood, player.ID); err != nil {
		t.Fatal(err)
	} else if catfood.total() != 10 {
		t.Fatalf("冲回结算后猫粮应为 10，got=%d", catfood.total())
	}

	// 关键事件留痕完整性（贯穿整个闭环）。
	events := countEventsByType(t, db)
	for _, want := range []string{
		"order.created", "order.payment_verified", "order.published_to_grab_hall", "order.grabbed",
		"order.service_started", "order.service_finished", "order.closed", "order.refund_executed",
	} {
		if events[want] == 0 {
			t.Fatalf("缺少关键事件 %s：%+v", want, events)
		}
	}

	// ========== 对账（账本无异常，余额与流水一致）==========
	result, err := reconcileLedgers(ctx, db, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Anomalies) != 0 {
		t.Fatalf("对账应无异常，实际 %d 条：%+v", len(result.Anomalies), result.Anomalies)
	}
}
