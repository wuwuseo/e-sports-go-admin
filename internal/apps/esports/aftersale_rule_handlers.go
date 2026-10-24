package esports

import (
	"fmt"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// 售后自动建单规则的管理端接口。
//
// 建议接线（主控统一加，PermAPI 路径用 {id}）：
//   - GET    /admin/aftersale-rules
//   - POST   /admin/aftersale-rules
//   - GET    /admin/aftersale-rules/:id
//   - PUT    /admin/aftersale-rules/:id
//   - DELETE /admin/aftersale-rules/:id
//   - GET    /admin/aftersale-rule-hits
// 全部为 AuthAdmin，需逐条配 PermAPI（码前缀 esports:aftersale:rules*）。

// adminAfterSaleRulesHandler 列出规则；status 为空返回全部。
func adminAfterSaleRulesHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listAfterSaleRules(c.Context(), global.ORM, c.Query("status"), queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// adminAfterSaleRuleDetailHandler 规则详情。
func adminAfterSaleRuleDetailHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "规则")
	if err != nil {
		return businessError(c, err)
	}
	item, err := getAfterSaleRule(c.Context(), global.ORM, id)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// adminCreateAfterSaleRuleHandler 新建规则。
func adminCreateAfterSaleRuleHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in afterSaleRuleInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := createAfterSaleRule(c.Context(), global.ORM, adminID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// adminUpdateAfterSaleRuleHandler 原地编辑规则（version 乐观锁）。
func adminUpdateAfterSaleRuleHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "规则")
	if err != nil {
		return businessError(c, err)
	}
	var in afterSaleRuleInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := updateAfterSaleRule(c.Context(), global.ORM, id, adminID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// adminDeleteAfterSaleRuleHandler 删除未命中的规则；已命中规则明确拒绝，提示改为停用。
func adminDeleteAfterSaleRuleHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "规则")
	if err != nil {
		return businessError(c, err)
	}
	if err = deleteAfterSaleRule(c.Context(), global.ORM, id, adminID, time.Now()); err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{"id": id, "deleted": true})
}

// adminAfterSaleRuleHitsHandler 列出命中留痕；pending=1 只返回未自动建单的客服待办命中。
func adminAfterSaleRuleHitsHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	ruleID, err := afterSaleRuleQueryInt(c, "rule_id")
	if err != nil {
		return businessError(c, err)
	}
	items, err := listAfterSaleRuleHits(c.Context(), global.ORM, ruleID,
		c.Query("source_type"), c.Query("pending") == "1", queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// afterSaleRuleQueryInt 解析可选的整数查询参数；缺省返回 0。
func afterSaleRuleQueryInt(c fiber.Ctx, name string) (int, error) {
	raw := c.Query(name)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%w：查询参数 %s 无效", errInvalidInput, name)
	}
	return value, nil
}
