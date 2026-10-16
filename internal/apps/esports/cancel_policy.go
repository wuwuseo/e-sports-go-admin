package esports

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsquotahold"
	"go-admin/internal/ent/esportsservicesegment"
	"go-admin/internal/ent/esportssku"
	"go-admin/internal/ent/systemconfig"
)

const (
	cancelFreeHoursKey = "app.esports.cancel_free_hours"
	cancelFeeBpKey     = "app.esports.cancel_fee_bp"
	// 默认政策：无免费窗口且不收取消费。业务方未签收前不擅自扣客户款，
	// 与「支付时限等参数缺省为 0」的口径一致；明确配置后才按政策扣费。
	defaultCancelFreeHours = 0
	defaultCancelFeeBp     = 0
)

// errCancelServiceStarted 已开工订单不允许直接取消：退款与补履约须走售后流程（§3.3 / §13）。
var errCancelServiceStarted = errors.New("订单已开始服务，请通过售后流程处理退款")

// cancelPolicy 是工作室公示的取消政策（§3.3）：付款后 FreeHours 小时内取消全额退，
// 超过窗口按应付金额的 FeeBp 万分比收取手续费。
type cancelPolicy struct {
	FreeHours int `json:"free_hours"`
	FeeBp     int `json:"fee_bp"`
}

// cancelQuote 是取消试算结果。取消是否收费都完整给出金额口径，供管理端「取消预览」
// 与客服口头告知客户，避免只在前端估算。
type cancelQuote struct {
	OrderID       int          `json:"order_id"`
	OrderNo       string       `json:"order_no"`
	Policy        cancelPolicy `json:"policy"`
	AnchorAt      time.Time    `json:"anchor_at"`
	FreeUntil     time.Time    `json:"free_until"`
	FreeCancel    bool         `json:"free_cancel"`
	PayableCents  int64        `json:"payable_cents"`
	RefundedCents int64        `json:"refunded_cents"`
	ReservedCents int64        `json:"refund_reserved_cents"`
	FeeCents      int64        `json:"fee_cents"`
	RefundCents   int64        `json:"refund_cents"`
	Cancelable    bool         `json:"cancelable"`
	BlockedReason string       `json:"blocked_reason"`
}

// cancelPolicyFromConfig 读取公示取消政策。配置缺失按默认（不收费）；配置存在但非法时直接报错，
// 避免用错误费率计算退款金额。
func cancelPolicyFromConfig(ctx context.Context, db *ent.Client) (cancelPolicy, error) {
	policy := cancelPolicy{FreeHours: defaultCancelFreeHours, FeeBp: defaultCancelFeeBp}
	if item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(cancelFreeHoursKey)).Only(ctx); err == nil {
		hours, convErr := strconv.Atoi(strings.TrimSpace(item.Value))
		if convErr != nil || hours < 0 || hours > 24*30 {
			return cancelPolicy{}, fmt.Errorf("%w：取消政策免费窗口配置无效", errInvalidInput)
		}
		policy.FreeHours = hours
	} else if !ent.IsNotFound(err) {
		return cancelPolicy{}, err
	}
	if item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(cancelFeeBpKey)).Only(ctx); err == nil {
		bp, convErr := strconv.Atoi(strings.TrimSpace(item.Value))
		if convErr != nil || bp < 0 || bp > 10000 {
			return cancelPolicy{}, fmt.Errorf("%w：取消政策手续费配置无效", errInvalidInput)
		}
		policy.FeeBp = bp
	} else if !ent.IsNotFound(err) {
		return cancelPolicy{}, err
	}
	return policy, nil
}

// cancelAnchorAt 取消政策的计时锚点：优先付款时间，历史缺付款时间时回落到创建时间。
func cancelAnchorAt(order *ent.EsportsOrder) time.Time {
	if order.PaidAt != nil {
		return order.PaidAt.UTC()
	}
	return order.CreatedAt.UTC()
}

