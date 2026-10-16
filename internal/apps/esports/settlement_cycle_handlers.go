package esports

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

func adminSettlementCycles(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listSettlementCycles(c.Context(), global.ORM, queryLimit(c, 200))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func createSettlementCycleHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in settlementCycleInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := createSettlementCycle(c.Context(), global.ORM, in, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func updateSettlementCycleHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "结算周期")
	if err != nil {
		return businessError(c, err)
	}
	var in settlementCycleInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := updateSettlementCycle(c.Context(), global.ORM, id, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func setSettlementCycleStatusHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "结算周期")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.Bind().Body(&in); err != nil || in.Enabled == nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := setSettlementCycleEnabled(c.Context(), global.ORM, id, *in.Enabled, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// freezeOrderCommissionHandler 手工冻结订单待结算分成明细（§12.6 争议冻结），可按陪玩收窄。
func freezeOrderCommissionHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		PlayerID int    `json:"player_id"`
		Reason   string `json:"reason"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		reason = "人工争议冻结"
	}
	var playerID *int
	if in.PlayerID > 0 {
		playerID = &in.PlayerID
	}
	count, err := freezeOrderCommissionForDispute(c.Context(), global.ORM, id, playerID, reason, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, map[string]interface{}{"order_id": id, "frozen": count})
}

// unfreezeOrderCommissionHandler 手工解冻订单被冻结的分成明细（争议人工结案）。
func unfreezeOrderCommissionHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	count, err := unfreezeOrderCommission(c.Context(), global.ORM, id)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, map[string]interface{}{"order_id": id, "unfrozen": count})
}
