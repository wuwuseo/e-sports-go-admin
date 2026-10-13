package esports

import (
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// 优先派单权（§5.4）与派单候选排序（§5.5 首版），挂「排单」菜单。

// adminOrderPriorityNotices 某订单的优先通知明细：谁被通知、谁拒绝、谁接单（§5.4 业务口径）。
func adminOrderPriorityNotices(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	items, err := listOrderDispatchNotices(c.Context(), global.ORM, orderID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// releaseOrderPriorityHandler 客服提前把优先通知窗口释放到抢单大厅（§5.2 可随时人工干预）。
func releaseOrderPriorityHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	order, err := releaseDispatchPriorityNotice(c.Context(), global.ORM, orderID, in.Reason, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

// playerDispatchNotices 陪玩端「我的优先通知」：窗口内待回应的通知。
func playerDispatchNotices(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	items, err := listPlayerDispatchNotices(c.Context(), global.ORM, player.ID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// declineDispatchNoticeHandler 陪玩端拒绝优先通知；名单内全部拒绝时订单释放到大厅。
func declineDispatchNoticeHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	noticeID, err := parsePathID(c, "优先通知")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	notice, err := declineDispatchPriorityNotice(c.Context(), global.ORM, player.ID, noticeID, in.Reason, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, notice)
}

func adminDispatchPriority(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listDispatchPriority(c.Context(), global.ORM, c.Query("game"), c.Query("status"), queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// grantDispatchPriorityHandler 授予优先派单权（next_period=true 时预生成下一期名单）。
func grantDispatchPriorityHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in dispatchPriorityInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	grant, err := grantDispatchPriority(c.Context(), global.ORM, adminID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, grant)
}

// revokeDispatchPriorityHandler 中途把陪玩移出名单（须填原因）。
func revokeDispatchPriorityHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "优先派单权")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	grant, err := revokeDispatchPriority(c.Context(), global.ORM, adminID, id, in.Reason, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, grant)
}

// adminDispatchCandidates 按 §5.5 排出该订单的派单候选（硬性条件过滤 + 优先派单权加权）。
func adminDispatchCandidates(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	order, err := global.ORM.EsportsOrder.Get(c.Context(), orderID)
	if err != nil {
		return businessError(c, err)
	}
	game, err := orderGame(c.Context(), global.ORM, order.SkuID)
	if err != nil {
		return businessError(c, err)
	}
	// 未显式传参时回落订单上的指定陪玩（§5.4）：指定单的候选应把本人置顶。
	preferred := queryInt(c, "preferred_player_id")
	if preferred < 1 && order.PreferredPlayerID != nil {
		preferred = *order.PreferredPlayerID
	}
	items, err := dispatchCandidates(c.Context(), global.ORM, order,
		preferred, queryInt(c, "limit"), time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{"game": game, "candidates": items})
}
