package esports

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// adminPaymentAccounts 收款账户列表（§4.3）：按渠道 / 状态筛选。
func adminPaymentAccounts(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listPaymentAccounts(c.Context(), global.ORM, c.Query("channel"), c.Query("status"), queryLimit(c, 200))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// createPaymentAccountHandler 提交收款账户资料：统一进入待财务审核，客服不能直接启用。
func createPaymentAccountHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in paymentAccountInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := createPaymentAccount(c.Context(), global.ORM, in, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// reviewPaymentAccountHandler 财务审核收款账户：通过即启用；提交人不得自审。
func reviewPaymentAccountHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "收款账户")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Approve    bool   `json:"approve"`
		Resolution string `json:"resolution"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := reviewPaymentAccount(c.Context(), global.ORM, id, adminID, in.Approve, in.Resolution, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// disablePaymentAccountHandler 停用收款账户：停用后不再接受新的到账核销。
func disablePaymentAccountHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "收款账户")
	if err != nil {
		return businessError(c, err)
	}
	item, err := disablePaymentAccount(c.Context(), global.ORM, id, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// adminPaymentChannels 收款方式启停状态（§4.3）：含启用中的账户数量。
func adminPaymentChannels(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listPaymentChannels(c.Context(), global.ORM)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// setPaymentChannelStatusHandler 启停收款方式：停用后不再接受新的到账凭证。
func setPaymentChannelStatusHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	channel := strings.TrimSpace(c.Params("channel"))
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.Bind().Body(&in); err != nil || in.Enabled == nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	if err := setPaymentChannelEnabled(c.Context(), global.ORM, channel, *in.Enabled); err != nil {
		return businessError(c, err)
	}
	items, err := listPaymentChannels(c.Context(), global.ORM)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// adminOfflinePaymentProofs 待核验收款单列表（§4.2）：财务核验外部到账的入口。
func adminOfflinePaymentProofs(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	orderID := queryInt(c, "order_id")
	items, err := listOfflinePaymentProofs(c.Context(), global.ORM, c.Query("status"), orderID, queryLimit(c, 200))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// submitOfflinePaymentProofHandler 客服提交外部到账凭证：生成「待核验收款」，核销前订单不派单。
func submitOfflinePaymentProofHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in offlinePaymentProofInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := submitOfflinePaymentProof(c.Context(), global.ORM, in, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// verifyOfflinePaymentProofHandler 财务核验并核销外部到账：订单进入待派单，提交人不得自审。
func verifyOfflinePaymentProofHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "待核验收款")
	if err != nil {
		return businessError(c, err)
	}
	item, err := verifyOfflinePaymentProof(c.Context(), global.ORM, id, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// rejectOfflinePaymentProofHandler 财务驳回外部到账凭证（须填原因）。
func rejectOfflinePaymentProofHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "待核验收款")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Resolution string `json:"resolution"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := rejectOfflinePaymentProof(c.Context(), global.ORM, id, adminID, in.Resolution, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}
