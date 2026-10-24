package esports

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportssku"
	"go-admin/internal/ent/esportsskususpension"
	"go-admin/internal/ent/esportsskususpensionitem"
)

// 停服申请 → 异人审批 → 受影响订单逐单处置：
//
//  1. 申请：管理员就某在售 SKU 发起停服申请，落原因、生效方式（立即 / 定时）、申请人，
//     并在申请时点抓一份受影响订单统计快照；同一 SKU 同一时刻只允许一张进行中申请。
//  2. 审批：必须由另一名管理员完成（申请人不得自审）。驳回只留结论、SKU 保持原状；
//     通过且立即生效时把 SKU 置为下架并生成逐单处置清单，定时生效的到点由 applyDueSkuSuspensions 下架。
//  3. 逐单处置：客服 / 管理员对清单中的每张未终结订单选择取消并退款（复用既有取消政策与售后退款单）、
//     改期（复用既有档期提案）或保留继续服务；逐单独立完成，允许部分完成。
//  4. 留痕：申请 / 审批 / 生效 / 每单处置都写 EsportsOutbox；每张受影响订单另有订单事件留痕。
//
// 与既有 商品上下架接口的关系：PUT /admin/products/{id} 直接改 status 的既有行为完全不变（不做审批、
// 不生成处置清单）；停服是「有审批与处置联动的下架」，只在下架需要审批与在途订单收敛时使用。

// 逐单处置方式。
const (
	suspensionDisposalCancelRefund = "cancel_refund"
	suspensionDisposalReschedule   = "reschedule"
	suspensionDisposalKeep         = "keep"
)

// suspensionAffectedOrderCap 单次枚举受影响订单的上限，避免一次下架拉爆清单；
// 达到上限时申请快照会带 truncated=true 提示清单被截断。
const suspensionAffectedOrderCap = 500

type skuSuspensionRequestInput struct {
	Reason string `json:"reason"`
	// EffectiveMode 生效方式：immediate（审批通过即下架）/ scheduled（到 effective_at 下架）。
	EffectiveMode string `json:"effective_mode"`
	// EffectiveAt 定时生效时刻，仅 scheduled 必填且须晚于当前时间。
	EffectiveAt *time.Time `json:"effective_at,omitempty"`
}

type skuSuspensionReviewInput struct {
	Approve bool   `json:"approve"`
	Note    string `json:"note"`
}

type skuSuspensionDisposalInput struct {
	Action string `json:"action"`
	Note   string `json:"note"`
	// StartAt 改期后的预约开始时间，仅 action=reschedule 必填且须晚于当前时间。
	StartAt *time.Time `json:"start_at,omitempty"`
}

// skuSuspensionProgress 逐单处置进度：允许部分完成，待处置数为剩余工作量。
type skuSuspensionProgress struct {
	Total   int `json:"total"`
	Pending int `json:"pending"`
	Done    int `json:"done"`
	Failed  int `json:"failed"`
}

// skuSuspensionReport 停服申请详情：申请本体 + 逐单处置清单 + 进度。
type skuSuspensionReport struct {
	Suspension *ent.EsportsSkuSuspension       `json:"suspension"`
	Items      []*ent.EsportsSkuSuspensionItem `json:"items"`
	Progress   skuSuspensionProgress           `json:"progress"`
}

// suspensionPendingKey 进行中占位键：同一 SKU 唯一，申请进入终结状态时清空。
func suspensionPendingKey(skuID int) string {
	return "product:" + strconv.Itoa(skuID)
}

// suspensionAffectedOrders 枚举该 SKU 下的未终结订单：未付款、已付款待派单、派单中、
// 服务中等都算受影响；已结单与已取消不在处置范围内。
func suspensionAffectedOrders(ctx context.Context, orders *ent.EsportsOrderClient, skuID int) ([]*ent.EsportsOrder, error) {
	return orders.Query().Where(
		esportsorder.SkuIDEQ(skuID),
		esportsorder.FulfillmentStatusNEQ(esportsorder.FulfillmentStatusCompleted),
		esportsorder.FulfillmentStatusNEQ(esportsorder.FulfillmentStatusCancelled),
	).Order(ent.Asc(esportsorder.FieldID)).Limit(suspensionAffectedOrderCap).All(ctx)
}

