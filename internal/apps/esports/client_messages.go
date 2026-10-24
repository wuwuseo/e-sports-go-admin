package esports

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/message"
	"go-admin/internal/global"
)

// 客户端站内消息中心：
// 通知投递侧的寻址约定是 customer:<customer_id> / player:<player_id>（见
// notification_recipients.go），本文件按同一地址读取 ent.Message，补齐客户与陪玩的
// 「可读」闭环。列表只返回展示必需字段，事件载荷 / extra 等内部字段不出网关。
//
// 数据边界完全由地址决定：地址由登录态解析（客户走 customerForVerifiedUser，
// 陪玩走 requestPlayer），调用方无法通过参数读取他人消息；标记已读同样以
// user_uid == address 作为更新条件，影响行数天然受限。

const (
	clientMessageDefaultLimit = 30
	clientMessageMaxLimit     = 100
	// clientMessageSummaryRunes 限制回给客户端的内容长度，避免历史脏数据把整段正文回传。
	clientMessageSummaryRunes = 200
)

// clientMessageView 是客户端可见的站内消息视图，不包含 extra / content_html 等内部字段。
type clientMessageView struct {
	ID        int        `json:"id"`
	Type      string     `json:"type"`
	Title     string     `json:"title"`
	Summary   string     `json:"summary"`
	CreatedAt time.Time  `json:"created_at"`
	ReadAt    *time.Time `json:"read_at"`
}

// listClientMessages 按收件地址倒序分页读取站内消息。
// limit 缺省 30、上限 100；beforeID>0 时作为游标只取更早的消息；unreadOnly 只看未读。
func listClientMessages(ctx context.Context, db *ent.Client, address string, limit, beforeID int, unreadOnly bool) ([]clientMessageView, error) {
	address = strings.TrimSpace(address)
	if db == nil || address == "" {
		return nil, errInvalidInput
	}
	if limit < 1 {
		limit = clientMessageDefaultLimit
	}
	if limit > clientMessageMaxLimit {
		limit = clientMessageMaxLimit
	}
	query := db.Message.Query().Where(message.UserUIDEQ(address))
	if beforeID > 0 {
		query = query.Where(message.IDLT(beforeID))
	}
	if unreadOnly {
		query = query.Where(message.ReadAtIsNil())
	}
	rows, err := query.Order(ent.Desc(message.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]clientMessageView, 0, len(rows))
	for _, row := range rows {
		views = append(views, clientMessageViewOf(row))
	}
	return views, nil
}

// markClientMessagesRead 把本人地址下指定的未读消息标记为已读，返回实际更新条数。
// 更新条件包含 user_uid == address，即使传入他人消息 ID 也不会被修改。
func markClientMessagesRead(ctx context.Context, db *ent.Client, address string, ids []int) (int, error) {
	address = strings.TrimSpace(address)
	if db == nil || address == "" {
		return 0, errInvalidInput
	}
	unique := make([]int, 0, len(ids))
	seen := make(map[int]bool, len(ids))
	for _, id := range ids {
		if id < 1 || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return 0, nil
	}
	return db.Message.Update().
		Where(
			message.UserUIDEQ(address),
			message.IDIn(unique...),
			message.ReadAtIsNil(),
		).
		SetReadAt(time.Now().UTC()).
		Save(ctx)
}

func clientMessageViewOf(row *ent.Message) clientMessageView {
	return clientMessageView{
		ID:        row.ID,
		Type:      row.Type,
		Title:     row.Title,
		Summary:   clientMessageSummary(row.Content),
		CreatedAt: row.CreatedAt,
		ReadAt:    row.ReadAt,
	}
}

// clientMessageSummary 截断正文，保证回传内容长度可控（按 rune 计，避免截断中文）。
func clientMessageSummary(content string) string {
	content = strings.TrimSpace(content)
	runes := []rune(content)
	if len(runes) <= clientMessageSummaryRunes {
		return content
	}
	return string(runes[:clientMessageSummaryRunes]) + "…"
}

// userMessagesHandler 客户站内消息列表（GET /user/messages）。
func userMessagesHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	items, err := listClientMessages(c.Context(), global.ORM, customerNotificationAddress(customer.ID),
		clientMessageQueryLimit(c), clientMessageBeforeID(c), clientMessageUnreadOnly(c))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// playerMessagesHandler 陪玩站内消息列表（GET /user/player/messages），参数与客户侧一致。
func playerMessagesHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	items, err := listClientMessages(c.Context(), global.ORM, playerNotificationAddress(player.ID),
		clientMessageQueryLimit(c), clientMessageBeforeID(c), clientMessageUnreadOnly(c))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// clientMessageReadInput 是标记已读的请求体。
type clientMessageReadInput struct {
	IDs []int `json:"ids"`
}

// userMessagesMarkReadHandler 客户标记本人消息已读（POST /user/messages/read）。
func userMessagesMarkReadHandler(c fiber.Ctx) error {
	uid, _ := c.Locals("uid").(string)
	customer, err := customerForVerifiedUser(c.Context(), global.ORM, uid)
	if err != nil {
		return businessError(c, err)
	}
	return markClientMessagesReadHandler(c, customerNotificationAddress(customer.ID))
}

// playerMessagesMarkReadHandler 陪玩标记本人消息已读（POST /user/player/messages/read）。
func playerMessagesMarkReadHandler(c fiber.Ctx) error {
	player, err := requestPlayer(c)
	if err != nil {
		return businessError(c, err)
	}
	return markClientMessagesReadHandler(c, playerNotificationAddress(player.ID))
}

func markClientMessagesReadHandler(c fiber.Ctx, address string) error {
	var in clientMessageReadInput
	if err := c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	if len(in.IDs) == 0 {
		return fail(c, fiber.StatusBadRequest, "消息 ID 不能为空")
	}
	updated, err := markClientMessagesRead(c.Context(), global.ORM, address, in.IDs)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{"updated": updated})
}

// clientMessageQueryLimit 解析 limit：缺省 30、上限 100（与 listClientMessages 保持一致）。
func clientMessageQueryLimit(c fiber.Ctx) int {
	value, err := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	if err != nil || value < 1 {
		return clientMessageDefaultLimit
	}
	if value > clientMessageMaxLimit {
		return clientMessageMaxLimit
	}
	return value
}

// clientMessageBeforeID 解析 before_id 游标；非法值视为首页。
func clientMessageBeforeID(c fiber.Ctx) int {
	value, err := strconv.Atoi(strings.TrimSpace(c.Query("before_id")))
	if err != nil || value < 1 {
		return 0
	}
	return value
}

// clientMessageUnreadOnly 解析 unread_only：支持 1 / true。
func clientMessageUnreadOnly(c fiber.Ctx) bool {
	switch strings.ToLower(strings.TrimSpace(c.Query("unread_only"))) {
	case "1", "true":
		return true
	}
	return false
}
