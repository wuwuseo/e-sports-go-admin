package esports

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscheckout"
	"go-admin/internal/global"
)

// 聚合收款 HTTP 入口：
//   - 客户侧：创建 / 查询本人业务单的 checkout；
//   - 公开侧：扫码页按入口令牌读取最小信息、创建支付尝试、轮询支付状态；
//   - 服务商回调：独立验签（不复用 http_hmac 网关验签器），验签通过后才进入结算边界；
//   - 管理端：只读查看服务商配置脱敏视图（密钥永不回显）。

// userCreateCheckoutHandler POST /user/checkouts
func userCreateCheckoutHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	var in checkoutCreateInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := createOrReuseCheckout(c.Context(), global.ORM, customer, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	view, err := checkoutViewOf(c.Context(), global.ORM, item)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// userCheckoutHandler GET /user/checkouts/:id
func userCheckoutHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "聚合收款入口")
	if err != nil {
		return businessError(c, err)
	}
	item, err := loadCheckoutByID(c.Context(), global.ORM, id, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	if item.CustomerID != customer.ID {
		return businessError(c, errForbidden)
	}
	view, err := checkoutViewOf(c.Context(), global.ORM, item)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// publicCheckoutHandler GET /public/checkouts/:token
// 只返回扫码页所需的最小信息，不暴露业务单号、客户与服务商配置。
func publicCheckoutHandler(c fiber.Ctx) error {
	item, err := loadCheckoutByToken(c.Context(), global.ORM, c.Params("token"), time.Now())
	if err != nil {
		return businessError(c, err)
	}
	wechat, alipay := false, false
	// 只有「聚合动态支付」入口才展示微信 / 支付宝渠道；固定码模式由收款方式列表给出账户。
	if item.Status == esportscheckout.StatusOpen && item.ChannelMode == esportscheckout.ChannelModeAggregate {
		availableWechat, availableAlipay, _, availabilityErr := checkoutChannelAvailability(c.Context(), global.ORM)
		if availabilityErr == nil {
			wechat, alipay = availableWechat, availableAlipay
		}
	}
	return reply(c, publicCheckoutView{
		BusinessType: item.BusinessType.String(), AmountCents: item.AmountCents,
		Status: item.Status.String(), BusinessExpiresAt: item.BusinessExpiresAt,
		Channels: publicCheckoutChannels{Wechat: wechat, Alipay: alipay},
	})
}

// publicCheckoutAttemptHandler POST /public/checkouts/:token/attempts
func publicCheckoutAttemptHandler(c fiber.Ctx) error {
	item, err := loadCheckoutByToken(c.Context(), global.ORM, c.Params("token"), time.Now())
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		PayChannel string `json:"pay_channel"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	ctx := context.WithValue(c.Context(), checkoutClientIPKey{}, c.IP())
	attempt, err := createCheckoutAttempt(ctx, global.ORM, item, in.PayChannel, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, checkoutAttemptViewOf(attempt))
}

// publicCheckoutStatusHandler GET /public/checkouts/:token/status
func publicCheckoutStatusHandler(c fiber.Ctx) error {
	item, err := loadCheckoutByToken(c.Context(), global.ORM, c.Params("token"), time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, checkoutStatusView{
		Status: item.Status.String(), Paid: item.Status == esportscheckout.StatusPaid,
	})
}

// adminPaymentProviders GET /admin/payment-providers
// 只读脱敏视图：密钥只显示「是否已配置」，任何情况下都不回显原值。
func adminPaymentProviders(c fiber.Ctx) error {
	cfg, err := loadPaymentProvidersConfig(c.Context(), global.ORM)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, maskedPaymentProviders(cfg))
}

// shouqianbaPaymentNotifyHandler POST /payment-notify/shouqianba
func shouqianbaPaymentNotifyHandler(c fiber.Ctx) error {
	return checkoutNotifyHandler(c, checkoutProviderShouqianba)
}

// huifuPaymentNotifyHandler POST /payment-notify/huifu
func huifuPaymentNotifyHandler(c fiber.Ctx) error {
	return checkoutNotifyHandler(c, checkoutProviderHuifu)
}

// checkoutNotifyHandler 服务商回调：独立验签 → 结算 → 按服务商要求应答。
// 验签失败 / 临时故障返回失败应答（服务商会重试）；业务不可入账已转异常事件，回应成功停止重试。
func checkoutNotifyHandler(c fiber.Ctx, providerName string) error {
	provider, err := checkoutProviderFor(c.Context(), global.ORM, providerName)
	if err != nil {
		// 配置缺失时无法验签，明确 503 让服务商重试；不回显配置细节。
		return c.Status(http.StatusServiceUnavailable).SendString("provider unavailable")
	}
	headers := map[string]string{}
	c.Request().Header.VisitAll(func(key, value []byte) {
		headers[string(key)] = string(value)
	})
	notice, err := provider.VerifyNotification(c.Context(), headers, c.Body())
	if err != nil {
		status, ack := provider.NotifyAck(false, "")
		return c.Status(status).SendString(ack)
	}
	_, settleErr := settleCheckoutPayment(c.Context(), global.ORM, providerName, notice, time.Now())
	if settleErr != nil {
		if isCheckoutBusinessException(settleErr) || isCheckoutNotFound(settleErr) {
			// 异常到账已写外盒事件交财务；未知商户单号不是本站业务，均不再让服务商重试。
			status, ack := provider.NotifyAck(true, notice.RequestID)
			return c.Status(status).SendString(ack)
		}
		status, ack := provider.NotifyAck(false, "")
		return c.Status(status).SendString(ack)
	}
	status, ack := provider.NotifyAck(true, notice.RequestID)
	return c.Status(status).SendString(ack)
}

// isCheckoutNotFound 判断错误是否为「按商户单号找不到尝试」。
func isCheckoutNotFound(err error) bool {
	return err != nil && ent.IsNotFound(err)
}

// parseQueryBusinessID 解析 business_id 查询参数。
func parseQueryBusinessID(raw string) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 1 {
		return 0, errInvalidInput
	}
	return value, nil
}