// suggestSuspensionDisposal 给出建议处置口径：服务中 / 待结单的订单建议保留继续服务；未付款且档期
// 仍在未来的预约单建议改期；其余（未付款在途、已付款未开工）建议取消并退款。
func suggestSuspensionDisposal(order *ent.EsportsOrder, now time.Time) string {
	switch order.FulfillmentStatus {
	case esportsorder.FulfillmentStatusInService,
		esportsorder.FulfillmentStatusPendingClose,
		esportsorder.FulfillmentStatusPendingCloseReview:
		return suspensionDisposalKeep
	}
	if order.PaymentStatus == esportsorder.PaymentStatusUnpaid &&
		order.AppointmentStatus == esportsorder.AppointmentStatusRequest &&
		order.RequestedStartAt != nil && order.RequestedStartAt.After(now.UTC()) {
		return suspensionDisposalReschedule
	}
	return suspensionDisposalCancelRefund
}

// estimateSuspensionRefundCents 预估该批订单按公示取消政策应退金额（分）：只统计政策口径下可取消
// 的已付款订单。取消政策配置非法时返回 nil（不做预估），不阻断停服申请本身。
func estimateSuspensionRefundCents(ctx context.Context, db *ent.Client, orders []*ent.EsportsOrder, now time.Time) *int64 {
	policy, err := cancelPolicyFromConfig(ctx, db)
	if err != nil {
		return nil
	}
	var total int64
	for _, order := range orders {
		quote := quoteCancelOrder(policy, order, now)
		if !quote.Cancelable {
			continue
		}
		total += quote.RefundCents
	}
	return &total
}

// buildSuspensionSnapshot 生成受影响订单统计快照：按履约状态、支付状态、建议处置分组计数，
// 并带上预估退款额与截断标记，供审批人评估影响面。
func buildSuspensionSnapshot(orders []*ent.EsportsOrder, estimatedRefundCents *int64, now time.Time) map[string]interface{} {
	byFulfillment := map[string]int{}
	byPayment := map[string]int{}
	bySuggested := map[string]int{}
	for _, order := range orders {
		byFulfillment[order.FulfillmentStatus.String()]++
		byPayment[order.PaymentStatus.String()]++
		bySuggested[suggestSuspensionDisposal(order, now)]++
	}
	snapshot := map[string]interface{}{
		"captured_at":        now.UTC().Format(time.RFC3339),
		"total":              len(orders),
		"fulfillment_status": byFulfillment,
		"payment_status":     byPayment,
		"suggested_action":   bySuggested,
		"truncated":          len(orders) >= suspensionAffectedOrderCap,
	}
	if estimatedRefundCents != nil {
		snapshot["estimated_refund_cents"] = *estimatedRefundCents
	}
	return snapshot
}

