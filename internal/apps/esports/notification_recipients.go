package esports

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"go-admin/internal/ent"
	entadmin "go-admin/internal/ent/admin"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsgiftorder"
	"go-admin/internal/ent/esportsorder"
)

// 通知收件人解析（§通知）。
//
// 寻址约定（ent.Message.user_uid）：
//   - 管理员：直接写 Admin.UID。后台消息中心（/api/v1/admin/auth/messages）按登录态 uid
//     （即 Admin.UID）读取，因此只有真实 UID 才能被客服看到；Admin 档案缺失时才退回
//     `admin:<admin_id>` 兜底地址。
//   - 客户 / 陪玩：写入 `customer:<customer_id>` / `player:<player_id>`。这两个前缀是稳定的
//     可寻址身份，推送网关（webhook）按 address 路由到客户端；站内消息由客户端消息中心
//     按同一地址读取：客户 GET /user/messages、陪玩 GET /user/player/messages
//     （见 client_messages.go），已读回执为对应 POST .../messages/read。
//
// 任何事件都必须解析出至少一个非空地址；解析不到时返回 errNotificationRecipientMissing，
// 由外盒退避重试直至 dead，绝不降级为 user_uid="" 的全员广播。

var errNotificationRecipientMissing = errors.New("通知事件没有可解析的收件人")

// 通知收件人角色。
const (
	notificationRoleCustomer = "customer"
	notificationRoleAdmin    = "admin"
	notificationRolePlayer   = "player"
)

// notificationRecipient 是一个可寻址的通知收件人。
// Role 是主角色，Roles 保留该收件人的全部角色（同一人多角色合并为一条，避免重复发送）。
type notificationRecipient struct {
	Role       string
	Roles      []string
	AdminID    int
	CustomerID int
	PlayerID   int
	Address    string
}

// resolveNotificationRecipients 按事件类型解析收件人：
//   - order.*                → 订单客户 + 责任客服 + 已接单席位陪玩
//   - aftersale.*            → 售后/来源客户 + 责任客服
//   - payment.*              → 责任客服（退款/拒付/撤销类同时通知客户）
//   - dispatch.remind_admin  → 责任客服
//   - gift.*                 → 送礼客户 + 收礼陪玩 + 礼物单快照的归属客服
//   - member.*               → 该客户 + 归属客服
//   - customer.*             → 该客户 + 归属客服
//   - player.*               → 该陪玩
func resolveNotificationRecipients(ctx context.Context, db *ent.Client, eventType, aggregateRef string, payload map[string]interface{}) ([]notificationRecipient, error) {
	if db == nil {
		return nil, errNotificationRecipientMissing
	}
	switch {
	case strings.HasPrefix(eventType, "order."):
		return resolveOrderNotificationRecipients(ctx, db, aggregateRef, payload)
	case strings.HasPrefix(eventType, "aftersale."):
		return resolveAfterSaleNotificationRecipients(ctx, db, aggregateRef, payload)
	case eventType == "dispatch.remind_admin":
		return resolveOrderAdminNotificationRecipients(ctx, db, aggregateRef, payload)
	case strings.HasPrefix(eventType, "payment."):
		return resolvePaymentNotificationRecipients(ctx, db, eventType, aggregateRef, payload)
	case strings.HasPrefix(eventType, "gift."):
		return resolveGiftNotificationRecipients(ctx, db, aggregateRef, payload)
	case strings.HasPrefix(eventType, "member."):
		return resolveCustomerNotificationRecipients(ctx, db, aggregateRef, payload)
	case strings.HasPrefix(eventType, "customer."):
		return resolveCustomerNotificationRecipients(ctx, db, aggregateRef, payload)
	case strings.HasPrefix(eventType, "player."):
		return resolvePlayerNotificationRecipients(ctx, db, aggregateRef, payload)
	}
	return nil, errNotificationRecipientMissing
}

