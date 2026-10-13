package service

import (
	"context"
	"strconv"

	"github.com/wuwuseo/cmf/sse"
)

// SSEHub 站内信渠道依赖的最小 Hub 能力集
//
// 独立定义而非直接使用 *sse.Hub，便于测试替身与后续替换底层实现。
type SSEHub interface {
	PushToUser(userUID string, evt sse.Event)
	Broadcast(evt sse.Event)
	CountTotalConnections() int
	CountUserConnections(userUID string) int
}

// SSE 站内信渠道推送的事件名
const (
	SSEEventMessage        = "message"
	SSEEventUnreadCount    = "unread_count"
	SSEEventMessageDeleted = "message_deleted"
)

// InAppNotifier 站内信渠道：将消息通过 SSE 长连接实时推给在线用户
//
// 该渠道始终「可用」（Hub 为 nil 时视为禁用），不依赖任何外部配置。
type InAppNotifier struct {
	hub SSEHub
}

// NewInAppNotifier 构造站内信渠道
func NewInAppNotifier(hub SSEHub) *InAppNotifier {
	return &InAppNotifier{hub: hub}
}

// Name 渠道标识
func (n *InAppNotifier) Name() string { return ChannelInApp }

// Enabled Hub 已注入即视为启用
func (n *InAppNotifier) Enabled() bool { return n != nil && n.hub != nil }

// Hub 暴露底层 Hub，供 SSE 连接建立、测试注册连接等场景使用
func (n *InAppNotifier) Hub() SSEHub {
	if n == nil {
		return nil
	}
	return n.hub
}

// Send 推送一条消息事件
func (n *InAppNotifier) Send(_ context.Context, payload NotifyPayload) error {
	if !n.Enabled() {
		return ErrNotifierDisabled
	}
	evt := sse.Event{
		ID:    strconv.Itoa(payload.MessageID),
		Event: SSEEventMessage,
		Data:  payloadToMessageResponse(payload),
	}
	if payload.UserUID == MessageBroadcastUID {
		n.hub.Broadcast(evt)
		return nil
	}
	n.hub.PushToUser(payload.UserUID, evt)
	return nil
}

// PushUnreadCount 下发未读数事件（非消息体事件，由 MessageService 主动触发）
func (n *InAppNotifier) PushUnreadCount(userUID string, cnt *UnreadCountResponse) {
	if !n.Enabled() || userUID == "" || cnt == nil {
		return
	}
	n.hub.PushToUser(userUID, sse.Event{
		Event: SSEEventUnreadCount,
		Data:  cnt,
	})
}

// PushDeleted 下发消息删除事件
func (n *InAppNotifier) PushDeleted(userUID string, ids []int) {
	if !n.Enabled() || userUID == "" || len(ids) == 0 {
		return
	}
	n.hub.PushToUser(userUID, sse.Event{
		Event: SSEEventMessageDeleted,
		Data:  map[string]any{"ids": ids},
	})
}

// payloadToMessageResponse 将渠道载荷还原为 SSE 客户端期望的响应结构
func payloadToMessageResponse(p NotifyPayload) *MessageResponse {
	return &MessageResponse{
		ID:          p.MessageID,
		UserUID:     p.UserUID,
		Type:        p.Type,
		Level:       p.Level,
		Title:       p.Title,
		Content:     p.Content,
		ContentHTML: p.ContentHTML,
		Extra:       p.Extra,
		CreatedAt:   p.CreatedAt,
	}
}

// payloadFromResponse 将落库响应转为渠道载荷
func payloadFromResponse(resp *MessageResponse) NotifyPayload {
	if resp == nil {
		return NotifyPayload{}
	}
	return NotifyPayload{
		MessageID:   resp.ID,
		UserUID:     resp.UserUID,
		Type:        resp.Type,
		Level:       resp.Level,
		Title:       resp.Title,
		Content:     resp.Content,
		ContentHTML: resp.ContentHTML,
		Extra:       resp.Extra,
		CreatedAt:   resp.CreatedAt,
	}
}
