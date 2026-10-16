package esports

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsgiftdispute"
	"go-admin/internal/ent/esportsgiftorder"
)

// 礼物争议（§9.5）：客户申请 → 客服审核 → 执行退款（支持部分退款）。
// 同一礼物订单同一时刻只允许一张进行中争议单；审核确认额不得超过申请额与礼物可退余额；
// 审核人与申请人（客服代建时）不得为同一人，执行人不得为审核人；执行复用礼物退款事务实现。

type giftDisputeInput struct {
	GiftOrderID        int    `json:"gift_order_id"`
	Kind               string `json:"kind"`
	Demand             string `json:"demand"`
	EvidenceRef        string `json:"evidence_ref"`
	RequestedRefundCents int64 `json:"requested_refund_cents"`
}

type giftDisputeReviewInput struct {
	Approve            bool   `json:"approve"`
	ApprovedRefundCents int64 `json:"approved_refund_cents"`
	Resolution         string `json:"resolution"`
}

var giftDisputeKindValues = map[string]bool{
	"not_received": true, "wrong_gift": true, "quality": true, "unauthorized": true, "other": true,
}

// openGiftDisputeStatuses 进行中（占用「同一订单一张争议单」配额）的状态。
func openGiftDisputeStatuses() []esportsgiftdispute.Status {
	return []esportsgiftdispute.Status{
		esportsgiftdispute.StatusPendingReview, esportsgiftdispute.StatusApproved,
	}
}

// createGiftDispute 客户自助或客服代客户建礼物争议单（channel 分别为 customer / admin）。
// 客户自助时 customerID 必须为礼物订单客户本人；客服代建时记录申请人，供后续禁止自审。
func createGiftDispute(ctx context.Context, db *ent.Client, in giftDisputeInput,
	channel string, customerID, adminID int, now time.Time) (*ent.EsportsGiftDispute, error) {
	in.Demand = strings.TrimSpace(in.Demand)
	in.EvidenceRef = strings.TrimSpace(in.EvidenceRef)
	if in.GiftOrderID < 1 || !giftDisputeKindValues[in.Kind] || in.Demand == "" ||
		len([]rune(in.Demand)) > 500 || len([]rune(in.EvidenceRef)) > 255 || in.RequestedRefundCents <= 0 {
		return nil, errInvalidInput
	}
	if channel != "customer" && channel != "admin" {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsGiftOrder.Get(ctx, in.GiftOrderID)
	if err != nil {
		return nil, err
	}
	if channel == "customer" {
		if customerID < 1 || order.CustomerID != customerID {
			return nil, errForbidden
		}
	} else if adminID < 1 {
		return nil, errInvalidInput
	}
	customer, err := tx.EsportsCustomer.Get(ctx, order.CustomerID)
	if err != nil {
		return nil, err
	}
	available := order.AmountCents - order.RefundCents
	if order.Status != esportsgiftorder.StatusPaid || available <= 0 {
		return nil, errConflict
	}
	if in.RequestedRefundCents > available {
		return nil, errInvalidInput
	}
	exists, err := tx.EsportsGiftDispute.Query().Where(
		esportsgiftdispute.GiftOrderIDEQ(order.ID),
		esportsgiftdispute.StatusIn(openGiftDisputeStatuses()...),
	).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, errConflict
	}
	create := tx.EsportsGiftDispute.Create().
		SetDisputeNo("GD" + uuid.NewString()).
		SetGiftOrderID(order.ID).SetGiftNo(order.GiftNo).SetCustomerID(order.CustomerID).
		SetKind(esportsgiftdispute.Kind(in.Kind)).
		SetChannel(esportsgiftdispute.Channel(channel)).
		SetDemand(in.Demand).SetEvidenceRef(in.EvidenceRef).
		SetRequestedRefundCents(in.RequestedRefundCents).
		SetStatus(esportsgiftdispute.StatusPendingReview)
	if customer.OwnerAdminID != nil {
		create.SetOwnerAdminID(*customer.OwnerAdminID)
	}
	if channel == "admin" {
		create.SetCreatedByAdminID(adminID)
	}
	item, err := create.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, errConflict
		}
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsGiftDispute.Get(ctx, item.ID)
}

// getGiftDispute 按 ID 读取争议单。
func getGiftDispute(ctx context.Context, db *ent.Client, id int) (*ent.EsportsGiftDispute, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	return db.EsportsGiftDispute.Get(ctx, id)
}

// listGiftDisputes 管理端列表：非超级管理员只看归属客服为客户建档归属的争议单。
func listGiftDisputes(ctx context.Context, db *ent.Client, adminID int, isSuper bool,
	status string, limit int) ([]*ent.EsportsGiftDispute, error) {
	if adminID < 1 {
		return nil, errForbidden
	}
	if limit < 1 || limit > 500 {
		limit = 200
	}
	q := db.EsportsGiftDispute.Query()
	if !isSuper {
		q.Where(esportsgiftdispute.OwnerAdminIDEQ(adminID))
	}
	if status = strings.TrimSpace(status); status != "" {
		switch status {
		case "pending_review", "approved", "rejected", "completed", "withdrawn":
			q.Where(esportsgiftdispute.StatusEQ(esportsgiftdispute.Status(status)))
		default:
			return nil, errInvalidInput
		}
	}
	return q.Order(ent.Desc(esportsgiftdispute.FieldID)).Limit(limit).All(ctx)
}

// listCustomerGiftOrders 客户自助：本人礼物单据（按时间倒序），用于发起争议。
func listCustomerGiftOrders(ctx context.Context, db *ent.Client, customerID, limit int) ([]*ent.EsportsGiftOrder, error) {
	if customerID < 1 {
		return nil, errForbidden
	}
	if limit < 1 || limit > 200 {
		limit = 100
	}
	return db.EsportsGiftOrder.Query().
		Where(esportsgiftorder.CustomerIDEQ(customerID)).
		Order(ent.Desc(esportsgiftorder.FieldID)).Limit(limit).All(ctx)
}

