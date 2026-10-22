package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsquotahold"
	"go-admin/internal/ent/esportsservicesegment"
	"go-admin/internal/ent/esportssku"
	"go-admin/internal/ent/esportsskususpension"
	"go-admin/internal/ent/esportsskususpensionitem"
	"go-admin/internal/ent/migrate"
)

// snapshotCount 读取统计快照里的计数：JSON 字段读回后数字是 float64，这里统一转成 int，
// 避免断言里散落类型转换。
func snapshotCount(t *testing.T, values map[string]interface{}, key string) int {
	t.Helper()
	switch value := values[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	default:
		t.Fatalf("统计快照缺少计数 %s：%+v", key, values)
		return 0
	}
}

// openSuspensionDB 停服测试基座：订单支付时限与重复报单提醒窗口都显式配置（窗口为 0 关闭提醒，
// 便于同一客户在同一 SKU 下批量造出多张在途订单）。
func openSuspensionDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	db := enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	ctx := t.Context()
	for _, item := range []struct{ key, name, value string }{
		{"app.esports.payment_deadline_minutes", "订单支付时限", "15"},
		{"app.esports.duplicate_order_window_minutes", "重复报单提醒时间窗", "0"},
	} {
		db.SystemConfig.Create().SetKey(item.key).SetName(item.name).SetValue(item.value).
			SetType("number").SetGroup("esports").SetStatus(1).SaveX(ctx)
	}
	return db
}

// seedSuspensionOrder 在指定 SKU 下报一张订单，可选带预约开始时间。
func seedSuspensionOrder(t *testing.T, db *ent.Client, ownerID, customerID, skuID int, quantity int64,
	key string, startAt *time.Time, now time.Time) *ent.EsportsOrder {
	t.Helper()
	order, err := placeOrder(t.Context(), db, ownerID, orderInput{
		CustomerID: customerID, SkuID: skuID, Quantity: quantity, RequestedStartAt: startAt,
	}, key, now)
	if err != nil {
		t.Fatal(err)
	}
	return order
}

