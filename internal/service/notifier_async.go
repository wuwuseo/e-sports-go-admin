package service

import (
	"context"
	"encoding/json"

	"github.com/wuwuseo/cmf/log"
	cmfqueue "github.com/wuwuseo/cmf/queue"
	"go.uber.org/zap"

	"go-admin/internal/global"
)

// TaskNotifyExternal 外部渠道异步投递任务名。
// 短信 / 邮件 / 极光推送等外部渠道不再在消息写入请求内同步投递，
// 而是入队交由 worker 异步执行，避免单条消息 API 延迟被最慢外部渠道拖慢。
const TaskNotifyExternal = "notify.external.dispatch"

// externalDispatchPayload 外部渠道投递任务的载荷：
// Channel 为目标渠道名（ChannelSMS / ChannelMail / ChannelPush），
// Payload 为原渠道投递载荷（含收件人、标题、正文等）。
type externalDispatchPayload struct {
	Channel string        `json:"channel"`
	Payload NotifyPayload `json:"payload"`
}

// NewExternalDispatchHandler 构造外部渠道投递的队列处理器。
//
// 处理器从任务载荷中还原渠道名与投递载荷，再交由注册表执行一次投递。
// 投递失败返回 error，交由队列按 max_retry 退避重试，超限进入死信；
// 失败不影响消息落库与站内信实时触达（二者在入队前已完成）。
func NewExternalDispatchHandler(reg *NotifierRegistry) cmfqueue.Handler {
	return func(ctx context.Context, t *cmfqueue.Task) error {
		var req externalDispatchPayload
		if err := json.Unmarshal(t.Payload, &req); err != nil {
			// 载荷非法属于编程错误，重试无意义，直接返回错误进入死信以便排查
			return err
		}
		if req.Channel == "" {
			return ErrNotifierNotReady
		}
		if reg == nil {
			return ErrNotifierNotReady
		}
		n, ok := reg.Get(req.Channel)
		if !ok {
			// 渠道未注册属于异常（入队前 ShouldDeliver 已校验），显式报错进入死信以便排查
			return ErrNotifierDisabled
		}
		if !n.Enabled() {
			return ErrNotifierDisabled
		}
		return n.Send(ctx, req.Payload)
	}
}

// enqueueExternal 将外部渠道投递入队异步执行。
//
// 队列运行时未初始化（如测试环境、Setup 失败）时，降级为同步投递，
// 保证行为与改造前一致、不丢通知。
func (r *NotifierRegistry) enqueueExternal(ctx context.Context, channel string, payload NotifyPayload) error {
	rt := global.QueueRuntime.Load()
	if rt == nil {
		// 降级：队列不可用时同步投递，维持原有语义
		return r.Dispatch(ctx, channel, payload)
	}
	raw, err := json.Marshal(externalDispatchPayload{Channel: channel, Payload: payload})
	if err != nil {
		return err
	}
	if _, err := rt.Enqueue(ctx, TaskNotifyExternal, raw); err != nil {
		// 入队失败（如 broker 不可达）降级为同步投递，避免静默丢通知
		log.Warn("外部渠道入队失败，降级为同步投递",
			zap.String("channel", channel),
			zap.Int("message_id", payload.MessageID),
			zap.Error(err),
		)
		return r.Dispatch(ctx, channel, payload)
	}
	return nil
}
