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

// 客户风控：
//  1. 高频退款客户按两档阈值自动标记：轻档落「疑似滥用复核」（watch，只标记复核、不限制下单）；
//     重档落「限制自助下单」（restricted，自助下单入口直接拦截并要求联系客服或预付）；
//  2. restricted 一旦落下即真实生效（自助下单 403），并保留申诉与人工复核入口：
//     客服可经既有 `/admin/customers/:id/risk` 改回 watch / normal，自动任务不降级、不重复标记；
//  3. 限制档阈值显式配为 0 即关闭自动限制（回落纯 watch 模式），避免误伤；
//  4. 赔付一律走系统流程（售后 / 追偿），系统不提供私下转账入口。

const (
	riskWindowDaysKey          = "app.esports.risk_window_days"
	riskRefundCountKey         = "app.esports.risk_refund_count"
	riskRefundCentsKey         = "app.esports.risk_refund_cents"
	riskRestrictCountKey       = "app.esports.risk_restrict_refund_count"
	riskRestrictCentsKey       = "app.esports.risk_restrict_refund_cents"
	defaultRiskWindowDays      = 30
	defaultRiskRefundCount     = 3
	defaultRiskRefundCents     = int64(50000)
	defaultRiskRestrictCount   = 6
	defaultRiskRestrictCents   = int64(200000)
	riskStatusWatch            = "watch"
	riskStatusRestricted       = "restricted"
	riskStatusNormal           = "normal"
	riskRestrictedReasonSuffix = "（自动限制自助下单，可联系客服申诉并由管理员复核解除）"
)

// customerRiskPolicy 高频退款自动标记策略：
// 复核档（watch）与限制档（restricted）各自独立配置次数与金额阈值。
type customerRiskPolicy struct {
	WindowDays         int
	RefundCountLimit   int
	RefundCentsLimit   int64
	RestrictCountLimit int
	RestrictCentsLimit int64
}

