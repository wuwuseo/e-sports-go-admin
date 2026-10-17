package esports

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/message"
	"go-admin/internal/ent/predicate"
)

// 通知发送适配层（§通知）：基于现有 EsportsOutbox，按事件类型把事件映射到通知通道
// （企微应用消息 / 语音 / 推送），因暂无渠道密钥，通道开关可配置（notification_channels），
// 发送失败的事件置 dead 并计入客服失败待办（csWorkbenchTodos 的 notification_failed 项）。
//
// 投递口径（P0 修复）：
//   - 收件人按订单/角色解析（notification_recipients.go），站内消息逐人写 user_uid，
//     绝不再写 user_uid="" 的全员广播，正文也不再拼接完整事件载荷。
//   - 站内信（ent.Message）是本地可观测的必投通道，不受 notification_channels 开关控制；
//     开关只决定是否调用 webhook 推送网关。
//   - 投递判定按「已启用且已配置」的通道计算：站内信成功 + 所有已配置推送通道失败时必须
//     返回错误，让 outbox 继续退避重试直至 dead，从而进入客服失败待办；通道未配置
//     （errNotificationWebhookNotConfigured）视为未启用，不阻断站内信。
//   - 反向容忍：站内信失败但任一推送通道成功，视为已投递（避免把已成功推送的事件无限重试）；
//     站内信失败会写日志留痕。
//   - 站内信按「事件定位键」幂等（见 notificationMessageDedupeKey），重试不会产生重复消息。

// 通知通道标识。
const (
	notifyChannelWecomApp = "wecom_app" // 企微应用消息
	notifyChannelVoice    = "voice"     // 语音
	notifyChannelPush     = "push"      // 推送
	// notifyChannelInApp 仅用于标注站内信记录，不出现在通道开关里。
	notifyChannelInApp = "inapp"
)

// notificationChannelsConfigKey 通知通道开关：逗号分隔启用的通道（如 "wecom_app,push"），
// 缺省为空表示不启用 webhook 推送；站内信始终投递。
const notificationChannelsConfigKey = "notification_channels"

var errNotificationSenderUnavailable = errors.New("通知发送通道尚未配置")

// NotificationSender 通知发送适配层接口。既有签名保持不变；新实现可通过
// notificationBatchSender 在一次调用中完成站内信 + 多通道推送，避免重复解析收件人。
type NotificationSender interface {
	Send(ctx context.Context, channel, eventType, aggregateRef string, payload map[string]interface{}) error
}

// notificationBatchSender 是一次投递内可处理多个通道的扩展接口。sendNotificationEvent 优先使用；
// 未实现该接口的旧发送器继续按通道逐个 Send，保持兼容。
// eventKey 是 outbox 事件唯一键，供站内信幂等去重使用（越界时退化为 eventType + aggregateRef）。
type notificationBatchSender interface {
	SendBatch(ctx context.Context, channels []string, eventKey, eventType, aggregateRef string, payload map[string]interface{}) error
}

// notifyChannelTargets 定义事件类型前缀 → 通知通道的映射（含顺序），是通知分类的唯一事实来源。
var notifyChannelTargets = []struct {
	prefix   string
	channels []string
}{
	{"dispatch.remind_admin", []string{notifyChannelWecomApp}},
	{"aftersale.", []string{notifyChannelWecomApp}},
	{"customer.", []string{notifyChannelWecomApp}},
	{"payment.", []string{notifyChannelWecomApp}},
	{"order.", []string{notifyChannelWecomApp, notifyChannelPush}},
	{"player.", []string{notifyChannelPush}},
}

// eventNotifyChannels 按 outbox 事件类型映射通知通道；未命中返回 nil（非通知事件，无需发送）。
func eventNotifyChannels(eventType string) []string {
	for _, target := range notifyChannelTargets {
		if strings.HasPrefix(eventType, target.prefix) {
			return target.channels
		}
	}
	return nil
}

// notificationEventPredicate 通知类事件的 SQL 过滤，供通知消费认领时限定范围，与业务事件消费互斥。
func notificationEventPredicate() predicate.EsportsOutbox {
	ps := make([]predicate.EsportsOutbox, 0, len(notifyChannelTargets))
	for _, target := range notifyChannelTargets {
		ps = append(ps, esportsoutbox.EventTypeHasPrefix(target.prefix))
	}
	return esportsoutbox.Or(ps...)
}

