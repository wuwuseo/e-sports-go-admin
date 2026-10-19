package esports

import (
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// 陪玩转单（§5.7）：陪玩端发起 / 撤回 / 查看，管理端确认。

// adminSeatTransfers 管理端列出转单申请，按状态 / 订单 / 陪玩过滤。
// §3.5-02：列表按订单归属收口（非超管只看本人负责 / 协作 / 公共池订单的申请）。
func adminSeatTransfers(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	items, err := listSeatTransfersForAdmin(c.Context(), global.ORM, adminID, isSuper, c.Query("status"),
		queryInt(c, "order_id"), queryInt(c, "player_id"), queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// decideSeatTransferHandler 客服确认转单：通过则释放席位为待补位，驳回须填原因。
func decideSeatTransferHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "转单申请")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Approve bool   `json:"approve"`
		Note    string `json:"note"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := decideSeatTransfer(c.Context(), global.ORM, adminID, id, in.Approve, in.Note, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// playerRequestTransfer 陪玩端发起转单申请（须携带幂等键）。
func playerRequestTransfer(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in seatTransferInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := requestSeatTransfer(c.Context(), global.ORM, player.ID, orderID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// playerCancelTransfer 陪玩端在客服确认前撤回转单申请。
func playerCancelTransfer(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "转单申请")
	if err != nil {
		return businessError(c, err)
	}
	item, err := cancelSeatTransfer(c.Context(), global.ORM, player.ID, id, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// playerTransferRequests 陪玩端我的转单申请列表。
func playerTransferRequests(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	items, err := listSeatTransfers(c.Context(), global.ORM, c.Query("status"),
		0, player.ID, queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}
