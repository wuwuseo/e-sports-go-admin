package esports

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/systemconfig"
)

// 客户风控（§13.7）：
//  1. 高频退款客户自动标记为「疑似滥用复核」（watch）——只标记复核，**不自动处罚**，受理与申诉入口照常；
//  2. 「限制自助下单」（restricted）只能由管理员人工设置，自助下单入口按该标记拦截；
//  3. 赔付一律走系统流程（售后 / 追偿），系统不提供私下转账入口。

const (
	riskWindowDaysKey      = "app.esports.risk_window_days"
	riskRefundCountKey     = "app.esports.risk_refund_count"
	riskRefundCentsKey     = "app.esports.risk_refund_cents"
	defaultRiskWindowDays  = 30
	defaultRiskRefundCount = 3
	defaultRiskRefundCents = int64(50000)
)

// customerRiskPolicy 高频退款自动标记策略（§13.7）。
type customerRiskPolicy struct {
	WindowDays       int
	RefundCountLimit int
	RefundCentsLimit int64
}

// customerRiskPolicyOf 读取风控阈值配置；缺失或非法回落默认值（30 天 / 3 次 / 500 元）。
func customerRiskPolicyOf(ctx context.Context, db *ent.Client) customerRiskPolicy {
	items, err := db.SystemConfig.Query().Where(systemconfig.KeyIn(
		riskWindowDaysKey, riskRefundCountKey, riskRefundCentsKey,
	)).Select(systemconfig.FieldKey, systemconfig.FieldValue).All(ctx)
	if err != nil {
		return customerRiskPolicy{}
	}
	values := map[string]string{}
	for _, item := range items {
		values[item.Key] = strings.TrimSpace(item.Value)
	}
	parse := func(key string, fallback int) int {
		raw, ok := values[key]
		if !ok {
			return fallback
		}
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			return fallback
		}
		return value
	}
	return customerRiskPolicy{
		WindowDays:       parse(riskWindowDaysKey, defaultRiskWindowDays),
		RefundCountLimit: parse(riskRefundCountKey, defaultRiskRefundCount),
		RefundCentsLimit: int64(parse(riskRefundCentsKey, int(defaultRiskRefundCents))),
	}
}

// ensureCustomerSelfServiceAllowed 自助入口的拦截（§13.7）：
// ① 客户未开启「自助下单」开关时拦截（SelfOrderEnabled=false，缺省关闭）；
// ② 「限制自助下单」时拦截，且只要求联系客服或先预付——「疑似滥用复核」不拦截、不自动处罚。
func ensureCustomerSelfServiceAllowed(customer *ent.EsportsCustomer) error {
	if customer == nil {
		return errForbidden
	}
	if !customer.SelfOrderEnabled {
		return fmt.Errorf("%w：该客户未开放自助下单，请联系客服报单", errForbidden)
	}
	if customer.RiskStatus == esportscustomer.RiskStatusRestricted {
		reason := strings.TrimSpace(customer.RiskReason)
		if reason == "" {
			reason = "存在退款争议"
		}
		return fmt.Errorf("%w：该客户已被限制自助下单（%s），请联系客服或先预付", errForbidden, reason)
	}
	return nil
}

// markCustomerRisk 管理员标记 / 解除客户风险（§13.7）。status=normal 时清空原因。
func markCustomerRisk(ctx context.Context, db *ent.Client, customerID int, status, reason string,
	now time.Time) (*ent.EsportsCustomer, error) {
	status = strings.TrimSpace(status)
	reason = strings.TrimSpace(reason)
	if customerID < 1 || len([]rune(reason)) > 255 {
		return nil, errInvalidInput
	}
	switch status {
	case "normal":
		reason = ""
	case "watch", "restricted":
		if reason == "" {
			return nil, fmt.Errorf("%w：标记风险须填写原因", errInvalidInput)
		}
	default:
		return nil, errInvalidInput
	}
	update := db.EsportsCustomer.Update().Where(esportscustomer.IDEQ(customerID)).
		SetRiskStatus(esportscustomer.RiskStatus(status)).SetRiskReason(reason).AddVersion(1)
	if status == "normal" {
		update = update.ClearRiskMarkedAt()
	} else {
		update = update.SetRiskMarkedAt(now.UTC())
	}
	if _, err := update.Save(ctx); err != nil {
		return nil, err
	}
	return db.EsportsCustomer.Get(ctx, customerID)
}

// listRiskCustomers 列出被标记风险的客户（watch / restricted），按 ID 倒序。
func listRiskCustomers(ctx context.Context, db *ent.Client, limit int) ([]*ent.EsportsCustomer, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	return db.EsportsCustomer.Query().Where(
		esportscustomer.RiskStatusIn(
			esportscustomer.RiskStatusWatch, esportscustomer.RiskStatusRestricted),
	).Order(ent.Desc(esportscustomer.FieldID)).Limit(limit).All(ctx)
}

