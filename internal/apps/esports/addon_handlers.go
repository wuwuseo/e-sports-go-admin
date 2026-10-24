package esports

import (
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// ── 加钟：客服对服务中订单发起，陪玩确认 ──

func listOrderAddonsHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	items, err := listOrderAddons(c.Context(), global.ORM, orderID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func createOrderAddonHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in addonInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	addon, err := requestOrderAddon(c.Context(), global.ORM, orderID, adminID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, addon)
}

func cancelOrderAddonHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "加钟单")
	if err != nil {
		return businessError(c, err)
	}
	addon, err := cancelOrderAddon(c.Context(), global.ORM, id, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, addon)
}

// payOrderAddonByBalanceHandler 客服收款：对已确认的加钟单用客户余额核销追加金额，
// 收款成功后按确认时保存的规则快照计提加钟分成。
func payOrderAddonByBalanceHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "加钟单")
	if err != nil {
		return businessError(c, err)
	}
	addon, err := payOrderAddonByBalance(c.Context(), global.ORM, adminID, id, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, addon)
}

// playerPendingAddonsHandler 陪玩端：我的加钟通知（待确认）。
func playerPendingAddonsHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	items, err := listPlayerPendingAddons(c.Context(), global.ORM, player.ID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// playerConfirmAddonHandler 陪玩端：确认加钟（保存分成快照并累加到订单，收款后再计提分成）。
func playerConfirmAddonHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "加钟单")
	if err != nil {
		return businessError(c, err)
	}
	addon, err := confirmOrderAddon(c.Context(), global.ORM, player.ID, id, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, addon)
}

// playerRejectAddonHandler 陪玩端：拒绝加钟（须填原因）。
func playerRejectAddonHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "加钟单")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	addon, err := rejectOrderAddon(c.Context(), global.ORM, player.ID, id, in.Reason, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, addon)
}
