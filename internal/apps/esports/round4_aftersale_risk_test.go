package esports

import (
	"errors"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsorderevent"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/esportsservicesegment"
)

// setSystemConfigForTest 写入一条系统配置（测试专用）。
func setSystemConfigForTest(t *testing.T, db *ent.Client, key, value string) {
	t.Helper()
	db.SystemConfig.Create().SetKey(key).SetName(key).SetValue(value).
		SetType("number").SetGroup("esports").SetStatus(1).SaveX(t.Context())
}

// orderEventCount 统计某订单上指定事件类型的条数。
func orderEventCount(t *testing.T, db *ent.Client, orderID int, eventType string) int {
	t.Helper()
	return db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(orderID),
		esportsorderevent.EventTypeEQ(eventType),
	).CountX(t.Context())
}

// lastOrderEventReason 取某订单指定事件类型最近一条的 details["reason"]。
func lastOrderEventReason(t *testing.T, db *ent.Client, orderID int, eventType string) string {
	t.Helper()
	row, err := db.EsportsOrderEvent.Query().Where(
		esportsorderevent.OrderIDEQ(orderID),
		esportsorderevent.EventTypeEQ(eventType),
	).Order(ent.Desc(esportsorderevent.FieldID)).First(t.Context())
	if err != nil {
		return ""
	}
	reason, _ := row.Details["reason"].(string)
	return reason
}

// seedRefundFailureOrder 构造一张没有钱包核销流水的已付款订单：
// 「退原核销款」找不到原核销流水，是当前唯一可确定性构造的退款执行失败路径。
func seedRefundFailureOrder(t *testing.T, db *ent.Client, noSuffix string) *ent.EsportsOrder {
	t.Helper()
	return db.EsportsOrder.Create().
		SetOrderNo("AS-R4-" + noSuffix).SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(2000).SetPayableCents(2000).
		SetPromisedServiceUnits(4).SetRequiredSeats(1).
		SetPaymentDeadline(time.Now().UTC().Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).
		SaveX(t.Context())
}

// seedPendingExecutionRefund 直接落一张待执行的原路退款售后单（跳过客户确认与审批）。
func seedPendingExecutionRefund(t *testing.T, db *ent.Client, orderID int, amount int64) *ent.EsportsAfterSale {
	t.Helper()
	return db.EsportsAfterSale.Create().SetAfterSaleNo(afterSaleNo()).
		SetSourceType(esportsaftersale.SourceTypeOrder).SetSourceID(orderID).
		SetKind(esportsaftersale.KindRefundRequest).SetChannel(esportsaftersale.ChannelCustomer).
		SetDemand("客户反馈服务量不足，申请退款").SetStatus(esportsaftersale.StatusPendingExecution).
		SetFaultParty(esportsaftersale.FaultPartyPlayer).
		SetPlanAction(esportsaftersale.PlanActionRefundOriginal).
		SetRefundCents(amount).SetResolution("按差额退款").
		SetHandlerAdminID(11).SaveX(t.Context())
}

// 订单级退款状态必须能落到 failed 且失败原因可查：
// 售后「退原核销款」执行失败时，订单不能停在旧的金额派生态，须置 failed 并留原因。
func TestAfterSaleRefundExecutionFailureMarksOrderFailed(t *testing.T) {
	db := openAfterSaleDB(t, "esports_r4_refund_failed")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

	order := seedRefundFailureOrder(t, db, "FAILED")
	item := seedPendingExecutionRefund(t, db, order.ID, 500)
	// 审批阶段应写入的预占额度：直接对齐，突出「执行失败」这一环。
	db.EsportsOrder.UpdateOneID(order.ID).SetRefundReservedCents(500).
		SetRefundStatus(esportsorder.RefundStatusPendingApproval).ExecX(ctx)

	if _, err := executeAfterSale(ctx, db, item.ID, 22, now); err == nil {
		t.Fatal("无原核销流水的退款执行应失败，实际返回 nil")
	}
	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.RefundStatus != esportsorder.RefundStatusFailed {
		t.Fatalf("退款执行失败后订单退款状态应落 failed，实际 %q", got.RefundStatus)
	}
	if reason := lastOrderEventReason(t, db, order.ID, "order.refund_failed"); reason == "" {
		t.Fatal("退款失败原因未留痕：缺少带 reason 的 order.refund_failed 事件")
	}
}

