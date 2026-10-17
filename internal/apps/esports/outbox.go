package esports

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/predicate"
)

// EventPublisher must deduplicate eventKey. A worker lease can expire while an
// external publish succeeds, so delivery is at least once by design.
type EventPublisher interface {
	Publish(ctx context.Context, eventKey, eventType, aggregateRef string, payload map[string]interface{}) error
}

var errOutboxPublisherUnavailable = errors.New("业务事件投递器尚未配置")

const (
	outboxLease       = 30 * time.Second
	outboxMaxAttempts = 8
)

// deliverOutboxBatch 投递业务（非通知类）外盒事件：条件认领 + 租约 + 退避重试 + 死信。
// 与通知类事件消费（deliverNotificationOutboxBatch）按事件类型互斥，避免重复发送。
func deliverOutboxBatch(ctx context.Context, db *ent.Client, publisher EventPublisher, now time.Time, limit int) (int, error) {
	return deliverOutboxEvents(ctx, db, publisher, businessEventPredicate(), now, limit)
}

// deliverOutboxEvents 是业务/通知两类外盒事件共享的投递循环。eventTypeFilter 非 nil 时按事件
// 类型限定认领范围，使业务事件与通知事件各自独立消费、互不抢占；其余认领、租约、退避、死信
// 逻辑完全一致，保证通知类消费与业务类消费走同一套幂等去重机制。
func deliverOutboxEvents(ctx context.Context, db *ent.Client, publisher EventPublisher, eventTypeFilter predicate.EsportsOutbox, now time.Time, limit int) (int, error) {
	if publisher == nil {
		return 0, errOutboxPublisherUnavailable
	}
	if limit < 1 || limit > 1000 {
		return 0, errInvalidInput
	}
	now = now.UTC()
	due := esportsoutbox.Or(
		esportsoutbox.And(esportsoutbox.StatusEQ(esportsoutbox.StatusPending), esportsoutbox.NextAttemptAtLTE(now)),
		esportsoutbox.And(esportsoutbox.StatusEQ(esportsoutbox.StatusProcessing), esportsoutbox.LeaseUntilLTE(now)),
	)
	q := db.EsportsOutbox.Query().Where(due)
	if eventTypeFilter != nil {
		q = q.Where(eventTypeFilter)
	}
	ids, err := q.Order(ent.Asc(esportsoutbox.FieldID)).Limit(limit).IDs(ctx)
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
		if event.Attempts >= outboxMaxAttempts {
			_, err = db.EsportsOutbox.Update().Where(
				esportsoutbox.IDEQ(id), esportsoutbox.AttemptsEQ(event.Attempts), due,
			).SetStatus(esportsoutbox.StatusDead).ClearLeaseUntil().
				SetLastError("达到最大重试次数，等待人工处理").Save(ctx)
			if err != nil {
				return count, err
			}
			continue
		}
		leaseUntil := now.Add(outboxLease)
		claimed, err := db.EsportsOutbox.Update().Where(
			esportsoutbox.IDEQ(id), esportsoutbox.AttemptsEQ(event.Attempts), due,
		).SetStatus(esportsoutbox.StatusProcessing).SetLeaseUntil(leaseUntil).AddAttempts(1).Save(ctx)
		if err != nil {
			return count, err
		}
		if claimed != 1 {
			continue
		}
		count++
		publishErr := publisher.Publish(ctx, event.EventKey, event.EventType, event.AggregateRef, event.Payload)
		update := db.EsportsOutbox.Update().Where(
			esportsoutbox.IDEQ(id), esportsoutbox.StatusEQ(esportsoutbox.StatusProcessing),
			esportsoutbox.AttemptsEQ(event.Attempts+1), esportsoutbox.LeaseUntilEQ(leaseUntil),
		)
		if publishErr == nil {
			_, err = update.SetStatus(esportsoutbox.StatusDelivered).SetDeliveredAt(now).
				ClearLeaseUntil().SetLastError("").Save(ctx)
		} else {
			message := []rune(publishErr.Error())
			if len(message) > 500 {
				message = message[:500]
			}
			update.SetLastError(string(message)).ClearLeaseUntil()
			if event.Attempts+1 >= outboxMaxAttempts {
				update.SetStatus(esportsoutbox.StatusDead)
			} else {
				update.SetStatus(esportsoutbox.StatusPending).
					SetNextAttemptAt(now.Add(outboxBackoff(event.Attempts + 1)))
			}
			_, err = update.Save(ctx)
		}
		if err != nil {
			return count, err
		}
	}
	return count, nil
}

// businessEventPredicate 过滤非通知类事件（业务事件），与通知类事件消费互斥。
func businessEventPredicate() predicate.EsportsOutbox {
	return esportsoutbox.Not(notificationEventPredicate())
}

func outboxBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 7 {
		attempt = 7
	}
	return time.Minute * time.Duration(1<<(attempt-1))
}

// messageEventPublisher 是本地可验证的业务事件投递器：把事件写入站内消息（admin_messages），
// 便于在管理后台消息中心观测外盒事件的投递结果；接入真实事件总线后替换为对应实现。
type messageEventPublisher struct {
	db *ent.Client
}

func (p *messageEventPublisher) Publish(ctx context.Context, eventKey, eventType, aggregateRef string, payload map[string]interface{}) error {
	_, err := p.db.Message.Create().
		SetUserUID("").
		SetType("task").
		SetLevel("info").
		SetTitle("业务事件：" + eventType).
		SetContent(outboxEventContent(eventType, aggregateRef, payload)).
		SetExtra(map[string]interface{}{
			"event_key":     eventKey,
			"event_type":    eventType,
			"aggregate_ref": aggregateRef,
			"payload":       payload,
		}).
		Save(ctx)
	return err
}

// outboxEventContent 把事件载荷序列化为可观测的文本内容，供审计/消息记录展示。
func outboxEventContent(eventType, aggregateRef string, payload map[string]interface{}) string {
	raw, err := json.Marshal(payload)
	if err != nil || len(raw) == 0 {
		return "event=" + eventType + " ref=" + aggregateRef
	}
	return "event=" + eventType + " ref=" + aggregateRef + " payload=" + string(raw)
}
