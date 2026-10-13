package esports

import (
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/global"
)

// giftView 是礼物目录的对外返回结构。
type giftView struct {
	ID              int       `json:"id"`
	Code            string    `json:"code"`
	Name            string    `json:"name"`
	Icon            string    `json:"icon"`
	PriceCents      int64     `json:"price_cents"`
	Category        string    `json:"category"`
	CommissionMode  string    `json:"commission_mode"`
	CommissionBp    int       `json:"commission_bp"`
	CommissionCents int64     `json:"commission_cents"`
	HighValue       bool      `json:"high_value"`
	RequireAge      bool      `json:"require_age"`
	SortOrder       int       `json:"sort_order"`
	Status          string    `json:"status"`
	Version         int       `json:"version"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func toGiftView(item *ent.EsportsGift) giftView {
	return giftView{
		ID: item.ID, Code: item.Code, Name: item.Name, Icon: item.Icon,
		PriceCents: item.PriceCents, Category: item.Category,
		CommissionMode: item.CommissionMode.String(), CommissionBp: item.CommissionBp,
		CommissionCents: item.CommissionCents, HighValue: item.HighValue,
		RequireAge: item.RequireAge, SortOrder: item.SortOrder,
		Status: item.Status.String(), Version: item.Version,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

func adminGifts(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listGifts(c.Context(), global.ORM, c.Query("status"), queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	views := make([]giftView, 0, len(items))
	for _, item := range items {
		views = append(views, toGiftView(item))
	}
	return reply(c, views)
}

func createGiftHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	var in struct {
		giftInput
		Status string `json:"status"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	if in.Status == "" {
		in.Status = "draft"
	}
	item, err := createGift(c.Context(), global.ORM, in.giftInput, in.Status)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, toGiftView(item))
}

func updateGiftHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "礼物")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		giftInput
		ExpectedVersion int `json:"expected_version"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := updateGift(c.Context(), global.ORM, id, in.ExpectedVersion, in.giftInput)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, toGiftView(item))
}

func setGiftStatusHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "礼物")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		ExpectedVersion int    `json:"expected_version"`
		Status          string `json:"status"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := setGiftStatus(c.Context(), global.ORM, id, in.ExpectedVersion, in.Status)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, toGiftView(item))
}

func adminGiftOrders(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	customerID := queryInt(c, "customer_id")
	playerID := queryInt(c, "player_id")
	items, err := listGiftOrders(c.Context(), global.ORM, customerID, playerID, c.Query("review_status"), queryLimit(c, 200))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// sendGiftHandler 客服代客送礼（§9.2）：原子扣减余额本金、生成礼物收入与成长值。
func sendGiftHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in giftSendInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	order, err := sendGift(c.Context(), global.ORM, adminID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

// refundGiftHandler 礼物退款（§9.5）：退还本金并冲回礼物分成与成长值。
func refundGiftHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "礼物单据")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	order, err := refundGift(c.Context(), global.ORM, adminID, id, in.Reason, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

// reviewGiftHandler 复核疑似关联礼物（§9.4）：approve 释放分成，reject 不释放。
func reviewGiftHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "礼物单据")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Decision string `json:"decision"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	order, err := reviewGiftOrder(c.Context(), global.ORM, adminID, id, in.Decision, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

// giftLeaderboardHandler 礼物榜单（§9.4）：board=customer 客户贡献榜 / player 陪玩收礼榜。
func giftLeaderboardHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	board := c.Query("board")
	period := c.Query("period")
	if board == "" {
		board = "player"
	}
	if period == "" {
		period = "daily"
	}
	rows, err := giftLeaderboard(c.Context(), global.ORM, board, period, time.Now(), queryLimit(c, 20))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, rows)
}
