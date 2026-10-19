package esports

import (
	"context"
	"errors"
	"time"

	"go-admin/internal/ent"
	entadmin "go-admin/internal/ent/admin"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/message"
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

// 业务事件站内信口径（§2.1-05 ①，P0）：
//   - 绝不写 user_uid="" 的全员广播；也绝不把事件载荷序列化进正文；
//   - 定向投递：载荷点名了管理员（操作人/责任人等）就只发给他；
//     资金/账务类业务事件载荷里没有管理员时，发给超管（最小可见范围）；
//   - 一个可定向收件人都解析不到时返回错误，由外盒退避重试直至 dead
//     （dead 计入客服「通知发送失败」待办），不伪成功、不降级广播。
//
// 事件本身（含完整载荷）长期保留在 esports_outbox 表中，即独立的事件表。
const (
	// businessEventMessageType 业务事件站内信的消息类型（沿用既有 "task"，不改消息中心分类）。
	businessEventMessageType = "task"
	// businessEventDedupeField 写入 ent.Message.extra 的事件定位键字段名。
	businessEventDedupeField = "dedupe_key"
	// businessEventDedupeScan 单次查重扫描的最近消息条数上限。
	businessEventDedupeScan = 200
)

// errBusinessEventRecipientMissing 业务事件解析不到可定向投递的管理员收件人。
var errBusinessEventRecipientMissing = errors.New("业务事件没有可定向投递的管理员收件人")

// businessEventAdminPayloadKeys 事件载荷中点名管理员的字段（任一命中即可定向）。
var businessEventAdminPayloadKeys = []string{
	"admin_id", "operator_id", "operator_admin_id", "created_by_admin_id",
	"responsible_admin_id", "handler_admin_id", "reviewer_admin_id", "decided_by_admin_id",
}

// messageEventPublisher 是本地可验证的业务事件投递器：把事件定向写入站内消息
// （admin_messages）便于在管理后台消息中心观测投递结果；接入真实事件总线后替换为对应实现。
type messageEventPublisher struct {
	db *ent.Client
}

func (p *messageEventPublisher) Publish(ctx context.Context, eventKey, eventType, aggregateRef string, payload map[string]interface{}) error {
	if p.db == nil {
		return errOutboxPublisherUnavailable
	}
	addresses, err := resolveBusinessEventAdminAddresses(ctx, p.db, payload)
	if err != nil {
		return err
	}
	if len(addresses) == 0 {
		return errBusinessEventRecipientMissing
	}

	dedupeKey := notificationMessageDedupeKey(eventKey, eventType, aggregateRef)
	title := "业务事件：" + notificationEventTitle(eventType)
	content := notificationEventSummary(eventType, aggregateRef, payload) +
		"（事件 " + eventType + "，聚合键 " + aggregateRef + "）"

	for _, address := range addresses {
		sent, err := p.messageAlreadySent(ctx, address, dedupeKey)
		if err != nil {
			return err
		}
		if sent {
			continue // 该事件已给此收件人写过站内信，重试时跳过。
		}
		extra := map[string]interface{}{
			"event_type":             eventType,
			"aggregate_ref":          aggregateRef,
			businessEventDedupeField: dedupeKey,
		}
		if eventKey != "" {
			extra["event_key"] = eventKey
		}
		if _, err := p.db.Message.Create().
			SetUserUID(address).
			SetType(businessEventMessageType).
			SetLevel("info").
			SetTitle(title).
			SetContent(content).
			SetExtra(extra).
			Save(ctx); err != nil {
			return err
		}
	}
	return nil
}

// messageAlreadySent 判断该收件人是否已收到同一业务事件的站内信：extra 是 JSON 字段，
// ent 未生成可移植的 JSON 条件，这里按收件人 + 类型 + 时间窗取最近若干条在内存比对
// （与通知类站内信同一套去重口径）。
func (p *messageEventPublisher) messageAlreadySent(ctx context.Context, address, dedupeKey string) (bool, error) {
	if dedupeKey == "" {
		return false, nil
	}
	recent, err := p.db.Message.Query().Where(
		message.UserUIDEQ(address),
		message.TypeEQ(businessEventMessageType),
		message.CreatedAtGTE(time.Now().UTC().Add(-notificationMessageDedupeWindow)),
	).Order(ent.Desc(message.FieldID)).Limit(businessEventDedupeScan).All(ctx)
	if err != nil {
		return false, err
	}
	for _, row := range recent {
		if stored, _ := row.Extra[businessEventDedupeField].(string); stored == dedupeKey {
			return true, nil
		}
	}
	return false, nil
}

// resolveBusinessEventAdminAddresses 解析业务事件的定向管理员收件地址：
// 先取载荷点名的管理员；取不到时退化为超管（仅超管可见，仍不是广播）。
func resolveBusinessEventAdminAddresses(ctx context.Context, db *ent.Client, payload map[string]interface{}) ([]string, error) {
	addresses := make([]string, 0, 2)
	seen := map[string]bool{}
	add := func(address string) {
		if address == "" || seen[address] {
			return
		}
		seen[address] = true
		addresses = append(addresses, address)
	}

	for _, key := range businessEventAdminPayloadKeys {
		if id := payloadInt(payload, key); id > 0 {
			add(adminNotificationAddress(ctx, db, id))
		}
	}
	if len(addresses) > 0 {
		return addresses, nil
	}

	supers, err := db.Admin.Query().
		Where(entadmin.IsSuperAdminEQ(true), entadmin.StatusEQ(entadmin.StatusActive)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range supers {
		add(adminNotificationAddress(ctx, db, row.ID))
	}
	return addresses, nil
}