// quoteCancelOrder 纯函数据政策算退款：可退基数 = 应付 − 已退 − 已预占，免费窗口内不收费。
// FreeHours = 0 表示不提供免费窗口（取消一律按费率计手续费），避免「0 小时免费窗口」被误读为随时免费。
func quoteCancelOrder(policy cancelPolicy, order *ent.EsportsOrder, now time.Time) cancelQuote {
	anchor := cancelAnchorAt(order)
	freeUntil := anchor.Add(time.Duration(policy.FreeHours) * time.Hour)
	quote := cancelQuote{
		OrderID:       order.ID,
		OrderNo:       order.OrderNo,
		Policy:        policy,
		AnchorAt:      anchor,
		FreeUntil:     freeUntil,
		FreeCancel:    policy.FreeHours > 0 && !now.UTC().After(freeUntil),
		PayableCents:  order.PayableCents,
		RefundedCents: order.RefundedCents,
		ReservedCents: order.RefundReservedCents,
	}
	base := order.PayableCents - order.RefundedCents - order.RefundReservedCents
	if base < 0 {
		base = 0
	}
	if !quote.FreeCancel {
		quote.FeeCents = base * int64(policy.FeeBp) / 10000
	}
	quote.RefundCents = base - quote.FeeCents
	switch {
	case order.PaymentStatus != esportsorder.PaymentStatusPaid:
		quote.BlockedReason = "订单尚未付款"
	case order.FulfillmentStatus == esportsorder.FulfillmentStatusCompleted:
		quote.BlockedReason = "订单已结单，退款请走售后"
	case order.FulfillmentStatus == esportsorder.FulfillmentStatusCancelled:
		quote.BlockedReason = "订单已取消"
	case order.RefundStatus != esportsorder.RefundStatusNone:
		quote.BlockedReason = "订单已有退款申请"
	default:
		quote.Cancelable = true
	}
	return quote
}

// previewCancelOrder 已付款订单的取消试算（§3.3 公示政策）：开工与否以是否存在服务段为准。
func previewCancelOrder(ctx context.Context, db *ent.Client, orderID, adminID int, isSuper bool, now time.Time) (*cancelQuote, error) {
	order, err := loadOwnedOrder(ctx, db, adminID, isSuper, orderID)
	if err != nil {
		return nil, err
	}
	policy, err := cancelPolicyFromConfig(ctx, db)
	if err != nil {
		return nil, err
	}
	quote := quoteCancelOrder(policy, order, now)
	if quote.Cancelable {
		started, err := orderServiceStarted(ctx, db, order.ID)
		if err != nil {
			return nil, err
		}
		if started {
			quote.Cancelable = false
			quote.BlockedReason = errCancelServiceStarted.Error()
		}
	}
	return &quote, nil
}

// orderServiceStarted 判断订单是否已有服务段（开工即存在服务段，无论是否已结束）。
func orderServiceStarted(ctx context.Context, db *ent.Client, orderID int) (bool, error) {
	return db.EsportsServiceSegment.Query().Where(
		esportsservicesegment.OrderIDEQ(orderID),
	).Exist(ctx)
}

// returnSoldQuota 取消已付款订单时把「已售」名额退回（§3.3）：占用记录转 returned，
// SKU 已售数量同步扣减，使取消后的商品容量可再次售卖。
func returnSoldQuota(ctx context.Context, tx *ent.Tx, orderID int) error {
	hold, err := tx.EsportsQuotaHold.Query().Where(esportsquotahold.OrderIDEQ(orderID)).Only(ctx)
	if err != nil {
		return err
	}
	if hold.Status != esportsquotahold.StatusSold {
		return errConflict
	}
	changed, err := tx.EsportsQuotaHold.Update().Where(
		esportsquotahold.IDEQ(hold.ID), esportsquotahold.StatusEQ(esportsquotahold.StatusSold),
	).SetStatus(esportsquotahold.StatusReturned).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errConflict
	}
	changed, err = tx.EsportsSku.Update().Where(
		esportssku.IDEQ(hold.SkuID), esportssku.QuotaSoldGTE(hold.Quantity),
	).AddQuotaSold(-hold.Quantity).AddVersion(1).Save(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return errConflict
	}
	return nil
}

// releaseSeatsOnOrderCancel 取消订单时把未终结的派单席位一并作废（§3.3）：
// 待回应邀请、已接单占位与空席位都不再有效，避免订单取消后仍占陪玩容量。
func releaseSeatsOnOrderCancel(ctx context.Context, tx *ent.Tx, orderID int) error {
	_, err := tx.EsportsDispatchSeat.Update().Where(
		esportsdispatchseat.OrderIDEQ(orderID),
		esportsdispatchseat.StatusIn(
			esportsdispatchseat.StatusOpen,
			esportsdispatchseat.StatusInvited,
			esportsdispatchseat.StatusAccepted,
		),
	).SetStatus(esportsdispatchseat.StatusRevoked).AddVersion(1).Save(ctx)
	return err
}
