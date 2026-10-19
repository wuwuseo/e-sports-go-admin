package esports

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect/sql"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportspaymentintent"
	"go-admin/internal/ent/esportspriceadjustment"
	"go-admin/internal/ent/esportsquotahold"
	"go-admin/internal/ent/esportssku"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/global"
)

type repriceInput struct {
	ExpectedVersion   int    `json:"expected_version"`
	Quantity          int64  `json:"quantity"`
	ManualAdjustCents int64  `json:"manual_adjust_cents"`
	Reason            string `json:"reason"`
	ApprovalID        int    `json:"approval_id"`
	// SkuID 改规格 / 改档位（§3.3-01）：目标子 SKU ID。0 或与本单当前 SKU 相同时只改价 / 改份数。
	// 改规格时会作废旧支付意图、抬升报价版本，并按新规格的快照价、名额与席位置重算。
	SkuID int `json:"sku_id,omitempty"`
}

type repriceQuote struct {
	OrderID              int   `json:"order_id"`
	QuoteVersion         int   `json:"quote_version"`
	SkuID                int   `json:"sku_id"`
	Quantity             int64 `json:"quantity"`
	UnitPriceCents       int64 `json:"unit_price_cents"`
	DiscountCents        int64 `json:"discount_cents"`
	ManualAdjustCents    int64 `json:"manual_adjust_cents"`
	PayableCents         int64 `json:"payable_cents"`
	PromisedServiceUnits int64 `json:"promised_service_units"`
	MinUnitPriceCents    int64 `json:"min_unit_price_cents"`
	ApprovalThresholdBp  int   `json:"approval_threshold_bp"`
	RequiresApproval     bool  `json:"requires_approval"`
	// SpecChanged 标记本次改价是否同时改了规格 / 档位（§3.3-01）。
	SpecChanged bool `json:"spec_changed"`
	// §10.4-08：改价时一并快照当时的会员等级与折扣基点（事后改规则不追溯历史单）。
	MemberLevelCode   string `json:"member_level_code"`
	MemberDiscountBps int    `json:"member_discount_bps"`
	// §15.3-01 / §5.3-05：改价后订单落库的每席承诺服务量（改规格时按目标 SKU 重算）。
	PerSeatServiceUnits int64 `json:"per_seat_service_units"`
}

func calculateRepriceQuote(ctx context.Context, db *ent.Client, order *ent.EsportsOrder,
	quantity, manualAdjustCents int64) (repriceQuote, error) {
	sku, err := db.EsportsSku.Get(ctx, order.SkuID)
	if err != nil {
		return repriceQuote{}, err
	}
	return calculateRepriceQuoteForSku(ctx, db, order, sku, quantity, manualAdjustCents)
}