// paySuspensionOrder 用客户余额把订单核销为已付款待派单（名额占用转 sold），供取消退款处置使用。
func paySuspensionOrder(t *testing.T, db *ent.Client, ownerID, customerID int,
	order *ent.EsportsOrder, key string, now time.Time) *ent.EsportsOrder {
	t.Helper()
	ctx := t.Context()
	if _, err := walletRecharge(ctx, db, ownerID, walletCustomerBalance, customerID,
		walletInput{PrincipalCents: 100000, Note: "停服处置测试充值"}, "suspend-recharge-"+key); err != nil {
		t.Fatal(err)
	}
	paid, err := payOrderWithBalance(ctx, db, ownerID, order.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	return paid
}

// seedSuspensionSku 建一个在售 SKU 与客户，配额取 -1（不限量）避免测试被名额卡住。
func seedSuspensionSku(t *testing.T, db *ent.Client, ownerID int, code string) (*ent.EsportsCustomer, *ent.EsportsSku) {
	t.Helper()
	customer := saveCustomerOrFail(t, db, ownerID)
	sku, err := saveSku(t.Context(), db, skuInput{
		Code: code, Title: "停服测试商品", ServiceType: "play", UnitPriceCents: 1000,
		ServiceUnits: 1, ServiceUnitName: "小时", RequiredSeats: 1, QuotaTotal: -1, Status: "on_sale",
	})
	if err != nil {
		t.Fatal(err)
	}
	return customer, sku
}

// suggestedActionOf 读取清单中某张订单的处置记录。
func suspensionItemsByOrder(t *testing.T, db *ent.Client, suspensionID int) map[int]*ent.EsportsSkuSuspensionItem {
	t.Helper()
	items := db.EsportsSkuSuspensionItem.Query().Where(
		esportsskususpensionitem.SuspensionIDEQ(suspensionID)).AllX(t.Context())
	byOrder := make(map[int]*ent.EsportsSkuSuspensionItem, len(items))
	for _, item := range items {
		byOrder[item.OrderID] = item
	}
	return byOrder
}

// 停服申请 → 异人审批（立即生效）→ 逐单处置：审批后 SKU 下架并生成清单，逐单可部分完成。
func TestSkuSuspensionApprovalGeneratesItemsAndDisposesByOrder(t *testing.T) {
	db := openSuspensionDB(t, "esports_sku_suspension_apply")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	customer, sku := seedSuspensionSku(t, db, 11, "SUSPEND-APPLY")
	start := now.Add(3 * time.Hour)

	unpaid := seedSuspensionOrder(t, db, 11, customer.ID, sku.ID, 1, "suspend-key-1", nil, now)
	paid := seedSuspensionOrder(t, db, 11, customer.ID, sku.ID, 2, "suspend-key-2", nil, now)
	paid = paySuspensionOrder(t, db, 11, customer.ID, paid, "1", now)
	booked := seedSuspensionOrder(t, db, 11, customer.ID, sku.ID, 3, "suspend-key-3", &start, now)
	serving := seedSuspensionOrder(t, db, 11, customer.ID, sku.ID, 4, "suspend-key-4", nil, now)
	serving = paySuspensionOrder(t, db, 11, customer.ID, serving, "2", now)
	db.EsportsOrder.UpdateOneID(serving.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusInService).AddVersion(1).SaveX(ctx)
	db.EsportsServiceSegment.Create().SetOrderID(serving.ID).SetPlayerID(1).SetSeatIndex(1).
		SetStatus(esportsservicesegment.StatusInService).SetStartedAt(now).SaveX(ctx)

	suspension, err := requestSkuSuspension(ctx, db, sku.ID, 11, skuSuspensionRequestInput{
		Reason: "供应商停止供货，需要停服并在途订单逐单收敛", EffectiveMode: "immediate",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	// 申请时点的统计快照：4 张未终结订单，建议处置 2 取消退款 / 1 改期 / 1 保留，预估退款 6000 分。
	if suspension.Status != esportsskususpension.StatusPending || suspension.AffectedOrderCount != 4 {
		t.Fatalf("申请未落下待审批状态与受影响订单统计：%+v", suspension)
	}
	snapshot := suspension.AffectedSnapshot
	suggested, _ := snapshot["suggested_action"].(map[string]interface{})
	if snapshotCount(t, suggested, suspensionDisposalCancelRefund) != 2 ||
		snapshotCount(t, suggested, suspensionDisposalReschedule) != 1 ||
		snapshotCount(t, suggested, suspensionDisposalKeep) != 1 {
		t.Fatalf("申请快照的建议处置分布不正确：%+v", snapshot)
	}
	if estimated := snapshotCount(t, snapshot, "estimated_refund_cents"); estimated != 6000 {
		t.Fatalf("申请快照的预估退款额不正确：%+v", snapshot)
	}

	// 申请人不得自审：状态、SKU 都不变。
	if _, err = reviewSkuSuspension(ctx, db, suspension.ID, 11,
		skuSuspensionReviewInput{Approve: true, Note: "自己批自己"}, now); !errors.Is(err, errForbidden) {
		t.Fatalf("申请人自审未被拒：%v", err)
	}
	if stored := db.EsportsSkuSuspension.GetX(ctx, suspension.ID); stored.Status != esportsskususpension.StatusPending {
		t.Fatalf("被拒的自审改动了申请：%+v", stored)
	}
	if db.EsportsSku.GetX(ctx, sku.ID).Status != esportssku.StatusOnSale {
		t.Fatal("被拒的自审改动了 SKU 状态")
	}
	// 同一 SKU 同一时刻只允许一张进行中申请。
	if _, err = requestSkuSuspension(ctx, db, sku.ID, 12, skuSuspensionRequestInput{
		Reason: "重复申请", EffectiveMode: "immediate",
	}, now); !errors.Is(err, errConflict) {
		t.Fatalf("同一 SKU 重复申请未被拒：%v", err)
	}

	approved, err := reviewSkuSuspension(ctx, db, suspension.ID, 12,
		skuSuspensionReviewInput{Approve: true, Note: "同意停服，逐单收敛在途订单"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != esportsskususpension.StatusApplied || approved.ReviewedByAdminID == nil ||
		*approved.ReviewedByAdminID != 12 || approved.AppliedAt == nil {
		t.Fatalf("立即生效的审批未推进到 applied：%+v", approved)
	}
	if stored := db.EsportsSku.GetX(ctx, sku.ID); stored.Status != esportssku.StatusOffSale {
		t.Fatalf("审批通过后 SKU 未下架：%+v", stored)
	}
	items := suspensionItemsByOrder(t, db, suspension.ID)
	if len(items) != 4 {
		t.Fatalf("未生成受影响订单逐单清单：%+v", items)
	}
	for orderID, want := range map[int]string{
		unpaid.ID:  suspensionDisposalCancelRefund,
		paid.ID:    suspensionDisposalCancelRefund,
		booked.ID:  suspensionDisposalReschedule,
		serving.ID: suspensionDisposalKeep,
	} {
		item := items[orderID]
		if item == nil || item.SuggestedAction.String() != want || item.Status != esportsskususpensionitem.StatusPending {
			t.Fatalf("订单 %d 的建议处置不正确：%+v", orderID, item)
		}
	}

	// 逐单处置 1：未付款订单取消并退款 —— 复用既有取消闭环，订单取消且名额释放。
	disposedAt := now.Add(2 * time.Hour)
	if _, err = disposeSkuSuspensionItem(ctx, db, suspension.ID, unpaid.ID, 11, false,
		skuSuspensionDisposalInput{Action: suspensionDisposalCancelRefund, Note: "客户同意取消"}, disposedAt); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsOrder.GetX(ctx, unpaid.ID); got.FulfillmentStatus != esportsorder.FulfillmentStatusCancelled {
		t.Fatalf("逐单取消未生效：%+v", got)
	}
	hold := db.EsportsQuotaHold.Query().Where(esportsquotahold.OrderIDEQ(unpaid.ID)).OnlyX(ctx)
	if hold.Status != esportsquotahold.StatusReleased {
		t.Fatalf("未付款订单取消后名额未释放：%+v", hold)
	}
	// 可部分完成：还有 3 单待处置。
	report, err := loadSkuSuspensionReport(ctx, db, suspension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Progress.Total != 4 || report.Progress.Done != 1 || report.Progress.Pending != 3 || report.Progress.Failed != 0 {
		t.Fatalf("逐单处置进度不正确：%+v", report.Progress)
	}

	// 逐单处置 2：已付款订单取消并退款 —— 复用既有所付款取消政策与售后退款单闭环。
	if _, err = disposeSkuSuspensionItem(ctx, db, suspension.ID, paid.ID, 11, false,
		skuSuspensionDisposalInput{Action: suspensionDisposalCancelRefund, Note: "按公示政策取消退款"}, disposedAt); err != nil {
		t.Fatal(err)
	}
	refunded := db.EsportsOrder.GetX(ctx, paid.ID)
	if refunded.FulfillmentStatus != esportsorder.FulfillmentStatusCancelled ||
		refunded.RefundStatus != esportsorder.RefundStatusPendingApproval {
		t.Fatalf("已付款订单未按取消政策落退款状态：%+v", refunded)
	}
	if count := db.EsportsAfterSale.Query().CountX(ctx); count != 1 {
		t.Fatalf("已付款订单取消失效：未生成承接审批的售后退款单（%d）", count)
	}

	// 逐单处置 3：预约单改期 —— 复用既有档期提案，档期推进到新时间。
	newStart := now.Add(6 * time.Hour)
	if _, err = disposeSkuSuspensionItem(ctx, db, suspension.ID, booked.ID, 11, false,
		skuSuspensionDisposalInput{
			Action: suspensionDisposalReschedule, Note: "客户改约下周", StartAt: &newStart,
		}, disposedAt); err != nil {
		t.Fatal(err)
	}
	rescheduled := db.EsportsOrder.GetX(ctx, booked.ID)
	if rescheduled.RequestedStartAt == nil || !rescheduled.RequestedStartAt.Equal(newStart) ||
		rescheduled.AppointmentStatus != esportsorder.AppointmentStatusRequest {
		t.Fatalf("逐单改期未落档期提案：%+v", rescheduled)
	}

	// 逐单处置 4：服务中订单保留继续服务 —— 订单不被改动。
	if _, err = disposeSkuSuspensionItem(ctx, db, suspension.ID, serving.ID, 11, false,
		skuSuspensionDisposalInput{Action: suspensionDisposalKeep, Note: "服务中，保留到结束"}, disposedAt); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsOrder.GetX(ctx, serving.ID); got.FulfillmentStatus != esportsorder.FulfillmentStatusInService {
		t.Fatalf("保留继续服务不应改动订单：%+v", got)
	}

	report, err = loadSkuSuspensionReport(ctx, db, suspension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Progress.Done != 4 || report.Progress.Pending != 0 {
		t.Fatalf("逐单处置未全部完成：%+v", report.Progress)
	}
	// 已完成的订单不可重复处置。
	if _, err = disposeSkuSuspensionItem(ctx, db, suspension.ID, serving.ID, 11, false,
		skuSuspensionDisposalInput{Action: suspensionDisposalKeep, Note: "重复处置"}, disposedAt); !errors.Is(err, errConflict) {
		t.Fatalf("已完成的逐单处置被重复执行：%v", err)
	}

	// 留痕：每张受影响订单都有停服通知事件，处置另有订单事件，外盒覆盖申请 / 审批 / 生效 / 逐单。
	notices := 0
	for _, event := range db.EsportsOrderEvent.Query().AllX(ctx) {
		if event.EventType == "order.sku_suspension_notice" {
			notices++
		}
	}
	if notices != 4 {
		t.Fatalf("受影响订单缺少停服通知留痕：%d", notices)
	}
	outboxTypes := map[string]int{}
	for _, event := range db.EsportsOutbox.Query().AllX(ctx) {
		outboxTypes[event.EventType]++
	}
	for _, want := range []string{"product.suspension_requested", "product.suspension_approved", "product.suspension_applied"} {
		if outboxTypes[want] != 1 {
			t.Fatalf("缺少外盒事件 %s：%+v", want, outboxTypes)
		}
	}
	if outboxTypes["order.sku_suspension_disposed"] != 4 {
		t.Fatalf("逐单处置外盒留痕不完整：%+v", outboxTypes)
	}
}

// 驳回保持原状并释放进行中占位；申请人可撤回待审批申请，他人不可撤回。
func TestSkuSuspensionRejectKeepsSkuAndCancelReleasesSlot(t *testing.T) {
	db := openSuspensionDB(t, "esports_sku_suspension_reject")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	customer, sku := seedSuspensionSku(t, db, 11, "SUSPEND-REJECT")
	seedSuspensionOrder(t, db, 11, customer.ID, sku.ID, 1, "suspend-reject-key-1", nil, now)

	suspension, err := requestSkuSuspension(ctx, db, sku.ID, 11, skuSuspensionRequestInput{
		Reason: "渠道调整，拟停服", EffectiveMode: "immediate",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	// 非申请人不能撤回他人的申请。
	if _, err = cancelSkuSuspension(ctx, db, suspension.ID, 12, false, now); !errors.Is(err, errForbidden) {
		t.Fatalf("非申请人撤回他人申请未被拒：%v", err)
	}
	rejected, err := reviewSkuSuspension(ctx, db, suspension.ID, 12,
		skuSuspensionReviewInput{Approve: false, Note: "在途订单过多，先不改"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Status != esportsskususpension.StatusRejected || rejected.ReviewNote != "在途订单过多，先不改" ||
		rejected.ReviewedAt == nil {
		t.Fatalf("驳回结论未留痕：%+v", rejected)
	}
	if rejected.PendingKey != nil {
		t.Fatalf("驳回后进行中占位未释放：%+v", rejected)
	}
	if stored := db.EsportsSku.GetX(ctx, sku.ID); stored.Status != esportssku.StatusOnSale {
		t.Fatalf("驳回改动了 SKU 状态：%+v", stored)
	}
	if count := db.EsportsSkuSuspensionItem.Query().CountX(ctx); count != 0 {
		t.Fatalf("驳回生成了逐单处置清单：%d", count)
	}
	// 驳回后占位释放：可以再发起一张。
	second, err := requestSkuSuspension(ctx, db, sku.ID, 12, skuSuspensionRequestInput{
		Reason: "重新评估后仍要停服", EffectiveMode: "immediate",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := cancelSkuSuspension(ctx, db, second.ID, 12, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != esportsskususpension.StatusCancelled || cancelled.CancelledAt == nil ||
		cancelled.PendingKey != nil {
		t.Fatalf("撤回未落终结状态：%+v", cancelled)
	}
	if db.EsportsSku.GetX(ctx, sku.ID).Status != esportssku.StatusOnSale {
		t.Fatal("撤回改动了 SKU 状态")
	}
	// 已终结的申请不可再撤回。
	if _, err = cancelSkuSuspension(ctx, db, second.ID, 12, true, now); !errors.Is(err, errConflict) {
		t.Fatalf("已撤回的申请被重复撤回：%v", err)
	}
}

// 定时生效：审批通过后 SKU 仍在上架，到点由 applyDueSkuSuspensions 下架并生成清单；
// 服务中订单的取消退款处置被既有政策拦截，失败留痕后仍可改选保留继续服务。
func TestSkuSuspensionScheduledApplyAndServiceStartedBlocked(t *testing.T) {
	db := openSuspensionDB(t, "esports_sku_suspension_scheduled")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	customer, sku := seedSuspensionSku(t, db, 11, "SUSPEND-SCHEDULED")

	unpaid := seedSuspensionOrder(t, db, 11, customer.ID, sku.ID, 1, "suspend-sch-key-1", nil, now)
	serving := seedSuspensionOrder(t, db, 11, customer.ID, sku.ID, 2, "suspend-sch-key-2", nil, now)
	serving = paySuspensionOrder(t, db, 11, customer.ID, serving, "1", now)
	db.EsportsOrder.UpdateOneID(serving.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusInService).AddVersion(1).SaveX(ctx)
	db.EsportsServiceSegment.Create().SetOrderID(serving.ID).SetPlayerID(1).SetSeatIndex(1).
		SetStatus(esportsservicesegment.StatusInService).SetStartedAt(now).SaveX(ctx)

	// 定时生效校验：缺生效时刻、生效时刻不晚于当前时间都被拒。
	if _, err := requestSkuSuspension(ctx, db, sku.ID, 11, skuSuspensionRequestInput{
		Reason: "缺少生效时刻", EffectiveMode: "scheduled",
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("定时生效缺少生效时刻未被拒：%v", err)
	}
	effectiveAt := now.Add(2 * time.Hour)
	past := now.Add(-time.Hour)
	if _, err := requestSkuSuspension(ctx, db, sku.ID, 11, skuSuspensionRequestInput{
		Reason: "生效时刻已过", EffectiveMode: "scheduled", EffectiveAt: &past,
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("定时生效时刻早于当前时间未被拒：%v", err)
	}

	suspension, err := requestSkuSuspension(ctx, db, sku.ID, 11, skuSuspensionRequestInput{
		Reason: "到点停服", EffectiveMode: "scheduled", EffectiveAt: &effectiveAt,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := reviewSkuSuspension(ctx, db, suspension.ID, 12,
		skuSuspensionReviewInput{Approve: true, Note: "同意，到点生效"}, now)
	if err != nil {
		t.Fatal(err)
	}
	// 已批准但未到点：SKU 仍上架，清单尚未生成，占位仍保留（仍属进行中）。
	if approved.Status != esportsskususpension.StatusApproved || approved.PendingKey == nil {
		t.Fatalf("定时审批状态不正确：%+v", approved)
	}
	if db.EsportsSku.GetX(ctx, sku.ID).Status != esportssku.StatusOnSale ||
		db.EsportsSkuSuspensionItem.Query().CountX(ctx) != 0 {
		t.Fatal("定时生效前 SKU 已下架或已生成清单")
	}
	if _, err = requestSkuSuspension(ctx, db, sku.ID, 12, skuSuspensionRequestInput{
		Reason: "重复申请", EffectiveMode: "immediate",
	}, now); !errors.Is(err, errConflict) {
		t.Fatalf("已批准未生效期间重复申请未被拒：%v", err)
	}
	// 未到点不下架。
	applied, err := applyDueSkuSuspensions(ctx, db, now.Add(time.Hour), 100)
	if err != nil || applied != 0 {
		t.Fatalf("未到点的定时停服被提前生效：%d %v", applied, err)
	}
	applied, err = applyDueSkuSuspensions(ctx, db, effectiveAt.Add(time.Minute), 100)
	if err != nil || applied != 1 {
		t.Fatalf("到点的定时停服未生效：%d %v", applied, err)
	}
	appliedAgain, err := applyDueSkuSuspensions(ctx, db, effectiveAt.Add(2*time.Hour), 100)
	if err != nil || appliedAgain != 0 {
		t.Fatalf("已生效的定时停服被重复生效：%d %v", appliedAgain, err)
	}
	stored := db.EsportsSkuSuspension.GetX(ctx, suspension.ID)
	if stored.Status != esportsskususpension.StatusApplied || stored.AppliedAt == nil || stored.PendingKey != nil {
		t.Fatalf("定时生效未推进到 applied：%+v", stored)
	}
	if db.EsportsSku.GetX(ctx, sku.ID).Status != esportssku.StatusOffSale {
		t.Fatal("定时生效未把 SKU 置为下架")
	}
	items := suspensionItemsByOrder(t, db, suspension.ID)
	if len(items) != 2 || items[serving.ID].SuggestedAction != esportsskususpensionitem.SuggestedActionKeep {
		t.Fatalf("定时生效未生成正确的逐单清单：%+v", items)
	}

	disposedAt := effectiveAt.Add(time.Hour)
	// 服务中订单取消并退款被既有政策拦截：落 failed 记录并留痕，订单保持原状。
	blocked, err := disposeSkuSuspensionItem(ctx, db, suspension.ID, serving.ID, 11, false,
		skuSuspensionDisposalInput{Action: suspensionDisposalCancelRefund, Note: "试图取消服务中的订单"}, disposedAt)
	if !errors.Is(err, errCancelServiceStarted) {
		t.Fatalf("服务中订单取消未被拦截：%v", err)
	}
	if blocked == nil || blocked.Status != esportsskususpensionitem.StatusFailed ||
		blocked.HandledByAdminID == nil || *blocked.HandledByAdminID != 11 || blocked.Note == "" {
		t.Fatalf("处置失败未留痕：%+v", blocked)
	}
	if got := db.EsportsOrder.GetX(ctx, serving.ID); got.FulfillmentStatus != esportsorder.FulfillmentStatusInService {
		t.Fatalf("被拦截的处置改动了订单：%+v", got)
	}
	report, err := loadSkuSuspensionReport(ctx, db, suspension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Progress.Failed != 1 || report.Progress.Pending != 1 {
		t.Fatalf("失败记录未计入进度：%+v", report.Progress)
	}
	failedEvents := 0
	for _, event := range db.EsportsOrderEvent.Query().AllX(ctx) {
		if event.EventType == "order.sku_suspension_disposal_failed" {
			failedEvents++
		}
	}
	if failedEvents != 1 {
		t.Fatalf("处置失败缺少订单事件留痕：%d", failedEvents)
	}
	// 看到阻塞原因后改选保留继续服务：失败记录可重试，改期之外的订单照常完成。
	if _, err = disposeSkuSuspensionItem(ctx, db, suspension.ID, serving.ID, 11, false,
		skuSuspensionDisposalInput{Action: suspensionDisposalKeep, Note: "服务中，改为保留"}, disposedAt); err != nil {
		t.Fatal(err)
	}
	if _, err = disposeSkuSuspensionItem(ctx, db, suspension.ID, unpaid.ID, 11, false,
		skuSuspensionDisposalInput{Action: suspensionDisposalCancelRefund, Note: "取消未付款订单"}, disposedAt); err != nil {
		t.Fatal(err)
	}
	report, err = loadSkuSuspensionReport(ctx, db, suspension.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Progress.Done != 2 || report.Progress.Failed != 0 || report.Progress.Pending != 0 {
		t.Fatalf("重试后逐单处置未全部完成：%+v", report.Progress)
	}
	if got := db.EsportsOrder.GetX(ctx, unpaid.ID); got.FulfillmentStatus != esportsorder.FulfillmentStatusCancelled {
		t.Fatalf("未付款订单逐单取消未生效：%+v", got)
	}
}
