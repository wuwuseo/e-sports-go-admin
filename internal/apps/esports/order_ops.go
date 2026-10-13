package esports

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportspaymentintent"
	"go-admin/internal/ent/esportsquotahold"
	"go-admin/internal/ent/esportssku"
)

type repriceInput struct {
	ExpectedVersion   int    `json:"expected_version"`
	Quantity          int64  `json:"quantity"`
	ManualAdjustCents int64  `json:"manual_adjust_cents"`
	Reason            string `json:"reason"`
}

type rescheduleInput struct {
	ExpectedVersion int       `json:"expected_version"`
	StartAt         time.Time `json:"start_at"`
	Reason          string    `json:"reason"`
}

// releaseReservedQuota 在事务内释放订单占用：名额占用记录转 released，SKU 已占名额退回。
func releaseReservedQuota(ctx context.Context, tx *ent.Tx, orderID int) error {
	hold, err := tx.EsportsQuotaHold.Query().Where(esportsquotahold.OrderIDEQ(orderID)).Only(ctx)
	if err != nil {
		return err
	}
	if hold.Status != esportsquotahold.StatusReserved {
		return errConflict
	}
	changed, err := tx.EsportsQuotaHold.Update().Where(
		esportsquotahold.IDEQ(hold.ID), esportsquotahold.StatusEQ(esportsquotahold.StatusReserved),
	).SetStatus(esportsquotahold.StatusReleased).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errConflict
	}
	changed, err = tx.EsportsSku.Update().Where(
		esportssku.IDEQ(hold.SkuID), esportssku.QuotaReservedGTE(hold.Quantity),
	).AddQuotaReserved(-hold.Quantity).AddVersion(1).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errConflict
	}
	return nil
}

// repriceOrder 改价 / 改份数。仅待付款未支付订单可用；先使旧支付意图失效，再按新报价
// 抬升报价版本，旧版本的客户确认随之作废。历史价格不入库，所有变更写订单事件。
func repriceOrder(ctx context.Context, db *ent.Client, orderID int, in repriceInput) (*ent.EsportsOrder, error) {
	in.Reason = strings.TrimSpace(in.Reason)
	if in.ExpectedVersion < 1 || in.Quantity < 1 || in.Quantity > 1000 ||
		in.Reason == "" || len([]rune(in.Reason)) > 255 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingPayment ||
		order.PaymentStatus != esportsorder.PaymentStatusUnpaid || order.Version != in.ExpectedVersion {
		return nil, errConflict
	}
	sku, err := tx.EsportsSku.Get(ctx, order.SkuID)
	if err != nil {
		return nil, err
	}
	payable := sku.UnitPriceCents*in.Quantity - order.DiscountCents + in.ManualAdjustCents
	if payable <= 0 || sku.ServiceUnits > math.MaxInt64/in.Quantity {
		return nil, errInvalidInput
	}
	delta := in.Quantity - order.Quantity
	if delta > 0 {
		// 条件更新抢最后一份名额，避免只靠先前的 SELECT 判断。
		updated, err := tx.EsportsSku.Update().Where(
			esportssku.IDEQ(sku.ID), esportssku.VersionEQ(sku.Version),
			esportssku.StatusEQ(esportssku.StatusOnSale),
			func(s *sql.Selector) {
				s.Where(sql.Or(sql.EQ(esportssku.FieldQuotaTotal, -1),
					sql.ExprP("quota_total - quota_reserved - quota_sold >= ?", delta)))
			},
		).AddQuotaReserved(delta).AddVersion(1).Save(ctx)
		if err != nil {
			return nil, err
		}
		if updated != 1 {
			return nil, errConflict
		}
	} else if delta < 0 {
		updated, err := tx.EsportsSku.Update().Where(
			esportssku.IDEQ(sku.ID), esportssku.QuotaReservedGTE(-delta),
		).AddQuotaReserved(delta).AddVersion(1).Save(ctx)
		if err != nil {
			return nil, err
		}
		if updated != 1 {
			return nil, errConflict
		}
	}
	if delta != 0 {
		changed, err := tx.EsportsQuotaHold.Update().
			Where(esportsquotahold.OrderIDEQ(orderID), esportsquotahold.StatusEQ(esportsquotahold.StatusReserved)).
			SetQuantity(in.Quantity).Save(ctx)
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, errConflict
		}
	}
	if _, err = tx.EsportsPaymentIntent.Update().Where(
		esportspaymentintent.OrderIDEQ(orderID),
		esportspaymentintent.StatusEQ(esportspaymentintent.StatusPending),
	).SetStatus(esportspaymentintent.StatusInvalidated).Save(ctx); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOrder.UpdateOne(order).
		SetQuantity(in.Quantity).SetManualAdjustCents(in.ManualAdjustCents).
		SetPayableCents(payable).SetPromisedServiceUnits(sku.ServiceUnits * in.Quantity).
		AddQuoteVersion(1).AddVersion(1).Save(ctx); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(order.Version + 1).SetEventType("order.repriced").
		SetActorType("admin").SetActorID(strconv.Itoa(order.ID)).
		SetDetails(map[string]interface{}{
			"reason": in.Reason, "quantity": in.Quantity,
			"manual_adjust_cents": in.ManualAdjustCents, "payable_cents": payable,
		}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}

