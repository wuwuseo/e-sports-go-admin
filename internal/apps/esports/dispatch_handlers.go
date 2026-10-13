package esports

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// parseSeatIndex 解析路由中的席位序号参数，必须为正整数。
func parseSeatIndex(c fiber.Ctx) (int, error) {
	index, err := strconv.Atoi(c.Params("seatIndex"))
	if err != nil || index < 1 {
		return 0, errInvalidInput
	}
	return index, nil
}

func dispatchBoardHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	board, err := loadDispatchBoard(c.Context(), global.ORM, adminID, isSuper)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, board)
}

func orderSeatsHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	seats, err := listOrderSeats(c.Context(), global.ORM, id)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, seats)
}

func assignSeatHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	seatIndex, err := parseSeatIndex(c)
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		PlayerID int `json:"player_id"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	seat, err := assignDispatchSeat(c.Context(), global.ORM, id, seatIndex, in.PlayerID, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, seat)
}

func revokeSeatHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	seatIndex, err := parseSeatIndex(c)
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	seat, err := revokeDispatchSeat(c.Context(), global.ORM, id, seatIndex, adminID, in.Reason)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, seat)
}

func claimOrderHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	order, err := claimOrder(c.Context(), global.ORM, id, adminID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

func transferOrderHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		ToAdminID int    `json:"to_admin_id"`
		Reason    string `json:"reason"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	order, err := transferOrder(c.Context(), global.ORM, id, in.ToAdminID, adminID, in.Reason)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

func remindOrderHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Level int    `json:"level"`
		Note  string `json:"note"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	if err := remindOrder(c.Context(), global.ORM, id, adminID, in.Level, in.Note); err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{"order_id": id})
}

// publishOrderToGrabHallHandler 客服把待派单订单发布到抢单大厅（§5.2 / §5.6）。
func publishOrderToGrabHallHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	order, err := publishOrderToGrabHall(c.Context(), global.ORM, id, adminID, in.Reason, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}
