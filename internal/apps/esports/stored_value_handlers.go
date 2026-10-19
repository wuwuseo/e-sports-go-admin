package esports

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// adminStoredValueCards 储值卡列表（§4.4 余额查询 / 到期提醒）：可按客户、状态与「临近到期」过滤。
func adminStoredValueCards(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	views, err := listStoredValueCards(c.Context(), global.ORM, operator.ID, isSuper,
		queryInt(c, "customer_id"), c.Query("status"), queryInt(c, "remind_days"),
		queryInt(c, "limit"), time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, views)
}

// adminStoredValueCardDetail 储值卡详情：卡信息 + 派生余额 + 流水。
func adminStoredValueCardDetail(c fiber.Ctx) error {
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
	view, err := storedValueCardViewOf(c.Context(), global.ORM, card)
	if err != nil {
		return businessError(c, err)
	}
	transactions, err := listStoredValueCardTransactions(c.Context(), global.ORM, cardID, queryInt(c, "limit"))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{"card": view, "transactions": transactions})
}

// createStoredValueCardHandler 开卡（§4.4）：客服代办或客户自助购买。
func createStoredValueCardHandler(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in storedValueOpenInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	if _, err := loadOwnedCustomer(c.Context(), global.ORM, operator.ID, isSuper, in.CustomerID); err != nil {
		return businessError(c, err)
	}
	view, err := openStoredValueCard(c.Context(), global.ORM, operator.ID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// verifyStoredValueCardPaymentHandler 独立财务核验实际到账后激活卡或驳回申请。
func verifyStoredValueCardPaymentHandler(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	cardID, err := parsePathID(c, "储值卡")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Approve       *bool  `json:"approve"`
		ReceiptRef    string `json:"receipt_ref"`
		ReceivedCents int64  `json:"received_cents"`
		EvidenceNote  string `json:"evidence_note"`
	}
	if err := c.Bind().Body(&in); err != nil || in.Approve == nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	view, err := verifyStoredValueCardPayment(c.Context(), global.ORM, operator.ID, cardID,
		*in.Approve, in.ReceiptRef, in.ReceivedCents, in.EvidenceNote, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// storedValueCardRefundQuoteHandler 退卡试算（§4.4）：展示可退金额与计算明细，不落库。
func storedValueCardRefundQuoteHandler(c fiber.Ctx) error {
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
	quote, refundable, err := storedValueRefundQuoteFor(c.Context(), global.ORM, card, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{"quote": quote, "refundable_cents": refundable})
}

// requestStoredValueRefundHandler 申请退卡：转待审并快照试算明细。
func requestStoredValueRefundHandler(c fiber.Ctx) error {
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
	view, err := requestStoredValueRefund(c.Context(), global.ORM, operator.ID, cardID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// decideStoredValueRefundHandler 财务审核退卡：通过与驳回都不得自审。
// 审核通过即「已出账」，须由操作人登记打款凭证 / 银行流水号（P1）：handler 先做必填校验并给明确提示，
// 服务层再按同一规则校验一次，防止绕过 handler 直接调用时出一笔无凭据的钱。
func decideStoredValueRefundHandler(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	cardID, err := parsePathID(c, "储值卡")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Approve    *bool  `json:"approve"`
		PayoutRef  string `json:"payout_ref"`
		PayoutNote string `json:"payout_note"`
	}
	if err := c.Bind().Body(&in); err != nil || in.Approve == nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	if *in.Approve && strings.TrimSpace(in.PayoutRef) == "" {
		return fail(c, fiber.StatusBadRequest, "退卡出账必须填写打款凭证或银行流水号")
	}
	view, err := decideStoredValueRefund(c.Context(), global.ORM, operator.ID, cardID, *in.Approve, time.Now(),
		storedValueRefundPayout{PayoutRef: in.PayoutRef, PayoutNote: in.PayoutNote})
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// payOrderByStoredValueCardHandler 报单时用储值卡足额核销（§4.4 消费）。
func payOrderByStoredValueCardHandler(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		CardID int `json:"card_id"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	order, err := payOrderWithStoredValueCard(c.Context(), global.ORM, operator.ID, orderID, in.CardID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}
