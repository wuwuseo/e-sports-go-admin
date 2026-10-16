package esports

import (
	"context"
	"errors"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsoutbox"
)

// 通知发送适配层壳（§通知）：基于现有 EsportsOutbox，按事件类型把事件映射到通知通道
// （企微应用消息 / 语音 / 推送），因暂无渠道密钥，通道开关可配置（notification_channels），
// 发送失败的事件置 dead 并计入客服失败待办（csWorkbenchTodos 的 notification_failed 项）。

// 通知通道标识。
const (
	notifyChannelWecomApp = "wecom_app" // 企微应用消息
	notifyChannelVoice    = "voice"     // 语音
	notifyChannelPush     = "push"      // 推送
)

// notificationChannelsConfigKey 通知通道开关：逗号分隔启用的通道（如 "wecom_app,push"），
// 缺省为空表示不启用任何通道（此时通知类事件直接置 dead，进入失败待办）。
const notificationChannelsConfigKey = "notification_channels"

var errNotificationSenderUnavailable = errors.New("通知发送通道尚未配置")

// NotificationSender 通知发送适配层接口。当前无密钥，实际实现由后续接入
// 企微应用消息 / 语音 / 推送 SDK 后补全；未接入时返回 errNotificationSenderUnavailable。
type NotificationSender interface {
	Send(ctx context.Context, channel, eventType, aggregateRef string, payload map[string]interface{}) error
}

// eventNotifyChannels 按 outbox 事件类型映射通知通道；未命中返回 nil（非通知事件，无需发送）。
func eventNotifyChannels(eventType string) []string {
	switch {
	case strings.HasPrefix(eventType, "dispatch.remind_admin"),
		strings.HasPrefix(eventType, "aftersale."),
		strings.HasPrefix(eventType, "customer."),
		strings.HasPrefix(eventType, "payment."):
		return []string{notifyChannelWecomApp}
	case strings.HasPrefix(eventType, "order."):
		return []string{notifyChannelWecomApp, notifyChannelPush}
	case strings.HasPrefix(eventType, "player."):
		return []string{notifyChannelPush}
	default:
		return nil
	}
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

// deliverNotificationOutboxBatch 处理待发送的通知类 outbox 事件（壳）：发送成功置 delivered，
// 失败置 dead（dead 即客服失败待办来源）。非通知事件直接置 delivered 跳过。
func deliverNotificationOutboxBatch(ctx context.Context, db *ent.Client, sender NotificationSender, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, errInvalidInput
	}
	enabled := enabledNotificationChannels(ctx, db)
	now = now.UTC()
	ids, err := db.EsportsOutbox.Query().Where(
		esportsoutbox.StatusEQ(esportsoutbox.StatusPending),
		esportsoutbox.NextAttemptAtLTE(now),
	).Order(ent.Asc(esportsoutbox.FieldID)).Limit(limit).IDs(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, id := range ids {
		event, err := db.EsportsOutbox.Get(ctx, id)
		if ent.IsNotFound(err) {
			continue
		}
		if err != nil {
			return count, err
		}
		sendErr := sendNotificationEvent(ctx, sender, enabled, event.EventType, event.AggregateRef, event.Payload)
		update := db.EsportsOutbox.Update().Where(
			esportsoutbox.IDEQ(id), esportsoutbox.StatusEQ(esportsoutbox.StatusPending),
		)
		if sendErr == nil {
			if _, err = update.SetStatus(esportsoutbox.StatusDelivered).SetDeliveredAt(now).
				ClearLeaseUntil().SetLastError("").Save(ctx); err != nil {
				return count, err
			}
		} else {
			message := []rune(sendErr.Error())
			if len(message) > 500 {
				message = message[:500]
			}
			if _, err = update.SetStatus(esportsoutbox.StatusDead).ClearLeaseUntil().
				SetLastError(string(message)).Save(ctx); err != nil {
				return count, err
			}
		}
		count++
	}
	return count, nil
}

// countFailedNotifications 统计通知发送失败（outbox status=dead）事件数，供客服失败待办。
func countFailedNotifications(ctx context.Context, db *ent.Client) (int, error) {
	return db.EsportsOutbox.Query().Where(esportsoutbox.StatusEQ(esportsoutbox.StatusDead)).Count(ctx)
}
