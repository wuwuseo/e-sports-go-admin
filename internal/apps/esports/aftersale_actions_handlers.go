package esports

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// listAfterSaleActionsHandler 列出某售后单的动作项与逐项进度（多动作售后方案）。
// 建议路由：GET /admin/after-sales/:id/actions（ownedAfterSaleHandler 包装）。
func listAfterSaleActionsHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "售后单")
	if err != nil {
		return businessError(c, err)
	}
	actions, err := listAfterSaleActions(c.Context(), global.ORM, id, queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	views, err := afterSaleActionViewsOf(c.Context(), global.ORM, actions)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{
		"progress": afterSaleActionProgressOf(actions),
		"actions":  views,
	})
}

// executeAfterSaleActionHandler 逐项执行单个动作项，供人工对失败项重试。
// 建议路由：POST /admin/after-sales/:id/actions/:seq/execute（ownedAfterSaleHandler 包装）。
func executeAfterSaleActionHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "售后单")
	if err != nil {
		return businessError(c, err)
	}
	seq, err := parseActionSeqParam(c)
	if err != nil {
		return businessError(c, err)
	}
	action, err := executeAfterSaleActionSeq(c.Context(), global.ORM, id, seq, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, action)
}

// parseActionSeqParam 解析动作项序号路径参数（:seq）。
func parseActionSeqParam(c fiber.Ctx) (int, error) {
	seq, err := strconv.Atoi(c.Params("seq"))
	if err != nil || seq < 1 || seq > maxAfterSaleActions {
		return 0, errInvalidInput
	}
	return seq, nil
}
