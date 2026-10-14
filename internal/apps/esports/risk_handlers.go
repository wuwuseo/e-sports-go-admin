package esports

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// 客户风控（§13.7）：风险名单查询、人工标记 / 解除，以及客户自助下单入口的风险拦截。

func listRiskCustomersHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listRiskCustomers(c.Context(), global.ORM, queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// markCustomerRiskHandler 人工标记 / 解除客户风险（§13.7）。
func markCustomerRiskHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	customerID, err := strconv.Atoi(c.Params("id"))
	if err != nil || customerID < 1 {
		return fail(c, fiber.StatusBadRequest, "客户 ID 无效")
	}
	var in struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := markCustomerRisk(c.Context(), global.ORM, customerID, in.Status, in.Reason, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// userPlaceOrderHandler 客户自助下单（§13.2）：须已绑定登录身份、成年且未被限制自助下单。
func userPlaceOrderHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	var in customerOrderInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	order, err := placeCustomerSelfOrder(c.Context(), global.ORM, customer, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}