// enabledNotificationChannels 读取通知通道开关，返回启用集合；缺省为空 = 不启用 webhook 推送。
func enabledNotificationChannels(ctx context.Context, db *ent.Client) map[string]bool {
	raw := assessmentConfigString(ctx, db, notificationChannelsConfigKey, "")
	out := make(map[string]bool)
	for _, part := range strings.Split(raw, ",") {
		switch c := strings.TrimSpace(part); c {
		case notifyChannelWecomApp, notifyChannelVoice, notifyChannelPush:
			out[c] = true
		}
	}
	return out
}

// sendNotificationEvent 按事件类型选择通道并发送；启用通道中有任一成功即视为已通知。
// 实现 notificationBatchSender 的发送器（组合投递器）会被调用一次，即使所有通道都关闭，
// 站内信也必须在这次调用中完成；旧发送器保持“全部失败返回错误”的既有语义。
func sendNotificationEvent(ctx context.Context, sender NotificationSender, enabled map[string]bool, eventKey, eventType, aggregateRef string, payload map[string]interface{}) error {
	channels := eventNotifyChannels(eventType)
	if len(channels) == 0 {
		return nil // 非通知事件，无需处理。
	}
	if sender == nil {
		return errNotificationSenderUnavailable
	}
	active := make([]string, 0, len(channels))
	for _, ch := range channels {
		if enabled[ch] {
			active = append(active, ch)
		}
	}
	if batch, ok := sender.(notificationBatchSender); ok {
		return batch.SendBatch(ctx, active, eventKey, eventType, aggregateRef, payload)
	}
	if len(active) == 0 {
		return errNotificationSenderUnavailable
	}
	var lastErr error
	sent := 0
	for _, ch := range active {
		if err := sender.Send(ctx, ch, eventType, aggregateRef, payload); err != nil {
			lastErr = err
			continue
		}
		sent++
	}
	if sent == 0 {
		if lastErr == nil {
			lastErr = errNotificationSenderUnavailable
		}
		return lastErr
	}
	return nil
}

// notificationEventPublisher 将 NotificationSender 适配为 EventPublisher，使通知类事件复用
// deliverOutboxEvents 的租约 + 退避重试 + eventKey 去重机制（避免重复发送）。
type notificationEventPublisher struct {
	sender  NotificationSender
	enabled map[string]bool
}

func (p *notificationEventPublisher) Publish(ctx context.Context, eventKey, eventType, aggregateRef string, payload map[string]interface{}) error {
	return sendNotificationEvent(ctx, p.sender, p.enabled, eventKey, eventType, aggregateRef, payload)
}

// deliverNotificationOutboxBatch 处理待发送的通知类 outbox 事件：认领通知类事件、发送成功置
// delivered，失败退避重试、达上限置 dead（dead 即客服失败待办来源）。非通知类事件不在本消费
// 认领范围，交由 deliverOutboxBatch 投递，二者互斥避免重复发送。
//
// app.go 注入的仍是站内信壳 messageNotificationSender；此处自动升级为组合投递器
// （站内信 + 推送 webhook），因此无需改动 app.go 的调用签名。
func deliverNotificationOutboxBatch(ctx context.Context, db *ent.Client, sender NotificationSender, now time.Time, limit int) (int, error) {
	if sender == nil {
		return 0, errNotificationSenderUnavailable
	}
	if _, ok := sender.(*messageNotificationSender); ok {
		sender = newCompositeNotificationSender(db)
	}
	enabled := enabledNotificationChannels(ctx, db)
	return deliverOutboxEvents(ctx, db, &notificationEventPublisher{sender: sender, enabled: enabled}, notificationEventPredicate(), now, limit)
}

// countFailedNotifications 统计外盒死信（outbox status=dead）数量，供客服失败待办
// （csWorkbenchTodos 的 notification_failed 项）。推送通道重试耗尽后同样落 dead，
// 因此「站内信成功 + 已配置推送全部失败」的故障会经由本统计进入待办，不会被吞掉。
//
// 口径说明：dead 是外盒的统一终态，业务事件与通知事件共用；待办按「外盒死信总数」展示，
// 两者都需要人工处理（通知事件可重发，业务事件需排查消费方）。
func countFailedNotifications(ctx context.Context, db *ent.Client) (int, error) {
	return db.EsportsOutbox.Query().Where(esportsoutbox.StatusEQ(esportsoutbox.StatusDead)).Count(ctx)
}

