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

// userCreateAfterSaleHandler 客户自助发起售后（§13.2）：仅可对本人订单发起；支持上传本人凭证附件。
func userCreateAfterSaleHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	var in customerAfterSaleInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := createCustomerAfterSaleWithEvidence(c.Context(), global.ORM, customer, in.afterSaleInput, in.EvidenceAttachmentIDs, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// userStoredValuePackagesHandler 可自助购买的储值卡套餐（§4.4）：只返回在售档位与购卡口径。
func userStoredValuePackagesHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	if _, err := customerForVerifiedUser(c.Context(), global.ORM, uid); err != nil {
		return businessError(c, err)
	}
	items, err := listStoredValuePackages(c.Context(), global.ORM, true)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{
		"packages":                 items,
		"payment_channels":         []string{"wechat_work", "merchant_qr", "corporate_transfer"},
		"min_principal_cents":      customerStoredValueMinPrincipalCents,
		"max_principal_cents":      customerStoredValueMaxPrincipalCents,
		"max_validity_days":        customerStoredValueMaxValidDays,
		"contract_required":        true,
		"pay_window_expires_hours": customerStoredValuePayWindowDefaultMinutes / 60,
	})
}

// userPurchaseStoredValueCardHandler 客户自助购卡（§4.4）：须显式确认合同；建卡后为零余额待核验并返回付款码。
func userPurchaseStoredValueCardHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	var in customerStoredValuePurchaseInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	view, err := purchaseCustomerStoredValueCard(c.Context(), global.ORM, customer, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// userStoredValuePurchaseDetailHandler 我的购卡申请详情（含支付意图与付款码），他人申请 403。
func userStoredValuePurchaseDetailHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	cardID, err := parsePathID(c, "购卡申请")
	if err != nil {
		return businessError(c, err)
	}
	view, err := getCustomerStoredValuePurchase(c.Context(), global.ORM, customer.ID, cardID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// userStoredValueCardsHandler 我的储值卡列表（§4.4 余额查询）：只返回本人卡。
func userStoredValueCardsHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	items, err := listCustomerStoredValueCards(c.Context(), global.ORM, customer.ID,
		c.Query("status"), queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// userStoredValueCardDetailHandler 我的储值卡详情：卡信息 + 派生余额 + 流水，他人卡 403。
func userStoredValueCardDetailHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	cardID, err := parsePathID(c, "储值卡")
	if err != nil {
		return businessError(c, err)
	}
	view, transactions, err := getCustomerStoredValueCard(c.Context(), global.ORM, customer.ID,
		cardID, queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{"card": view, "transactions": transactions})
}