// 外部渠道退款执行：订单级退款状态须真实经过 executing，并在执行失败时落 failed 且可查原因。
func TestExternalRefundExecutionWritesExecutingThenFailed(t *testing.T) {
	db := openAfterSaleDB(t, "esports_r4_external_refund_states")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

	order := seedRefundFailureOrder(t, db, "ER-STATES")
	// 已预占 800：伪造一笔 1500 的待执行外部退款，执行时额度条件不成立 → 执行失败。
	db.EsportsOrder.UpdateOneID(order.ID).SetRefundReservedCents(800).ExecX(ctx)
	forged := seedExternalRefundRow(t, db, "ER-R4-STATES-1", order.ID, 1500, 11, nil)

	if _, err := executeExternalRefund(ctx, db, 12, forged.ID, now); !errors.Is(err, errConflict) {
		t.Fatalf("超预占的外部退款执行应被拦截，实际 err=%v", err)
	}
	if n := orderEventCount(t, db, order.ID, "order.refund_executing"); n != 1 {
		t.Fatalf("外部退款执行须把订单退款状态置 executing 并留痕，order.refund_executing 事件数=%d", n)
	}
	got := db.EsportsOrder.GetX(ctx, order.ID)
	if got.RefundStatus != esportsorder.RefundStatusFailed {
		t.Fatalf("外部退款执行失败后订单退款状态应落 failed，实际 %q", got.RefundStatus)
	}
	if reason := lastOrderEventReason(t, db, order.ID, "order.refund_failed"); reason == "" {
		t.Fatal("外部退款执行失败原因未留痕：缺少带 reason 的 order.refund_failed 事件")
	}
}

// seedProrateOrder 构造一张承诺 4 个单位、实际只履约 served 个单位的已付款订单（应付 2000 分）。
func seedProrateOrder(t *testing.T, db *ent.Client, suffix string, servedUnits int64) *ent.EsportsOrder {
	t.Helper()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	order := db.EsportsOrder.Create().
		SetOrderNo("AS-R4-PR-" + suffix).SetCustomerID(1).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(2000).SetPayableCents(2000).
		SetPromisedServiceUnits(4).SetRequiredSeats(1).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingClose).
		SaveX(ctx)
	db.EsportsServiceSegment.Create().SetOrderID(order.ID).SetPlayerID(1).SetSeatIndex(1).
		SetStatus(esportsservicesegment.StatusFinished).SetStartedAt(now).
		SetReportedUnits(servedUnits).SaveX(ctx)
	return order
}

