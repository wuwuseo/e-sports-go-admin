package esports

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// listPlayerCreditEventsHandler 管理端查询陪玩信用记载（§13.5），可按陪玩与状态过滤。
func listPlayerCreditEventsHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	playerID, err := queryIntDefault(c, "player_id", 0)
	if err != nil {
		return businessError(c, errInvalidInput)
	}
	events, err := listPlayerCreditEvents(c.Context(), global.ORM, playerID, c.Query("status"), queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, events)
}

// reviewPlayerCreditEventHandler 复核陪玩信用记载（§13.5）：确认责任并暂停接单，或判定不构成责任。
func reviewPlayerCreditEventHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "信用记载")
	if err != nil {
		return businessError(c, err)
	}
	var in playerCreditReviewInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	event, err := reviewPlayerCreditEvent(c.Context(), global.ORM, id, adminID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, event)
}

// playerCreditEventsHandler 陪玩端查询本人信用记载（§13.5），只读且只看本人。
func playerCreditEventsHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	events, err := listPlayerCreditEvents(c.Context(), global.ORM, player.ID, c.Query("status"), queryLimit(c, 50))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, events)
}

// queryIntDefault 读取整型查询参数，缺失或空串时返回默认值。
func queryIntDefault(c fiber.Ctx, key string, fallback int) (int, error) {
	raw := c.Query(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, errInvalidInput
	}
	return value, nil
}
