package esports

import (
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// §4.5 充值独立支付意图与活动快照的 HTTP 入口。
//
//	POST /user/recharge-intents                           客户发起充值意图（锁定活动快照 + 付款指引）
//	POST /admin/recharge-intents/:intentNo/verify         财务核验充值到账并按快照入账
//	settleRechargeIntent 仅供已验签通道适配器调用（无公开路由）。

// userCreateRechargeIntentHandler 客户自助发起充值意图（§4.5）：按当前生效活动锁定赠送快照，
// 生成独立支付意图与付款指引；赠送到账由财务核验或已验签通道核销后按快照入账。
func userCreateRechargeIntentHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	var in rechargeIntentInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	view, err := createRechargeIntent(c.Context(), global.ORM, customer, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// adminVerifyRechargeIntentHandler 财务核验充值到账（§4.5）：approve=true 按意图内锁定的活动快照
// 入账，approve=false 驳回并把支付意图置无效。
func adminVerifyRechargeIntentHandler(c fiber.Ctx) error {
	operator, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	intentNo := c.Params("intentNo")
	if intentNo == "" {
		return fail(c, fiber.StatusBadRequest, "充值意图号无效")
	}
	var in struct {
		Approve       *bool  `json:"approve"`
		ReceivedCents int64  `json:"received_cents"`
		EvidenceNote  string `json:"evidence_note"`
	}
	if err := c.Bind().Body(&in); err != nil || in.Approve == nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	view, err := verifyRechargeIntentPayment(c.Context(), global.ORM, operator.ID, intentNo,
		*in.Approve, in.ReceivedCents, in.EvidenceNote, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}
