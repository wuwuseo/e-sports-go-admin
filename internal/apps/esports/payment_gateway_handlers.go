package esports

import (
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/global"
)

// 在线收款 HTTP 入口（§4.3）：
// 1. 客户为本人待付款订单发起在线收款，返回渠道收银台地址；
// 2. 渠道异步回调必须以「原始 body + 签名头」验签，验签通过后才允许核销到账。
// 本文件只做协议适配，业务边界仍在 startOrderPayment / handlePaymentNotification。

// userStartOrderPaymentHandler 客户为本人订单发起在线收款。
// 请求体：{"channel":"wechat_work"}；返回 {intent_no, channel, pay_url, amount_cents, expires_at}。
func userStartOrderPaymentHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Channel string `json:"channel"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	view, err := startOrderPayment(c.Context(), global.ORM, customer.ID, orderID, in.Channel, time.Now())
	if err != nil {
		// errPaymentGatewayUnavailable 未纳入 businessError 的公共映射，单独给出 503 与明确文案。
		if errors.Is(err, errPaymentGatewayUnavailable) {
			return fail(c, fiber.StatusServiceUnavailable, err.Error())
		}
		return businessError(c, err)
	}
	return reply(c, view)
}

// paymentGatewayNotifyHandler 在线支付渠道验签回调。
//
// 渠道编码取自路径参数 :channel（推荐路由 /payment-notify/:channel），
// 兼容既有 /payment-notify 路由时退化为读 X-Pay-Channel 请求头。
// 签名读取 X-Pay-Timestamp + X-Pay-Signature：签名头既可以是裸 hex（此时用时间戳头拼装），
// 也可以是 "t=<unix>,v1=<hex>" 复合串。body 必须使用原始字节，不能先解析再序列化。
func paymentGatewayNotifyHandler(c fiber.Ctx) error {
	channel := strings.TrimSpace(c.Params("channel"))
	if channel == "" {
		channel = strings.TrimSpace(c.Get("X-Pay-Channel"))
	}
	body := c.Body()
	signature := strings.TrimSpace(c.Get("X-Pay-Signature"))
	timestamp := strings.TrimSpace(c.Get("X-Pay-Timestamp"))
	if channel == "" || len(body) == 0 || signature == "" {
		return fail(c, fiber.StatusBadRequest, "回调请求无效")
	}
	if !strings.HasPrefix(signature, "t=") && timestamp != "" {
		signature = "t=" + timestamp + "," + paymentSignatureVersion + "=" + signature
	}
	outcome, err := handlePaymentNotification(c.Context(), global.ORM, channel, body, signature, time.Now())
	if err != nil {
		return paymentNotifyFailure(c, err)
	}
	return reply(c, fiber.Map{
		"receipt_id": outcome.ReceiptID, "applied_cents": outcome.AppliedCents,
		"exception_cents": outcome.ExceptionCents, "order_dispatched": outcome.OrderDispatched,
	})
}

// paymentNotifyFailure 回调错误到 HTTP 状态的映射：200 成功 / 400 载荷无效 /
// 403 验签失败 / 409 幂等冲突 / 503 渠道未开通或暂不可用（渠道可稍后重试）。
func paymentNotifyFailure(c fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, errForbidden):
		return fail(c, fiber.StatusForbidden, err.Error())
	case errors.Is(err, errConflict), ent.IsConstraintError(err):
		return fail(c, fiber.StatusConflict, errConflict.Error())
	case errors.Is(err, errInvalidInput), ent.IsValidationError(err), ent.IsNotFound(err):
		return fail(c, fiber.StatusBadRequest, errInvalidInput.Error())
	default:
		return fail(c, fiber.StatusServiceUnavailable, errPaymentGatewayUnavailable.Error())
	}
}
