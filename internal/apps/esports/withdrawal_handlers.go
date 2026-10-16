package esports

import (
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// 提现（§6.3 / §12.6）：陪玩端自助申请，管理端财务审核与打款登记。管理端挂「结算」菜单。

func adminWithdrawals(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listWithdrawals(c.Context(), global.ORM,
		queryInt(c, "player_id"), c.Query("status"), queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func adminWithdrawalDetail(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "提现单")
	if err != nil {
		return businessError(c, err)
	}
	item, err := getWithdrawal(c.Context(), global.ORM, id)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// reviewWithdrawalHandler 财务审核提现：通过与驳回都不得自审以外的越权（首版一律财务审核）。
func reviewWithdrawalHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "提现单")
	if err != nil {
		return businessError(c, err)
	}
	var in withdrawalReviewInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := reviewWithdrawal(c.Context(), global.ORM, adminID, id, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// payWithdrawalHandler 登记打款：留外部打款凭证与完税凭证（§11.2）。
func payWithdrawalHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "提现单")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		PayoutRef string `json:"payout_ref"`
		TaxRef    string `json:"tax_ref"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := payWithdrawal(c.Context(), global.ORM, adminID, id, in.PayoutRef, in.TaxRef, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// playerApplyWithdrawal 陪玩端申请提现（§6.3）：须携带幂等键，重复提交返回既有提现单。
func playerApplyWithdrawal(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	var in withdrawalApplyInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := applyWithdrawal(c.Context(), global.ORM, player.ID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// playerWithdrawals 陪玩端我的提现单列表。
func playerWithdrawals(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	items, err := listWithdrawals(c.Context(), global.ORM, player.ID, c.Query("status"), queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// playerWithdrawalSummaryHandler 陪玩端提现概览：可提现余额、待审金额与累计已提现。
func playerWithdrawalSummaryHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	view, err := playerWithdrawalSummary(c.Context(), global.ORM, player.ID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// requestWithdrawalPayoutHandler 财务提交打款执行（§11.2 补全）：仅「已审核通过」的提现单可提交。
func requestWithdrawalPayoutHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "提现单")
	if err != nil {
		return businessError(c, err)
	}
	var in requestWithdrawalPayoutInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := requestWithdrawalPayout(c.Context(), global.ORM, adminID, id, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// markWithdrawalPayoutPaidHandler 标记打款成功（§11.2 补全）：回填完税凭证、到账时间与打款人。
func markWithdrawalPayoutPaidHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "打款执行单")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		TaxRef string `json:"tax_ref"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := markWithdrawalPayoutPaid(c.Context(), global.ORM, adminID, id, in.TaxRef, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// markWithdrawalPayoutFailedHandler 标记打款失败（§11.2 补全）：提现单保持已审核通过，可重新提交打款。
func markWithdrawalPayoutFailedHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "打款执行单")
	if err != nil {
		return businessError(c, err)
	}
	var in failWithdrawalPayoutInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := markWithdrawalPayoutFailed(c.Context(), global.ORM, adminID, id, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// listWithdrawalPayoutsHandler 查看某提现单的打款执行记录。
func listWithdrawalPayoutsHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "提现单")
	if err != nil {
		return businessError(c, err)
	}
	items, err := listWithdrawalPayouts(c.Context(), global.ORM, id, queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// registerExternalRefundHandler 财务登记外部渠道退款凭证（§13 补全）。
func registerExternalRefundHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in registerExternalRefundInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := registerExternalRefund(c.Context(), global.ORM, adminID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// executeExternalRefundHandler 财务核验并执行外部退款（§13 补全）：对齐订单退款状态。
func executeExternalRefundHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "外部退款登记")
	if err != nil {
		return businessError(c, err)
	}
	item, err := executeExternalRefund(c.Context(), global.ORM, adminID, id, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// listExternalRefundsHandler 查询外部退款登记，可按订单与状态过滤。
func listExternalRefundsHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	orderID, err := queryIntDefault(c, "order_id", 0)
	if err != nil {
		return businessError(c, errInvalidInput)
	}
	items, err := listExternalRefunds(c.Context(), global.ORM, orderID, c.Query("status"), queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}
