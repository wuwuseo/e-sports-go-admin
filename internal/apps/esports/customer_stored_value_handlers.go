package esports

import (
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// adminVerifyCustomerStoredValuePurchaseHandler 财务核验客户自助购卡到账（§4.4）：
// 复用既有核验落库边界（状态、卡内余额、预付款批次、自审拦截），核验后同步支付意图状态
// 并写卡激活外盒。approve=false 驳回申请并把支付意图置无效。
// 建议路由：POST /admin/stored-value-cards/:id/verify-self-purchase。
func adminVerifyCustomerStoredValuePurchaseHandler(c fiber.Ctx) error {
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
		ReceivedCents int64  `json:"received_cents"`
		EvidenceNote  string `json:"evidence_note"`
	}
	if err := c.Bind().Body(&in); err != nil || in.Approve == nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	view, err := verifyCustomerStoredValuePurchasePayment(c.Context(), global.ORM, operator.ID, cardID,
		*in.Approve, in.ReceivedCents, in.EvidenceNote, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}
