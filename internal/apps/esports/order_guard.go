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
	"go-admin/internal/ent/esportsstoredvaluecard"
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
	// customerDebtRemindKey 客户欠款提醒阈值（分，§4.6-04）：欠款超过该值即提醒（默认 0 = 有欠款就提醒）。
	// 提醒与拦截分两档：默认只提醒不拦截，配了 customerDebtLimitKey 才升级为强制拦截。
	customerDebtRemindKey = "app.esports.customer_debt_remind_cents"
	// priceDeviationThresholdKey 订单价偏离 SKU 标准价的提醒阈值（基点 / 万分比，§4.6-02）：
	// 默认 3000（30%）；0 表示关闭判定。
	priceDeviationThresholdKey = "app.esports.price_deviation_threshold_bp"
	// priceDeviationReasonRequiredKey 超阈值时是否强制填写偏离原因（§4.6-02）：默认强制（true）。
	priceDeviationReasonRequiredKey = "app.esports.price_deviation_reason_required"
	// orderDiscountLimitKey 单笔会员折扣上限（基点，§4.2-06）：超过即拒绝下单；默认 0 不限制。
	orderDiscountLimitKey = "app.esports.order_discount_limit_bps"
	// defaultDuplicateOrderWindow 重复报单提醒内置默认时间窗（分钟）。
	defaultDuplicateOrderWindow = 10
	// defaultPriceDeviationThresholdBp 订单价偏离提醒内置默认阈值（基点 = 30%）。
	defaultPriceDeviationThresholdBp = 3000
)

var (
	// errDuplicateOrder 疑似重复报单：未确认时拒绝，handler 映射成明确中文提示（§4.6）。
	errDuplicateOrder = errors.New("疑似重复报单")
	// errCustomerDebt 客户欠款超限：拒绝新报单，handler 映射成明确中文提示（§4.6）。
	errCustomerDebt = errors.New("客户欠款超限，暂不可报单")
	// errAppointmentResource 预约时段资源复核未通过（§4.6 / §5.5）：同时包裹派单容量判定里的
	// errInvalidInput，便于 handler 与调用方分别识别「资源冲突」与「参数非法」。
	errAppointmentResource = errors.New("预约时段资源冲突")
	// errPriceDeviation 订单价明显偏离 SKU 标准价（§4.6-02）：未填偏离原因时拒绝下单。
	errPriceDeviation = errors.New("订单价明显偏离 SKU 标准价")
)

// orderGuardConfigInt64 读取整数型系统配置（§4.6）：配置缺失或非法时返回 fallback。
func orderGuardConfigInt64(ctx context.Context, db *ent.Client, key string, fallback int64) int64 {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(key)).Only(ctx)
	if err != nil {
		return fallback
	}
	value, convErr := strconv.ParseInt(strings.TrimSpace(item.Value), 10, 64)
	if convErr != nil {
		return fallback
	}
	return value
}

// orderGuardConfigBool 读取布尔型系统配置：仅显式 false / 0 / no 视为关闭，其余按 fallback 处理。
func orderGuardConfigBool(ctx context.Context, db *ent.Client, key string, fallback bool) bool {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(key)).Only(ctx)
	if err != nil {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(item.Value)) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	default:
		return fallback
	}
}

// customerDebtRemindCents 欠款提醒阈值（分，§4.6-04）：默认 0 表示只要有欠款就提醒。
func customerDebtRemindCents(ctx context.Context, db *ent.Client) int64 {
	value := orderGuardConfigInt64(ctx, db, customerDebtRemindKey, 0)
	if value < 0 {
		return 0
	}
	return value
}

// priceDeviationThresholdBp 读取订单价偏离提醒阈值（基点，§4.6-02）：非法回落默认 30%。
func priceDeviationThresholdBp(ctx context.Context, db *ent.Client) int {
	value := orderGuardConfigInt64(ctx, db, priceDeviationThresholdKey, defaultPriceDeviationThresholdBp)
	if value < 0 || value > 10000 {
		return defaultPriceDeviationThresholdBp
	}
	return int(value)
}

