package esports

import (
	"errors"
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

// publicGiftView 是客户端礼物目录的公开字段：不含分成配置等内部信息。
type publicGiftView struct {
	ID         int    `json:"id"`
	Code       string `json:"code"`
	Name       string `json:"name"`
	Icon       string `json:"icon"`
	PriceCents int64  `json:"price_cents"`
	Category   string `json:"category"`
	HighValue  bool   `json:"high_value"`
	RequireAge bool   `json:"require_age"`
	Status     string `json:"status"`
}

func toPublicGiftView(item *ent.EsportsGift) publicGiftView {
	return publicGiftView{
		ID: item.ID, Code: item.Code, Name: item.Name, Icon: item.Icon,
		PriceCents: item.PriceCents, Category: item.Category,
		HighValue: item.HighValue, RequireAge: item.RequireAge,
		Status: item.Status.String(),
	}
}

// publicGifts 客户端礼物目录（AuthPublic）：仅返回在售礼物的公开字段。
func publicGifts(c fiber.Ctx) error {
	items, err := listGifts(c.Context(), global.ORM, "on_sale", queryLimit(c, 100))
	if err != nil {
		return businessError(c, err)
	}
	views := make([]publicGiftView, 0, len(items))
	for _, item := range items {
		views = append(views, toPublicGiftView(item))
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
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	customerID := queryInt(c, "customer_id")
	playerID := queryInt(c, "player_id")
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	items, err := listGiftOrders(c.Context(), global.ORM, adminID, isSuper, customerID, playerID, c.Query("review_status"), queryLimit(c, 200))
	if err != nil {
		return businessError(c, err)
	}
	views, err := giftOrderViewsOf(c.Context(), global.ORM, items)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, views)
}

func adminGiftIntents(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	items, err := listAdminGiftIntents(c.Context(), global.ORM, adminID, isSuper)
	if err != nil {
		return businessError(c, err)
	}
	views, err := adminGiftIntentViewsOf(c.Context(), global.ORM, items)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, views)
}

func createGiftIntentHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in giftIntentInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	item, err := createGiftIntent(c.Context(), global.ORM, adminID, isSuper, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

func userGiftIntents(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	items, err := listCustomerGiftIntents(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// userCreateGiftIntentHandler 客户自助创建礼物确认单：解析登录客户并按本人身份校验。
func userCreateGiftIntentHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	var in customerGiftIntentInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := createCustomerGiftIntent(c.Context(), global.ORM, uid, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, toGiftIntentView(item))
}

func userConfirmGiftIntent(c fiber.Ctx) error {
	id, err := parsePathID(c, "礼物确认单")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		ExpectedVersion int  `json:"expected_version"`
		Accepted        bool `json:"accepted"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	uid, _ := c.Locals("uid").(string)
	item, err := confirmGiftIntent(c.Context(), global.ORM, uid, id, in.ExpectedVersion, in.Accepted, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, item)
}

// sendGiftHandler 只能执行客户已明确确认的礼物单。
func sendGiftHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		IntentID int `json:"intent_id"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	order, err := executeGiftIntent(c.Context(), global.ORM, adminID, isSuper, in.IntentID, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

// refundGiftHandler 礼物退款：退还本金并冲回礼物分成与成长值。
func refundGiftHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "礼物单据")
	if err != nil {
		return businessError(c, err)
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	if _, err = loadOwnedGiftOrder(c.Context(), global.ORM, adminID, isSuper, id); err != nil {
		return businessError(c, err)
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	order, err := refundGift(c.Context(), global.ORM, adminID, isSuper, id, in.Reason, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

// reviewGiftHandler 复核疑似关联礼物：approve 释放分成，reject 不释放。
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

// giftLeaderboardHandler 礼物榜单：board=customer 客户贡献榜 / player 陪玩收礼榜。
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

// giftTierView 是礼物价格档位的对外返回结构。
type giftTierView struct {
	ID         int       `json:"id"`
	GiftID     int       `json:"gift_id"`
	TierCode   string    `json:"tier_code"`
	Name       string    `json:"name"`
	PriceCents int64     `json:"price_cents"`
	HighValue  bool      `json:"high_value"`
	RequireAge bool      `json:"require_age"`
	IsDefault  bool      `json:"is_default"`
	SortOrder  int       `json:"sort_order"`
	Status     string    `json:"status"`
	Version    int       `json:"version"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func toGiftTierView(item *ent.EsportsGiftTier) giftTierView {
	return giftTierView{
		ID: item.ID, GiftID: item.GiftID, TierCode: item.TierCode, Name: item.Name,
		PriceCents: item.PriceCents, HighValue: item.HighValue, RequireAge: item.RequireAge,
		IsDefault: item.IsDefault, SortOrder: item.SortOrder,
		Status: item.Status.String(), Version: item.Version,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

// giftTierListHandler 礼物的价格档位列表。
func giftTierListHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	giftID, err := parsePathID(c, "礼物")
	if err != nil {
		return businessError(c, err)
	}
	items, err := listGiftTiers(c.Context(), global.ORM, giftID, c.Query("status"))
	if err != nil {
		return businessError(c, err)
	}
	views := make([]giftTierView, 0, len(items))
	for _, item := range items {
		views = append(views, toGiftTierView(item))
	}
	return reply(c, views)
}

// createGiftTierHandler 新建礼物价格档位。
func createGiftTierHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	giftID, err := parsePathID(c, "礼物")
	if err != nil {
		return businessError(c, err)
	}
	var in giftTierInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	in.GiftID = giftID
	item, err := createGiftTier(c.Context(), global.ORM, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, toGiftTierView(item))
}

// updateGiftTierHandler 编辑价格档位；gift_id / tier_code 不可改。
func updateGiftTierHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "礼物档位")
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		giftTierInput
		ExpectedVersion int `json:"expected_version"`
	}
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	item, err := updateGiftTier(c.Context(), global.ORM, id, in.ExpectedVersion, in.giftTierInput)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, toGiftTierView(item))
}

// deleteGiftTierHandler 删除价格档位：已被礼物单引用时只能停用。
func deleteGiftTierHandler(c fiber.Ctx) error {
	if _, err := requireAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "礼物档位")
	if err != nil {
		return businessError(c, err)
	}
	if err := deleteGiftTier(c.Context(), global.ORM, id, queryInt(c, "expected_version")); err != nil {
		return businessError(c, err)
	}
	return reply(c, map[string]any{"id": id, "deleted": true})
}

// startGiftPaymentHandler 对礼物确认单发起收款：未开通渠道明确失败。
func startGiftPaymentHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "礼物确认单")
	if err != nil {
		return businessError(c, err)
	}
	var in giftPaymentStartInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	view, err := startGiftPayment(c.Context(), global.ORM, adminID, isSuper, id, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// verifyGiftPaymentHandler 财务核销礼物收款并成单：发起人不得自审。
func verifyGiftPaymentHandler(c fiber.Ctx) error {
	adminID, err := requireAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	id, err := parsePathID(c, "礼物确认单")
	if err != nil {
		return businessError(c, err)
	}
	var in giftPaymentVerifyInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	isSuper, _ := c.Locals("isSuperAdmin").(bool)
	order, err := verifyGiftPayment(c.Context(), global.ORM, adminID, isSuper, id, in, time.Now())
	if errors.Is(err, errGiftPaymentRejected) {
		return reply(c, map[string]any{"intent_id": id, "payment_status": "failed"})
	}
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

// giftPaymentNotifyHandler 在线渠道回调核销礼物收款（，AuthPublic）：
// 请求体为原始字节（验签依据），通道与签名从请求头 / 查询串取。
func giftPaymentNotifyHandler(c fiber.Ctx) error {
	channel := c.Query("channel")
	if channel == "" {
		channel = c.Get("X-Pay-Channel")
	}
	signature := c.Get("X-Pay-Signature")
	body := c.Body()
	order, err := handleGiftPaymentNotification(c.Context(), global.ORM, channel, body, signature, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, order)
}

// userPlayerGiftRecordsHandler 陪玩端收礼记录与本人榜单排名。
func userPlayerGiftRecordsHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	period := c.Query("period")
	if period == "" {
		period = "daily"
	}
	view, err := listPlayerGiftRecords(c.Context(), global.ORM, uid, period, time.Now(), queryLimit(c, 20))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}
