package esports

import (
	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// orderCloseConditions 结单条件逐项状态（结单条件自定义）：
// 前端据此把「条件不满足」的结单按钮置灰，口径与结单执行时的拦截完全一致。
// 归属校验由路由上的 ownedOrderHandler 完成。
func orderCloseConditions(c fiber.Ctx) error {
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	status, err := orderCloseConditionStatus(c.Context(), global.ORM, orderID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, status)
}