// priceDeviationReasonRequired 超阈值时是否强制填写偏离原因（§4.6-02）：默认强制。
func priceDeviationReasonRequired(ctx context.Context, db *ent.Client) bool {
	return orderGuardConfigBool(ctx, db, priceDeviationReasonRequiredKey, true)
}

// orderDiscountLimitBps 读取单笔会员折扣上限（基点，§4.2-06）：0 或非法表示不限制。
func orderDiscountLimitBps(ctx context.Context, db *ent.Client) int {
	value := orderGuardConfigInt64(ctx, db, orderDiscountLimitKey, 0)
	if value <= 0 || value > 10000 {
		return 0
	}
	return int(value)
}

// ensureOrderDiscountWithinLimit 单笔折扣上限复核（§4.2-06）：会员折扣基点超过配置上限即拒绝下单。
func ensureOrderDiscountWithinLimit(discountBps, limitBps int) error {
	if limitBps <= 0 || discountBps <= limitBps {
		return nil
	}
	return fmt.Errorf("%w：本次会员折扣 %d 基点已超过单笔折扣上限 %d 基点，请调整折扣或走人工议价",
		errInvalidInput, discountBps, limitBps)
}

// priceDeviationOf 计算订单价相对 SKU 标准价的偏离度（基点，绝对值）。标准价为 0 时返回 0。
// 用 diff*10000/standard 而不是 diff/(standard/10000)，避免标准价不足 10000 分时整除为 0。
func priceDeviationOf(standardCents, payableCents int64) int {
	if standardCents <= 0 {
		return 0
	}
	diff := payableCents - standardCents
	if diff < 0 {
		diff = -diff
	}
	return int(diff * 10000 / standardCents)
}

// priceDeviation 是一次「明显偏离标准价」的判定结果，用于提醒与留痕。
type priceDeviation struct {
	StandardCents int64  `json:"standard_cents"`
	PayableCents  int64  `json:"payable_cents"`
	DeviationBp   int    `json:"deviation_bp"`
	Reason        string `json:"reason,omitempty"`
}

// evaluatePriceDeviation 判定订单价是否明显偏离 SKU 标准价并决定是否强制填写原因（§4.6-02）：
// 未超阈值返回 nil（无需提醒）；超阈值且开启强制时未填原因（4–255 字）即拒绝；
// 超阈值但关闭强制时只返回判定结果，交由调用方落提醒留痕。
//
// P1-13：入参字段名固定为 price_deviation_reason（orderInput.PriceDeviationReason），
// 缺原因时的错误必须点名该字段与当前阈值，前端与客服才知道要补什么、补了是否够；
// 超长原因同样点名该字段，不再退化为无上下文的「请求参数无效」。
func evaluatePriceDeviation(ctx context.Context, db *ent.Client, standardCents, payableCents int64,
	reason string) (*priceDeviation, error) {
	threshold := priceDeviationThresholdBp(ctx, db)
	if threshold <= 0 {
		return nil, nil
	}
	deviationBp := priceDeviationOf(standardCents, payableCents)
	if deviationBp <= threshold {
		return nil, nil
	}
	reason = strings.TrimSpace(reason)
	if len([]rune(reason)) > 255 {
		return nil, fmt.Errorf(
			"%w：price_deviation_reason 最长 255 字，当前 %d 字，请精简后重新提交",
			errInvalidInput, len([]rune(reason)))
	}
	if priceDeviationReasonRequired(ctx, db) && len([]rune(reason)) < 4 {
		return nil, fmt.Errorf(
			"%w：订单价较 SKU 标准价偏离 %d 基点（标准 %d 分，应付 %d 分），超过当前阈值 %d 基点"+
				"（配置项 %s）；请在报单入参中填写 price_deviation_reason（4–255 字）说明偏离原因",
			errPriceDeviation, deviationBp, standardCents, payableCents, threshold, priceDeviationThresholdKey)
	}
	return &priceDeviation{
		StandardCents: standardCents, PayableCents: payableCents,
		DeviationBp: deviationBp, Reason: reason,
	}, nil
}

