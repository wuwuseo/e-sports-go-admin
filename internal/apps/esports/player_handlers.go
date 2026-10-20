package esports

import (
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/global"
)

// requestPlayer 从当前登录用户解析陪玩档案，解析失败按 errForbidden 处理。
func requestPlayer(c fiber.Ctx) (*ent.EsportsPlayer, error) {
	uid, _ := c.Locals("uid").(string)
	return currentPlayer(c.Context(), global.ORM, uid)
}

func adminPlayers(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	// 新建陪玩时的用户搜索复用本路由（同一 players:list 权限），
	// 避免额外要求客服拥有系统用户管理权限。
	if c.Query("scope") == "user_candidates" {
		candidates, err := listPlayerUserCandidates(c.Context(), global.ORM,
			c.Query("keyword"), queryLimit(c, 50))
		if err != nil {
			return businessError(c, err)
		}
		return reply(c, candidates)
	}
	players, err := listPlayers(c.Context(), global.ORM, c.Query("keyword"), queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	views, err := playerViewsWithUsers(c.Context(), global.ORM, players)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, views)
}

// publicPlayers 客户端陪玩推荐（AuthPublic）：仅返回合格且在线的陪玩公开字段，
// 并按 player_recommend 组件 props 透传的查询参数（game / level / online / bookable）收窄（§16.3-07）。
func publicPlayers(c fiber.Ctx) error {
	props, err := parsePlayerRecommendProps(c)
	if err != nil {
		return businessError(c, err)
	}
	views, err := listPublicPlayersByProps(c.Context(), global.ORM, props, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, views)
}

func createPlayer(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	var in playerInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	player, err := savePlayer(c.Context(), global.ORM, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, player)
}

func updatePlayerHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "陪玩")
	if err != nil {
		return businessError(c, err)
	}
	var in playerUpdateInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	player, err := updatePlayer(c.Context(), global.ORM, id, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, player)
}

func playerDashboardHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	view, err := playerDashboard(c.Context(), global.ORM, player.ID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

func playerPresenceHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Online bool `json:"online"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	updated, err := setPlayerPresence(c.Context(), global.ORM, player.ID, in.Online)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, updated)
}

// playerHeartbeatHandler 陪玩端保活（§6.4）：只刷新心跳时戳，不改变在线状态。
func playerHeartbeatHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	updated, err := recordPlayerHeartbeat(c.Context(), global.ORM, player.ID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, updated)
}

// playerProfileHandler 陪玩端「我的资料 / 我的等级」（§6.1 / §6.4）：
// 返回本人档案（昵称、等级、资格、上下线、接单游戏与接单上限），供陪玩端展示。
func playerProfileHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, player)
}

func playerInvitesHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	invites, err := listPlayerInvites(c.Context(), global.ORM, player.ID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, invites)
}

func playerAcceptInviteHandler(c fiber.Ctx) error {
	return respondInviteHandler(c, true)
}

func playerRejectInviteHandler(c fiber.Ctx) error {
	return respondInviteHandler(c, false)
}

// respondInviteHandler 接受 / 拒绝邀请共用：:id 为席位 ID。
func respondInviteHandler(c fiber.Ctx, accept bool) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	seatID, err := parsePathID(c, "邀请")
	if err != nil {
		return businessError(c, err)
	}
	if err := respondPlayerInvite(c.Context(), global.ORM, player.ID, seatID, accept, time.Now()); err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{"seat_id": seatID, "accepted": accept})
}

func playerGrabHallHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	orders, err := listGrabHallItems(c.Context(), global.ORM, player.ID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, orders)
}

func playerGrabHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	order, err := grabPlayerOrder(c.Context(), global.ORM, player.ID, orderID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

func playerOrdersHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	orders, err := listPlayerOrders(c.Context(), global.ORM, player.ID, c.Query("bucket"))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, orders)
}

func playerStartServiceHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	order, err := startPlayerService(c.Context(), global.ORM, player.ID, orderID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

func playerServiceTimingsHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	items, err := listPlayerServiceTimings(c.Context(), global.ORM, player.ID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func playerPauseServiceHandler(c fiber.Ctx) error {
	return playerServicePauseHandler(c, true)
}

func playerResumeServiceHandler(c fiber.Ctx) error {
	return playerServicePauseHandler(c, false)
}

func playerServicePauseHandler(c fiber.Ctx, pause bool) error {
	player, err := requestPlayer(c)
	if err != nil {
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
	item, err := setPlayerServicePause(c.Context(), global.ORM, player.ID, orderID, pause, in.Reason, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func playerFinishServiceHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		ReportedUnits         int64 `json:"reported_units"`
		ReportedSeconds       int64 `json:"reported_seconds"`
		EvidenceAttachmentIDs []int `json:"evidence_attachment_ids"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	order, err := finishPlayerServiceWithEvidence(c.Context(), global.ORM, player.ID, orderID,
		in.ReportedUnits, in.ReportedSeconds, in.EvidenceAttachmentIDs, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

// playerCreateOrderAfterSaleHandler 陪玩就本人服务的订单发起售后申诉（§13.2）。
func playerCreateOrderAfterSaleHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	var in playerAfterSaleInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := createPlayerOrderAfterSale(c.Context(), global.ORM, player.ID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// playerOrderAfterSalesHandler 陪玩端查询本人相关售后（§13.2）。
func playerOrderAfterSalesHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	items, err := listPlayerOrderAfterSales(c.Context(), global.ORM, player.ID, queryLimit(c, 50))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// playerInviteTeammatesHandler 主陪玩在陪玩端就本人已接单的订单邀请固排占剩余席位（§5.3）。
func playerInviteTeammatesHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		TeammatePlayerIDs []int `json:"teammate_player_ids"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	seats, err := playerInviteTeammates(c.Context(), global.ORM, player.ID, orderID,
		in.TeammatePlayerIDs, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, seats)
}

// playerTeammateCandidatesHandler 陪玩端查看本人可邀请的固排候选（§5.3）。
func playerTeammateCandidatesHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	candidates, err := playerTeammateCandidates(c.Context(), global.ORM, player.ID, orderID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, candidates)
}
