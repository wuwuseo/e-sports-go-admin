package esports

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// 客户自助端（§13.2 / §10）：统一以「登录用户绑定的客户」为数据边界。

// userProfileHandler 我的资料（会员等级、成长值、标签、风险只读）。
func userProfileHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	profile, err := customerSelfProfileOf(c.Context(), global.ORM, customer)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, profile)
}

// userOrdersHandler 我的订单（bucket: all / pending_payment / pending_dispatch / in_progress / pending_close / completed / cancelled）。
func userOrdersHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	items, err := listCustomerOrders(c.Context(), global.ORM, customer.ID,
		c.Query("bucket"), queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// userOrderMembersHandler 订单服务成员（§7.5 服务评价前置数据）：仅限本人订单。
func userOrderMembersHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	orderID, err := strconv.Atoi(c.Params("id"))
	if err != nil || orderID < 1 {
		return fail(c, fiber.StatusBadRequest, "订单 ID 无效")
	}
	members, err := listCustomerOrderMembers(c.Context(), global.ORM, customer.ID, orderID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, members)
}

// userOrderDetailHandler 我的订单详情。
func userOrderDetailHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	orderID, err := strconv.Atoi(c.Params("id"))
	if err != nil || orderID < 1 {
		return fail(c, fiber.StatusBadRequest, "订单 ID 无效")
	}
	order, err := getCustomerOrder(c.Context(), global.ORM, customer.ID, orderID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

func userCancelUnpaidOrderHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	order, err := cancelCustomerUnpaidOrder(c.Context(), global.ORM, customer.ID, orderID, in.Reason)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

// userWalletHandler 我的余额与权益。
func userWalletHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	wallet, err := customerSelfWalletOf(c.Context(), global.ORM, customer.ID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, wallet)
}

// userAfterSalesHandler 我的售后单（§13.2 进度可查）。
func userAfterSalesHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	items, err := listCustomerAfterSales(c.Context(), global.ORM, customer.ID, queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// userCreateAfterSaleHandler 客户自助发起售后（§13.2）：仅可对本人订单发起。
func userCreateAfterSaleHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	var in afterSaleInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := createCustomerAfterSale(c.Context(), global.ORM, customer, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}