// cancelOrder 取消订单。待付款未支付直接取消并释放名额；已付款只登记退款申请，
// 财务审批与渠道执行属于退款流程。
// ponytail: 已付款分支只置 refund_status=pending_approval，退款审批与执行未实现。
func cancelOrder(ctx context.Context, db *ent.Client, orderID int, reason string) (*ent.EsportsOrder, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len([]rune(reason)) > 255 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	unpaid := order.FulfillmentStatus == esportsorder.FulfillmentStatusPendingPayment &&
		order.PaymentStatus == esportsorder.PaymentStatusUnpaid
	refundable := order.PaymentStatus == esportsorder.PaymentStatusPaid &&
		order.FulfillmentStatus != esportsorder.FulfillmentStatusCompleted &&
		order.FulfillmentStatus != esportsorder.FulfillmentStatusCancelled
	switch {
	case unpaid:
		changed, err := tx.EsportsOrder.Update().Where(
			esportsorder.IDEQ(order.ID), esportsorder.VersionEQ(order.Version),
			esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingPayment),
			esportsorder.PaymentStatusEQ(esportsorder.PaymentStatusUnpaid),
		).SetFulfillmentStatus(esportsorder.FulfillmentStatusCancelled).AddVersion(1).Save(ctx)
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, errConflict
		}
		if err = releaseReservedQuota(ctx, tx, order.ID); err != nil {
			return nil, err
		}
		if _, err = tx.EsportsPaymentIntent.Update().Where(
			esportspaymentintent.OrderIDEQ(order.ID),
			esportspaymentintent.StatusEQ(esportspaymentintent.StatusPending),
		).SetStatus(esportspaymentintent.StatusInvalidated).Save(ctx); err != nil {
			return nil, err
		}
		eventType := "order.cancelled"
		if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
			SetOrderVersion(order.Version + 1).SetEventType(eventType).
			SetActorType("admin").SetActorID(strconv.Itoa(order.ID)).
			SetDetails(map[string]interface{}{"reason": reason}).Save(ctx); err != nil {
			return nil, err
		}
		if _, err = tx.EsportsOutbox.Create().SetEventKey("order.cancelled:" + order.OrderNo).
			SetEventType(eventType).SetAggregateRef(order.OrderNo).
			SetPayload(map[string]interface{}{"order_no": order.OrderNo}).Save(ctx); err != nil {
			return nil, err
		}
	case refundable:
		if order.RefundStatus != esportsorder.RefundStatusNone {
			return nil, errConflict
		}
		if _, err = tx.EsportsOrder.UpdateOne(order).
			SetRefundStatus(esportsorder.RefundStatusPendingApproval).AddVersion(1).Save(ctx); err != nil {
			return nil, err
		}
		if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
			SetOrderVersion(order.Version + 1).SetEventType("order.refund_requested").
			SetActorType("admin").SetActorID(strconv.Itoa(order.ID)).
			SetDetails(map[string]interface{}{"reason": reason}).Save(ctx); err != nil {
			return nil, err
		}
	default:
		return nil, errConflict
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}

// rescheduleOrder 改期。仅待付款未支付订单可用，改期不改变价格与名额占用。
// ponytail: 未实现限时改期提案与成员逐一确认，直接生效并留痕；接入多席位后须改为提案+原子切换。
func rescheduleOrder(ctx context.Context, db *ent.Client, orderID int, in rescheduleInput, now time.Time) (*ent.EsportsOrder, error) {
	in.Reason = strings.TrimSpace(in.Reason)
	if in.ExpectedVersion < 1 || in.Reason == "" || len([]rune(in.Reason)) > 255 ||
		in.StartAt.IsZero() || !in.StartAt.After(now.UTC()) {
		return nil, errInvalidInput
	}
	changed, err := db.EsportsOrder.Update().Where(
		esportsorder.IDEQ(orderID), esportsorder.VersionEQ(in.ExpectedVersion),
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingPayment),
		esportsorder.PaymentStatusEQ(esportsorder.PaymentStatusUnpaid),
	).SetRequestedStartAt(in.StartAt.UTC()).
		SetAppointmentStatus(esportsorder.AppointmentStatusRequest).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	order, err := db.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if _, err = db.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(order.Version).SetEventType("order.rescheduled").
		SetActorType("admin").SetActorID(strconv.Itoa(order.ID)).
		SetDetails(map[string]interface{}{
			"reason": in.Reason, "requested_start_at": in.StartAt.UTC().Format(time.RFC3339),
		}).Save(ctx); err != nil {
		return nil, err
	}
	return order, nil
}