// requestSkuSuspension 发起 SKU 停服申请。只允许对在售 SKU 发起；同一 SKU 同一时刻只允许一张
// 进行中申请 —— 由 pending_key 唯一索引兜底，重复申请直接以冲突返回。
func requestSkuSuspension(ctx context.Context, db *ent.Client, skuID, adminID int,
	in skuSuspensionRequestInput, now time.Time) (*ent.EsportsSkuSuspension, error) {
	in.Reason = strings.TrimSpace(in.Reason)
	if skuID < 1 || adminID < 1 || in.Reason == "" || len([]rune(in.Reason)) > 500 {
		return nil, errInvalidInput
	}
	mode := esportsskususpension.EffectiveMode(in.EffectiveMode)
	switch mode {
	case esportsskususpension.EffectiveModeImmediate:
		in.EffectiveAt = nil
	case esportsskususpension.EffectiveModeScheduled:
		if in.EffectiveAt == nil || !in.EffectiveAt.After(now.UTC()) {
			return nil, errInvalidInput
		}
	default:
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	sku, err := tx.EsportsSku.Get(ctx, skuID)
	if err != nil {
		return nil, err
	}
	if sku.Status != esportssku.StatusOnSale {
		return nil, errConflict
	}
	orders, err := suspensionAffectedOrders(ctx, tx.EsportsOrder, skuID)
	if err != nil {
		return nil, err
	}
	create := tx.EsportsSkuSuspension.Create().SetSkuID(skuID).SetRequestedByAdminID(adminID).
		SetReason(in.Reason).SetEffectiveMode(mode).SetPendingKey(suspensionPendingKey(skuID)).
		SetAffectedOrderCount(len(orders)).
		SetAffectedSnapshot(buildSuspensionSnapshot(orders,
			estimateSuspensionRefundCents(ctx, tx.Client(), orders, now), now))
	if mode == esportsskususpension.EffectiveModeScheduled {
		create.SetEffectiveAt(in.EffectiveAt.UTC())
	}
	suspension, err := create.Save(ctx)
	if ent.IsConstraintError(err) {
		return nil, errConflict
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey(fmt.Sprintf("product.suspension_requested:%d", suspension.ID)).
		SetEventType("product.suspension_requested").
		SetAggregateRef("product:" + strconv.Itoa(skuID)).
		SetPayload(map[string]interface{}{
			"suspension_id": suspension.ID, "product_id": skuID,
			"requested_by_admin_id": adminID, "effective_mode": mode.String(),
			"affected_order_count": len(orders),
		}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsSkuSuspension.Get(ctx, suspension.ID)
}

// reviewSkuSuspension 异人审批。申请人不得自审；驳回只留结论并释放进行中占位，SKU 保持原状；
// 通过且立即生效时在同事务内下架 SKU 并生成逐单处置清单（可看作申请 → 审批 → 处置的一次闭环）。
func reviewSkuSuspension(ctx context.Context, db *ent.Client, id, reviewerID int,
	in skuSuspensionReviewInput, now time.Time) (*ent.EsportsSkuSuspension, error) {
	in.Note = strings.TrimSpace(in.Note)
	if id < 1 || reviewerID < 1 || in.Note == "" || len([]rune(in.Note)) > 500 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	suspension, err := tx.EsportsSkuSuspension.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if suspension.Status != esportsskususpension.StatusPending {
		return nil, errConflict
	}
	if suspension.RequestedByAdminID == reviewerID {
		return nil, errForbidden
	}
	status := esportsskususpension.StatusRejected
	if in.Approve {
		status = esportsskususpension.StatusApproved
	}
	// 立即生效的通过在同一事务内直接推进到 applied；定时生效的停在 approved 等 worker 下架。
	applied := in.Approve && suspension.EffectiveMode == esportsskususpension.EffectiveModeImmediate
	if applied {
		status = esportsskususpension.StatusApplied
	}
	update := tx.EsportsSkuSuspension.Update().Where(
		esportsskususpension.IDEQ(id),
		esportsskususpension.StatusEQ(esportsskususpension.StatusPending),
	).SetStatus(status).SetReviewedByAdminID(reviewerID).SetReviewNote(in.Note).
		SetReviewedAt(now.UTC()).AddVersion(1)
	// 仅「已批准但尚未生效」仍属进行中，保留 pending_key 占位；其余终结状态释放占位。
	if !in.Approve || applied {
		update.ClearPendingKey()
	}
	if applied {
		update.SetAppliedAt(now.UTC())
	}
	changed, err := update.Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if applied {
		if err = applySkuSuspension(ctx, tx, suspension, "admin", strconv.Itoa(reviewerID), now); err != nil {
			return nil, err
		}
	}
	// 审批结论单独留痕；立即生效时下架与清单生成另由 product.suspension_applied 留痕，两类事件职责不重叠。
	eventType := "product.suspension_rejected"
	if in.Approve {
		eventType = "product.suspension_approved"
	}
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey(fmt.Sprintf("product.suspension_reviewed:%d", id)).
		SetEventType(eventType).
		SetAggregateRef("product:" + strconv.Itoa(suspension.SkuID)).
		SetPayload(map[string]interface{}{
			"suspension_id": id, "product_id": suspension.SkuID, "approved": in.Approve,
			"reviewed_by_admin_id": reviewerID, "effective_mode": suspension.EffectiveMode.String(),
		}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsSkuSuspension.Get(ctx, id)
}

// cancelSkuSuspension 撤回尚未审批的停服申请（状态机里的 cancelled）：申请人本人或超级管理员
// 可撤回，SKU 保持原状，进行中占位释放。已审批的申请不可撤回。
func cancelSkuSuspension(ctx context.Context, db *ent.Client, id, adminID int, isSuper bool,
	now time.Time) (*ent.EsportsSkuSuspension, error) {
	if id < 1 || adminID < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	suspension, err := tx.EsportsSkuSuspension.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !isSuper && suspension.RequestedByAdminID != adminID {
		return nil, errForbidden
	}
	changed, err := tx.EsportsSkuSuspension.Update().Where(
		esportsskususpension.IDEQ(id),
		esportsskususpension.StatusEQ(esportsskususpension.StatusPending),
	).SetStatus(esportsskususpension.StatusCancelled).SetCancelledAt(now.UTC()).
		ClearPendingKey().AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey(fmt.Sprintf("product.suspension_cancelled:%d", id)).
		SetEventType("product.suspension_cancelled").
		SetAggregateRef("product:" + strconv.Itoa(suspension.SkuID)).
		SetPayload(map[string]interface{}{
			"suspension_id": id, "product_id": suspension.SkuID, "cancelled_by_admin_id": adminID,
		}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsSkuSuspension.Get(ctx, id)
}

// applySkuSuspension 在事务内执行下架与清单生成：SKU 由在售转下架（已下架则跳过，保持幂等），
// 为每张未终结订单建一条逐单处置记录并写一条通知留痕事件，最后写一条整体外盒事件。
func applySkuSuspension(ctx context.Context, tx *ent.Tx, suspension *ent.EsportsSkuSuspension,
	actorType, actorID string, now time.Time) error {
	sku, err := tx.EsportsSku.Get(ctx, suspension.SkuID)
	if err != nil {
		return err
	}
	if sku.Status == esportssku.StatusOnSale {
		changed, err := tx.EsportsSku.Update().Where(
			esportssku.IDEQ(sku.ID), esportssku.VersionEQ(sku.Version),
			esportssku.StatusEQ(esportssku.StatusOnSale),
		).SetStatus(esportssku.StatusOffSale).AddVersion(1).Save(ctx)
		if err != nil {
			return err
		}
		if changed != 1 {
			return errConflict
		}
	}
	orders, err := suspensionAffectedOrders(ctx, tx.EsportsOrder, sku.ID)
	if err != nil {
		return err
	}
	orderIDs := make([]int, 0, len(orders))
	for _, order := range orders {
		suggested := suggestSuspensionDisposal(order, now)
		if _, err = tx.EsportsSkuSuspensionItem.Create().
			SetSuspensionID(suspension.ID).SetOrderID(order.ID).SetOrderNo(order.OrderNo).
			SetOrderStatus(order.FulfillmentStatus.String()).
			SetPaymentStatus(order.PaymentStatus.String()).
			SetSuggestedAction(esportsskususpensionitem.SuggestedAction(suggested)).
			Save(ctx); err != nil {
			return err
		}
		if err = recordSuspensionOrderEvent(ctx, tx, order.ID, actorType, actorID,
			"order.sku_suspension_notice", map[string]interface{}{
				"suspension_id": suspension.ID, "product_id": sku.ID,
				"order_status":     order.FulfillmentStatus.String(),
				"suggested_action": suggested,
			}); err != nil {
			return err
		}
		orderIDs = append(orderIDs, order.ID)
	}
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey(fmt.Sprintf("product.suspension_applied:%d", suspension.ID)).
		SetEventType("product.suspension_applied").
		SetAggregateRef("product:" + strconv.Itoa(sku.ID)).
		SetPayload(map[string]interface{}{
			"suspension_id": suspension.ID, "product_id": sku.ID,
			"affected_order_count": len(orderIDs), "order_ids": orderIDs,
		}).Save(ctx); err != nil {
		return err
	}
	return nil
}

// recordSuspensionOrderEvent 为受影响订单写一条留痕事件：先按当前版本占用一个新的订单版本号，
// 再落事件，与既有订单事件「一版本一条」的唯一约束一致。
func recordSuspensionOrderEvent(ctx context.Context, tx *ent.Tx, orderID int,
	actorType, actorID, eventType string, details map[string]interface{}) error {
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return err
	}
	version, err := bumpOrder(ctx, tx, orderID, order.Version, "")
	if err != nil {
		return err
	}
	_, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).SetOrderVersion(version).
		SetEventType(eventType).SetActorType(actorType).SetActorID(actorID).
		SetDetails(details).Save(ctx)
	return err
}

// applyDueSkuSuspensions 把已批准且到点的定时停服申请逐个生效（下架 + 生成逐单处置清单）。
// 每个申请一个独立事务，单个冲突（例如同时被撤下架）跳过不影响其余；返回本轮生效张数。
func applyDueSkuSuspensions(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, errInvalidInput
	}
	ids, err := db.EsportsSkuSuspension.Query().Where(
		esportsskususpension.StatusEQ(esportsskususpension.StatusApproved),
		esportsskususpension.EffectiveAtLTE(now.UTC()),
	).Order(ent.Asc(esportsskususpension.FieldEffectiveAt)).Limit(limit).IDs(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, id := range ids {
		suspension, err := db.EsportsSkuSuspension.Get(ctx, id)
		if ent.IsNotFound(err) {
			continue
		}
		if err != nil {
			return count, err
		}
		applied, err := applySkuSuspensionNow(ctx, db, suspension, now)
		if errors.Is(err, errConflict) {
			continue
		}
		if err != nil {
			return count, err
		}
		if applied {
			count++
		}
	}
	return count, nil
}

// applySkuSuspensionNow 在独立事务内把一张已批准的申请推进到 applied：置 applied、下架 SKU、
// 生成清单并释放进行中占位。条件更新失败（已被别处推进）返回 applied=false。
func applySkuSuspensionNow(ctx context.Context, db *ent.Client, suspension *ent.EsportsSkuSuspension,
	now time.Time) (bool, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	changed, err := tx.EsportsSkuSuspension.Update().Where(
		esportsskususpension.IDEQ(suspension.ID),
		esportsskususpension.StatusEQ(esportsskususpension.StatusApproved),
	).SetStatus(esportsskususpension.StatusApplied).SetAppliedAt(now.UTC()).
		ClearPendingKey().AddVersion(1).Save(ctx)
	if err != nil {
		return false, err
	}
	if changed != 1 {
		return false, nil
	}
	if err = applySkuSuspension(ctx, tx, suspension, "system", "suspension", now); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// listSkuSuspensions 列出停服申请；非超级管理员只看自己发起的申请（与改价审批同一可见性口径）。
func listSkuSuspensions(ctx context.Context, db *ent.Client, adminID int, isSuper bool,
	status string) ([]*ent.EsportsSkuSuspension, error) {
	if adminID < 1 {
		return nil, errForbidden
	}
	q := db.EsportsSkuSuspension.Query().Order(ent.Desc(esportsskususpension.FieldID)).Limit(200)
	if !isSuper {
		q.Where(esportsskususpension.RequestedByAdminIDEQ(adminID))
	}
	switch status {
	case "":
	case "pending", "approved", "rejected", "applied", "cancelled":
		q.Where(esportsskususpension.StatusEQ(esportsskususpension.Status(status)))
	default:
		return nil, errInvalidInput
	}
	return q.All(ctx)
}

// loadSkuSuspensionReport 返回申请详情与逐单处置清单及进度（允许部分完成，故给出四个计数）。
func loadSkuSuspensionReport(ctx context.Context, db *ent.Client, id int) (*skuSuspensionReport, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	suspension, err := db.EsportsSkuSuspension.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	items, err := db.EsportsSkuSuspensionItem.Query().Where(
		esportsskususpensionitem.SuspensionIDEQ(id),
	).Order(ent.Asc(esportsskususpensionitem.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	report := &skuSuspensionReport{Suspension: suspension, Items: items}
	report.Progress.Total = len(items)
	for _, item := range items {
		switch item.Status {
		case esportsskususpensionitem.StatusDone:
			report.Progress.Done++
		case esportsskususpensionitem.StatusFailed:
			report.Progress.Failed++
		default:
			report.Progress.Pending++
		}
	}
	return report, nil
}

// suspensionDisposalResult 是一次逐单处置的落库参数（成功与失败共用，失败也留痕）。
type suspensionDisposalResult struct {
	Action        string
	OK            bool
	Note          string
	EventType     string
	AggregateRef  string
	OrderStatus   string
	PaymentStatus string
	Extra         map[string]interface{}
}

// disposeSkuSuspensionItem 对停服清单中的一张订单做逐单处置。取消并退款复用既有已付款取消政策与
// 售后退款单（cancelOrder），改期复用既有档期提案（rescheduleOrder），保留继续服务只留痕。
// 逐单独立完成，允许部分完成；处置失败（如已开工订单不可直接取消）同样落一条 failed 记录并留痕，
// 客服看到阻塞原因后可改选处置方式重试（failed 记录可再次处置，done 记录不可）。
func disposeSkuSuspensionItem(ctx context.Context, db *ent.Client, suspensionID, orderID, adminID int,
	isSuper bool, in skuSuspensionDisposalInput, now time.Time) (*ent.EsportsSkuSuspensionItem, error) {
	in.Note = strings.TrimSpace(in.Note)
	if suspensionID < 1 || orderID < 1 || adminID < 1 || len([]rune(in.Note)) > 255 {
		return nil, errInvalidInput
	}
	switch in.Action {
	case suspensionDisposalCancelRefund, suspensionDisposalReschedule, suspensionDisposalKeep:
	default:
		return nil, errInvalidInput
	}
	if in.Action == suspensionDisposalReschedule && (in.StartAt == nil || !in.StartAt.After(now.UTC())) {
		return nil, errInvalidInput
	}
	suspension, err := db.EsportsSkuSuspension.Get(ctx, suspensionID)
	if err != nil {
		return nil, err
	}
	if suspension.Status != esportsskususpension.StatusApplied {
		return nil, errConflict
	}
	item, err := db.EsportsSkuSuspensionItem.Query().Where(
		esportsskususpensionitem.SuspensionIDEQ(suspensionID),
		esportsskususpensionitem.OrderIDEQ(orderID),
	).Only(ctx)
	if err != nil {
		return nil, err
	}
	if item.Status == esportsskususpensionitem.StatusDone {
		return nil, errConflict
	}
	// 订单行归属沿用订单边界：客服只能处置自己负责的订单，超级管理员不限。
	order, err := loadOwnedOrder(ctx, db, adminID, isSuper, orderID)
	if err != nil {
		return nil, err
	}
	reason := in.Note
	if reason == "" {
		reason = fmt.Sprintf("SKU 停服（申请 #%d）逐单处置", suspensionID)
	}
	reason = truncateRunes(reason, 200)
	if actionErr := performSkuSuspensionDisposal(ctx, db, order, adminID, in.Action, reason, in.StartAt, now); actionErr != nil {
		// 失败也要留痕（failed 记录 + 订单事件 + 外盒），然后把阻塞原因回给调用方。
		failed, err := finalizeSkuSuspensionItem(ctx, db, item.ID, order, adminID, suspensionID,
			suspensionDisposalResult{
				Action: in.Action, OK: false, Note: truncateRunes(actionErr.Error(), 255),
				EventType:     "order.sku_suspension_disposal_failed",
				AggregateRef:  order.OrderNo,
				OrderStatus:   order.FulfillmentStatus.String(),
				PaymentStatus: order.PaymentStatus.String(),
				Extra:         map[string]interface{}{"blocked_reason": actionErr.Error()},
			}, now)
		if err != nil {
			return nil, err
		}
		return failed, actionErr
	}
	refreshed, err := db.EsportsOrder.Get(ctx, order.ID)
	if err != nil {
		return nil, err
	}
	return finalizeSkuSuspensionItem(ctx, db, item.ID, order, adminID, suspensionID,
		suspensionDisposalResult{
			Action: in.Action, OK: true, Note: in.Note,
			EventType:     "order.sku_suspension_disposed",
			AggregateRef:  order.OrderNo,
			OrderStatus:   refreshed.FulfillmentStatus.String(),
			PaymentStatus: refreshed.PaymentStatus.String(),
			Extra: map[string]interface{}{
				"refund_status": refreshed.RefundStatus.String(),
				"suspension_id": suspensionID,
				"order_no":      order.OrderNo,
			},
		}, now)
}

// performSkuSuspensionDisposal 按处置方式调用既有闭环；保留继续服务不改动订单，只返回 nil 留痕。
func performSkuSuspensionDisposal(ctx context.Context, db *ent.Client, order *ent.EsportsOrder,
	adminID int, action, reason string, startAt *time.Time, now time.Time) error {
	switch action {
	case suspensionDisposalCancelRefund:
		// 复用既有取消闭环：未付款取消释放名额；已付款按公示取消政策生成可审批的售后退款单；
		// 已开工订单会被 errCancelServiceStarted 拦截（须改走售后或保留）。
		_, err := cancelOrder(ctx, db, order.ID, adminID, reason, now)
		return err
	case suspensionDisposalReschedule:
		// 复用既有改期提案：仅待付款未支付订单可用，落档期提案与确认时限，不改变价格与名额占用。
		_, err := rescheduleOrder(ctx, db, order.ID, adminID, rescheduleInput{
			ExpectedVersion: order.Version, StartAt: startAt.UTC(), Reason: reason,
		}, now)
		return err
	default:
		return nil
	}
}

// finalizeSkuSuspensionItem 在一次事务内落处置结论（done / failed）、刷新订单状态快照，
// 并写订单事件与外盒事件留痕。失败记录可再次处置，故条件更新接受 pending 与 failed 两种前置状态。
func finalizeSkuSuspensionItem(ctx context.Context, db *ent.Client, itemID int, order *ent.EsportsOrder,
	adminID, suspensionID int, result suspensionDisposalResult, now time.Time) (*ent.EsportsSkuSuspensionItem, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	status := esportsskususpensionitem.StatusDone
	if !result.OK {
		status = esportsskususpensionitem.StatusFailed
	}
	update := tx.EsportsSkuSuspensionItem.Update().Where(
		esportsskususpensionitem.IDEQ(itemID),
		esportsskususpensionitem.StatusIn(esportsskususpensionitem.StatusPending, esportsskususpensionitem.StatusFailed),
	).SetAction(result.Action).SetStatus(status).SetNote(truncateRunes(result.Note, 255)).
		SetHandledByAdminID(adminID).SetHandledAt(now.UTC())
	if result.OrderStatus != "" {
		update.SetOrderStatus(result.OrderStatus)
	}
	if result.PaymentStatus != "" {
		update.SetPaymentStatus(result.PaymentStatus)
	}
	changed, err := update.Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	details := map[string]interface{}{
		"action": result.Action, "succeeded": result.OK, "suspension_id": suspensionID,
	}
	for key, value := range result.Extra {
		if key == "suspension_id" {
			continue
		}
		details[key] = value
	}
	if err = recordSuspensionOrderEvent(ctx, tx, order.ID, "admin", strconv.Itoa(adminID),
		result.EventType, details); err != nil {
		return nil, err
	}
	// 事件键带随机后缀：同一张订单的失败重试与多次处置都会各写一条留痕，不会被外盒去重吃掉。
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey(fmt.Sprintf("product.suspension_item:%d:%s", itemID, uuid.NewString())).
		SetEventType(result.EventType).SetAggregateRef(result.AggregateRef).
		SetPayload(details).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsSkuSuspensionItem.Get(ctx, itemID)
}
