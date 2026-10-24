package esports

import (
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// adminGiftDisputes 礼物争议列表：非超管只看归属客户的争议单，可按状态筛选。
func adminGiftDisputes(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	items, err := listGiftDisputes(c.Context(), global.ORM, adminID, isSuper, c.Query("status"), queryLimit(c, 200))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// createGiftDisputeByAdminHandler 客服代客户建礼物争议单，须为归属客户。
func createGiftDisputeByAdminHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		giftDisputeInput
		CustomerID int `json:"customer_id"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	if in.CustomerID < 1 {
		return fail(c, fiber.StatusBadRequest, "客户 ID 无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	if _, err = loadOwnedCustomer(c.Context(), global.ORM, adminID, isSuper, in.CustomerID); err != nil {
		return businessError(c, err)
	}
	item, err := createGiftDispute(c.Context(), global.ORM, in.giftDisputeInput, "admin", in.CustomerID, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// reviewGiftDisputeHandler 审核礼物争议：通过须给确认金额，驳回须给原因；申请人不得自审。
func reviewGiftDisputeHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "礼物争议")
	if err != nil {
		return businessError(c, err)
	}
	var in giftDisputeReviewInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	item, err := reviewGiftDispute(c.Context(), global.ORM, id, adminID, isSuper, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// executeGiftDisputeHandler 执行礼物争议退款：按确认金额部分退款并冲回分成，执行人不得为审核人。
func executeGiftDisputeHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "礼物争议")
	if err != nil {
		return businessError(c, err)
	}
	item, err := executeGiftDispute(c.Context(), global.ORM, id, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// userGiftOrdersHandler 客户自助：我的礼物单据（含已退金额），用于发起争议。
func userGiftOrdersHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	items, err := listCustomerGiftOrders(c.Context(), global.ORM, customer.ID, queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// userGiftDisputes 客户自助：我的礼物争议单。
func userGiftDisputes(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	items, err := listCustomerGiftDisputes(c.Context(), global.ORM, customer.ID, queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// userCreateGiftDisputeHandler 客户自助发起礼物争议：仅可对本人礼物订单发起。
func userCreateGiftDisputeHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	var in giftDisputeInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := createGiftDispute(c.Context(), global.ORM, in, "customer", customer.ID, 0, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// userWithdrawGiftDisputeHandler 客户自助撤回礼物争议（进行中的争议单）。
func userWithdrawGiftDisputeHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "礼物争议")
	if err != nil {
		return businessError(c, err)
	}
	item, err := withdrawGiftDispute(c.Context(), global.ORM, id, customer.ID, 0, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}
