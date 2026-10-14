package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsfulfillmentmakeup"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportswallettx"
	"go-admin/internal/ent/migrate"
)

func openAfterSaleDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	return enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
}

// seedBalancePaidOrder 构造一张以客户余额核销的已付款订单（应付 2000，核销流水：本金 -1000 / 赠送 -1000）。
func seedBalancePaidOrder(t *testing.T, db *ent.Client, suffix string) *ent.EsportsOrder {
	t.Helper()
	ctx := t.Context()
	db.SystemConfig.Create().SetKey("app.esports.payment_deadline_minutes").SetName("订单支付时限").
		SetValue("15").SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	customer := saveCustomerOrFail(t, db, 11)
	sku, err := saveSku(ctx, db, skuInput{
		Code: "GAME-AS-" + suffix, Title: "售后退款测试商品", ServiceType: "play",
		UnitPriceCents: 1000, ServiceUnits: 1, ServiceUnitName: "局", RequiredSeats: 1,
		QuotaTotal: 10, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	order, err := placeOrder(ctx, db, 11, orderInput{CustomerID: customer.ID, SkuID: sku.ID, Quantity: 2},
		"as-key-"+suffix, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := walletRecharge(ctx, db, 11, walletCustomerBalance, customer.ID,
		walletInput{PrincipalCents: 3000, BonusCents: 1000, Note: "售后退款测试充值"},
		"as-recharge-"+suffix); err != nil {
		t.Fatal(err)
	}
	paid, err := payOrderWithBalance(ctx, db, 11, order.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	return paid
}

func newRefundAfterSale(t *testing.T, db *ent.Client, orderID int, kind string, refundCents int64, handler, approver int, now time.Time) *ent.EsportsAfterSale {
	t.Helper()
	ctx := t.Context()
	created, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "order", SourceID: orderID, Kind: kind, Channel: "customer",
		Demand: "客户反馈服务与约定不符，申请退款", ExpectedPlan: "按差额退款",
	}, handler, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acceptAfterSale(ctx, db, created.ID, handler, now); err != nil {
		t.Fatal(err)
	}
	if _, err := submitAfterSalePlan(ctx, db, created.ID, handler, afterSalePlanInput{
		FaultParty: "player", PlanAction: "refund_original", RefundCents: refundCents, Resolution: "陪玩迟到，按差额退款",
	}, now); err != nil {
		t.Fatal(err)
	}
	// §13.3：方案提交后须经「待客户确认」节点；用例中由客服代客户确认进入待审批。
	planned, err := confirmAfterSaleByAdmin(ctx, db, created.ID, handler, now)
	if err != nil {
		t.Fatal(err)
	}
	return planned
}

// 售后全流程：受理 → 方案 → 禁自审 → 换人审批（预占额度）→ 执行（退款流水 + 额度落账 + 幂等）。
func TestAfterSaleRefundOriginalLifecycle(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_flow")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	order := seedBalancePaidOrder(t, db, "flow")
	created, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "order", SourceID: order.ID, Kind: "refund_request", Channel: "customer",
		Demand: "客户反馈服务时长不足，申请退款", ExpectedPlan: "按差额退款",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != esportsaftersale.StatusPendingAccept {
		t.Fatalf("新建售后状态不正确：%+v", created)
	}
	if countEventsByType(t, db)["order.after_sale_created"] != 1 {
		t.Fatal("缺少 order.after_sale_created 事件")
	}
	// 同来源同问题重复受理被拦截。
	if _, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "order", SourceID: order.ID, Kind: "refund_request", Channel: "admin", Demand: "重复受理",
	}, 11, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复售后未拦截：%v", err)
	}

	accepted, err := acceptAfterSale(ctx, db, created.ID, 11, now)
	if err != nil || accepted.Status != esportsaftersale.StatusProcessing {
		t.Fatalf("受理失败：%v %+v", err, accepted)
	}
	planned, err := submitAfterSalePlan(ctx, db, created.ID, 11, afterSalePlanInput{
		FaultParty: "player", PlanAction: "refund_original", RefundCents: 1500, Resolution: "陪玩迟到，按差额退款",
	}, now)
	if err != nil || planned.Status != esportsaftersale.StatusPendingCustomerConfirm {
		t.Fatalf("提交方案应进入待客户确认：%v %+v", err, planned)
	}
	// §13.3：客户未确认时不得审批。
	if _, err := approveAfterSale(ctx, db, created.ID, 12, now); !errors.Is(err, errConflict) {
		t.Fatalf("未确认方案不应可审批：%v", err)
	}
	confirmed, err := confirmAfterSaleByAdmin(ctx, db, created.ID, 11, now)
	if err != nil || confirmed.Status != esportsaftersale.StatusPendingApproval ||
		confirmed.CustomerConfirmChannel != esportsaftersale.CustomerConfirmChannelAdminProxy {
		t.Fatalf("代客户确认失败：%v %+v", err, confirmed)
	}
	// 自审被禁止（审批人 == 处理人）。
	if _, err := approveAfterSale(ctx, db, created.ID, 11, now); !errors.Is(err, errForbidden) {
		t.Fatalf("自审未被拦截：%v", err)
	}
	approved, err := approveAfterSale(ctx, db, created.ID, 12, now)
	if err != nil || approved.Status != esportsaftersale.StatusPendingExecution {
		t.Fatalf("换人审批失败：%v %+v", err, approved)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundReservedCents != 1500 {
		t.Fatalf("审批未预占退款额度：%+v", got)
	}

	done, err := executeAfterSale(ctx, db, created.ID, 12, now)
	if err != nil || done.Status != esportsaftersale.StatusCompleted {
		t.Fatalf("执行失败：%v %+v", err, done)
	}
	// 幂等：重复执行被拦截。
	if _, err := executeAfterSale(ctx, db, created.ID, 12, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复执行未拦截：%v", err)
	}

	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.RefundedCents != 1500 || got.RefundReservedCents != 0 || got.RefundStatus != esportsorder.RefundStatusPartial {
		t.Fatalf("订单退款额度落账不正确：%+v", got)
	}
	refundTx := db.EsportsWalletTx.Query().Where(
		esportswallettx.EntryTypeEQ(esportswallettx.EntryTypeRefundReturn)).OnlyX(ctx)
	if refundTx.PrincipalDeltaCents != 1000 || refundTx.BonusDeltaCents != 500 {
		t.Fatalf("退款流水构成为先本金后赠送不正确：%+v", refundTx)
	}
	if refundTx.SourceType != "after_sale" || refundTx.SourceID != created.AfterSaleNo || refundTx.OwnerID != order.CustomerID {
		t.Fatalf("退款流水幂等键或归属不正确：%+v", refundTx)
	}
	if countEventsByType(t, db)["order.refund_executed"] != 1 {
		t.Fatal("缺少 order.refund_executed 事件")
	}
}

// 超额退款被拦截；撤回非终态售后成功，已终态不能再撤。
func TestAfterSaleOverRefundAndWithdraw(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_guard")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	order := seedBalancePaidOrder(t, db, "guard")
	first := newRefundAfterSale(t, db, order.ID, "refund_request", 1000, 11, 12, now)
	if _, err := approveAfterSale(ctx, db, first.ID, 12, now); err != nil {
		t.Fatal(err)
	}
	// 第二张（不同问题）叠加后超出应付额 2000，审批应因额度不足被拦截。
	second := newRefundAfterSale(t, db, order.ID, "duration_short", 2500, 11, 12, now)
	if _, err := approveAfterSale(ctx, db, second.ID, 12, now); !errors.Is(err, errConflict) {
		t.Fatalf("超额退款未被拦截：%v", err)
	}
	withdrawn, err := withdrawAfterSale(ctx, db, second.ID, 11, now)
	if err != nil || withdrawn.Status != esportsaftersale.StatusWithdrawn || withdrawn.ResolvedAt == nil {
		t.Fatalf("撤回失败：%v %+v", err, withdrawn)
	}
	if _, err := withdrawAfterSale(ctx, db, second.ID, 11, now); !errors.Is(err, errConflict) {
		t.Fatalf("已终态售后被重复撤回：%v", err)
	}
}

// 已审批预占额度的售后单被撤回时，必须原路释放预占，否则该订单的退款上限会被永久占住。
func TestAfterSaleWithdrawReleasesReservation(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_withdraw_release")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	order := seedBalancePaidOrder(t, db, "withdraw")
	item := newRefundAfterSale(t, db, order.ID, "refund_request", 1200, 11, 12, now)
	if _, err := approveAfterSale(ctx, db, item.ID, 12, now); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundReservedCents != 1200 {
		t.Fatalf("审批未预占额度：%+v", got)
	}
	if _, err := withdrawAfterSale(ctx, db, item.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.RefundReservedCents != 0 || got.RefundedCents != 0 {
		t.Fatalf("撤回未释放预占额度：%+v", got)
	}
}

// 无原余额核销流水时退原核销款不可执行；补履约方案缺参数时在提交方案阶段被拒。
func TestAfterSaleExecuteUnsupportedPaths(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_unsupported")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	// 未走余额核销的订单，无 consume 流水。
	noBalance := seedPaidOrder(t, db, "as-no-balance", 1)
	noBalanceItem := newRefundAfterSale(t, db, noBalance.ID, "refund_request", 500, 11, 12, now)
	if _, err := approveAfterSale(ctx, db, noBalanceItem.ID, 12, now); err != nil {
		t.Fatal(err)
	}
	if _, err := executeAfterSale(ctx, db, noBalanceItem.ID, 12, now); !errors.Is(err, errNotConfigured) {
		t.Fatalf("无余额核销流水的退款未拦截：%v", err)
	}
	if got := db.EsportsAfterSale.GetX(ctx, noBalanceItem.ID); got.Status != esportsaftersale.StatusPendingExecution {
		t.Fatalf("配置缺失不应落为执行失败：%+v", got)
	}

	// 补履约方案缺少承接陪玩与补钟 / 换人类型时，提交方案阶段即被拒（§13.6）。
	makeupOrder := seedPaidOrder(t, db, "as-makeup", 1)
	created, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "order", SourceID: makeupOrder.ID, Kind: "no_show", Channel: "player", Demand: "陪玩未到场，需补钟",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acceptAfterSale(ctx, db, created.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	if _, err := submitAfterSalePlan(ctx, db, created.ID, 11, afterSalePlanInput{
		FaultParty: "player", PlanAction: "makeup_service", RefundCents: 0, Resolution: "安排补钟",
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("补履约方案缺少类型与承接陪玩未拦截：%v", err)
	}
}

// 补履约（补钟）：方案校验承接陪玩 / 席位 / 补足量、审批不占退款额度、执行创建任务，任务可开始与验收。
func TestAfterSaleMakeupServiceFlow(t *testing.T) {
	db := openAfterSaleDB(t, "esports_aftersale_makeup")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	order := seedBalancePaidOrder(t, db, "makeup")
	player := seedPlayer(t, db, 4201, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	pending := seedPlayer(t, db, 4202, esportsplayer.QualificationPending, esportsplayer.PresenceOnline)

	created, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "order", SourceID: order.ID, Kind: "duration_short", Channel: "customer",
		Demand: "客户反馈时长不足，要求补钟", ExpectedPlan: "补足时长",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = acceptAfterSale(ctx, db, created.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	// 非法补履约方案：缺类型 / 缺陪玩 / 席位越界 / 补钟量非正 / 陪玩未合格 / 带退款金额。
	for i, in := range []afterSalePlanInput{
		{FaultParty: "player", PlanAction: "makeup_service", MakeupPlayerID: player.ID, MakeupSeatIndex: 1, MakeupUnits: 1},
		{FaultParty: "player", PlanAction: "makeup_service", MakeupKind: "makeup_time", MakeupSeatIndex: 1, MakeupUnits: 1},
		{FaultParty: "player", PlanAction: "makeup_service", MakeupKind: "makeup_time", MakeupPlayerID: player.ID, MakeupSeatIndex: 9, MakeupUnits: 1},
		{FaultParty: "player", PlanAction: "makeup_service", MakeupKind: "makeup_time", MakeupPlayerID: player.ID, MakeupSeatIndex: 1, MakeupUnits: 0},
		{FaultParty: "player", PlanAction: "makeup_service", MakeupKind: "makeup_time", MakeupPlayerID: pending.ID, MakeupSeatIndex: 1, MakeupUnits: 1},
		{FaultParty: "player", PlanAction: "makeup_service", MakeupKind: "makeup_time", MakeupPlayerID: player.ID, MakeupSeatIndex: 1, MakeupUnits: 1, RefundCents: 100},
	} {
		if _, err = submitAfterSalePlan(ctx, db, created.ID, 11, in, now); !errors.Is(err, errInvalidInput) {
			t.Fatalf("第 %d 组非法补履约方案未拦截：%v", i+1, err)
		}
	}

	planned, err := submitAfterSalePlan(ctx, db, created.ID, 11, afterSalePlanInput{
		FaultParty: "player", PlanAction: "makeup_service", MakeupKind: "makeup_time",
		MakeupPlayerID: player.ID, MakeupSeatIndex: 1, MakeupUnits: 2,
		MakeupCompanyPayCents: 800, Resolution: "陪玩时长不足，公司出钱补钟 2 局",
	}, now)
	if err != nil || planned.Status != esportsaftersale.StatusPendingCustomerConfirm ||
		planned.MakeupKind != esportsaftersale.MakeupKindMakeupTime ||
		planned.MakeupPlayerID == nil || *planned.MakeupPlayerID != player.ID ||
		planned.RefundCents != 0 {
		t.Fatalf("补履约方案提交失败：%v %+v", err, planned)
	}
	// §13.3：补履约方案同样经「待客户确认」后才进入待审批。
	if _, err = confirmAfterSaleByAdmin(ctx, db, created.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	if _, err = approveAfterSale(ctx, db, created.ID, 12, now); err != nil {
		t.Fatal(err)
	}
	// 补履约不涉及退款：审批不预占额度。
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundReservedCents != 0 || got.RefundedCents != 0 {
		t.Fatalf("补履约不应占用退款额度：%+v", got)
	}

	done, err := executeAfterSale(ctx, db, created.ID, 12, now)
	if err != nil || done.Status != esportsaftersale.StatusCompleted {
		t.Fatalf("补履约执行失败：%v %+v", err, done)
	}
	// 原订单实收 / 成交额不变，退款额度不变。
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.RefundedCents != 0 || got.RefundReservedCents != 0 {
		t.Fatalf("补履约不应改变订单退款额：%+v", got)
	}
	task := db.EsportsFulfillmentMakeup.Query().OnlyX(ctx)
	if task.Status != esportsfulfillmentmakeup.StatusPending ||
		task.Kind != esportsfulfillmentmakeup.KindMakeupTime ||
		task.PlayerID != player.ID || task.SeatIndex != 1 ||
		task.Units != 2 || task.CompanyPayCents != 800 ||
		task.AfterSaleID != created.ID {
		t.Fatalf("补履约任务创建不正确：%+v", task)
	}
	if countEventsByType(t, db)["order.makeup_created"] != 1 {
		t.Fatal("缺少 order.makeup_created 事件")
	}
	if _, err := executeAfterSale(ctx, db, created.ID, 12, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复执行未拦截：%v", err)
	}

	// 开始服务 → 客服验收；重复开始 / 重复验收被拦截。
	started, err := startFulfillmentMakeup(ctx, db, task.ID, 12, now.Add(time.Minute))
	if err != nil || started.Status != esportsfulfillmentmakeup.StatusServicing || started.StartedAt == nil {
		t.Fatalf("开始补履约失败：%v %+v", err, started)
	}
	if _, err = startFulfillmentMakeup(ctx, db, task.ID, 12, now); !errors.Is(err, errConflict) {
		t.Fatalf("重复开始未拦截：%v", err)
	}
	completed, err := completeFulfillmentMakeup(ctx, db, task.ID, 12, "已补足 2 局", now.Add(31*time.Minute))
	if err != nil || completed.Status != esportsfulfillmentmakeup.StatusCompleted ||
		completed.SystemSeconds != 1800 || completed.FinishedAt == nil ||
		completed.AcceptedByAdminID == nil || *completed.AcceptedByAdminID != 12 ||
		completed.AcceptedAt == nil {
		t.Fatalf("补履约验收失败：%v %+v", err, completed)
	}
	if _, err = completeFulfillmentMakeup(ctx, db, task.ID, 12, "", now); !errors.Is(err, errConflict) {
		t.Fatalf("重复验收未拦截：%v", err)
	}
}