// compositeNotificationSender 组合投递：先写站内信（失败不阻断推送），再按启用的通道调用
// webhook 推送网关。投递判定见 deliver：站内信成功不足以掩盖「已配置推送通道全部失败」，
// 后者必须上抛给 outbox 重试/死信；通道未配置继续容忍。
type compositeNotificationSender struct {
	inApp   *messageNotificationSender
	webhook *webhookNotificationSender
}

func newCompositeNotificationSender(db *ent.Client) *compositeNotificationSender {
	return &compositeNotificationSender{
		inApp:   &messageNotificationSender{db: db},
		webhook: newWebhookNotificationSender(db),
	}
}

func (s *compositeNotificationSender) Send(ctx context.Context, channel, eventType, aggregateRef string, payload map[string]interface{}) error {
	return s.deliver(ctx, []string{channel}, "", eventType, aggregateRef, payload)
}

// SendBatch 是 sendNotificationEvent 的批量入口：channels 为本次启用的推送通道（可为空），
// eventKey 为 outbox 事件唯一键（供站内信幂等）。
func (s *compositeNotificationSender) SendBatch(ctx context.Context, channels []string, eventKey, eventType, aggregateRef string, payload map[string]interface{}) error {
	return s.deliver(ctx, channels, eventKey, eventType, aggregateRef, payload)
}

// deliver 执行一次「站内信 + 推送通道」组合投递，并给出投递判定：
//   - 站内信成功且（无已配置推送通道 或 至少一个推送通道成功）→ 已投递；
//   - 站内信成功但所有已配置推送通道失败 → 返回错误，交由 outbox 退避重试直至 dead；
//     这是本次 P0 修复的关键：不能再因为站内信成功而把推送故障吞掉；
//   - 站内信失败但任一推送成功 → 已投递（站内信失败只记日志，不触发无意义的重复推送）；
//   - 全部失败 → 返回组合错误。
//
// 未配置的通道（errNotificationWebhookNotConfigured）不计入「已配置通道」，保持降级语义。
func (s *compositeNotificationSender) deliver(ctx context.Context, channels []string, eventKey, eventType, aggregateRef string, payload map[string]interface{}) error {
	recipients, err := resolveNotificationRecipients(ctx, s.inApp.db, eventType, aggregateRef, payload)
	if err != nil {
		return err
	}
	inAppErr := s.inApp.sendToRecipients(ctx, notifyChannelInApp, eventKey, eventType, aggregateRef, payload, recipients)
	if inAppErr != nil {
		// 站内信失败但推送可能成功，这里先留痕；最终是否算投递由下方判定决定。
		log.Printf("[esports][notification] 站内信投递失败 event=%s ref=%s err=%v", eventType, aggregateRef, inAppErr)
	}
	configured, pushed := 0, 0
	var pushErr error
	for _, channel := range channels {
		err := s.webhook.send(ctx, channel, eventType, aggregateRef, payload, recipients)
		if errors.Is(err, errNotificationWebhookNotConfigured) {
			continue // 未配置 = 该通道未启用，不阻断站内信。
		}
		configured++
		if err == nil {
			pushed++
			continue
		}
		pushErr = fmt.Errorf("通道 %s 推送失败: %w", channel, err)
	}
	switch {
	case inAppErr == nil && (configured == 0 || pushed > 0):
		return nil
	case inAppErr == nil:
		// 站内信成功、已配置推送通道全部失败：必须上抛，让事件进入重试/死信与失败待办。
		return pushErr
	case pushed > 0:
		return nil
	case pushErr != nil:
		return errors.Join(inAppErr, pushErr)
	default:
		return inAppErr
	}
}

// messageNotificationSender 把通知逐收件人写入站内消息（admin_messages / ent.Message）：
// user_uid 落具体地址（Admin.UID / customer:<id> / player:<id>），正文只含业务标题与摘要，
// 事件载荷不再广播。完整载荷仍保留在 EsportsOutbox 供审计，消息 extra 只放必要字段。
//
// 幂等：同一事件可能因推送失败而被 outbox 反复重试，写站内信前先按「地址 + 事件定位键」
// 查重，命中即跳过并视为成功，保证同一事件只产生一条站内信。
type messageNotificationSender struct {
	db *ent.Client
}

