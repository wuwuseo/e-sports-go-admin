package esports

import (
	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// adminLedgerAllocations 批次分摊明细（§4.3 / §11.3）：按账户类型 + 账户 ID 查「每笔消费动用了哪些批次」。
func adminLedgerAllocations(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listLedgerAllocations(c.Context(), global.ORM,
		c.Query("account_type"), c.Query("account_id"), queryInt(c, "limit"))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}
