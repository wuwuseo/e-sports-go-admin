package esports

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/systemconfig"
)

// §4.6 报单校验补充（P0）：重复报单提醒、客户欠款拦截、预约冲突与资源复核。
//
// 三项校验都插在下单主流程 placeOrderOnce 的同一事务内，任一未通过即整单回滚
// （不写订单、不占名额、不写幂等记录），既有幂等键、名额占用与报价快照语义不变：
//
//  1. 重复报单提醒（checkDuplicateOrder）：同客户在可配置时间窗内已有「未取消」的**相同报单**
//     （同 SKU + 同数量 + 同指定陪玩）时提醒；未确认即以 errDuplicateOrder 拒绝并回带相似订单号，
//     订单体带 confirm_duplicate=true 时放行。
//  2. 客户欠款拦截（ensureCustomerDebtAllowed）：客户未结清应付款累计超过阈值即拒绝新报单。
//  3. 预约冲突与资源复核（checkAppointmentCapacity）：报单带预约开始时间且指定陪玩时，
//     复用派单同一套并发 / 每日容量口径复核该陪玩能否在该时段占位。
//
// 配置读取沿用同包既有写法（系统配置表 SystemConfig，键前缀 app.esports.，缺省回落内置默认值）。

const (
	// duplicateOrderWindowKey 重复报单提醒时间窗（分钟）：0 表示关闭提醒。
	duplicateOrderWindowKey = "app.esports.duplicate_order_window_minutes"
	// customerDebtLimitKey 客户欠款拦截阈值（分）：<=0（默认 0）表示关闭拦截。
	customerDebtLimitKey = "app.esports.customer_debt_limit_cents"
	// defaultDuplicateOrderWindow 重复报单提醒内置默认时间窗（分钟）。
	defaultDuplicateOrderWindow = 10
)

var (
	// errDuplicateOrder 疑似重复报单：未确认时拒绝，handler 映射成明确中文提示（§4.6）。
	errDuplicateOrder = errors.New("疑似重复报单")
	// errCustomerDebt 客户欠款超限：拒绝新报单，handler 映射成明确中文提示（§4.6）。
	errCustomerDebt = errors.New("客户欠款超限，暂不可报单")
	// errAppointmentResource 预约时段资源复核未通过（§4.6 / §5.5）：同时包裹派单容量判定里的
	// errInvalidInput，便于 handler 与调用方分别识别「资源冲突」与「参数非法」。
	errAppointmentResource = errors.New("预约时段资源冲突")
)

// duplicateOrderWindowMinutes 读取重复报单提醒时间窗（分钟，§4.6）。
// 配置缺失、非法或为负一律回落内置默认 10 分钟；0 表示关闭提醒。
func duplicateOrderWindowMinutes(ctx context.Context, db *ent.Client) int {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(duplicateOrderWindowKey)).Only(ctx)
	if err != nil {
		return defaultDuplicateOrderWindow
	}
	minutes, convErr := strconv.Atoi(strings.TrimSpace(item.Value))
	if convErr != nil || minutes < 0 || minutes > 24*60 {
		return defaultDuplicateOrderWindow
	}
	return minutes
}

// customerDebtLimitCents 读取客户欠款拦截阈值（分，§4.6）。
// 配置缺失、非法或 <=0 一律按 0 处理 —— 默认关闭拦截，避免误挡正常报单。
func customerDebtLimitCents(ctx context.Context, db *ent.Client) int64 {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(customerDebtLimitKey)).Only(ctx)
	if err != nil {
		return 0
	}
	limit, convErr := strconv.ParseInt(strings.TrimSpace(item.Value), 10, 64)
	if convErr != nil || limit < 0 {
		return 0
	}
	return limit
}

// checkDuplicateOrder 重复报单提醒（§4.6）：同客户在时间窗内已有未取消的相同报单时拒绝，
// 由调用方带 confirm_duplicate=true 二次提交放行。
//
// 相似口径（本实现明确为「完全相同的报单」，避免把同客户正常的连续下单误判为重复）：
// 同客户 + 同 SKU + 同数量 + 同指定陪玩（两者都未指定视为相同），且履约状态不为 cancelled。
// 时间窗取 [now-window, now]：只看报单时刻之前已存在的订单，避免传入的 now 与库内时钟不一致时
// 把「未来单」算成相似单。
//
// 时间窗在 Go 侧比较 created_at 而不是写进 SQL 条件：created_at 由 Go 默认值（本地时区）写入，
// 与 UTC 时间字面量的字符串比较在不同驱动下不可靠（同 dispatchPriorityWindowOpen 的既有做法）。
func checkDuplicateOrder(ctx context.Context, db *ent.Client, in orderInput, now time.Time, windowMinutes int) error {
	if windowMinutes <= 0 || in.ConfirmDuplicate {
		return nil
	}
	query := db.EsportsOrder.Query().Where(
		esportsorder.CustomerIDEQ(in.CustomerID),
		esportsorder.SkuIDEQ(in.SkuID),
		esportsorder.QuantityEQ(in.Quantity),
		esportsorder.FulfillmentStatusNEQ(esportsorder.FulfillmentStatusCancelled),
	)
	if in.PreferredPlayerID > 0 {
		query = query.Where(esportsorder.PreferredPlayerIDEQ(in.PreferredPlayerID))
	} else {
		query = query.Where(esportsorder.PreferredPlayerIDIsNil())
	}
	// 按 ID 倒序取最近若干条（同客户同 SKU 的量级由下单频次决定），再在 Go 侧按时间窗过滤。
	items, err := query.Order(ent.Desc(esportsorder.FieldID)).Limit(20).All(ctx)
	if err != nil {
		return err
	}
	start := now.UTC().Add(-time.Duration(windowMinutes) * time.Minute)
	refs := make([]string, 0, 5)
	for _, item := range items {
		created := item.CreatedAt.UTC()
		if created.Before(start) || created.After(now.UTC()) {
			continue
		}
		refs = append(refs, item.OrderNo)
		if len(refs) >= 5 {
			break
		}
	}
	if len(refs) == 0 {
		return nil
	}
	return fmt.Errorf("%w：客户在最近 %d 分钟内已提交过相同报单（订单号 %s），如确需重复下单请带 confirm_duplicate=true 重新提交",
		errDuplicateOrder, windowMinutes, strings.Join(refs, "、"))
}