// calculateRepriceQuoteForSku 按目标 SKU 试算改价（§3.3-01）：
// 目标 SKU 与订单当前 SKU 相同时沿用订单快照价 / 快照承诺量（历史单不追溯 SKU 后续改价）；
// 目标 SKU 不同（改规格 / 改档位）时改按新 SKU 的标价、每份服务量、最低允许价与免审额度计算。
func calculateRepriceQuoteForSku(ctx context.Context, db *ent.Client, order *ent.EsportsOrder,
	target *ent.EsportsSku, quantity, manualAdjustCents int64) (repriceQuote, error) {
	if target == nil || quantity < 1 || quantity > 1000 || order.Quantity <= 0 {
		return repriceQuote{}, errInvalidInput
	}
	if target.Status != esportssku.StatusOnSale {
		return repriceQuote{}, errConflict
	}
	specChanged := target.ID != order.SkuID
	unitPrice := order.UnitPriceCents
	unitsPerQuantity := int64(0)
	perSeatUnits := order.PerSeatServiceUnits
	floor := order.MinUnitPriceCents
	approvalBp := order.AdjustmentApprovalBp
	if specChanged {
		if target.UnitPriceCents <= 0 || target.ServiceUnits <= 0 {
			return repriceQuote{}, errInvalidInput
		}
		unitPrice = target.UnitPriceCents
		// §15.3-01 / §5.3-05：改规格时按目标 SKU（及其规格组）重算每席承诺服务量，
		// 多人规格缺每席量即拒绝改价，避免改完规格订单承诺量与新规格口径脱节。
		var targetGroup *ent.EsportsSpecGroup
		if target.SpecGroupID != nil {
			group, groupErr := db.EsportsSpecGroup.Get(ctx, *target.SpecGroupID)
			if groupErr != nil {
				return repriceQuote{}, groupErr
			}
			targetGroup = group
		}
		targetPerSeat, targetSeatTotal, _, promiseErr := resolveSeatPromise(target, targetGroup)
		if promiseErr != nil {
			return repriceQuote{}, promiseErr
		}
		perSeatUnits = targetPerSeat
		unitsPerQuantity = targetSeatTotal
		floor = target.MinUnitPriceCents
		approvalBp = target.AdjustmentApprovalBp
	} else {
		if order.PromisedServiceUnits <= 0 || order.PromisedServiceUnits%order.Quantity != 0 {
			return repriceQuote{}, errInvalidInput
		}
		unitsPerQuantity = order.PromisedServiceUnits / order.Quantity
	}
	if unitPrice <= 0 || unitPrice > math.MaxInt64/quantity ||
		unitsPerQuantity <= 0 || unitsPerQuantity > math.MaxInt64/quantity {
		return repriceQuote{}, errInvalidInput
	}
	customer, err := db.EsportsCustomer.Get(ctx, order.CustomerID)
	if err != nil {
		return repriceQuote{}, err
	}
	discountBps, err := memberDiscountBps(ctx, db, customer.MemberLevelCode)
	if err != nil {
		return repriceQuote{}, err
	}
	subtotal := unitPrice * quantity
	discount := memberDiscountCents(subtotal, discountBps)
	base := subtotal - discount
	if manualAdjustCents <= -base || manualAdjustCents > math.MaxInt64-base ||
		manualAdjustCents > math.MaxInt64-subtotal {
		return repriceQuote{}, errInvalidInput
	}
	if floor <= 0 {
		floor = unitPrice
	}
	if floor > unitPrice || subtotal+manualAdjustCents < floor*quantity {
		return repriceQuote{}, fmt.Errorf("%w：人工议价低于 SKU 最低允许价", errInvalidInput)
	}
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
		SkuID: target.ID, Quantity: quantity, UnitPriceCents: unitPrice,
		DiscountCents: discount, ManualAdjustCents: manualAdjustCents,
		PayableCents:         base + manualAdjustCents,
		PromisedServiceUnits: unitsPerQuantity * quantity,
		MinUnitPriceCents:    floor, ApprovalThresholdBp: approvalBp,
		RequiresApproval: absAdjust > threshold, SpecChanged: specChanged,
		MemberLevelCode: customer.MemberLevelCode, MemberDiscountBps: discountBps,
		PerSeatServiceUnits: perSeatUnits,
	}, nil
}

