package esports

import (
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// backfillOrderHandler 管理员补录历史订单（§3.3 / §4.1）：须带 Idempotency-Key，
// 默认不计提业绩，返回体同时给出「是否计入当期业绩」的结论。
func backfillOrderHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in backfillOrderInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	key := c.Get("Idempotency-Key")
	if len(key) < 8 || len(key) > 128 {
		return fail(c, fiber.StatusBadRequest, "缺少有效的 Idempotency-Key（8-128 字符）")
	}
	order, err := backfillOrder(c.Context(), global.ORM, adminID, in, key, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, backfillOrderView(order))
}
