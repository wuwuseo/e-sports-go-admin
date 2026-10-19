package esports

import (
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// backfillOrderHandler 管理员补录历史订单（§3.3 / §4.1 / §12.5）：须带 Idempotency-Key，
// 陪玩分成与成长值默认正常计提、客服提成默认不计入，返回体同时给出「是否计入客服提成」的结论。
// 超管标识沿用同包既有写法（PermissionMiddleware 注入 Locals），用于放行「他人客户」的历史补录（P0-13）。
func backfillOrderHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	var in backfillOrderInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	key := c.Get("Idempotency-Key")
	if len(key) < 8 || len(key) > 128 {
		return fail(c, fiber.StatusBadRequest, "缺少有效的 Idempotency-Key（8-128 字符）")
	}
	order, err := backfillOrderWithRole(c.Context(), global.ORM, adminID, isSuper, in, key, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, backfillOrderView(order))
}