// flagHighRiskCustomers 按风控阈值把高频退款客户标记为「疑似滥用复核」（watch），返回新标记数量。
// 口径：窗口内「已完成且退款金额 > 0」的售后单，按客户归集次数与退款总额；两者任一达阈值即标记。
// 已经是 restricted 的客户不会被降级；已经是 watch 的客户不重复计入。只标记复核，不自动处罚（§13.7）。
func flagHighRiskCustomers(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	policy := customerRiskPolicyOf(ctx, db)
	if policy.RefundCountLimit <= 0 && policy.RefundCentsLimit <= 0 {
		return 0, nil
	}
	start := now.UTC().AddDate(0, 0, -policy.WindowDays)
	items, err := db.EsportsAfterSale.Query().Where(
		esportsaftersale.StatusEQ(esportsaftersale.StatusCompleted),
		esportsaftersale.RefundCentsGT(0),
	).Select(esportsaftersale.FieldCustomerID, esportsaftersale.FieldRefundCents,
		esportsaftersale.FieldCreatedAt).All(ctx)
	if err != nil {
		return 0, err
	}
	counts := map[int]int{}
	amounts := map[int]int64{}
	for _, item := range items {
		if item.CustomerID == nil || item.CreatedAt.Before(start) {
			continue
		}
		counts[*item.CustomerID]++
		amounts[*item.CustomerID] += item.RefundCents
	}
	flagged := 0
	for customerID, count := range counts {
		if flagged >= limit {
			break
		}
		overCount := policy.RefundCountLimit > 0 && count >= policy.RefundCountLimit
		overAmount := policy.RefundCentsLimit > 0 && amounts[customerID] >= policy.RefundCentsLimit
		if !overCount && !overAmount {
			continue
		}
		customer, err := db.EsportsCustomer.Get(ctx, customerID)
		if err != nil {
			if ent.IsNotFound(err) {
				continue
			}
			return flagged, err
		}
		if customer.RiskStatus != esportscustomer.RiskStatusNormal {
			continue
		}
		reason := fmt.Sprintf("近 %d 天内退款 %d 次、合计 %d 分，超过风控阈值（自动标记复核）",
			policy.WindowDays, count, amounts[customerID])
		if _, err = markCustomerRisk(ctx, db, customerID, "watch", reason, now); err != nil {
			return flagged, err
		}
		if _, err = db.EsportsOutbox.Create().
			SetEventKey(fmt.Sprintf("customer.risk_flagged:%d:%s", customerID, now.UTC().Format("2006-01-02"))).
			SetEventType("customer.risk_flagged").SetAggregateRef(strconv.Itoa(customerID)).
			SetPayload(map[string]interface{}{
				"customer_id": customerID, "refund_count": count,
				"refund_cents": amounts[customerID], "window_days": policy.WindowDays,
			}).Save(ctx); err != nil && !ent.IsConstraintError(err) {
			return flagged, err
		}
		flagged++
	}
	return flagged, nil
}

// customerOrderInput 客户自助下单入参（§13.2 客户自助端）。
type customerOrderInput struct {
	SkuID             int    `json:"sku_id"`
	Quantity          int64  `json:"quantity"`
	PreferredPlayerID int    `json:"preferred_player_id"`
	IdempotencyKey    string `json:"idempotency_key"`
}

// placeCustomerSelfOrder 客户自助下单（§13.2 / §13.7）：须已绑定登录身份与成年资格，
// 且未被「限制自助下单」；归属客服作为订单负责客服，幂等键按客户身份隔离。
func placeCustomerSelfOrder(ctx context.Context, db *ent.Client, customer *ent.EsportsCustomer,
	in customerOrderInput, now time.Time) (*ent.EsportsOrder, error) {
	if customer == nil || customer.ID < 1 {
		return nil, errForbidden
	}
	if err := ensureCustomerSelfServiceAllowed(customer); err != nil {
		return nil, err
	}
	if customer.AgeStatus != esportscustomer.AgeStatusAdult {
		return nil, errAgeUnverified
	}
	if customer.OwnerAdminID == nil || *customer.OwnerAdminID < 1 {
		return nil, fmt.Errorf("%w：该客户尚未分配归属客服，请联系客服下单", errForbidden)
	}
	return placeOrderWithPrincipal(ctx, db, fmt.Sprintf("customer:%d", customer.ID),
		*customer.OwnerAdminID, orderInput{
			CustomerID:        customer.ID,
			SkuID:             in.SkuID,
			Quantity:          in.Quantity,
			PreferredPlayerID: in.PreferredPlayerID,
		}, in.IdempotencyKey, now)
}
