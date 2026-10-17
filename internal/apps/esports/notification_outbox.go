package esports

import (
	"context"
	"errors"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/predicate"
)

// 通知发送适配层（§通知）：基于现有 EsportsOutbox，按事件类型把事件映射到通知通道
// （企微应用消息 / 语音 / 推送），因暂无渠道密钥，通道开关可配置（notification_channels），
// 发送失败的事件置 dead 并计入客服失败待办（csWorkbenchTodos 的 notification_failed 项）。

// 通知通道标识。
const (
	notifyChannelWecomApp = "wecom_app" // 企微应用消息
	notifyChannelVoice    = "voice"     // 语音
	notifyChannelPush     = "push"      // 推送
)

// notificationChannelsConfigKey 通知通道开关：逗号分隔启用的通道（如 "wecom_app,push"），
// 缺省为空表示不启用任何通道（此时通知类事件置 dead，进入失败待办）。
const notificationChannelsConfigKey = "notification_channels"

var errNotificationSenderUnavailable = errors.New("通知发送通道尚未配置")

// NotificationSender 通知发送适配层接口。真实渠道（企微/语音/推送）无密钥，本地可验证实现为
// messageNotificationSender（写入站内消息）；接入 SDK 后替换为对应实现。
type NotificationSender interface {
	Send(ctx context.Context, channel, eventType, aggregateRef string, payload map[string]interface{}) error
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

// enabledNotificationChannels 读取通知通道开关，返回启用集合；缺省为空 = 全关闭。
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

// sendNotificationEvent 按事件类型选择通道并逐通道发送；任一通道发送成功即视为已通知，
// 全部失败（含未启用通道 / 无发送器）返回错误，由上层把事件置 dead 并计入失败待办。
func sendNotificationEvent(ctx context.Context, sender NotificationSender, enabled map[string]bool, eventType, aggregateRef string, payload map[string]interface{}) error {
	channels := eventNotifyChannels(eventType)
	if len(channels) == 0 {
		return nil // 非通知事件，无需处理。
	}
	if sender == nil {
		return errNotificationSenderUnavailable
	}
	var lastErr error
	sent := 0
	for _, ch := range channels {
		if !enabled[ch] {
			continue
		}
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

func (p *notificationEventPublisher) Publish(ctx context.Context, _ string, eventType, aggregateRef string, payload map[string]interface{}) error {
	return sendNotificationEvent(ctx, p.sender, p.enabled, eventType, aggregateRef, payload)
}

// deliverNotificationOutboxBatch 处理待发送的通知类 outbox 事件：认领通知类事件、发送成功置
// delivered，失败退避重试、达上限置 dead（dead 即客服失败待办来源）。非通知类事件不在本消费
// 认领范围，交由 deliverOutboxBatch 投递，二者互斥避免重复发送。
func deliverNotificationOutboxBatch(ctx context.Context, db *ent.Client, sender NotificationSender, now time.Time, limit int) (int, error) {
	if sender == nil {
		return 0, errNotificationSenderUnavailable
	}
	enabled := enabledNotificationChannels(ctx, db)
	return deliverOutboxEvents(ctx, db, &notificationEventPublisher{sender: sender, enabled: enabled}, notificationEventPredicate(), now, limit)
}

// countFailedNotifications 统计通知发送失败（outbox status=dead）事件数，供客服失败待办。
func countFailedNotifications(ctx context.Context, db *ent.Client) (int, error) {
	return db.EsportsOutbox.Query().Where(esportsoutbox.StatusEQ(esportsoutbox.StatusDead)).Count(ctx)
}

// messageNotificationSender 是本地可验证的通知投递器：把通知写入站内消息（admin_messages），
// 便于在管理后台消息中心观测通知送达；接入企微/语音/推送 SDK 后替换为对应实现。
type messageNotificationSender struct {
	db *ent.Client
}

func (s *messageNotificationSender) Send(ctx context.Context, channel, eventType, aggregateRef string, payload map[string]interface{}) error {
	_, err := s.db.Message.Create().
		SetUserUID("").
		SetType("business").
		SetLevel("info").
		SetTitle(notificationTitle(channel, eventType)).
		SetContent(outboxEventContent(eventType, aggregateRef, payload)).
		SetExtra(map[string]interface{}{
			"channel":       channel,
			"event_type":    eventType,
			"aggregate_ref": aggregateRef,
			"payload":       payload,
		}).
		Save(ctx)
	return err
}

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