func resolveOrderNotificationRecipients(ctx context.Context, db *ent.Client, aggregateRef string, payload map[string]interface{}) ([]notificationRecipient, error) {
	order, err := findNotificationOrder(ctx, db, aggregateRef, payload)
	if err != nil {
		return nil, err
	}
	if order == nil {
		return nil, errNotificationRecipientMissing
	}
	set := newNotificationRecipientSet()
	set.addCustomer(order.CustomerID, notificationRoleCustomer)
	if order.ResponsibleAdminID != nil {
		set.addAdmin(ctx, db, *order.ResponsibleAdminID, notificationRoleAdmin)
	}
	seats, err := db.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(order.ID),
		esportsdispatchseat.StatusEQ(esportsdispatchseat.StatusAccepted),
		esportsdispatchseat.PlayerIDNotNil(),
	).Order(ent.Asc(esportsdispatchseat.FieldSeatIndex)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, seat := range seats {
		if seat.PlayerID != nil {
			set.addPlayer(*seat.PlayerID, notificationRolePlayer)
		}
	}
	return set.result()
}

func resolveAfterSaleNotificationRecipients(ctx context.Context, db *ent.Client, aggregateRef string, payload map[string]interface{}) ([]notificationRecipient, error) {
	afterSaleNo := payloadString(payload, "after_sale_no")
	if afterSaleNo == "" {
		afterSaleNo = aggregateRef
	}
	if afterSaleNo == "" {
		return nil, errNotificationRecipientMissing
	}
	item, err := db.EsportsAfterSale.Query().Where(esportsaftersale.AfterSaleNoEQ(afterSaleNo)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, errNotificationRecipientMissing
	}
	if err != nil {
		return nil, err
	}
	set := newNotificationRecipientSet()
	customerID := 0
	if item.CustomerID != nil {
		customerID = *item.CustomerID
	}
	var responsibleAdminID *int
	if item.SourceType == esportsaftersale.SourceTypeOrder {
		order, err := db.EsportsOrder.Get(ctx, item.SourceID)
		if err != nil && !ent.IsNotFound(err) {
			return nil, err
		}
		if order != nil {
			if customerID == 0 {
				customerID = order.CustomerID
			}
			responsibleAdminID = order.ResponsibleAdminID
		}
	}
	if customerID > 0 {
		set.addCustomer(customerID, notificationRoleCustomer)
	}
	if responsibleAdminID == nil && item.HandlerAdminID != nil {
		responsibleAdminID = item.HandlerAdminID
	}
	if responsibleAdminID != nil {
		set.addAdmin(ctx, db, *responsibleAdminID, notificationRoleAdmin)
	}
	return set.result()
}

// resolveGiftNotificationRecipients 解析礼物事件收件人：送礼客户 + 收礼陪玩 + 礼物单快照的
// 业绩归属客服。事件聚合键即礼物单号（gift.paid / gift.refunded 均以 gift_no 为 aggregate_ref），
// 退款事件载荷可能不含客户/陪玩 ID，因此统一按礼物单回查。查不到礼物单按收件人缺失处理，
// 由外盒退避重试直至 dead，绝不降级为广播。
func resolveGiftNotificationRecipients(ctx context.Context, db *ent.Client, aggregateRef string, payload map[string]interface{}) ([]notificationRecipient, error) {
	giftNo := payloadString(payload, "gift_no")
	if giftNo == "" {
		giftNo = strings.TrimSpace(aggregateRef)
	}
	if giftNo == "" {
		return nil, errNotificationRecipientMissing
	}
	order, err := db.EsportsGiftOrder.Query().Where(esportsgiftorder.GiftNoEQ(giftNo)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, errNotificationRecipientMissing
	}
	if err != nil {
		return nil, err
	}
	set := newNotificationRecipientSet()
	set.addCustomer(order.CustomerID, notificationRoleCustomer)
	set.addPlayer(order.PlayerID, notificationRolePlayer)
	if order.ResponsibleAdminID != nil {
		set.addAdmin(ctx, db, *order.ResponsibleAdminID, notificationRoleAdmin)
	}
	return set.result()
}

