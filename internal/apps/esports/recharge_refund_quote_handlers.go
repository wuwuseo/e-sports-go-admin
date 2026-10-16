package esports

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// adminWalletRefundQuote 客户余额账户按充值批次的退款试算（§4.5）：纯只读，
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

// adminStoredValueCardRefundBatchQuote 储值卡退卡的批次维度试算（§4.5 / §4.4）：与既有
// `stored-value-cards/:id/refund-quote` 同口径（未消费本金可退、未消费赠送随退作废），
// 额外给出按预付款批次（account_type=stored_value_prepay）展开的逐批明细与异常清单；纯只读。
// 建议接线：GET /admin/stored-value-cards/:id/refund-batch-quote。
func adminStoredValueCardRefundBatchQuote(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	cardID, err := parsePathID(c, "储值卡")
	if err != nil {
		return businessError(c, err)
	}
	card, err := global.ORM.EsportsStoredValueCard.Get(c.Context(), cardID)
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	if _, err := loadOwnedCustomer(c.Context(), global.ORM, operator.ID, isSuper, card.CustomerID); err != nil {
		return businessError(c, err)
	}
	quote, err := rechargeRefundQuoteFor(c.Context(), global.ORM, storedValuePrepayAccount, strconv.Itoa(cardID), time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, quote)
}
