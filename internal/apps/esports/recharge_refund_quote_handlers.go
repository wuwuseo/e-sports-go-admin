package esports

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// adminWalletRefundQuote 客户余额账户按充值批次的退款试算：纯只读，
// 返回逐批还原明细、可退本金合计、作废赠送合计与批次异常清单，不写任何账本。
// 建议接线：GET /admin/wallets/:id/refund-quote（id 为钱包归属人，即客户 ID）。
func adminWalletRefundQuote(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	ownerID, err := parsePathID(c, "钱包归属人")
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	if _, err := loadOwnedCustomer(c.Context(), global.ORM, operator.ID, isSuper, ownerID); err != nil {
		return businessError(c, err)
	}
	quote, err := rechargeRefundQuoteFor(c.Context(), global.ORM, walletCustomerBalance, strconv.Itoa(ownerID), time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, quote)
}