func resolveOrderAdminNotificationRecipients(ctx context.Context, db *ent.Client, aggregateRef string, payload map[string]interface{}) ([]notificationRecipient, error) {
	set := newNotificationRecipientSet()
	if id := payloadInt(payload, "admin_id"); id > 0 {
		set.addAdmin(ctx, db, id, notificationRoleAdmin)
	}
	order, err := findNotificationOrder(ctx, db, aggregateRef, payload)
	if err != nil {
		return nil, err
	}
	if order != nil && order.ResponsibleAdminID != nil {
		set.addAdmin(ctx, db, *order.ResponsibleAdminID, notificationRoleAdmin)
	}
	return set.result()
}

func resolvePaymentNotificationRecipients(ctx context.Context, db *ent.Client, eventType, aggregateRef string, payload map[string]interface{}) ([]notificationRecipient, error) {
	set := newNotificationRecipientSet()
	order, err := findNotificationOrder(ctx, db, aggregateRef, payload)
	if err != nil {
		return nil, err
	}
	if id := payloadInt(payload, "admin_id"); id > 0 {
		set.addAdmin(ctx, db, id, notificationRoleAdmin)
	}
	if order != nil && order.ResponsibleAdminID != nil {
		set.addAdmin(ctx, db, *order.ResponsibleAdminID, notificationRoleAdmin)
	}
	// 退款 / 拒付 / 冲正类支付事件需要同步告知客户。
	if notificationPaymentNotifiesCustomer(eventType) {
		customerID := payloadInt(payload, "customer_id")
		if customerID == 0 && order != nil {
			customerID = order.CustomerID
		}
		if customerID > 0 {
			set.addCustomer(customerID, notificationRoleCustomer)
		}
	}
	return set.result()
}

func resolveCustomerNotificationRecipients(ctx context.Context, db *ent.Client, aggregateRef string, payload map[string]interface{}) ([]notificationRecipient, error) {
	customerID := payloadInt(payload, "customer_id")
	if customerID == 0 {
		customerID = trailingID(aggregateRef, "customer")
	}
	if customerID == 0 {
		return nil, errNotificationRecipientMissing
	}
	customer, err := db.EsportsCustomer.Get(ctx, customerID)
	if ent.IsNotFound(err) {
		return nil, errNotificationRecipientMissing
	}
	if err != nil {
		return nil, err
	}
	set := newNotificationRecipientSet()
	set.addCustomer(customer.ID, notificationRoleCustomer)
	if customer.OwnerAdminID != nil {
		set.addAdmin(ctx, db, *customer.OwnerAdminID, notificationRoleAdmin)
	} else if id := payloadInt(payload, "admin_id"); id > 0 {
		set.addAdmin(ctx, db, id, notificationRoleAdmin)
	}
	return set.result()
}

func resolvePlayerNotificationRecipients(ctx context.Context, db *ent.Client, aggregateRef string, payload map[string]interface{}) ([]notificationRecipient, error) {
	playerID := payloadInt(payload, "player_id")
	if playerID == 0 {
		playerID = trailingID(aggregateRef, "player")
	}
	if playerID == 0 {
		return nil, errNotificationRecipientMissing
	}
	set := newNotificationRecipientSet()
	set.addPlayer(playerID, notificationRolePlayer)
	return set.result()
}

// findNotificationOrder 按 payload 的 order_id/order_no、aggregateRef（订单号或数字 ID）定位订单。
// 找不到时返回 (nil, nil)，由调用方决定是否视为收件人缺失。
func findNotificationOrder(ctx context.Context, db *ent.Client, aggregateRef string, payload map[string]interface{}) (*ent.EsportsOrder, error) {
	if id := payloadInt(payload, "order_id"); id > 0 {
		order, err := db.EsportsOrder.Get(ctx, id)
		if err == nil {
			return order, nil
		}
		if !ent.IsNotFound(err) {
			return nil, err
		}
	}
	orderNo := payloadString(payload, "order_no")
	if orderNo == "" && trailingID(aggregateRef, "") == 0 {
		orderNo = strings.TrimSpace(aggregateRef)
	}
	if orderNo != "" {
		order, err := db.EsportsOrder.Query().Where(esportsorder.OrderNoEQ(orderNo)).Only(ctx)
		if err == nil {
			return order, nil
		}
		if !ent.IsNotFound(err) {
			return nil, err
		}
	}
	if id := trailingID(aggregateRef, ""); id > 0 {
		order, err := db.EsportsOrder.Get(ctx, id)
		if err == nil {
			return order, nil
		}
		if !ent.IsNotFound(err) {
			return nil, err
		}
	}
	return nil, nil
}

