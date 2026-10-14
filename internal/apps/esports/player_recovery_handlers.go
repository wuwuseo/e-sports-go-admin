package esports

import (
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// 陪玩追偿明细（§13.4）：售后赔付由陪玩承担时生成的扣回单，审批后从猫粮扣回。

func listPlayerRecoveriesHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listPlayerRecoveries(c.Context(), global.ORM,
		queryInt(c, "player_id"), c.Query("status"), queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func approvePlayerRecoveryHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "追偿单")
	if err != nil {
		return businessError(c, err)
	}
	item, err := approvePlayerRecovery(c.Context(), global.ORM, id, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func rejectPlayerRecoveryHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "追偿单")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := rejectPlayerRecovery(c.Context(), global.ORM, id, adminID, in.Reason, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func executePlayerRecoveryHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "追偿单")
	if err != nil {
		return businessError(c, err)
	}
	item, err := executePlayerRecovery(c.Context(), global.ORM, id, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// playerRecoveriesHandler 陪玩端：我的追偿明细（只读）。
func playerRecoveriesHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	items, err := listPlayerRecoveries(c.Context(), global.ORM, player.ID, "", 100)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}
