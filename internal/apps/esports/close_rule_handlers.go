package esports

import (
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// 结单规则（首版）：可配置结单条件与金额复核阈值，挂在「结算」菜单下。

func listCloseRulesHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listCloseRules(c.Context(), global.ORM, queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func createCloseRuleHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in closeRuleInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := createCloseRule(c.Context(), global.ORM, in, adminID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func updateCloseRuleHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "结单规则")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		ExpectedVersion int `json:"expected_version"`
		closeRuleInput
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := updateCloseRule(c.Context(), global.ORM, id, in.ExpectedVersion, in.closeRuleInput)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func activateCloseRuleHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "结单规则")
	if err != nil {
		return businessError(c, err)
	}
	item, err := activateCloseRule(c.Context(), global.ORM, id, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func rollbackCloseRuleHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "结单规则")
	if err != nil {
		return businessError(c, err)
	}
	item, err := rollbackCloseRule(c.Context(), global.ORM, id, adminID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}
