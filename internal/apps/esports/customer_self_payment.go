package esports

import (
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// 客户自助站内付款入口：客户为本人待付款订单用余额足额核销。
// 与管理端核销共用同一业务边界（金额、订单状态、支付时限、配额与账本），额外核对订单归属与
// 站内渠道启停；渠道停用 / 余额不足 / 非本人订单一律返回明确业务错误，不做「伪成功」兜底。

// userPayOrderByBalanceHandler 客户自助余额核销订单。
func userPayOrderByBalanceHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	order, err := payOrderWithBalanceAsCustomer(c.Context(), global.ORM, customer.ID, orderID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}
