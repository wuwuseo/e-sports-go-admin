package esports

import (
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// §3.3 限时改期提案的 HTTP 入口。已付款订单改期一律走提案；未付款订单仍走
// PUT /admin/orders/:id/appointment 的直接改期。
//
//	POST /admin/orders/:id/reschedule-proposals                    客服发起提案
//	GET  /admin/orders/:id/reschedule-proposals                    查看提案与确认进度
//	POST /admin/reschedule-proposals/:id/withdraw                  客服撤回进行中的提案
//	POST /user/reschedule-proposals/:id/confirm                    已绑定身份客户确认 / 拒绝
//	POST /user/player/reschedule-proposals/:id/confirm             本单已接单成员确认 / 拒绝

// ownedRescheduleProposalHandler 提案路由的可见范围校验：由提案反查订单再判归属。
func ownedRescheduleProposalHandler(next fiber.Handler) fiber.Handler {
	return func(c fiber.Ctx) error {
		adminID, err := requireAdmin(c)
		if err != nil {
			return businessError(c, err)
		}
		id, err := parsePathID(c, "改期提案")
		if err != nil {
			return businessError(c, err)
		}
		item, err := global.ORM.EsportsRescheduleProposal.Get(c.Context(), id)
		if err != nil {
			return businessError(c, err)
		}
		isSuper, _ := c.Locals("isSuperAdmin").(bool)
		if _, err = loadOwnedOrder(c.Context(), global.ORM, adminID, isSuper, item.OrderID); err != nil {
			return businessError(c, err)
		}
		return next(c)
	}
}

// proposeRescheduleHandler 客服对已付款订单发起限时改期提案（§3.3）：提案本身不改订单。
func proposeRescheduleHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in rescheduleProposalInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := proposeReschedule(c.Context(), global.ORM, id, adminID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// listOrderRescheduleProposalsHandler 查看订单的改期提案与进行中提案的确认进度（§3.3）。
func listOrderRescheduleProposalsHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	items, err := listRescheduleProposals(c.Context(), global.ORM, id)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// withdrawRescheduleProposalHandler 客服撤回进行中的改期提案（§3.3）：提案转 withdrawn，订单不变。
func withdrawRescheduleProposalHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "改期提案")
	if err != nil {
		return businessError(c, err)
	}
	item, err := withdrawRescheduleProposal(c.Context(), global.ORM, id, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// customerConfirmRescheduleProposalHandler 已绑定身份客户确认 / 拒绝本人订单的改期提案（§3.3）。
// 确认须来自已绑定的客户身份（customerForVerifiedUser），不接受客服代确认。
func customerConfirmRescheduleProposalHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	id, err := parsePathID(c, "改期提案")
	if err != nil {
		return businessError(c, err)
	}
	var in rescheduleProposalDecisionInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := confirmRescheduleProposalByCustomer(c.Context(), global.ORM, uid, id,
		in.Accept, in.Note, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// playerConfirmRescheduleProposalHandler 订单上已接单成员确认 / 拒绝改期提案（§3.3）：
// 仅本人在该订单持有 accepted 席位时可确认。
func playerConfirmRescheduleProposalHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	id, err := parsePathID(c, "改期提案")
	if err != nil {
		return businessError(c, err)
	}
	var in rescheduleProposalDecisionInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := confirmRescheduleProposalByPlayer(c.Context(), global.ORM, uid, id,
		in.Accept, in.Note, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}