// customerAvailableFundsCents 客户可用于站内收款方式的资金（分，§4.2-06）：
// 余额可用额（余额一律由流水求和派生）与启用中储值卡的本金余额合计。
func customerAvailableFundsCents(ctx context.Context, db *ent.Client, customerID int) (int64, int64, error) {
	if customerID < 1 {
		return 0, 0, errInvalidInput
	}
	balance, err := walletBalanceOf(ctx, db, walletCustomerBalance, customerID)
	if err != nil {
		return 0, 0, err
	}
	cards, err := db.EsportsStoredValueCard.Query().Where(
		esportsstoredvaluecard.CustomerIDEQ(customerID),
		esportsstoredvaluecard.StatusEQ(esportsstoredvaluecard.StatusActive),
	).All(ctx)
	if err != nil {
		return 0, 0, err
	}
	var principal int64
	for _, card := range cards {
		cardBalance, err := storedValueCardBalance(ctx, db, card.ID)
		if err != nil {
			return 0, 0, err
		}
		principal += cardBalance.PrincipalCents
	}
	return balance.PrincipalCents, principal, nil
}

// ensureOrderFundsAvailable 下单时的资金可用余额复核（§4.2-06）：显式选择站内收款方式
// （余额 / 储值卡）时，可用本金不足即拒绝下单，避免「下单成功、收款时才失败」；
// 未指定站内方式（走外部收款）时不做资金复核。
func ensureOrderFundsAvailable(ctx context.Context, db *ent.Client, customerID int,
	payableCents int64, channel string) error {
	channel = strings.TrimSpace(channel)
	// 未指定站内收款方式（走线下 / 在线外部收款）→ 下单不锁资金。
	if channel == "" || channel == "external" {
		return nil
	}
	balanceCents, storedValueCents, err := customerAvailableFundsCents(ctx, db, customerID)
	if err != nil {
		return err
	}
	switch channel {
	case walletCustomerBalance:
		if balanceCents < payableCents {
			return fmt.Errorf("%w：客户余额不足（可用 %d 分，应付 %d 分），请改为外部收款",
				errInsufficientBalance, balanceCents, payableCents)
		}
	case "stored_value_card":
		if storedValueCents < payableCents {
			return fmt.Errorf("%w：客户储值卡可用本金不足（可用 %d 分，应付 %d 分），请改为外部收款",
				errInsufficientBalance, storedValueCents, payableCents)
		}
	default:
		return fmt.Errorf("%w：不支持的下单收款方式 %s", errInvalidInput, channel)
	}
	return nil
}

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

// checkDuplicateOrder 重复报单提醒（§4.6）：同客户在时间窗内已有相似报单时拒绝，
// 由调用方带 confirm_duplicate=true 二次提交放行。
//
// 相似口径（round4 §4.6-01 修正）分两支：
//  1. **预约单**（本次与历史单都带预约开始时间）：同游戏分区 + 预约服务时段重叠即视为相似，
//     不再要求同 SKU / 同份数 / 同指定陪玩 —— 同一客户在同一时段重复约同一游戏的局就是重复报单；
//     游戏分区取 SKU 所属分类的 game，任一侧取不到时回落「同 SKU」判定。
//  2. **非预约单**：沿用严格的「完全相同报单」口径（同 SKU + 同份数 + 同指定陪玩），
//     避免把同客户正常的连续下单误判为重复。
//
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
		esportsorder.FulfillmentStatusNEQ(esportsorder.FulfillmentStatusCancelled),
	)
	if in.RequestedStartAt == nil {
		// 非预约单：先按严格口径收敛候选，避免把同客户其他报单都拉出来。
		query = query.Where(esportsorder.SkuIDEQ(in.SkuID), esportsorder.QuantityEQ(in.Quantity))
		if in.PreferredPlayerID > 0 {
			query = query.Where(esportsorder.PreferredPlayerIDEQ(in.PreferredPlayerID))
		} else {
			query = query.Where(esportsorder.PreferredPlayerIDIsNil())
		}
	}
	// 按 ID 倒序取最近若干条（同客户同游戏 / 同 SKU 的量级由下单频次决定），再在 Go 侧按时间窗过滤。
	items, err := query.Order(ent.Desc(esportsorder.FieldID)).Limit(20).All(ctx)
	if err != nil {
		return err
	}
	targetGame := orderGamePartition(ctx, db, in.SkuID)
	targetDuration := orderServiceDuration(ctx, db, in.SkuID)
	start := now.UTC().Add(-time.Duration(windowMinutes) * time.Minute)
	refs := make([]string, 0, 5)
	for _, item := range items {
		created := item.CreatedAt.UTC()
		if created.Before(start) || created.After(now.UTC()) {
			continue
		}
		if in.RequestedStartAt == nil {
			refs = append(refs, item.OrderNo)
		} else if similarAppointmentOrder(ctx, db, in, targetGame, targetDuration, item) {
			refs = append(refs, item.OrderNo)
		}
		if len(refs) >= 5 {
			break
		}
	}
	if len(refs) == 0 {
		return nil
	}
	return fmt.Errorf("%w：客户在最近 %d 分钟内已提交过相似报单（订单号 %s），如确需重复下单请带 confirm_duplicate=true 重新提交",
		errDuplicateOrder, windowMinutes, strings.Join(refs, "、"))
}

