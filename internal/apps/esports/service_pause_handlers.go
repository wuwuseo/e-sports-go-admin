package esports

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// parsePlayerPathID 解析路由中的陪玩 ID 参数，必须为正整数。
func parsePlayerPathID(c fiber.Ctx) (int, error) {
	id, err := strconv.Atoi(c.Params("playerId"))
	if err != nil || id < 1 {
		return 0, errInvalidInput
	}
	return id, nil
}

func adminPausePlayerServiceHandler(c fiber.Ctx) error {
	return adminServicePauseHandler(c, true)
}

func adminResumePlayerServiceHandler(c fiber.Ctx) error {
	return adminServicePauseHandler(c, false)
}

// adminServicePauseHandler 客服代陪玩暂停 / 恢复服务段：理由必填，事件以管理员身份留痕。
func adminServicePauseHandler(c fiber.Ctx, pause bool) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	playerID, err := parsePlayerPathID(c)
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	var in struct {
		Reason string `json:"reason"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := setServicePauseByAdmin(c.Context(), global.ORM, adminID, orderID, playerID, isSuper, pause, in.Reason, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// adminCorrectServiceTimingHandler 争议更正：对已结束服务段的有效 / 暂停秒数留痕更正，
// 同时登记停工量（累计暂停秒数与停工次数）与责任方，必须附带原因。
func adminCorrectServiceTimingHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	playerID, err := parsePlayerPathID(c)
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	var in struct {
		EffectiveSeconds int64  `json:"effective_seconds"`
		PausedSeconds    int64  `json:"paused_seconds"`
		Responsibility   string `json:"responsibility"`
		Reason           string `json:"reason"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	result, err := correctServiceTiming(c.Context(), global.ORM, adminID, orderID, playerID, isSuper,
		in.EffectiveSeconds, in.PausedSeconds, in.Responsibility, in.Reason, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, result)
}
