package esports

import (
	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// 账本核验异常的管理端查看入口。

// adminLedgerReconcileAnomalies 列出最近的账本核验异常事件：逐账户余额为负、批次超分摊、
// 分摊引用批次缺失、钱包流水求和与批次账本求和不一致、预付账户出账未登记批次。
func adminLedgerReconcileAnomalies(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listLedgerReconcileAnomalies(c.Context(), global.ORM, queryInt(c, "limit"))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}
