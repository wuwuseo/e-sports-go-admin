package esports

import (
	"context"
	"errors"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsoutbox"
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

// deliverOutboxBatch claims due rows with a conditional update, then records
// every delivery result. Failed rows remain visible for retry or manual repair.
func deliverOutboxBatch(ctx context.Context, db *ent.Client, publisher EventPublisher, now time.Time, limit int) (int, error) {
	if publisher == nil {
		return 0, errOutboxPublisherUnavailable
	}
	if limit < 1 || limit > 1000 {
		return 0, errInvalidInput
	}
	now = now.UTC()
	ids, err := db.EsportsOutbox.Query().Where(esportsoutbox.Or(
		esportsoutbox.And(esportsoutbox.StatusEQ(esportsoutbox.StatusPending), esportsoutbox.NextAttemptAtLTE(now)),
		esportsoutbox.And(esportsoutbox.StatusEQ(esportsoutbox.StatusProcessing), esportsoutbox.LeaseUntilLTE(now)),
	)).Order(ent.Asc(esportsoutbox.FieldID)).Limit(limit).IDs(ctx)
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
				esportsoutbox.IDEQ(id), esportsoutbox.AttemptsEQ(event.Attempts),
				esportsoutbox.Or(
					esportsoutbox.And(esportsoutbox.StatusEQ(esportsoutbox.StatusPending), esportsoutbox.NextAttemptAtLTE(now)),
					esportsoutbox.And(esportsoutbox.StatusEQ(esportsoutbox.StatusProcessing), esportsoutbox.LeaseUntilLTE(now)),
				),
			).SetStatus(esportsoutbox.StatusDead).ClearLeaseUntil().
				SetLastError("达到最大重试次数，等待人工处理").Save(ctx)
			if err != nil {
				return count, err
			}
			continue
		}
		leaseUntil := now.Add(outboxLease)
		claimed, err := db.EsportsOutbox.Update().Where(
			esportsoutbox.IDEQ(id), esportsoutbox.AttemptsEQ(event.Attempts),
			esportsoutbox.Or(
				esportsoutbox.And(esportsoutbox.StatusEQ(esportsoutbox.StatusPending), esportsoutbox.NextAttemptAtLTE(now)),
				esportsoutbox.And(esportsoutbox.StatusEQ(esportsoutbox.StatusProcessing), esportsoutbox.LeaseUntilLTE(now)),
			),
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

func outboxBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 7 {
		attempt = 7
	}
	return time.Minute * time.Duration(1<<(attempt-1))
}