func notificationPaymentNotifiesCustomer(eventType string) bool {
	normalized := strings.ToLower(eventType)
	for _, marker := range []string{"refund", "reversal", "chargeback", "reject"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

type notificationRecipientSet struct {
	items []notificationRecipient
	index map[string]int
}

func newNotificationRecipientSet() *notificationRecipientSet {
	return &notificationRecipientSet{index: make(map[string]int)}
}

func (s *notificationRecipientSet) addCustomer(id int, role string) {
	if id < 1 {
		return
	}
	s.add(notificationRecipient{
		Role: role, Roles: []string{role}, CustomerID: id,
		Address: customerNotificationAddress(id),
	})
}

func (s *notificationRecipientSet) addPlayer(id int, role string) {
	if id < 1 {
		return
	}
	s.add(notificationRecipient{
		Role: role, Roles: []string{role}, PlayerID: id,
		Address: playerNotificationAddress(id),
	})
}

func (s *notificationRecipientSet) addAdmin(ctx context.Context, db *ent.Client, id int, role string) {
	if id < 1 {
		return
	}
	s.add(notificationRecipient{
		Role: role, Roles: []string{role}, AdminID: id,
		Address: adminNotificationAddress(ctx, db, id),
	})
}

func (s *notificationRecipientSet) add(item notificationRecipient) {
	if idx, ok := s.index[item.Address]; ok {
		for _, role := range item.Roles {
			if !containsString(s.items[idx].Roles, role) {
				s.items[idx].Roles = append(s.items[idx].Roles, role)
			}
		}
		return
	}
	s.index[item.Address] = len(s.items)
	s.items = append(s.items, item)
}

func (s *notificationRecipientSet) result() ([]notificationRecipient, error) {
	if len(s.items) == 0 {
		return nil, errNotificationRecipientMissing
	}
	return s.items, nil
}

// adminNotificationAddress 优先返回后台消息中心可读取的 Admin.UID；档案缺失时退回
// `admin:<id>` 兜底地址（推送网关仍可按该地址路由）。
func adminNotificationAddress(ctx context.Context, db *ent.Client, id int) string {
	row, err := db.Admin.Query().Where(entadmin.IDEQ(id)).Only(ctx)
	if err == nil && strings.TrimSpace(row.UID) != "" {
		return row.UID
	}
	return "admin:" + strconv.Itoa(id)
}

func customerNotificationAddress(id int) string { return "customer:" + strconv.Itoa(id) }
func playerNotificationAddress(id int) string   { return "player:" + strconv.Itoa(id) }

// trailingID 解析 "prefix:<id>" 或裸数字；prefix 为空时接受任意前缀。
func trailingID(ref, prefix string) int {
	ref = strings.TrimSpace(ref)
	if prefix != "" {
		ref = strings.TrimPrefix(ref, prefix+":")
	}
	if idx := strings.LastIndex(ref, ":"); idx >= 0 {
		ref = ref[idx+1:]
	}
	id, err := strconv.Atoi(ref)
	if err != nil || id < 1 {
		return 0
	}
	return id
}

func payloadString(payload map[string]interface{}, key string) string {
	if payload == nil {
		return ""
	}
	switch value := payload[key].(type) {
	case string:
		return strings.TrimSpace(value)
	case fmt.Stringer:
		return strings.TrimSpace(value.String())
	default:
		return ""
	}
}

func payloadInt(payload map[string]interface{}, key string) int {
	if payload == nil {
		return 0
	}
	switch value := payload[key].(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float64:
		return int(value)
	case float32:
		return int(value)
	case string:
		id, err := strconv.Atoi(strings.TrimSpace(value))
		if err == nil {
			return id
		}
	case fmt.Stringer:
		id, err := strconv.Atoi(strings.TrimSpace(value.String()))
		if err == nil {
			return id
		}
	}
	return 0
}