// orderGamePartition 取 SKU 所属分类的游戏分区（§4.6-01）；分类缺失或游戏为空时返回空串。
func orderGamePartition(ctx context.Context, db *ent.Client, skuID int) string {
	sku, err := db.EsportsSku.Get(ctx, skuID)
	if err != nil || sku.CategoryID == nil {
		return ""
	}
	category, err := db.EsportsProductCategory.Get(ctx, *sku.CategoryID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(category.Game)
}

// orderServiceDuration 取 SKU 的标准服务时长（分钟）；读不到时返回 0。
func orderServiceDuration(ctx context.Context, db *ent.Client, skuID int) int {
	sku, err := db.EsportsSku.Get(ctx, skuID)
	if err != nil {
		return 0
	}
	return sku.ServiceDurationMinutes
}

// similarAppointmentOrder 判定一张历史预约单是否与本次报单「相似」（§4.6-01）：
// 同游戏分区（任一侧无游戏分区时回落同 SKU）+ 预约服务时段重叠。
func similarAppointmentOrder(ctx context.Context, db *ent.Client, in orderInput, targetGame string,
	targetDuration int, item *ent.EsportsOrder) bool {
	if item.RequestedStartAt == nil {
		return false
	}
	candidateGame := orderGamePartition(ctx, db, item.SkuID)
	switch {
	case targetGame != "" && candidateGame != "":
		if targetGame != candidateGame {
			return false
		}
	default:
		if item.SkuID != in.SkuID {
			return false
		}
	}
	startA := in.RequestedStartAt.UTC()
	startB := item.RequestedStartAt.UTC()
	return serviceWindowsOverlap(startA, targetDuration, startB, item.ServiceDurationMinutes)
}

// serviceWindowsOverlap 判定两个服务时段是否重叠：任一时长为 0 时按「同一个开始时刻」处理
// （时长未配置的 SKU 无法做区间比较，退化为同时刻判定），避免把不同时段误判为相似单。
func serviceWindowsOverlap(startA time.Time, durationA int, startB time.Time, durationB int) bool {
	if durationA <= 0 || durationB <= 0 {
		return startA.Equal(startB)
	}
	endA := startA.Add(time.Duration(durationA) * time.Minute)
	endB := startB.Add(time.Duration(durationB) * time.Minute)
	return startA.Before(endB) && startB.Before(endA)
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

// customerDebtTier 客户欠款两档判定（§4.6-04）：默认「有欠款即提醒」，配置阈值后升级为强制拦截。
// 返回欠款合计与是否需要提醒；欠款超过拦截阈值时返回 errCustomerDebt。
// 默认（limitCents <= 0）不拦截，但欠款超过 remindCents（默认 0）时提醒。
func customerDebtTier(ctx context.Context, db *ent.Client, customerID int,
	limitCents, remindCents int64, now time.Time) (int64, bool, error) {
	debt, err := customerOutstandingDebtCents(ctx, db, customerID, now)
	if err != nil {
		return 0, false, err
	}
	if limitCents > 0 && debt > limitCents {
		return debt, false, fmt.Errorf("%w：客户未结清应付款合计 %d 分，已超过阈值 %d 分，请先结清后再报单",
			errCustomerDebt, debt, limitCents)
	}
	if remindCents < 0 {
		remindCents = 0
	}
	return debt, debt > remindCents, nil
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