func (s *messageNotificationSender) Send(ctx context.Context, channel, eventType, aggregateRef string, payload map[string]interface{}) error {
	recipients, err := resolveNotificationRecipients(ctx, s.db, eventType, aggregateRef, payload)
	if err != nil {
		return err
	}
	return s.sendToRecipients(ctx, channel, "", eventType, aggregateRef, payload, recipients)
}

func (s *messageNotificationSender) sendToRecipients(ctx context.Context, channel, eventKey, eventType, aggregateRef string, payload map[string]interface{}, recipients []notificationRecipient) error {
	if len(recipients) == 0 {
		return errNotificationRecipientMissing
	}
	title := notificationEventTitle(eventType)
	summary := notificationEventSummary(eventType, aggregateRef, payload)
	dedupeKey := notificationMessageDedupeKey(eventKey, eventType, aggregateRef)
	for _, recipient := range recipients {
		if strings.TrimSpace(recipient.Address) == "" {
			return errNotificationRecipientMissing
		}
		sent, err := s.messageAlreadySent(ctx, recipient.Address, dedupeKey)
		if err != nil {
			return err
		}
		if sent {
			continue // 该事件已给此收件人写过站内信，重试时跳过。
		}
		extra := map[string]interface{}{
			"channel":       channel,
			"event_type":    eventType,
			"aggregate_ref": aggregateRef,
			"roles":         recipient.Roles,
			"dedupe_key":    dedupeKey,
		}
		if eventKey != "" {
			extra["event_key"] = eventKey
		}
		if orderNo := notificationOrderNo(eventType, aggregateRef, payload); orderNo != "" {
			extra["order_no"] = orderNo
		}
		if afterSaleNo := payloadString(payload, "after_sale_no"); afterSaleNo != "" {
			extra["after_sale_no"] = afterSaleNo
		}
		if _, err := s.db.Message.Create().
			SetUserUID(recipient.Address).
			SetType(notificationMessageType).
			SetLevel("info").
			SetTitle(title).
			SetContent(summary).
			SetExtra(extra).
			Save(ctx); err != nil {
			return err
		}
	}
	return nil
}

// 站内信幂等参数。
const (
	notificationMessageType = "business"
	// notificationMessageDedupeField 是写入 ent.Message.extra 的事件定位键字段名。
	notificationMessageDedupeField = "dedupe_key"
	// notificationMessageDedupeWindow 覆盖 outbox 的全部退避重试周期（8 次以内约 2 小时），
	// 留足余量；查询命中 (user_uid, created_at) 索引且有条数上限。
	notificationMessageDedupeWindow = 72 * time.Hour
	// notificationMessageDedupeScan 限定单次查重的扫描行数，避免收件箱很大时全量加载。
	notificationMessageDedupeScan = 200
)

// notificationMessageDedupeKey 生成事件定位键：优先 outbox 事件唯一键 eventKey
// （同一 eventType + aggregateRef 的多次业务事件也能区分），缺失时退化为
// eventType + aggregateRef，保证至少不会在重试中重复。
func notificationMessageDedupeKey(eventKey, eventType, aggregateRef string) string {
	if key := strings.TrimSpace(eventKey); key != "" {
		return key
	}
	return eventType + "@" + aggregateRef
}

// messageAlreadySent 判断该收件人是否已收到同一事件的站内信。extra 是 JSON 字段，
// ent 未生成可移植的 JSON 条件，这里按收件人 + 类型 + 时间窗取最近若干条在内存比对。
func (s *messageNotificationSender) messageAlreadySent(ctx context.Context, address, dedupeKey string) (bool, error) {
	if dedupeKey == "" {
		return false, nil
	}
	recent, err := s.db.Message.Query().Where(
		message.UserUIDEQ(address),
		message.TypeEQ(notificationMessageType),
		message.CreatedAtGTE(time.Now().UTC().Add(-notificationMessageDedupeWindow)),
	).Order(ent.Desc(message.FieldID)).Limit(notificationMessageDedupeScan).All(ctx)
	if err != nil {
		return false, err
	}
	for _, row := range recent {
		if stored, _ := row.Extra[notificationMessageDedupeField].(string); stored == dedupeKey {
			return true, nil
		}
	}
	return false, nil
}