// customerOutstandingDebtCents 计算客户「未结清应付款」累计（分，§4.6）。口径：
//
//  1. 逾期未付款：payment_status=unpaid 且 payment_deadline 已过、履约未取消 → 按报价快照 payable_cents 全额计。
//     仍在支付时限内的 unpaid 不计入 —— 那属于正常在途报单，计入会误挡同客户连续下单。
//  2. 部分收款 / 支付异常：payment_status ∈ {partial, exception}（履约未取消）→ 订单未记录已收金额，
//     保守按 payable_cents 全额计。
//
// 已退款金额不冲减欠款（退款走售后单独核算，且退款单与欠款成因不同），订单被取消后自然不再计入。
func customerOutstandingDebtCents(ctx context.Context, db *ent.Client, customerID int, now time.Time) (int64, error) {
	items, err := db.EsportsOrder.Query().Where(
		esportsorder.CustomerIDEQ(customerID),
		esportsorder.FulfillmentStatusNEQ(esportsorder.FulfillmentStatusCancelled),
		esportsorder.Or(
			esportsorder.PaymentStatusIn(esportsorder.PaymentStatusPartial, esportsorder.PaymentStatusException),
			esportsorder.And(
				esportsorder.PaymentStatusEQ(esportsorder.PaymentStatusUnpaid),
				esportsorder.PaymentDeadlineLT(now.UTC()),
			),
		),
	).Select(esportsorder.FieldPayableCents).All(ctx)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, item := range items {
		total += item.PayableCents
	}
	return total, nil
}

// ensureCustomerDebtAllowed 客户欠款超过阈值时拒绝新报单（§4.6）。
// limitCents <= 0 表示关闭拦截（默认），此时不查库。
func ensureCustomerDebtAllowed(ctx context.Context, db *ent.Client, customerID int, limitCents int64, now time.Time) error {
	if limitCents <= 0 {
		return nil
	}
	debt, err := customerOutstandingDebtCents(ctx, db, customerID, now)
	if err != nil {
		return err
	}
	if debt > limitCents {
		return fmt.Errorf("%w：客户未结清应付款合计 %d 分，已超过阈值 %d 分，请先结清后再报单",
			errCustomerDebt, debt, limitCents)
	}
	return nil
}

// validateAppointmentStart 校验报单携带的预约开始时间：必须晚于当前时刻（§3.2 / §5.5），
// 与改期 rescheduleOrder 的口径一致。
func validateAppointmentStart(in orderInput, now time.Time) error {
	if in.RequestedStartAt == nil {
		return nil
	}
	if !in.RequestedStartAt.After(now.UTC()) {
		return fmt.Errorf("%w：预约开始时间须晚于当前时间", errInvalidInput)
	}
	return nil
}

// checkAppointmentCapacity 预约冲突与资源复核（§4.6 / §3.2 / §5.5）：报单带预约开始时间且指定了陪玩时，
// 按派单同一套口径（checkPlayerDispatchLimit 的时段重叠 / 服务开始自然日归集）复核该陪玩在该时段
// 是否仍可占位 —— 与 PUT /admin/orders/{id}/appointment（改期提案）及 POST .../appointment/confirm
// （档期确认）保持同一判定，不另起一套口径。
//
// 待建订单尚未落库，故用内存快照提供预约开始时间与服务时长（供时段重叠判定）；与档期确认一致，
// 这里只做校验不做占位（真正占位发生在派单/接单时），因此并发下仍可能被后来的订单抢走，属已知口径。
func checkAppointmentCapacity(ctx context.Context, tx *ent.Tx, player *ent.EsportsPlayer,
	sku *ent.EsportsSku, startAt *time.Time, now time.Time) error {
	if player == nil || sku == nil || startAt == nil {
		return nil
	}
	start := startAt.UTC()
	target := &ent.EsportsOrder{
		SkuID:                  sku.ID,
		RequestedStartAt:       &start,
		ServiceDurationMinutes: sku.ServiceDurationMinutes,
		RequiredSeats:          sku.RequiredSeats,
	}
	if err := checkPlayerDispatchLimit(ctx, tx, player, target, now, 0); err != nil {
		return fmt.Errorf("%w：%w（请改约其他时段或联系客服人工复核）", errAppointmentResource, err)
	}
	return nil
}
