package esports

import (
	"context"
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/migrate"
)

// recordingNotificationSender 记录每次发送并可按需失败，用于验证通知投递与去重。
type recordingNotificationSender struct {
	fail  bool
	sends []string
}

func (s *recordingNotificationSender) Send(_ context.Context, channel, eventType, _ string, _ map[string]interface{}) error {
	s.sends = append(s.sends, channel+":"+eventType)
	if s.fail {
		return errors.New("channel down")
	}
	return nil
}

func openOutboxDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	return enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
}

func TestNotificationOutboxDeliveryAndDedup(t *testing.T) {
	db := openOutboxDB(t, "esports_notify_delivery")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	// 只启用企微通道，order.* 会命中 wecom_app。
	db.SystemConfig.Create().SetKey(notificationChannelsConfigKey).SetName("通知通道").
		SetValue(notifyChannelWecomApp).SaveX(ctx)
	db.EsportsOutbox.Create().SetEventKey("notify:closed:1").SetEventType("order.closed").
		SetAggregateRef("order-1").SetPayload(map[string]interface{}{"order_no": "order-1"}).
		SetNextAttemptAt(now).SaveX(ctx)
	sender := &messageNotificationSender{db: db}
	count, err := deliverNotificationOutboxBatch(ctx, db, sender, now, 10)
	if err != nil || count != 1 {
		t.Fatalf("first delivery: %d %v", count, err)
	}
	msgCount, err := db.Message.Query().Count(ctx)
	if err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if msgCount != 1 {
		t.Fatalf("expected one observable notification, got %d", msgCount)
	}
	stored := db.EsportsOutbox.GetX(ctx, 1)
	if stored.Status != esportsoutbox.StatusDelivered || stored.DeliveredAt == nil {
		t.Fatalf("notification not delivered: %+v", stored)
	}
	// 重跑：delivered 不再被认领，消息数不变（不重复发送）。
	if count, err = deliverNotificationOutboxBatch(ctx, db, sender, now.Add(time.Minute), 10); err != nil || count != 0 {
		t.Fatalf("re-run delivered: %d %v", count, err)
	}
	msgCount2, err := db.Message.Query().Count(ctx)
	if err != nil {
		t.Fatalf("count messages after re-run: %v", err)
	}
	if msgCount2 != 1 {
		t.Fatalf("notification re-sent on re-run: %d -> %d", msgCount, msgCount2)
	}
}

func TestNotificationOutboxFailureDeadLetterAndTodo(t *testing.T) {
	db := openOutboxDB(t, "esports_notify_dead")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	db.SystemConfig.Create().SetKey(notificationChannelsConfigKey).SetName("通知通道").
		SetValue(notifyChannelWecomApp).SaveX(ctx)
	first := db.EsportsOutbox.Create().SetEventKey("notify:fail:1").SetEventType("payment.receipt").
		SetAggregateRef("order-2").SetPayload(map[string]interface{}{"order_no": "order-2"}).
		SetNextAttemptAt(now).SaveX(ctx)
	sender := &recordingNotificationSender{fail: true}
	when := now
	for attempt := 1; attempt <= outboxMaxAttempts; attempt++ {
		count, err := deliverNotificationOutboxBatch(ctx, db, sender, when, 10)
		if err != nil || count != 1 {
			t.Fatalf("attempt %d: %d %v", attempt, count, err)
		}
		when = db.EsportsOutbox.GetX(ctx, first.ID).NextAttemptAt
	}
	dead := db.EsportsOutbox.GetX(ctx, first.ID)
	if dead.Status != esportsoutbox.StatusDead || dead.Attempts != outboxMaxAttempts {
		t.Fatalf("failed notification not dead-lettered: %+v", dead)
	}
	failed, err := countFailedNotifications(ctx, db)
	if err != nil || failed != 1 {
		t.Fatalf("failed notification count: %d %v", failed, err)
	}
	todos, err := csWorkbenchTodos(ctx, db, 0, true, now)
	if err != nil {
		t.Fatalf("cs todos: %v", err)
	}
	var todoCount int
	found := false
	for _, item := range todos {
		if item.Key == "notification_failed" {
			found = true
			todoCount = item.Count
		}
	}
	if !found || todoCount != 1 {
		t.Fatalf("notification_failed todo missing/wrong: found=%v count=%d todos=%+v", found, todoCount, todos)
	}
}

func TestNotificationOutboxLeaseExpiryReclaim(t *testing.T) {
	db := openOutboxDB(t, "esports_notify_lease")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	db.SystemConfig.Create().SetKey(notificationChannelsConfigKey).SetName("通知通道").
		SetValue(notifyChannelWecomApp).SaveX(ctx)
	stale := db.EsportsOutbox.Create().SetEventKey("notify:stale:1").SetEventType("order.closed").
		SetAggregateRef("order-3").SetPayload(map[string]interface{}{}).
		SetStatus(esportsoutbox.StatusProcessing).SetAttempts(1).
		SetLeaseUntil(now.Add(-time.Minute)).SetNextAttemptAt(now).SaveX(ctx)
	sender := &messageNotificationSender{db: db}
	count, err := deliverNotificationOutboxBatch(ctx, db, sender, now, 10)
	if err != nil || count != 1 {
		t.Fatalf("expired lease reclaim: %d %v", count, err)
	}
	got := db.EsportsOutbox.GetX(ctx, stale.ID)
	if got.Status != esportsoutbox.StatusDelivered || got.Attempts != 2 {
		t.Fatalf("expired lease not reclaimed: %+v", got)
	}
}

func TestOutboxBusinessAndNotificationPartition(t *testing.T) {
	db := openOutboxDB(t, "esports_outbox_partition")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	db.SystemConfig.Create().SetKey(notificationChannelsConfigKey).SetName("通知通道").
		SetValue(notifyChannelWecomApp).SaveX(ctx)
	db.EsportsOutbox.Create().SetEventKey("biz:1").SetEventType("gift.paid").
		SetAggregateRef("gift-1").SetPayload(map[string]interface{}{}).SetNextAttemptAt(now).SaveX(ctx)
	db.EsportsOutbox.Create().SetEventKey("ntf:1").SetEventType("order.closed").
		SetAggregateRef("order-1").SetPayload(map[string]interface{}{}).SetNextAttemptAt(now).SaveX(ctx)

	publisher := &recordingPublisher{}
	if count, err := deliverOutboxBatch(ctx, db, publisher, now, 10); err != nil || count != 1 {
		t.Fatalf("business batch: %d %v", count, err)
	}
	if len(publisher.keys) != 1 || publisher.keys[0] != "biz:1" {
		t.Fatalf("business consumer claimed notification event: %+v", publisher.keys)
	}

	sender := &recordingNotificationSender{}
	if count, err := deliverNotificationOutboxBatch(ctx, db, sender, now, 10); err != nil || count != 1 {
		t.Fatalf("notification batch: %d %v", count, err)
	}
	if len(sender.sends) != 1 || sender.sends[0] != notifyChannelWecomApp+":order.closed" {
		t.Fatalf("notification consumer claimed business event: %+v", sender.sends)
	}
}