// notificationEventText 是事件类型 → 业务可读标题的映射；未命中时按前缀给出通用标题。
var notificationEventText = map[string]string{
	"order.created":                        "订单已创建",
	"order.closed":                         "订单已结单",
	"order.cancelled":                      "订单已取消",
	"order.payment_verified":               "订单支付已核销",
	"order.payment_expired":                "订单支付已超时",
	"order.payment_reversal_registered":    "订单支付冲正已登记",
	"order.seat_invited":                   "新的接单邀请",
	"order.seat_accepted":                  "陪玩已接单",
	"order.seat_rejected":                  "陪玩已拒绝接单",
	"order.seat_expired":                   "接单邀请已过期",
	"order.published_to_grab_hall":         "订单已释放到抢单大厅",
	"order.dispatch_released":              "订单已释放派单",
	"order.service_started":                "服务已开始",
	"order.service_paused":                 "服务已暂停",
	"order.service_finished":               "服务已结束",
	"order.service_overtime":               "服务时长已超时",
	"order.addon_requested":                "加钟待确认",
	"order.reschedule_proposed":            "改期提案待确认",
	"order.reschedule_applied":             "改期已生效",
	"order.restored":                       "订单已恢复",
	"order.transferred":                    "订单已转移",
	"order.after_sale_created":             "订单售后已创建",
	"aftersale.source_created":             "售后单已创建",
	"aftersale.review_requested":           "售后待审批",
	"aftersale.sla_breach":                 "售后处理已超时",
	"aftersale.actions_partially_executed": "售后执行部分完成",
	"payment.receipt_applied":              "支付已入账",
	"payment.receipt_exception":            "支付异常待处理",
	"payment.reversal_registered":          "支付冲正已登记",
	"dispatch.remind_admin":                "派单提醒（客服）",
	"customer.age_check_requested":         "客户年龄核验待处理",
	"customer.age_check_reviewed":          "客户年龄核验已完成",
	"customer.risk_flagged":                "客户触发风控复核",
	"player.heartbeat_timeout":             "陪玩心跳超时",
	"player.credit_recorded":               "陪玩信用已记录",
}

// notificationEventTitle 返回业务可读标题（不含通道前缀）。
func notificationEventTitle(eventType string) string {
	if text, ok := notificationEventText[eventType]; ok {
		return text
	}
	switch {
	case strings.HasPrefix(eventType, "order."):
		return "订单通知"
	case strings.HasPrefix(eventType, "aftersale."):
		return "售后通知"
	case strings.HasPrefix(eventType, "payment."):
		return "支付通知"
	case strings.HasPrefix(eventType, "customer."):
		return "客户提醒"
	case strings.HasPrefix(eventType, "player."):
		return "陪玩提醒"
	case strings.HasPrefix(eventType, "dispatch."):
		return "派单提醒"
	}
	return "业务通知"
}

// notificationEventSummary 生成业务可读摘要：标题 + 少量关键单号，绝不序列化完整事件载荷。
func notificationEventSummary(eventType, aggregateRef string, payload map[string]interface{}) string {
	parts := []string{notificationEventTitle(eventType)}
	if orderNo := notificationOrderNo(eventType, aggregateRef, payload); orderNo != "" {
		parts = append(parts, "订单号 "+orderNo)
	}
	if afterSaleNo := payloadString(payload, "after_sale_no"); afterSaleNo != "" {
		parts = append(parts, "售后单号 "+afterSaleNo)
	}
	if level := payloadInt(payload, "level"); level > 0 {
		parts = append(parts, "提醒档位 "+strconv.Itoa(level))
	}
	return strings.Join(parts, "，")
}

// notificationOrderNo 提取订单号：优先 payload.order_no；order.* / payment.* 与
// dispatch.remind_admin 的 aggregateRef 即订单号（player.* / customer.* / aftersale.* 不是）。
func notificationOrderNo(eventType, aggregateRef string, payload map[string]interface{}) string {
	if orderNo := payloadString(payload, "order_no"); orderNo != "" {
		return orderNo
	}
	if (strings.HasPrefix(eventType, "order.") || strings.HasPrefix(eventType, "payment.") ||
		eventType == "dispatch.remind_admin") && trailingID(aggregateRef, "") == 0 {
		return strings.TrimSpace(aggregateRef)
	}
	return ""
}

// notificationTitle 保留旧函数签名，供既有调用/测试使用；新站内信不再把通道拼进标题。
func notificationTitle(channel, eventType string) string {
	switch channel {
	case notifyChannelWecomApp:
		return "企微通知：" + eventType
	case notifyChannelVoice:
		return "语音通知：" + eventType
	case notifyChannelPush:
		return "推送通知：" + eventType
	default:
		return "通知：" + eventType
	}
}
