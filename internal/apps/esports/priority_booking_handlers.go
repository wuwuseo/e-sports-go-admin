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

// playerAvailabilityQuery 解析可用时段查询的通用参数（player 来自路径或查询串）。
func playerAvailabilityQuery(c fiber.Ctx, playerID int) (*playerAvailabilityView, error) {
	now := time.Now()
	from, err := playerAvailabilityQueryFrom(c.Query("from"), now)
	if err != nil {
		return nil, err
	}
	to, err := playerAvailabilityQueryFrom(c.Query("to"), now.AddDate(0, 0, 7))
	if err != nil {
		return nil, err
	}
	return playerAvailableSlots(c.Context(), global.ORM, playerID, from, to, queryInt(c, "slot_minutes"))
}

// adminPlayerAvailabilityHandler 管理端查询陪玩可用时段（§10.4-04）：含已占用 / 可预约判定。
func adminPlayerAvailabilityHandler(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	playerID, err := parsePathID(c, "陪玩")
	if err != nil {
		return businessError(c, err)
	}
	view, err := playerAvailabilityQuery(c, playerID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// userPlayerAvailabilityHandler 客户端查询陪玩可用时段（§10.4-04）：会员提交预约前先看档期。
func userPlayerAvailabilityHandler(c fiber.Ctx) error {
	playerID, err := parsePathID(c, "陪玩")
	if err != nil {
		return businessError(c, err)
	}
	view, err := playerAvailabilityQuery(c, playerID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}