// 未手填 refund_cents 时须按「不足比例」折算应退金额：
// 承诺 4 单位只履约 1 单位 → 不足 75% → 应退 2000 × 75% = 1500 分。
func TestAfterSalePlanProratesRefundByShortfall(t *testing.T) {
	db := openAfterSaleDB(t, "esports_r4_prorate")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

	order := seedProrateOrder(t, db, "SHORT", 1)
	created, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "order", SourceID: order.ID, Kind: "duration_short", Channel: "customer",
		Demand: "承诺 4 局只服务 1 局，要求按不足比例赔付",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = acceptAfterSale(ctx, db, created.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	planned, err := submitAfterSalePlan(ctx, db, created.ID, 11, afterSalePlanInput{
		FaultParty: "player", PlanAction: "refund_original", Resolution: "按不足比例折算赔付",
	}, now)
	if err != nil {
		t.Fatalf("未手填退款额时提交方案应成功（按不足比例折算）：%v", err)
	}
	if planned.RefundCents != 1500 {
		t.Fatalf("未手填 refund_cents 时应按不足比例折算为 1500 分，实际 %d 分", planned.RefundCents)
	}
}

// 不足比例为 0（履约量已达承诺量）时不得静默落 0，须明确失败提示手工填写。
func TestAfterSalePlanProrationRejectsNoShortfall(t *testing.T) {
	db := openAfterSaleDB(t, "esports_r4_prorate_zero")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

	order := seedProrateOrder(t, db, "FULL", 4)
	created, err := createAfterSale(ctx, db, afterSaleInput{
		SourceType: "order", SourceID: order.ID, Kind: "duration_short", Channel: "customer",
		Demand: "服务量已足，仍申请按比例赔付",
	}, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = acceptAfterSale(ctx, db, created.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	_, err = submitAfterSalePlan(ctx, db, created.ID, 11, afterSalePlanInput{
		FaultParty: "player", PlanAction: "refund_original", Resolution: "按不足比例折算赔付",
	}, now)
	if !errors.Is(err, errInvalidInput) {
		t.Fatalf("履约量已达承诺量、未手填退款额时应明确失败（不静默落 0），实际 err=%v", err)
	}
}

// 退款金额达阈值须管理员 / 财务角色复核：
// 阈值以上：非财务角色审批被拒；阈值以下：照常放行。
func TestLargeRefundApprovalRequiresFinanceReview(t *testing.T) {
	db := openAfterSaleDB(t, "esports_r4_refund_threshold")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	setSystemConfigForTest(t, db, "app.esports.aftersale_refund_review_threshold_cents", "1000")

	order := seedRefundFailureOrder(t, db, "THRESHOLD")
	big := newRefundAfterSale(t, db, order.ID, "refund_request", 1500, 11, 12, now)
	if _, err := approveAfterSale(ctx, db, big.ID, 12, now); !errors.Is(err, errForbidden) {
		t.Fatalf("1500 分退款（阈值 1000）非财务角色审批应被拒，实际 err=%v", err)
	}
	if got := db.EsportsAfterSale.GetX(ctx, big.ID); got.Status != esportsaftersale.StatusPendingApproval {
		t.Fatalf("大额审批被拒后状态不应变化：%+v", got)
	}

	order2 := seedRefundFailureOrder(t, db, "THRESHOLD-SMALL")
	small := newRefundAfterSale(t, db, order2.ID, "refund_request", 800, 11, 13, now)
	if _, err := approveAfterSale(ctx, db, small.ID, 13, now); err != nil {
		t.Fatalf("阈值以下退款审批不应被财务门槛拦截：%v", err)
	}
}

// 高频退款客户须能被自动置 restricted 并真正拦住自助下单（不再只落 watch）。
func TestHighRiskCustomerAutoRestricted(t *testing.T) {
	db := openAfterSaleDB(t, "esports_r4_risk_restrict")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

	customer := saveCustomerOrFail(t, db, 11)
	for i := 0; i < 6; i++ {
		db.EsportsAfterSale.Create().SetAfterSaleNo("AS-R4-RISK-" + string(rune('A'+i))).
			SetSourceType(esportsaftersale.SourceTypeOrder).SetSourceID(i + 1).
			SetKind(esportsaftersale.KindRefundRequest).SetChannel(esportsaftersale.ChannelCustomer).
			SetDemand("客户申请退款").SetStatus(esportsaftersale.StatusCompleted).
			SetCustomerID(customer.ID).SetRefundCents(1000).
			SetCreatedAt(now.Add(-time.Hour)).SaveX(ctx)
	}
	flagged, err := flagHighRiskCustomers(ctx, db, now, 200)
	if err != nil || flagged != 1 {
		t.Fatalf("超过限制档阈值应标记 1 个客户：%v %d", err, flagged)
	}
	got := db.EsportsCustomer.GetX(ctx, customer.ID)
	if got.RiskStatus != esportscustomer.RiskStatusRestricted {
		t.Fatalf("超过限制档阈值的客户应自动置 restricted，实际 %q", got.RiskStatus)
	}
	if err = ensureCustomerSelfServiceAllowed(got); !errors.Is(err, errForbidden) {
		t.Fatalf("自动 restricted 后自助下单须被拦截，实际 err=%v", err)
	} else if !strings.Contains(err.Error(), "申诉") {
		t.Fatalf("拦截提示须保留申诉入口指引，实际：%v", err)
	}
	if _, err = placeCustomerSelfOrder(ctx, db, got, customerOrderInput{
		SkuID: 1, Quantity: 1, IdempotencyKey: "r4-risk-restricted"}, now); !errors.Is(err, errForbidden) {
		t.Fatalf("自动 restricted 后自助下单须 403 拦截，实际 err=%v", err)
	}
	// 申诉 / 人工复核入口：客服可解除并恢复自助下单。
	cleared, err := markCustomerRisk(ctx, db, customer.ID, "normal", "", now)
	if err != nil || cleared.RiskStatus != esportscustomer.RiskStatusNormal {
		t.Fatalf("人工复核解除失败：%v %+v", err, cleared)
	}
	if err = ensureCustomerSelfServiceAllowed(cleared); err != nil {
		t.Fatalf("解除后自助下单不应再被风控拦截：%v", err)
	}
}

// 误伤护栏：把限制档阈值显式置 0（未配置）时，自动风控只落 watch、不限制下单。
func TestHighRiskAutoRestrictDisabledKeepsWatch(t *testing.T) {
	db := openAfterSaleDB(t, "esports_r4_risk_watch_only")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	setSystemConfigForTest(t, db, "app.esports.risk_restrict_refund_count", "0")
	setSystemConfigForTest(t, db, "app.esports.risk_restrict_refund_cents", "0")

	customer := saveCustomerOrFail(t, db, 11)
	for i := 0; i < 6; i++ {
		db.EsportsAfterSale.Create().SetAfterSaleNo("AS-R4-WATCH-" + string(rune('A'+i))).
			SetSourceType(esportsaftersale.SourceTypeOrder).SetSourceID(i + 1).
			SetKind(esportsaftersale.KindRefundRequest).SetChannel(esportsaftersale.ChannelCustomer).
			SetDemand("客户申请退款").SetStatus(esportsaftersale.StatusCompleted).
			SetCustomerID(customer.ID).SetRefundCents(1000).
			SetCreatedAt(now.Add(-time.Hour)).SaveX(ctx)
	}
	if _, err := flagHighRiskCustomers(ctx, db, now, 200); err != nil {
		t.Fatal(err)
	}
	got := db.EsportsCustomer.GetX(ctx, customer.ID)
	if got.RiskStatus != esportscustomer.RiskStatusWatch {
		t.Fatalf("限制档未配置时只应落 watch（避免误伤），实际 %q", got.RiskStatus)
	}
	if err := ensureCustomerSelfServiceAllowed(got); err != nil {
		t.Fatalf("watch 不应拦截自助下单：%v", err)
	}
}

// 严格模式：未关联售后单的外部退款登记（无赔付依据的对外付款）必须被拒。
func TestExternalRefundWithoutAfterSaleRejectedInStrictMode(t *testing.T) {
	db := openAfterSaleDB(t, "esports_r4_external_refund_strict")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	setSystemConfigForTest(t, db, "app.esports.external_refund_require_aftersale", "1")

	order := seedRefundFailureOrder(t, db, "ER-STRICT")
	_, err := registerExternalRefund(ctx, db, 11, registerExternalRefundInput{
		Channel: "wechat_work", TransactionNo: "ER-R4-STRICT-1", AmountCents: 300,
		OrderNo: order.OrderNo, Reason: "私下赔付",
	}, now)
	if !errors.Is(err, errInvalidInput) {
		t.Fatalf("严格模式下未关联售后单的外部退款登记应被拒，实际 err=%v", err)
	}
	if n := db.EsportsExternalRefund.Query().CountX(ctx); n != 0 {
		t.Fatalf("被拒的登记不应落库：%d", n)
	}
}

// 私下赔付留痕：未关联售后单的对外付款必须额外写「私下赔付疑似」外盒，供风控 / 财务复核。
func TestExternalRefundWithoutAfterSaleLeavesOffbookTrail(t *testing.T) {
	db := openAfterSaleDB(t, "esports_r4_external_refund_offbook")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

	order := seedRefundFailureOrder(t, db, "ER-OFFBOOK")
	if _, err := registerExternalRefund(ctx, db, 11, registerExternalRefundInput{
		Channel: "wechat_work", TransactionNo: "ER-R4-OFFBOOK-1", AmountCents: 300,
		OrderNo: order.OrderNo, Reason: "无售后依据的对外付款",
	}, now); err != nil {
		t.Fatalf("默认模式下未关联售后单的外部退款登记应可登记：%v", err)
	}
	rows, err := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("payout.offbook_suspected"),
	).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("未关联售后单的对外付款应写私下赔付疑似留痕，实际 %d 条", len(rows))
	}
	if payload := rows[0].Payload; len(payload) == 0 || payload["refund_no"] == nil {
		t.Fatalf("私下赔付留痕内容不完整：%+v", payload)
	}
	// 登记事件仍须留痕，且不得再有售后依据（本用例未关联售后单）。
	if n := orderEventCount(t, db, order.ID, "order.external_refund_registered"); n != 1 {
		t.Fatalf("外部退款登记事件应留痕 1 条，实际 %d", n)
	}
}
