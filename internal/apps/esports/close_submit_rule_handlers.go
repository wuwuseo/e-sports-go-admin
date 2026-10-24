package esports

import (
	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// 结单提交规则：单人 / 多人 / 自定义（代提交、提交比例、未提交处理），挂在「结单表单」菜单下。

func listCloseSubmitRulesHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listCloseSubmitRules(c.Context(), global.ORM, queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func createCloseSubmitRuleHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in closeSubmitRuleInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := createCloseSubmitRule(c.Context(), global.ORM, in, adminID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func updateCloseSubmitRuleHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "结单提交规则")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		ExpectedVersion int `json:"expected_version"`
		closeSubmitRuleInput
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := updateCloseSubmitRule(c.Context(), global.ORM, id, in.ExpectedVersion, in.closeSubmitRuleInput)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func activateCloseSubmitRuleHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "结单提交规则")
	if err != nil {
		return businessError(c, err)
	}
	item, err := activateCloseSubmitRule(c.Context(), global.ORM, id)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func rollbackCloseSubmitRuleHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "结单提交规则")
	if err != nil {
		return businessError(c, err)
	}
	item, err := rollbackCloseSubmitRule(c.Context(), global.ORM, id, adminID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}
