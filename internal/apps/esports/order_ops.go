package esports

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportspaymentintent"
	"go-admin/internal/ent/esportspriceadjustment"
	"go-admin/internal/ent/esportsquotahold"
	"go-admin/internal/ent/esportssku"
	"go-admin/internal/ent/systemconfig"
)

type repriceInput struct {
	ExpectedVersion   int    `json:"expected_version"`
	Quantity          int64  `json:"quantity"`
	ManualAdjustCents int64  `json:"manual_adjust_cents"`
	Reason            string `json:"reason"`
	ApprovalID        int    `json:"approval_id"`
}

type repriceQuote struct {
	OrderID              int   `json:"order_id"`
	QuoteVersion         int   `json:"quote_version"`
	Quantity             int64 `json:"quantity"`
	UnitPriceCents       int64 `json:"unit_price_cents"`
	DiscountCents        int64 `json:"discount_cents"`
	ManualAdjustCents    int64 `json:"manual_adjust_cents"`
	PayableCents         int64 `json:"payable_cents"`
	PromisedServiceUnits int64 `json:"promised_service_units"`
	MinUnitPriceCents    int64 `json:"min_unit_price_cents"`
	ApprovalThresholdBp  int   `json:"approval_threshold_bp"`
	RequiresApproval     bool  `json:"requires_approval"`
}

func calculateRepriceQuote(ctx context.Context, db *ent.Client, order *ent.EsportsOrder,
	quantity, manualAdjustCents int64) (repriceQuote, error) {
	if quantity < 1 || quantity > 1000 || order.UnitPriceCents <= 0 ||
		order.UnitPriceCents > math.MaxInt64/quantity || order.Quantity <= 0 ||
		order.PromisedServiceUnits <= 0 || order.PromisedServiceUnits%order.Quantity != 0 {
		return repriceQuote{}, errInvalidInput
	}
	serviceUnitsPerQuantity := order.PromisedServiceUnits / order.Quantity
	if serviceUnitsPerQuantity > math.MaxInt64/quantity {
		return repriceQuote{}, errInvalidInput
	}
	sku, err := db.EsportsSku.Get(ctx, order.SkuID)
	if err != nil {
		return repriceQuote{}, err
	}
	if sku.Status != esportssku.StatusOnSale {
		return repriceQuote{}, errConflict
	}
	customer, err := db.EsportsCustomer.Get(ctx, order.CustomerID)
	if err != nil {
		return repriceQuote{}, err
	}
	discountBps, err := memberDiscountBps(ctx, db, customer.MemberLevelCode)
	if err != nil {
		return repriceQuote{}, err
	}
	subtotal := order.UnitPriceCents * quantity
	discount := memberDiscountCents(subtotal, discountBps)
	base := subtotal - discount
	if manualAdjustCents <= -base || manualAdjustCents > math.MaxInt64-base ||
		manualAdjustCents > math.MaxInt64-subtotal {
		return repriceQuote{}, errInvalidInput
	}
	floor := order.MinUnitPriceCents
	if floor <= 0 {
		floor = order.UnitPriceCents
	}
	if floor > order.UnitPriceCents || subtotal+manualAdjustCents < floor*quantity {
		return repriceQuote{}, fmt.Errorf("%w：人工议价低于 SKU 最低允许价", errInvalidInput)
	}
	approvalBp := order.AdjustmentApprovalBp
	if approvalBp <= 0 || approvalBp > 10000 {
		approvalBp = 2000
	}
	threshold := subtotal/10000*int64(approvalBp) + subtotal%10000*int64(approvalBp)/10000
	absAdjust := manualAdjustCents
	if absAdjust < 0 {
		absAdjust = -absAdjust
	}
	return repriceQuote{
		OrderID: order.ID, QuoteVersion: order.QuoteVersion + 1,
		Quantity: quantity, UnitPriceCents: order.UnitPriceCents,
		DiscountCents: discount, ManualAdjustCents: manualAdjustCents,
		PayableCents:         base + manualAdjustCents,
		PromisedServiceUnits: serviceUnitsPerQuantity * quantity,
		MinUnitPriceCents:    floor, ApprovalThresholdBp: approvalBp,
		RequiresApproval: absAdjust > threshold,
	}, nil
}