// customerRiskPolicyOf 读取风控阈值配置；缺失或非法回落默认值
// （30 天 / 3 次 500 元落 watch / 6 次 2000 元落 restricted）。
func customerRiskPolicyOf(ctx context.Context, db *ent.Client) customerRiskPolicy {
	items, err := db.SystemConfig.Query().Where(systemconfig.KeyIn(
		riskWindowDaysKey, riskRefundCountKey, riskRefundCentsKey,
		riskRestrictCountKey, riskRestrictCentsKey,
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
		WindowDays:         parse(riskWindowDaysKey, defaultRiskWindowDays),
		RefundCountLimit:   parse(riskRefundCountKey, defaultRiskRefundCount),
		RefundCentsLimit:   int64(parse(riskRefundCentsKey, int(defaultRiskRefundCents))),
		RestrictCountLimit: parse(riskRestrictCountKey, defaultRiskRestrictCount),
		RestrictCentsLimit: int64(parse(riskRestrictCentsKey, int(defaultRiskRestrictCents))),
	}
}

// riskTier 自动风控落点。
type riskTier int

const (
	// riskTierNone 未命中任何自动风控阈值。
	riskTierNone riskTier = iota
	// riskTierWatch 命中复核档：只标记 watch（人工复核），不限制自助下单。
	riskTierWatch
	// riskTierRestricted 命中限制档：自动置 restricted，自助下单入口拦截。
	riskTierRestricted
)

// classifyRiskTier 判定归集结果命中的自动风控档位：先看更高阈值的限制档，再看复核档。
// 限制档两键同时 ≤0 表示未配置自动限制（回落纯 watch 模式），避免无配置就自动处罚客户。
func (p customerRiskPolicy) classifyRiskTier(count int, cents int64) riskTier {
	if (p.RestrictCountLimit > 0 && count >= p.RestrictCountLimit) ||
		(p.RestrictCentsLimit > 0 && cents >= p.RestrictCentsLimit) {
		return riskTierRestricted
	}
	if (p.RefundCountLimit > 0 && count >= p.RefundCountLimit) ||
		(p.RefundCentsLimit > 0 && cents >= p.RefundCentsLimit) {
		return riskTierWatch
	}
	return riskTierNone
}

// riskStatusRank 风险档位序：normal < watch < restricted。
// 自动标记只在「目标档位高于当前档位」时写入，既保证 watch 可升级为 restricted，
// 又保证不把人工设置的 restricted 降级、不重复标记。
func riskStatusRank(status esportscustomer.RiskStatus) int {
	switch status {
	case esportscustomer.RiskStatusRestricted:
		return int(riskTierRestricted)
	case esportscustomer.RiskStatusWatch:
		return int(riskTierWatch)
	}
	return int(riskTierNone)
}

// ensureCustomerSelfServiceAllowed 自助入口的拦截：
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

// markCustomerRisk 管理员标记 / 解除客户风险。status=normal 时清空原因。
func markCustomerRisk(ctx context.Context, db *ent.Client, customerID int, status, reason string,
	now time.Time) (*ent.EsportsCustomer, error) {
	status = strings.TrimSpace(status)
	reason = strings.TrimSpace(reason)
	if customerID < 1 || len([]rune(reason)) > 255 {
		return nil, errInvalidInput
	}
	switch status {
	case riskStatusNormal:
		reason = ""
	case riskStatusWatch, riskStatusRestricted:
		if reason == "" {
			return nil, fmt.Errorf("%w：标记风险须填写原因", errInvalidInput)
		}
	default:
		return nil, errInvalidInput
	}
	update := db.EsportsCustomer.Update().Where(esportscustomer.IDEQ(customerID)).
		SetRiskStatus(esportscustomer.RiskStatus(status)).SetRiskReason(reason).AddVersion(1)
	if status == riskStatusNormal {
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

// flagHighRiskCustomers 按风控两档阈值把高频退款客户自动标记，返回新标记数量。
// 口径：窗口内「已完成且退款金额 > 0」的售后单，按客户归集次数与退款总额；命中限制档 → restricted，
// 只命中复核档 → watch。自动路径只在档位升高时写入（normal → watch → restricted 可升不可降），
// 已是 restricted 的客户不会被改回 watch；命中限制档的客户自助下单入口即被拦截（403）。
func flagHighRiskCustomers(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	policy := customerRiskPolicyOf(ctx, db)
	if policy.RefundCountLimit <= 0 && policy.RefundCentsLimit <= 0 &&
		policy.RestrictCountLimit <= 0 && policy.RestrictCentsLimit <= 0 {
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
		tier := policy.classifyRiskTier(count, amounts[customerID])
		if tier == riskTierNone {
			continue
		}
		customer, err := db.EsportsCustomer.Get(ctx, customerID)
		if err != nil {
			if ent.IsNotFound(err) {
				continue
			}
			return flagged, err
		}
		if riskStatusRank(customer.RiskStatus) >= int(tier) {
			continue
		}
		status := riskStatusWatch
		reason := fmt.Sprintf("近 %d 天内退款 %d 次、合计 %d 分，超过风控阈值（自动标记复核）",
			policy.WindowDays, count, amounts[customerID])
		if tier == riskTierRestricted {
			status = riskStatusRestricted
			reason = fmt.Sprintf("近 %d 天内退款 %d 次、合计 %d 分，超过风控限制阈值%s",
				policy.WindowDays, count, amounts[customerID], riskRestrictedReasonSuffix)
		}
		if _, err = markCustomerRisk(ctx, db, customerID, status, reason, now); err != nil {
			return flagged, err
		}
		if _, err = db.EsportsOutbox.Create().
			SetEventKey(fmt.Sprintf("customer.risk_flagged:%d:%s", customerID, now.UTC().Format("2006-01-02"))).
			SetEventType("customer.risk_flagged").SetAggregateRef(strconv.Itoa(customerID)).
			SetPayload(map[string]interface{}{
				"customer_id": customerID, "refund_count": count,
				"refund_cents": amounts[customerID], "window_days": policy.WindowDays,
				"risk_status": status,
			}).Save(ctx); err != nil && !ent.IsConstraintError(err) {
			return flagged, err
		}
		flagged++
	}
	return flagged, nil
}

// customerOrderInput 客户自助下单入参（客户自助端）。
type customerOrderInput struct {
	SkuID             int    `json:"product_id"`
	Quantity          int64  `json:"quantity"`
	PreferredPlayerID int    `json:"preferred_player_id"`
	IdempotencyKey    string `json:"idempotency_key"`
}

// customerSelfPrincipal 自助入口的幂等主体口径（客户 ID）：下单 / 充值 / 礼物等自助写接口共用，
// 保证「同一客户 + 同一幂等键」在所有自助入口按同一主体去重。
func customerSelfPrincipal(customerID int) string {
	return "customer:" + strconv.Itoa(customerID)
}

// placeCustomerSelfOrder 客户自助下单：须已绑定登录身份与成年资格，
// 且未被「限制自助下单」；有归属客服时该客服即订单负责客服，**无归属客服时也必须能自助下单**，
// 此时订单 responsible_admin_id 为 NULL（进公共池，由 dispatch 认领）。
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
	// ：归属客服为空不再拦截自助下单，改以 adminID=0 落单（责任客服 NULL → 公共池）。
	ownerAdminID := 0
	if customer.OwnerAdminID != nil && *customer.OwnerAdminID > 0 {
		ownerAdminID = *customer.OwnerAdminID
	}
	return placeOrderWithPrincipal(ctx, db, customerSelfPrincipal(customer.ID),
		ownerAdminID, orderInput{
			CustomerID:        customer.ID,
			SkuID:             in.SkuID,
			Quantity:          in.Quantity,
			PreferredPlayerID: in.PreferredPlayerID,
		}, in.IdempotencyKey, now)
}
