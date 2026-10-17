package esports

import (
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// 会员优先预约（§10.4）：自助端提交预约意向 + 客服端待办列表。

// userCreatePriorityBookingHandler 客户自助提交预约意向（§10.4）：仅当前等级启用 priority_booking 的客户可用。
func userCreatePriorityBookingHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	var in priorityBookingInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := createPriorityBooking(c.Context(), global.ORM, customer, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// adminPriorityBookingsHandler 客服端待处理预约意向列表（§10.4）：按会员等级置顶，非超管只看归属本人客户。
func adminPriorityBookingsHandler(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	items, err := listPriorityBookings(c.Context(), global.ORM, operator.ID, isSuper, queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}