// validateSpecSwitchTarget 改规格 / 改档位的目标校验（§3.3-01）：
// 只能切到同一规格组（同父商品的兄弟 SKU）内的在售规格，避免跨商品改单绕过规格定义。
func validateSpecSwitchTarget(current, target *ent.EsportsSku) error {
	if current == nil || target == nil || current.ID == target.ID {
		return nil
	}
	if current.ProductID != nil && target.ProductID != nil && *current.ProductID != *target.ProductID {
		return fmt.Errorf("%w：改规格只能在同一商品下的规格之间切换", errInvalidInput)
	}
	if current.SpecGroupID != nil && target.SpecGroupID != nil && *current.SpecGroupID != *target.SpecGroupID {
		return fmt.Errorf("%w：改档位只能切到同一规格组内的规格", errInvalidInput)
	}
	return nil
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
	current, err := db.EsportsSku.Get(ctx, order.SkuID)
	if err != nil {
		return repriceQuote{}, err
	}
	target := current
	if in.SkuID > 0 && in.SkuID != order.SkuID {
		target, err = db.EsportsSku.Get(ctx, in.SkuID)
		if err != nil {
			return repriceQuote{}, err
		}
		if err = validateSpecSwitchTarget(current, target); err != nil {
			return repriceQuote{}, err
		}
	}
	return calculateRepriceQuoteForSku(ctx, db, order, target, in.Quantity, in.ManualAdjustCents)
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
	// §3.3-01 改规格 / 改档位：目标子 SKU 须在同一规格组内且在售，规格不同则按新规格重算。
	targetSku := sku
	if in.SkuID > 0 && in.SkuID != order.SkuID {
		targetSku, err = tx.EsportsSku.Get(ctx, in.SkuID)
		if err != nil {
			return nil, err
		}
		if err = validateSpecSwitchTarget(sku, targetSku); err != nil {
			return nil, err
		}
	}
	quote, err := calculateRepriceQuoteForSku(ctx, tx.Client(), order, targetSku, in.Quantity, in.ManualAdjustCents)
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
	if quote.SpecChanged {
		// §3.3-01：旧 SKU 整单退还占用，新 SKU 重新占位；占用记录改挂新 SKU。
		updated, err := tx.EsportsSku.Update().Where(
			esportssku.IDEQ(sku.ID), esportssku.QuotaReservedGTE(order.Quantity),
		).AddQuotaReserved(-order.Quantity).AddVersion(1).Save(ctx)
		if err != nil {
			return nil, err
		}
		if updated != 1 {
			return nil, errConflict
		}
		updated, err = tx.EsportsSku.Update().Where(
			esportssku.IDEQ(targetSku.ID), esportssku.VersionEQ(targetSku.Version),
			esportssku.StatusEQ(esportssku.StatusOnSale),
			func(s *sql.Selector) {
				s.Where(sql.Or(sql.EQ(esportssku.FieldQuotaTotal, -1),
					sql.ExprP("quota_total - quota_reserved - quota_sold >= ?", in.Quantity)))
			},
		).AddQuotaReserved(in.Quantity).AddVersion(1).Save(ctx)
		if err != nil {
			return nil, err
		}
		if updated != 1 {
			return nil, errConflict
		}
		changed, err := tx.EsportsQuotaHold.Update().
			Where(esportsquotahold.OrderIDEQ(orderID), esportsquotahold.StatusEQ(esportsquotahold.StatusReserved)).
			SetSkuID(targetSku.ID).SetQuantity(in.Quantity).Save(ctx)
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, errConflict
		}
	} else if delta > 0 {
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
	if delta != 0 && !quote.SpecChanged {
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
	orderUpdate := tx.EsportsOrder.UpdateOne(order).
		SetQuantity(in.Quantity).SetDiscountCents(quote.DiscountCents).SetManualAdjustCents(in.ManualAdjustCents).
		SetPayableCents(quote.PayableCents).SetPromisedServiceUnits(quote.PromisedServiceUnits).
		SetPerSeatServiceUnits(quote.PerSeatServiceUnits).
		// §10.4-08：改价时按当时生效的等级规则重新快照等级与折扣基点。
		SetMemberLevelCode(quote.MemberLevelCode).SetMemberDiscountBps(quote.MemberDiscountBps).
		AddQuoteVersion(1).AddVersion(1)
	if quote.SpecChanged {
		// 新规格快照：SKU、标价、最低允许价、免审额度与逐席角色一并切到目标规格（§15.6-03 名称同步切换）。
		orderUpdate = orderUpdate.SetSkuID(targetSku.ID).
			SetSkuCode(targetSku.Code).SetSkuName(targetSku.Title).
			SetSpecName("").
			SetUnitPriceCents(targetSku.UnitPriceCents).SetMinUnitPriceCents(targetSku.MinUnitPriceCents).
			SetAdjustmentApprovalBp(targetSku.AdjustmentApprovalBp)
		if targetSku.SpecGroupID != nil {
			group, groupErr := tx.EsportsSpecGroup.Get(ctx, *targetSku.SpecGroupID)
			if groupErr != nil {
				return nil, groupErr
			}
			orderUpdate = orderUpdate.SetSeatRoles(group.Seats).SetSpecName(group.Name)
		} else {
			orderUpdate = orderUpdate.ClearSeatRoles()
		}
	}
	if _, err = orderUpdate.Save(ctx); err != nil {
		return nil, err
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(order.Version + 1).SetEventType("order.repriced").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"reason": in.Reason, "quantity": in.Quantity,
			"discount_cents": quote.DiscountCents, "manual_adjust_cents": in.ManualAdjustCents,
			"payable_cents":   quote.PayableCents,
			"spec_changed":    quote.SpecChanged,
			"previous_sku_id": order.SkuID,
			"sku_id":          quote.SkuID,
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

// cancelOrder 取消订单（未指定责任方，按客户方责任落账，见 cancelOrderWithFault）。
func cancelOrder(ctx context.Context, db *ent.Client, orderID, adminID int, reason string, now time.Time) (*ent.EsportsOrder, error) {
	return cancelOrderWithFault(ctx, db, orderID, adminID, reason, "", now)
}

// cancelOrderWithFullRefund 全额退还取消（§5.5-06 档期确认超时处置）：固定平台责任方，
// 跳过公示取消政策的手续费，按「应付 − 已退 − 已预占」全额生成退款单。
func cancelOrderWithFullRefund(ctx context.Context, db *ent.Client, orderID, adminID int,
	reason string, now time.Time) (*ent.EsportsOrder, error) {
	return cancelOrderWithRefund(ctx, db, orderID, adminID, reason, "platform", true, now)
}

// cancelOrderFaultParties 是取消时可落的责任方枚举（与售后单 fault_party 一致，§3.3-08）。
var cancelOrderFaultParties = map[string]string{
	"customer": "customer", "player": "player", "admin": "admin",
	"platform": "platform", "none": "none",
}

// normalizeCancelFaultParty 归一化取消责任方（§3.3-08）：未指定时按客户方责任落账
// （取消多由客户申请），避免售后单责任方停在 undetermined；显式传入须为合法枚举。
func normalizeCancelFaultParty(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "customer", nil
	}
	value, ok := cancelOrderFaultParties[raw]
	if !ok {
		return "", fmt.Errorf("%w：取消责任方只能是 customer / player / admin / platform / none", errInvalidInput)
	}
	return value, nil
}

// cancelOrderWithFault 取消订单。待付款未支付直接取消并释放名额；已付款订单按公示取消政策（§3.3）
// 计算应退金额，同事务退回名额、作废席位与支付意图，并生成一张可审批的售后退款单
// （kind=refund_request）承接后续审批与执行；已开工订单不允许直接取消，须走售后。
// faultParty 为责任方（§3.3-08）：其值随退款单一起落库，供售后追偿与责任统计使用。
func cancelOrderWithFault(ctx context.Context, db *ent.Client, orderID, adminID int, reason, faultParty string,
	now time.Time) (*ent.EsportsOrder, error) {
	return cancelOrderWithRefund(ctx, db, orderID, adminID, reason, faultParty, false, now)
}

// cancelOrderWithRefund 取消订单的公共实现；fullRefund=true 时按全额退还（不按公示政策扣手续费），
// 供 §5.5-06「档期确认超时」的平台责任处置使用。
func cancelOrderWithRefund(ctx context.Context, db *ent.Client, orderID, adminID int, reason, faultParty string,
	fullRefund bool, now time.Time) (*ent.EsportsOrder, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len([]rune(reason)) > 255 {
		return nil, errInvalidInput
	}
	faultParty, err := normalizeCancelFaultParty(faultParty)
	if err != nil {
		return nil, err
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
		if fullRefund {
			// §5.5-06：档期确认超时属平台原因，全额退还本金，不收手续费。
			quote.FeeCents = 0
			quote.FreeCancel = true
			quote.RefundCents = quote.PayableCents - quote.RefundedCents - quote.ReservedCents
			if quote.RefundCents < 0 {
				quote.RefundCents = 0
			}
			quote.Cancelable = true
			quote.BlockedReason = ""
		}
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
			// §3.3-08：取消时即落责任方，避免售后单责任方停在 undetermined。
			if _, err = tx.EsportsAfterSale.Update().Where(
				esportsaftersale.IDEQ(ticket.ID),
				esportsaftersale.FaultPartyEQ(esportsaftersale.FaultPartyUndetermined),
			).SetFaultParty(esportsaftersale.FaultParty(faultParty)).Save(ctx); err != nil {
				return nil, err
			}
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
				"cancel_policy": quote.Policy, "fault_party": faultParty,
				"full_refund": fullRefund,
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

// ── §5.5-06 档期确认到期扫描与「全额退还 / 改期」两个入口 ──

// appointmentExpireActionKey 档期确认过期处置方式：refund（只允许全额退）/ reschedule（只允许改期）/
// both（两种都允许，默认）。配置缺失或非法一律回落 both。
const appointmentExpireActionKey = "app.esports.appointment_expire_action"

const (
	appointmentExpireActionRefund     = "refund"
	appointmentExpireActionReschedule = "reschedule"
	appointmentExpireActionBoth       = "both"
)

// appointmentExpireAction 返回档期确认过期后允许的处置方式。
func appointmentExpireAction(ctx context.Context, db *ent.Client) string {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(appointmentExpireActionKey)).Only(ctx)
	if err != nil {
		return appointmentExpireActionBoth
	}
	switch strings.TrimSpace(item.Value) {
	case appointmentExpireActionRefund:
		return appointmentExpireActionRefund
	case appointmentExpireActionReschedule:
		return appointmentExpireActionReschedule
	default:
		return appointmentExpireActionBoth
	}
}

// expireAppointmentConfirmations 档期确认到期扫描（§5.5-06）：对「已付款、有预约开始时间、
// appointment_status=request 且已过 appointment_confirmation_deadline」的订单置为
// pending_confirm（已付款待确认档期超时）并写 appointment_suspended_at 暂停派单，
// 同时落订单事件与客服待办外盒。逐单独立事务，重复运行幂等（已处置的单不再命中）。
func expireAppointmentConfirmations(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	ids, err := db.EsportsOrder.Query().Where(
		esportsorder.AppointmentStatusEQ(esportsorder.AppointmentStatusRequest),
		esportsorder.AppointmentSuspendedAtIsNil(),
		esportsorder.RequestedStartAtNotNil(),
		esportsorder.AppointmentConfirmationDeadlineNotNil(),
		esportsorder.AppointmentConfirmationDeadlineLTE(now.UTC()),
		esportsorder.PaymentStatusEQ(esportsorder.PaymentStatusPaid),
		esportsorder.FulfillmentStatusNEQ(esportsorder.FulfillmentStatusCancelled),
		esportsorder.FulfillmentStatusNEQ(esportsorder.FulfillmentStatusCompleted),
	).Order(ent.Asc(esportsorder.FieldAppointmentConfirmationDeadline)).Limit(limit).IDs(ctx)
	if err != nil {
		return 0, err
	}
	settled := 0
	for _, id := range ids {
		changed, err := suspendExpiredAppointment(ctx, db, id, now)
		if errors.Is(err, errConflict) {
			continue // 已被其它任务 / 人工处置
		}
		if err != nil {
			return settled, err
		}
		if changed {
			settled++
		}
	}
	return settled, nil
}

// suspendExpiredAppointment 单笔置为「已付款待确认档期超时」（§5.5-06），返回是否本次完成处置。
func suspendExpiredAppointment(ctx context.Context, db *ent.Client, orderID int, now time.Time) (bool, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	order, err := tx.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return false, err
	}
	if order.AppointmentStatus != esportsorder.AppointmentStatusRequest ||
		order.AppointmentConfirmationDeadline == nil ||
		order.AppointmentConfirmationDeadline.After(now.UTC()) ||
		order.PaymentStatus != esportsorder.PaymentStatusPaid {
		return false, nil
	}
	action := appointmentExpireAction(ctx, tx.Client())
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(order.ID), esportsorder.VersionEQ(order.Version),
		esportsorder.AppointmentStatusEQ(esportsorder.AppointmentStatusRequest),
	).SetAppointmentStatus(esportsorder.AppointmentStatusPendingConfirm).
		SetNillableAppointmentSuspendedAt(&now).AddVersion(1).Save(ctx)
	if err != nil {
		return false, err
	}
	if changed != 1 {
		return false, errConflict
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(order.Version + 1).SetEventType("order.appointment_confirmation_expired").
		SetActorType("system").SetActorID("appointment_expiry").
		SetDetails(map[string]interface{}{
			"requested_start_at":                order.RequestedStartAt.UTC().Format(time.RFC3339),
			"appointment_confirmation_deadline": order.AppointmentConfirmationDeadline.UTC().Format(time.RFC3339),
			"allowed_actions":                   action,
		}).Save(ctx); err != nil {
		return false, err
	}
	if _, err = tx.EsportsOutbox.Create().
		SetEventKey("order.appointment_overdue:" + order.OrderNo).
		SetEventType("order.appointment_overdue").SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{
			"order_no": order.OrderNo, "order_id": order.ID,
			"responsible_admin_id": order.ResponsibleAdminID,
			"allowed_actions":      action,
		}).Save(ctx); err != nil && !ent.IsConstraintError(err) {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// appointmentPendingDisposals 列出「已付款待确认档期超时」的待处置订单（§5.5-06），
// 供客服待办列表与派单看板查询。重复调用只读，不改变状态。
func appointmentPendingDisposals(ctx context.Context, db *ent.Client, limit int) ([]*ent.EsportsOrder, error) {
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	return db.EsportsOrder.Query().Where(
		esportsorder.AppointmentStatusEQ(esportsorder.AppointmentStatusPendingConfirm),
		esportsorder.AppointmentSuspendedAtNotNil(),
	).Order(ent.Asc(esportsorder.FieldAppointmentSuspendedAt)).Limit(limit).All(ctx)
}

// appointmentPendingDisposalCount 待处置档期超时单量（客服待办计数用，§5.5-06）。
func appointmentPendingDisposalCount(ctx context.Context, db *ent.Client) (int, error) {
	return db.EsportsOrder.Query().Where(
		esportsorder.AppointmentStatusEQ(esportsorder.AppointmentStatusPendingConfirm),
		esportsorder.AppointmentSuspendedAtNotNil(),
	).Count(ctx)
}

// scanAppointmentExpiryTask 档期确认到期扫描任务（§5.5-06）。
func scanAppointmentExpiryTask(ctx context.Context, logger io.Writer) error {
	count, err := expireAppointmentConfirmations(ctx, global.ORM, time.Now(), 200)
	if err != nil {
		return err
	}
	if logger != nil {
		fmt.Fprintf(logger, "esports appointment expiry scan: %d suspended\n", count)
	}
	return nil
}

// appointmentDisposalInput 是「已付款待确认档期超时」订单的处置入参（§5.5-06）：
// action=refund 全额退还（平台责任、不收手续费）；action=reschedule 改期（须给出新的预约开始时间）。
type appointmentDisposalInput struct {
	Action          string     `json:"action"`
	Reason          string     `json:"reason"`
	StartAt         *time.Time `json:"start_at"`
	ExpectedVersion int        `json:"expected_version"`
}

// disposeExpiredAppointment 处置「已付款待确认档期超时」订单（§5.5-06）：显式提供全额退还与改期两个入口。
// 允许的处置方式受配置 app.esports.appointment_expire_action 限制（refund / reschedule / both）。
// 归属边界在路由层（ownedOrderHandler，P0-12）：非负责客服 / 非超管在进入本函数前即被拒绝；
// 全额退款这种动钱操作因此不会落到服务层再判一次（避免两处口径漂移）。
func disposeExpiredAppointment(ctx context.Context, db *ent.Client, orderID, adminID int,
	in appointmentDisposalInput, now time.Time) (*ent.EsportsOrder, error) {
	in.Action = strings.TrimSpace(in.Action)
	in.Reason = strings.TrimSpace(in.Reason)
	if orderID < 1 || adminID < 1 || in.ExpectedVersion < 1 || in.Reason == "" ||
		len([]rune(in.Reason)) > 255 {
		return nil, errInvalidInput
	}
	order, err := db.EsportsOrder.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.AppointmentStatus != esportsorder.AppointmentStatusPendingConfirm ||
		order.AppointmentSuspendedAt == nil {
		return nil, errConflict
	}
	if order.Version != in.ExpectedVersion {
		return nil, errConflict
	}
	allowed := appointmentExpireAction(ctx, db)
	if allowed != appointmentExpireActionBoth && allowed != in.Action {
		return nil, fmt.Errorf("%w：当前档期超时只允许 %s 处置", errForbidden, allowed)
	}
	switch in.Action {
	case appointmentExpireActionRefund:
		return cancelOrderWithFullRefund(ctx, db, orderID, adminID,
			"档期确认超时，全额退还："+in.Reason, now)
	case appointmentExpireActionReschedule:
		return rescheduleExpiredAppointment(ctx, db, order, in, adminID, now)
	default:
		return nil, fmt.Errorf("%w：档期超时处置方式只能是 refund 或 reschedule", errInvalidInput)
	}
}

// rescheduleExpiredAppointment 改期处置（§5.5-06）：把档期超时单重新开回 request 并写新的确认时限，
// 同时清除 appointment_suspended_at，恢复可确认（进而可派单）的状态。
func rescheduleExpiredAppointment(ctx context.Context, db *ent.Client, order *ent.EsportsOrder,
	in appointmentDisposalInput, adminID int, now time.Time) (*ent.EsportsOrder, error) {
	if in.StartAt == nil || !in.StartAt.After(now.UTC()) {
		return nil, fmt.Errorf("%w：改期须给出晚于当前时间的预约开始时间", errInvalidInput)
	}
	startAt := in.StartAt.UTC()
	deadline := now.UTC().Add(time.Duration(appointmentConfirmHours(ctx, db)) * time.Hour)
	if deadline.After(startAt) {
		deadline = startAt
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	changed, err := tx.EsportsOrder.Update().Where(
		esportsorder.IDEQ(order.ID), esportsorder.VersionEQ(order.Version),
		esportsorder.AppointmentStatusEQ(esportsorder.AppointmentStatusPendingConfirm),
	).SetAppointmentStatus(esportsorder.AppointmentStatusRequest).
		SetRequestedStartAt(startAt).SetAppointmentConfirmationDeadline(deadline).
		ClearAppointmentSuspendedAt().AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	if _, err = tx.EsportsOrderEvent.Create().SetOrderID(order.ID).
		SetOrderVersion(order.Version + 1).SetEventType("order.appointment_rescheduled").
		SetActorType("admin").SetActorID(strconv.Itoa(adminID)).
		SetDetails(map[string]interface{}{
			"reason":                            in.Reason,
			"requested_start_at":                startAt.Format(time.RFC3339),
			"appointment_confirmation_deadline": deadline.Format(time.RFC3339),
			"from":                              "appointment_confirmation_expired",
		}).Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsOrder.Get(ctx, order.ID)
}

// appointmentDisposalOptionView 是「档期超时可用处置动作」的只读视图（审核报告 §3.1 缺口）：
// 前端据此渲染处置按钮，不再把 refund / reschedule 硬编码在页面里。
// 只回显与该次处置直接相关的配置生效值，不回显其它配置、不含任何密钥类内容，也不改变任何状态。
type appointmentDisposalOptionView struct {
	// AllowedActions 当前配置下允许的处置方式，取值 refund / reschedule。
	AllowedActions []string `json:"allowed_actions"`
	// CurrentAction 配置的生效值：refund / reschedule / both（未配置或非法回落 both）。
	CurrentAction string `json:"current_action"`
	// ExpireAt 本单档期确认截止时间（RFC3339）；缺失时回落「暂停派单时间」，都缺失则为空字符串。
	ExpireAt string `json:"expire_at"`
}

// appointmentDisposalOptions 由配置 app.esports.appointment_expire_action 与订单档期状态推导可用处置动作
// （纯只读）：refund → 只允许全额退还；reschedule → 只允许改期；both → 两者皆可。
// 判定口径与 disposeExpiredAppointment 的允许校验共用 appointmentExpireAction，两处不会漂移。
func appointmentDisposalOptions(ctx context.Context, db *ent.Client, order *ent.EsportsOrder) appointmentDisposalOptionView {
	action := appointmentExpireAction(ctx, db)
	view := appointmentDisposalOptionView{CurrentAction: action}
	switch action {
	case appointmentExpireActionRefund:
		view.AllowedActions = []string{appointmentExpireActionRefund}
	case appointmentExpireActionReschedule:
		view.AllowedActions = []string{appointmentExpireActionReschedule}
	default:
		view.AllowedActions = []string{appointmentExpireActionRefund, appointmentExpireActionReschedule}
	}
	if order != nil {
		switch {
		case order.AppointmentConfirmationDeadline != nil:
			view.ExpireAt = order.AppointmentConfirmationDeadline.UTC().Format(time.RFC3339)
		case order.AppointmentSuspendedAt != nil:
			view.ExpireAt = order.AppointmentSuspendedAt.UTC().Format(time.RFC3339)
		}
	}
	return view
}