func previewReprice(ctx context.Context, db *ent.Client, orderID int, in repriceInput) (repriceQuote, error) {
	if orderID < 1 || in.ExpectedVersion < 1 {
		return repriceQuote{}, errInvalidInput
	}
	order, err := db.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return repriceQuote{}, err
	}
	if order.Version != in.ExpectedVersion ||
		order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingPayment ||
		order.PaymentStatus != esportsorder.PaymentStatusUnpaid {
		return repriceQuote{}, errConflict
	}
	return calculateRepriceQuote(ctx, db, order, in.Quantity, in.ManualAdjustCents)
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
func repriceOrder(ctx context.Context, db *ent.Client, orderID, adminID int, isSuper bool, in repriceInput) (*ent.EsportsOrder, error) {
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
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	if !isSuper && (order.ResponsibleAdminID == nil || *order.ResponsibleAdminID != adminID) {
		return nil, errForbidden
	}
	if order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingPayment ||
		order.PaymentStatus != esportsorder.PaymentStatusUnpaid || order.Version != in.ExpectedVersion {
		return nil, errConflict
	}
	sku, err := tx.EsportsSku.Get(ctx, order.SkuID)
	if err != nil {
		return nil, err
	}
	quote, err := calculateRepriceQuote(ctx, tx.Client(), order, in.Quantity, in.ManualAdjustCents)
	if err != nil {
		return nil, err
	}
	var approval *ent.EsportsPriceAdjustment
	if quote.RequiresApproval {
		if in.ApprovalID < 1 {
			return nil, fmt.Errorf("%w：人工调整超过免审阈值，请先发起审批", errInvalidInput)
		}
		approval, err = tx.EsportsPriceAdjustment.Get(ctx, in.ApprovalID)
		if err != nil {
			return nil, err
		}
		if approval.Status != esportspriceadjustment.StatusApproved || approval.OrderID != order.ID ||
			approval.OrderVersion != order.Version || approval.Quantity != in.Quantity ||
			approval.ManualAdjustCents != in.ManualAdjustCents || approval.PayableCents != quote.PayableCents ||
			approval.RequestedByAdminID != adminID || approval.Reason != in.Reason ||
			approval.ReviewedByAdminID == nil || *approval.ReviewedByAdminID == adminID {
			return nil, errConflict
		}
	} else if in.ApprovalID > 0 {
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
		SetQuantity(in.Quantity).SetDiscountCents(quote.DiscountCents).SetManualAdjustCents(in.ManualAdjustCents).
		SetPayableCents(quote.PayableCents).SetPromisedServiceUnits(quote.PromisedServiceUnits).
		AddQuoteVersion(1).AddVersion(1).Save(ctx); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(order.Version + 1).SetEventType("order.repriced").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"reason": in.Reason, "quantity": in.Quantity,
			"discount_cents": quote.DiscountCents, "manual_adjust_cents": in.ManualAdjustCents,
			"payable_cents": quote.PayableCents,
		}).Save(ctx); err != nil {
		return nil, err
	}
	if approval != nil {
		changed, err := tx.EsportsPriceAdjustment.Update().Where(
			esportspriceadjustment.IDEQ(approval.ID), esportspriceadjustment.StatusEQ(esportspriceadjustment.StatusApproved),
		).SetStatus(esportspriceadjustment.StatusConsumed).SetConsumedAt(time.Now().UTC()).Save(ctx)
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, errConflict
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}

