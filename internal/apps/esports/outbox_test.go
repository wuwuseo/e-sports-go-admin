package esports

import (
	"context"
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
)

type recordingPublisher struct {
	fail bool
	keys []string
}

func (p *recordingPublisher) Publish(_ context.Context, key, _, _ string, _ map[string]interface{}) error {
	p.keys = append(p.keys, key)
	if p.fail {
		return errors.New("channel unavailable")
	}
	return nil
}

func TestOutboxRetryAndDeadLetter(t *testing.T) {
	db := enttest.Open(t, "sqlite3", "file:esports_outbox?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	first := db.EsportsOutbox.Create().SetEventKey("event:first").SetEventType("gift.paid").
		SetAggregateRef("gift-1").SetPayload(map[string]interface{}{"gift_no": "gift-1"}).
		SetNextAttemptAt(now).SaveX(ctx)
	publisher := &recordingPublisher{fail: true}
	if _, err := deliverOutboxBatch(ctx, db, nil, now, 10); !errors.Is(err, errOutboxPublisherUnavailable) {
		t.Fatalf("missing publisher: %v", err)
	}
	count, err := deliverOutboxBatch(ctx, db, publisher, now, 10)
	if err != nil || count != 1 {
		t.Fatalf("first attempt: %d %v", count, err)
	}
	stored := db.EsportsOutbox.GetX(ctx, first.ID)
	if stored.Status != "pending" || stored.Attempts != 1 || stored.LastError == "" ||
		!stored.NextAttemptAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("retry not persisted: %+v", stored)
	}
	if count, err := deliverOutboxBatch(ctx, db, publisher, now.Add(30*time.Second), 10); err != nil || count != 0 {
		t.Fatalf("retry ran before due: %d %v", count, err)
	}
	publisher.fail = false
	if count, err := deliverOutboxBatch(ctx, db, publisher, now.Add(time.Minute), 10); err != nil || count != 1 {
		t.Fatalf("due retry: %d %v", count, err)
	}
	stored = db.EsportsOutbox.GetX(ctx, first.ID)
	if stored.Status != "delivered" || stored.Attempts != 2 || stored.DeliveredAt == nil ||
		len(publisher.keys) != 2 || publisher.keys[0] != publisher.keys[1] {
		t.Fatalf("delivery not idempotently keyed: %+v %+v", stored, publisher)
	}
	second := db.EsportsOutbox.Create().SetEventKey("event:dead").SetEventType("ledger.reconcile_anomaly").
		SetAggregateRef("ledger").SetPayload(map[string]interface{}{}).
		SetNextAttemptAt(now).SaveX(ctx)
	publisher.fail = true
	when := now
	for attempt := 1; attempt <= outboxMaxAttempts; attempt++ {
		if count, err := deliverOutboxBatch(ctx, db, publisher, when, 10); err != nil || count != 1 {
			t.Fatalf("attempt %d: %d %v", attempt, count, err)
		}
		when = db.EsportsOutbox.GetX(ctx, second.ID).NextAttemptAt
	}
	dead := db.EsportsOutbox.GetX(ctx, second.ID)
	if dead.Status != "dead" || dead.Attempts != outboxMaxAttempts {
		t.Fatalf("failed event not dead-lettered: %+v", dead)
	}
	if count, err := deliverOutboxBatch(ctx, db, publisher, when.Add(time.Hour), 10); err != nil || count != 0 {
		t.Fatalf("dead event was retried: %d %v", count, err)
	}
	stale := db.EsportsOutbox.Create().SetEventKey("event:stale").SetEventType("gift.refunded").
		SetAggregateRef("gift-3").SetPayload(map[string]interface{}{}).
		SetStatus("processing").SetAttempts(outboxMaxAttempts).
		SetLeaseUntil(now.Add(-time.Minute)).SetNextAttemptAt(now).SaveX(ctx)
	before := len(publisher.keys)
	if count, err := deliverOutboxBatch(ctx, db, publisher, now, 10); err != nil || count != 0 {
		t.Fatalf("expired final lease was republished: %d %v", count, err)
	}
	if got := db.EsportsOutbox.GetX(ctx, stale.ID); got.Status != "dead" || len(publisher.keys) != before {
		t.Fatalf("expired final lease did not dead-letter: %+v", got)
	}
}
