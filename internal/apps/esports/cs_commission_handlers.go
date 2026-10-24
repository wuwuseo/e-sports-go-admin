package esports

import (
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// 客服提成：规则配置、明细查询、独立结算单与发放登记。挂「结算」菜单。

func adminCsCommissionRules(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listCsCommissionRules(c.Context(), global.ORM, c.Query("rule_no"), queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func createCsCommissionRuleHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in csCommissionRuleInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	rule, err := createCsCommissionRule(c.Context(), global.ORM, in, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, rule)
}

func activateCsCommissionRuleHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "客服提成规则")
	if err != nil {
		return businessError(c, err)
	}
	rule, err := activateCsCommissionRule(c.Context(), global.ORM, id, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, rule)
}

func rollbackCsCommissionRuleHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "客服提成规则")
	if err != nil {
		return businessError(c, err)
	}
	rule, err := rollbackCsCommissionRule(c.Context(), global.ORM, id, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, rule)
}

func adminCsCommissionEntries(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listCsCommissionEntries(c.Context(), global.ORM,
		queryInt(c, "admin_id"), queryInt(c, "order_id"), c.Query("status"), queryLimit(c, 200))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// adminCsCommissionSummary 客服提成概览（当月累计）：不传 admin_id 时看本人。
func adminCsCommissionSummary(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	adminID := queryInt(c, "admin_id")
	if adminID < 1 {
		adminID = operator.ID
	}
	view, err := csCommissionSummary(c.Context(), global.ORM, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// createCsSettlementHandler 生成客服提成独立结算单。
func createCsSettlementHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		AdminID     int    `json:"admin_id"`
		PeriodStart string `json:"period_start"`
		PeriodEnd   string `json:"period_end"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	start, err := time.Parse(time.RFC3339, in.PeriodStart)
	if err != nil {
		return fail(c, fiber.StatusBadRequest, "周期起始时间格式无效")
	}
	end, err := time.Parse(time.RFC3339, in.PeriodEnd)
	if err != nil {
		return fail(c, fiber.StatusBadRequest, "周期结束时间格式无效")
	}
	item, err := settleCsCommission(c.Context(), global.ORM, in.AdminID, start, end, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// markSettlementPaidHandler 登记发放：locked → paid 并留外部打款凭证。
func markSettlementPaidHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "结算单")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		PayoutRef string `json:"payout_ref"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := markSettlementPaid(c.Context(), global.ORM, id, adminID, in.PayoutRef, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}