// listCustomerGiftDisputes 客户自助列表：只看本人争议单。
func listCustomerGiftDisputes(ctx context.Context, db *ent.Client, customerID, limit int) ([]*ent.EsportsGiftDispute, error) {
	if customerID < 1 {
		return nil, errForbidden
	}
	if limit < 1 || limit > 200 {
		limit = 100
	}
	return db.EsportsGiftDispute.Query().
		Where(esportsgiftdispute.CustomerIDEQ(customerID)).
		Order(ent.Desc(esportsgiftdispute.FieldID)).Limit(limit).All(ctx)
}

// reviewGiftDispute 审核争议（§9.5）：pending_review → approved（须给确认金额）/ rejected。
// 归属客服之外的管理员、以及建单申请人不得审核；确认金额不得超过申请额与可退余额。
func reviewGiftDispute(ctx context.Context, db *ent.Client, id, adminID int, isSuper bool,
	in giftDisputeReviewInput, now time.Time) (*ent.EsportsGiftDispute, error) {
	in.Resolution = strings.TrimSpace(in.Resolution)
	if id < 1 || adminID < 1 || len([]rune(in.Resolution)) > 255 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	item, err := tx.EsportsGiftDispute.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.Status != esportsgiftdispute.StatusPendingReview {
		return nil, errConflict
	}
	if !isSuper && (item.OwnerAdminID == nil || *item.OwnerAdminID != adminID) {
		return nil, errForbidden
	}
	if item.CreatedByAdminID != nil && *item.CreatedByAdminID == adminID {
		return nil, errForbidden
	}
	update := tx.EsportsGiftDispute.Update().Where(
		esportsgiftdispute.IDEQ(id),
		esportsgiftdispute.StatusEQ(esportsgiftdispute.StatusPendingReview),
	).SetResolution(in.Resolution).SetReviewedByAdminID(adminID).
		SetReviewedAt(now.UTC()).AddVersion(1)
	if !in.Approve {
		if in.Resolution == "" {
			return nil, errInvalidInput
		}
		update.SetStatus(esportsgiftdispute.StatusRejected)
	} else {
		order, err := tx.EsportsGiftOrder.Get(ctx, item.GiftOrderID)
		if err != nil {
			return nil, err
		}
		available := order.AmountCents - order.RefundCents
		if in.ApprovedRefundCents <= 0 || in.ApprovedRefundCents > item.RequestedRefundCents ||
			in.ApprovedRefundCents > available {
			return nil, errInvalidInput
		}
		update.SetStatus(esportsgiftdispute.StatusApproved).
			SetApprovedRefundCents(in.ApprovedRefundCents)
	}
	changed, err := update.Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsGiftDispute.Get(ctx, id)
}

// executeGiftDispute 执行争议退款（§9.5）：approved → completed，按确认金额做部分退款并冲回分成。
// 执行人不得为审核人；礼物的可退余额不足时拒绝（可能已被其他退款占用）。
func executeGiftDispute(ctx context.Context, db *ent.Client, id, adminID int, now time.Time) (*ent.EsportsGiftDispute, error) {
	if id < 1 || adminID < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	item, err := tx.EsportsGiftDispute.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.Status != esportsgiftdispute.StatusApproved || item.ApprovedRefundCents <= 0 {
		return nil, errConflict
	}
	if item.ReviewedByAdminID != nil && *item.ReviewedByAdminID == adminID {
		return nil, errForbidden
	}
	order, err := tx.EsportsGiftOrder.Get(ctx, item.GiftOrderID)
	if err != nil {
		return nil, err
	}
	if order.Status != esportsgiftorder.StatusPaid ||
		order.AmountCents-order.RefundCents < item.ApprovedRefundCents {
		return nil, errConflict
	}
	reason := item.Resolution
	if reason == "" {
		reason = "礼物争议退款"
	}
	if err = refundGiftIn(ctx, tx, order, item.ApprovedRefundCents,
		"gift_dispute:"+item.DisputeNo, reason, adminID, now); err != nil {
		return nil, err
	}
	changed, err := tx.EsportsGiftDispute.Update().Where(
		esportsgiftdispute.IDEQ(id),
		esportsgiftdispute.StatusEQ(esportsgiftdispute.StatusApproved),
	).SetStatus(esportsgiftdispute.StatusCompleted).SetExecutedByAdminID(adminID).
		SetExecutedAt(now.UTC()).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsGiftDispute.Get(ctx, id)
}

// withdrawGiftDispute 撤回争议（§9.5）：进行中的争议单可由客户本人或归属客服撤回。
func withdrawGiftDispute(ctx context.Context, db *ent.Client, id, customerID, adminID int, now time.Time) (*ent.EsportsGiftDispute, error) {
	if id < 1 {
		return nil, errInvalidInput
	}
	item, err := db.EsportsGiftDispute.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if customerID > 0 {
		if item.CustomerID != customerID {
			return nil, errForbidden
		}
	} else if adminID > 0 {
		if item.OwnerAdminID == nil || *item.OwnerAdminID != adminID {
			return nil, errForbidden
		}
	} else {
		return nil, errInvalidInput
	}
	changed, err := db.EsportsGiftDispute.Update().Where(
		esportsgiftdispute.IDEQ(id),
		esportsgiftdispute.StatusIn(openGiftDisputeStatuses()...),
	).SetStatus(esportsgiftdispute.StatusWithdrawn).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsGiftDispute.Get(ctx, id)
}
