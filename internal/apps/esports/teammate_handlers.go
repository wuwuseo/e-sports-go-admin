package esports

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// teammateInput 陪玩发起固排邀请入参。
type teammateInput struct {
	TeammatePlayerID int    `json:"teammate_player_id"`
	Game             string `json:"game"`
	IdempotencyKey   string `json:"idempotency_key"`
}

func playerTeammatesHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	views, err := listConfirmedTeammates(c.Context(), global.ORM, player.ID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, views)
}

func playerTeammateRequestsHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	views, err := listPendingTeammateRequests(c.Context(), global.ORM, player.ID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, views)
}

func playerRequestTeammateHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	var in teammateInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	relation, err := requestTeammate(c.Context(), global.ORM, player.ID, in.TeammatePlayerID,
		in.Game, in.IdempotencyKey, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, relation)
}

func playerAcceptTeammateHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "固排")
	if err != nil {
		return businessError(c, err)
	}
	relation, err := acceptTeammateRequest(c.Context(), global.ORM, player.ID, id, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, relation)
}

func playerDeclineTeammateHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "固排")
	if err != nil {
		return businessError(c, err)
	}
	relation, err := declineTeammateRequest(c.Context(), global.ORM, player.ID, id, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, relation)
}

func playerDissolveTeammateHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "固排")
	if err != nil {
		return businessError(c, err)
	}
	relation, err := dissolveTeammate(c.Context(), global.ORM, player.ID, id, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, relation)
}

func playerSearchPlayersHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	players, err := searchTeammateCandidates(c.Context(), global.ORM, c.Query("keyword"), player.ID, queryLimit(c, 20))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, players)
}

// teamDispatchInput 客服固排一次成队入参。
type teamDispatchInput struct {
	MainPlayerID      int   `json:"main_player_id"`
	TeammatePlayerIDs []int `json:"teammate_player_ids"`
}

// adminTeammateCandidates 返回主陪玩在该订单上可一起成队的合格固排。
func adminTeammateCandidates(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	mainPlayerID, err := strconv.Atoi(c.Query("main_player_id"))
	if err != nil || mainPlayerID < 1 {
		return businessError(c, errInvalidInput)
	}
	items, err := listTeammateCandidatesForOrder(c.Context(), global.ORM, orderID, mainPlayerID)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// teamDispatchHandler 客服勾选主陪玩 + 固排一次成队。
func teamDispatchHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	orderID, err := parsePathID(c, "订单")
	if err != nil {
		return businessError(c, err)
	}
	var in teamDispatchInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	seats, err := teamDispatch(c.Context(), global.ORM, orderID, in.MainPlayerID, in.TeammatePlayerIDs, adminID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, seats)
}
