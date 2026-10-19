package esports

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// queryInt 读取整型 query 参数，缺失或非法返回 0。
func queryInt(c fiber.Ctx, key string) int {
	value, err := strconv.Atoi(c.Query(key))
	if err != nil {
		return 0
	}
	return value
}

func adminCommissionRules(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listCommissionRules(c.Context(), global.ORM, c.Query("rule_no"), queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func createCommissionRuleHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in ruleInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	rule, err := createCommissionRule(c.Context(), global.ORM, in, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, rule)
}

func activateCommissionRuleHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "分成规则")
	if err != nil {
		return businessError(c, err)
	}
	rule, err := activateCommissionRule(c.Context(), global.ORM, id, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, rule)
}

func rollbackCommissionRuleHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "分成规则")
	if err != nil {
		return businessError(c, err)
	}
	rule, err := rollbackCommissionRule(c.Context(), global.ORM, id, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, rule)
}

// setOrderManualCommissionRuleHandler 设置 / 清除订单的「单笔手动指定」分成规则（§12.3）。
// rule_id=0 表示清除；订单已锁定成员规则时返回冲突。
func setOrderManualCommissionRuleHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		RuleID int `json:"rule_id"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	order, err := setOrderManualCommissionRule(c.Context(), global.ORM, id, in.RuleID, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

func adminCommissionEntries(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	// §12.7-01：返回读模型（规则数值 + 扣减项 + 到手净额 + 逐单位留痕），而非原始明细。
	items, err := listCommissionEntryViews(c.Context(), global.ORM,
		queryInt(c, "player_id"), queryInt(c, "order_id"), c.Query("status"), queryLimit(c, 200))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func adminSettlements(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listSettlements(c.Context(), global.ORM, queryInt(c, "player_id"), queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func createSettlementHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		PlayerID    int    `json:"player_id"`
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
	item, err := settlePlayerCommission(c.Context(), global.ORM, in.PlayerID, start, end, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func lockSettlementHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "结算单")
	if err != nil {
		return businessError(c, err)
	}
	item, err := lockSettlement(c.Context(), global.ORM, id, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// adminSettlementDetail 结算单详情：主表 + 本期可下钻明细（带规则版本，§12.6-06）。
// 建议接线：GET /admin/settlements/:id（管理端 Auth）。
func adminSettlementDetail(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "结算单")
	if err != nil {
		return businessError(c, err)
	}
	item, err := getSettlementDetail(c.Context(), global.ORM, id, queryLimit(c, 200))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// playerCommissionEntries 陪玩端分成明细：与管理员同一读模型，
// 供陪玩端渲染分成方式、规则版本号、扣减项与到手净额（§12.7-01）。
func playerCommissionEntries(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	items, err := listCommissionEntryViews(c.Context(), global.ORM, player.ID, 0,
		c.Query("status"), queryLimit(c, 200))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func playerCommissionSummaryHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	view, err := playerCommissionSummary(c.Context(), global.ORM, player.ID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}
