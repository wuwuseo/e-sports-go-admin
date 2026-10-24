package esports

import (
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// registerPaymentReversalHandler 财务登记已核销收款的渠道撤销 / 拒付。
func registerPaymentReversalHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in paymentReversalInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := registerPaymentReversal(c.Context(), global.ORM, adminID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// listPaymentReversalsHandler 查询渠道撤销 / 拒付事件，可按订单与状态过滤。
func listPaymentReversalsHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	orderID, err := orderIDByNo(c.Context(), global.ORM, c.Query("order_no"))
	if err != nil {
		return businessError(c, err)
	}
	items, err := listPaymentReversals(c.Context(), global.ORM, orderID, c.Query("status"), queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	views, err := paymentReversalViewsOf(c.Context(), global.ORM, items)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, views)
}

// decidePaymentReversalHandler 处置渠道撤销 / 拒付：登记人不得自审。
func decidePaymentReversalHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "撤销事件")
	if err != nil {
		return businessError(c, err)
	}
	var in paymentReversalDecisionInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := decidePaymentReversal(c.Context(), global.ORM, id, adminID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}