// cancelOrder 取消订单。待付款未支付直接取消并释放名额；已付款订单按公示取消政策（§3.3）
// 计算应退金额，同事务退回名额、作废席位与支付意图，并生成一张可审批的售后退款单
// （kind=refund_request）承接后续审批与执行；已开工订单不允许直接取消，须走售后。
func cancelOrder(ctx context.Context, db *ent.Client, orderID, adminID int, reason string, now time.Time) (*ent.EsportsOrder, error) {
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
	if err = ensureOrderNotFrozen(order); err != nil {
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
			SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
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
		started, err := orderServiceStarted(ctx, tx.Client(), order.ID)
		if err != nil {
			return nil, err
		}
		if started {
			return nil, errCancelServiceStarted
		}
		policy, err := cancelPolicyFromConfig(ctx, tx.Client())
		if err != nil {
			return nil, err
		}
		quote := quoteCancelOrder(policy, order, now)
		refundStatus := esportsorder.RefundStatusNone
		if quote.RefundCents > 0 {
			refundStatus = esportsorder.RefundStatusPendingApproval
		}
		changed, err := tx.EsportsOrder.Update().Where(
			esportsorder.IDEQ(order.ID), esportsorder.VersionEQ(order.Version),
			esportsorder.PaymentStatusEQ(esportsorder.PaymentStatusPaid),
			esportsorder.RefundStatusEQ(esportsorder.RefundStatusNone),
		).SetFulfillmentStatus(esportsorder.FulfillmentStatusCancelled).
			SetRefundStatus(refundStatus).AddVersion(1).Save(ctx)
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, errConflict
		}
		// 名额退回（占用记录 sold → returned）与席位作废：取消后商品容量可再售，陪玩容量不再被占。
		if err = returnSoldQuota(ctx, tx, order.ID); err != nil {
			return nil, err
		}
		if err = releaseSeatsOnOrderCancel(ctx, tx, order.ID); err != nil {
			return nil, err
		}
		if _, err = tx.EsportsPaymentIntent.Update().Where(
			esportspaymentintent.OrderIDEQ(order.ID),
			esportspaymentintent.StatusEQ(esportspaymentintent.StatusPending),
		).SetStatus(esportspaymentintent.StatusInvalidated).Save(ctx); err != nil {
			return nil, err
		}
		// 有可退金额时生成售后单，退款从此走统一的售后审批与执行流程；
		// 政策为「全额收取手续费」时不建单，也不置退款状态。
		var afterSaleNo string
		if quote.RefundCents > 0 {
			ticket, err := createAfterSaleTx(ctx, tx, afterSaleInput{
				SourceType: "order", SourceID: order.ID, Kind: "refund_request", Channel: "admin",
				Demand: fmt.Sprintf("客服取消已付款订单（%s）；按公示取消政策应退 %d 分，手续费 %d 分",
					reason, quote.RefundCents, quote.FeeCents),
				ExpectedPlan: "按公示取消政策退款",
			}, afterSaleCreateOptions{
				ActorType: "admin", ActorID: strconv.Itoa(adminID), EventType: "order.after_sale_created",
				Details: map[string]interface{}{
					"refund_cents": quote.RefundCents, "fee_cents": quote.FeeCents,
					"cancel_policy": quote.Policy, "free_cancel": quote.FreeCancel,
				},
			})
			if err != nil {
				return nil, err
			}
			afterSaleNo = ticket.AfterSaleNo
		}
		current, err := tx.EsportsOrder.Get(ctx, order.ID)
		if err != nil {
			return nil, err
		}
		newVersion, err := bumpOrder(ctx, tx, order.ID, current.Version, "")
		if err != nil {
			return nil, err
		}
		if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
			SetOrderVersion(newVersion).SetEventType("order.cancelled").
			SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
			SetDetails(map[string]interface{}{
				"reason": reason, "refund_status": refundStatus.String(),
				"after_sale_no": afterSaleNo, "free_cancel": quote.FreeCancel,
				"fee_cents": quote.FeeCents, "refund_cents": quote.RefundCents,
				"cancel_policy": quote.Policy,
			}).Save(ctx); err != nil {
			return nil, err
		}
		if _, err = tx.EsportsOutbox.Create().SetEventKey("order.cancelled:" + order.OrderNo).
			SetEventType("order.cancelled").SetAggregateRef(order.OrderNo).
			SetPayload(map[string]interface{}{
				"order_no": order.OrderNo, "after_sale_no": afterSaleNo,
			}).Save(ctx); err != nil {
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

// rescheduleOrder 直接改期。仅待付款未支付订单可用，改期不改变价格与名额占用。
// §3.3：已付款订单的档期已被派单席位占用，改期须走限时改期提案（proposeReschedule：
// 客户与订单上已接单成员逐人确认后，在同一事务内原子切换档期并复核占用目标档期资源），
// 本函数只保留未付款订单的直接改期口径。
func rescheduleOrder(ctx context.Context, db *ent.Client, orderID, adminID int, in rescheduleInput, now time.Time) (*ent.EsportsOrder, error) {
	in.Reason = strings.TrimSpace(in.Reason)
	if in.ExpectedVersion < 1 || in.Reason == "" || len([]rune(in.Reason)) > 255 ||
		in.StartAt.IsZero() || !in.StartAt.After(now.UTC()) {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// §3.3 分流：已付款订单的档期是已占用资源，改期必须走限时改期提案；直接改期只给未付款订单，
	// 避免付费后由客服单方面挪动档期而绕过客户与已接单成员的确认。
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	if order.PaymentStatus != esportsorder.PaymentStatusUnpaid {
		return nil, errRescheduleProposalRequired
	}
	if order.Version != in.ExpectedVersion ||
		order.FulfillmentStatus != esportsorder.FulfillmentStatusPendingPayment {
		return nil, errConflict
	}
	// 改期建立限时档期提案（§3.2 / §5.5）：写确认时限，缺省 24 小时内确认，且不晚于预约开始时间。
	deadline := now.UTC().Add(time.Duration(appointmentConfirmHours(ctx, db)) * time.Hour)
	if deadline.After(in.StartAt.UTC()) {
		deadline = in.StartAt.UTC()
	}
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(orderID), esportsorder.VersionEQ(in.ExpectedVersion),
		esportsorder.FulfillmentStatusEQ(esportsorder.FulfillmentStatusPendingPayment),
		esportsorder.PaymentStatusEQ(esportsorder.PaymentStatusUnpaid),
	).SetRequestedStartAt(in.StartAt.UTC()).
		SetAppointmentConfirmationDeadline(deadline).
		SetAppointmentStatus(esportsorder.AppointmentStatusRequest).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	order, err = tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(order.Version).SetEventType("order.rescheduled").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"reason":                            in.Reason,
			"requested_start_at":                in.StartAt.UTC().Format(time.RFC3339),
			"appointment_confirmation_deadline": deadline.Format(time.RFC3339),
		}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}

// appointmentConfirmHours 返回档期确认时限（小时，§3.2），缺省 24。配置缺失或非法时回落缺省值，
// 不因配置问题阻断改期。
func appointmentConfirmHours(ctx context.Context, db *ent.Client) int {
	const fallback = 24
	item, err := db.SystemConfig.Query().Where(
		systemconfig.KeyEQ("app.esports.appointment_confirm_hours")).Only(ctx)
	if err != nil {
		return fallback
	}
	hours, err := strconv.Atoi(strings.TrimSpace(item.Value))
	if err != nil || hours < 1 || hours > 24*30 {
		return fallback
	}
	return hours
}

// ensureAppointmentConfirmed 拦截「付费预约但档期未确认」的派单（§3.2 / §5.5）。
// 有预约开始时间却仍处于 request 的订单不得派单，避免虚构档期占位。
func ensureAppointmentConfirmed(order *ent.EsportsOrder) error {
	if order == nil || order.RequestedStartAt == nil {
		return nil
	}
	if order.AppointmentStatus == esportsorder.AppointmentStatusConfirmed {
		return nil
	}
	return fmt.Errorf("%w：预约订单须先确认档期后才能派单", errConflict)
}

// confirmOrderAppointment 确认预约档期（§3.2 / §5.5）：把 appointment_status 由 request 原子推进到
// confirmed，并在同一事务内做资源核验——客户指定陪玩时校验该时段并发与每日容量是否仍可占用。
// 未确认档期的预约单不允许派单。
func confirmOrderAppointment(ctx context.Context, db *ent.Client, orderID, adminID int, now time.Time) (*ent.EsportsOrder, error) {
	if orderID < 1 || adminID < 1 {
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
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	if order.AppointmentStatus != esportsorder.AppointmentStatusRequest || order.RequestedStartAt == nil {
		return nil, errConflict
	}
	start := order.RequestedStartAt.UTC()
	if !start.After(now.UTC()) {
		return nil, errConflict
	}
	if order.AppointmentConfirmationDeadline != nil && !order.AppointmentConfirmationDeadline.After(now.UTC()) {
		return nil, errConflict
	}
	if order.PreferredPlayerID != nil {
		player, pErr := tx.EsportsPlayer.Get(ctx, *order.PreferredPlayerID)
		if pErr != nil {
			return nil, errConflict
		}
		if err := checkPlayerDispatchLimit(ctx, tx, player, order, now, 0); err != nil {
			return nil, err
		}
	}
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(orderID), esportsorder.VersionEQ(order.Version),
		esportsorder.AppointmentStatusEQ(esportsorder.AppointmentStatusRequest),
	).SetAppointmentStatus(esportsorder.AppointmentStatusConfirmed).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(order.Version + 1).SetEventType("order.appointment_confirmed").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"requested_start_at":       start.Format(time.RFC3339),
			"service_duration_minutes": order.ServiceDurationMinutes,
		}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}

// setOrderChannelCost 登记订单已知渠道成本（§12.1）。结单计提客服提成时从可分基数扣除，
// 并参与「工作室剩余 = 服务应付 − 陪玩分成 − 客服提成 − 渠道成本」反算。
// 渠道成本须为非负且不超过订单应付；结单后不可再改（避免与已计提快照不一致）。
func setOrderChannelCost(ctx context.Context, db *ent.Client, orderID int, channelCostCents int64, adminID int, now time.Time) (*ent.EsportsOrder, error) {
	if orderID < 1 || channelCostCents < 0 {
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
	if err = ensureOrderNotFrozen(order); err != nil {
		return nil, err
	}
	// 结单后（completed / cancelled）不可再改，避免与已计提的快照不一致；待结单及更早均可改。
	if order.FulfillmentStatus == esportsorder.FulfillmentStatusCompleted ||
		order.FulfillmentStatus == esportsorder.FulfillmentStatusCancelled {
		return nil, errConflict
	}
	if channelCostCents > order.PayableCents {
		return nil, errInvalidInput
	}
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(orderID), esportsorder.VersionEQ(order.Version),
	).SetChannelCostCents(channelCostCents).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(orderID).
		SetOrderVersion(order.Version + 1).SetEventType("order.channel_cost_updated").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{"channel_cost_cents": channelCostCents}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, orderID)
}
