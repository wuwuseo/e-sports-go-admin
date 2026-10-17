package esports

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/message"
)

// ── 投递判定：站内信成功不能掩盖已配置推送通道的失败 ──

// failingPushWebhook 启动一个恒返回 500 的推送网关，并写入 webhook 配置。
func failingPushWebhook(t *testing.T, db *ent.Client) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	cfg, _ := json.Marshal(map[string]interface{}{"url": server.URL, "secret": "s", "timeout_seconds": 2})
	db.SystemConfig.Create().SetKey(notificationPushWebhookConfigKey).SetName("推送网关").
		SetValue(string(cfg)).SaveX(t.Context())
	return server
}

// 推送通道已配置但网关 500：组合投递必须返回错误，outbox 保持待重试而不是 delivered。
func TestNotificationCompositePushFailureMarksOutboxPending(t *testing.T) {
	db := openOutboxDB(t, "esports_notify_push_fail")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)

	customer := seedNotificationCustomer(t, db, "推送失败客户")
	order := seedNotificationOrder(t, db, "NO-PUSH-FAIL-1", customer.ID, 0)
	enableNotificationChannel(t, db, notifyChannelPush)
	failingPushWebhook(t, db)

	event := db.EsportsOutbox.Create().SetEventKey("notify:push-fail:1").SetEventType("order.closed").
		SetAggregateRef(order.OrderNo).SetPayload(map[string]interface{}{"order_no": order.OrderNo}).
		SetNextAttemptAt(now).SaveX(ctx)

	count, err := deliverNotificationOutboxBatch(ctx, db, &messageNotificationSender{db: db}, now, 10)
	if err != nil || count != 1 {
		t.Fatalf("delivery: %d %v", count, err)
	}
	stored := db.EsportsOutbox.GetX(ctx, event.ID)
	if stored.Status != esportsoutbox.StatusPending {
		t.Fatalf("push failure must keep event retryable, got %+v", stored)
	}
	if stored.Attempts != 1 || !strings.Contains(stored.LastError, "500") {
		t.Fatalf("push failure not recorded: attempts=%d last_error=%q", stored.Attempts, stored.LastError)
	}
	if messages := notificationMessages(t, db); len(messages) != 1 {
		t.Fatalf("in-app message should still be written once: %+v", messages)
	}
}

// 站内信成功 + 推送失败：重试不会重复写站内信（同一事件只产生一条）。
func TestNotificationCompositeRetryIsIdempotent(t *testing.T) {
	db := openOutboxDB(t, "esports_notify_push_retry_idem")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)

	customer := seedNotificationCustomer(t, db, "重试客户")
	order := seedNotificationOrder(t, db, "NO-RETRY-1", customer.ID, 0)
	enableNotificationChannel(t, db, notifyChannelPush)
	failingPushWebhook(t, db)

	event := db.EsportsOutbox.Create().SetEventKey("notify:retry:1").SetEventType("order.closed").
		SetAggregateRef(order.OrderNo).SetPayload(map[string]interface{}{"order_no": order.OrderNo}).
		SetNextAttemptAt(now).SaveX(ctx)

	sender := &messageNotificationSender{db: db}
	when := now
	for attempt := 1; attempt <= 2; attempt++ {
		if count, err := deliverNotificationOutboxBatch(ctx, db, sender, when, 10); err != nil || count != 1 {
			t.Fatalf("attempt %d: %d %v", attempt, count, err)
		}
		when = db.EsportsOutbox.GetX(ctx, event.ID).NextAttemptAt
	}
	messages := notificationMessages(t, db)
	if len(messages) != 1 {
		t.Fatalf("retry duplicated in-app message: %d %+v", len(messages), messages)
	}
	if key, _ := messages[0].Extra[notificationMessageDedupeField].(string); key != "notify:retry:1" {
		t.Fatalf("dedupe key = %q, want outbox event key", key)
	}
	if stored := db.EsportsOutbox.GetX(ctx, event.ID); stored.Attempts != 2 {
		t.Fatalf("expected two attempts, got %+v", stored)
	}
}

// 推送通道已启用但 webhook 未配置：容忍降级，站内信成功即 delivered。
func TestNotificationCompositeToleratesUnconfiguredPush(t *testing.T) {
	db := openOutboxDB(t, "esports_notify_push_unconfigured")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)

	customer := seedNotificationCustomer(t, db, "未配置客户")
	order := seedNotificationOrder(t, db, "NO-UNCONFIG-1", customer.ID, 0)
	enableNotificationChannel(t, db, notifyChannelPush) // 只有开关，没有 webhook 配置。

	event := db.EsportsOutbox.Create().SetEventKey("notify:unconfig:1").SetEventType("order.closed").
		SetAggregateRef(order.OrderNo).SetPayload(map[string]interface{}{"order_no": order.OrderNo}).
		SetNextAttemptAt(now).SaveX(ctx)

	if count, err := deliverNotificationOutboxBatch(ctx, db, &messageNotificationSender{db: db}, now, 10); err != nil || count != 1 {
		t.Fatalf("delivery: %d %v", count, err)
	}
	stored := db.EsportsOutbox.GetX(ctx, event.ID)
	if stored.Status != esportsoutbox.StatusDelivered {
		t.Fatalf("unconfigured push must not block in-app delivery: %+v", stored)
	}
	if messages := notificationMessages(t, db); len(messages) != 1 {
		t.Fatalf("in-app message missing: %+v", messages)
	}
}

// 直接调用组合投递器：推送失败必须返回 error；重复调用不重复写站内信。
func TestNotificationCompositeSendReturnsPushError(t *testing.T) {
	db := openOutboxDB(t, "esports_notify_composite_send_err")
	defer db.Close()
	ctx := t.Context()

	customer := seedNotificationCustomer(t, db, "组合失败客户")
	order := seedNotificationOrder(t, db, "NO-SEND-ERR-1", customer.ID, 0)
	failingPushWebhook(t, db)

	sender := newCompositeNotificationSender(db)
	if err := sender.Send(ctx, notifyChannelPush, "order.closed", order.OrderNo,
		map[string]interface{}{"order_no": order.OrderNo}); err == nil {
		t.Fatal("in-app success must not hide push failure")
	}
	if err := sender.Send(ctx, notifyChannelPush, "order.closed", order.OrderNo,
		map[string]interface{}{"order_no": order.OrderNo}); err == nil {
		t.Fatal("push failure should repeat on resend")
	}
	if messages := notificationMessages(t, db); len(messages) != 1 {
		t.Fatalf("same event must only produce one message: %d", len(messages))
	}
}

// 站内信写入按「地址 + 事件定位键」查重：不同事件各自落一条，同事件不重复。
func TestNotificationMessageDedupeKeyScopedPerAddress(t *testing.T) {
	db := openOutboxDB(t, "esports_notify_dedupe_scope")
	defer db.Close()
	ctx := t.Context()

	customer := seedNotificationCustomer(t, db, "幂等客户")
	order := seedNotificationOrder(t, db, "NO-DEDUPE-SCOPE-1", customer.ID, 0)
	sender := &messageNotificationSender{db: db}

	payload := map[string]interface{}{"order_no": order.OrderNo}
	if err := sender.Send(ctx, notifyChannelInApp, "order.closed", order.OrderNo, payload); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if err := sender.Send(ctx, notifyChannelInApp, "order.closed", order.OrderNo, payload); err != nil {
		t.Fatalf("duplicate send: %v", err)
	}
	if messages := notificationMessages(t, db); len(messages) != 1 {
		t.Fatalf("dedupe failed: %d", len(messages))
	}
	// 同一订单的另一类事件不共享定位键，应正常追加。
	if err := sender.Send(ctx, notifyChannelInApp, "order.cancelled", order.OrderNo, payload); err != nil {
		t.Fatalf("second event: %v", err)
	}
	messages := notificationMessages(t, db)
	if len(messages) != 2 {
		t.Fatalf("distinct events must both be stored: %d", len(messages))
	}
	for i, msg := range messages {
		if msg.UserUID != customerNotificationAddress(customer.ID) {
			t.Fatalf("message %d address = %q", i, msg.UserUID)
		}
	}
}

// 确认站内信幂等字段使用 queryable 的 type，而不是把定位键塞进正文。
func TestNotificationMessageKeepsBusinessType(t *testing.T) {
	db := openOutboxDB(t, "esports_notify_message_type")
	defer db.Close()
	ctx := t.Context()

	customer := seedNotificationCustomer(t, db, "类型客户")
	order := seedNotificationOrder(t, db, "NO-TYPE-1", customer.ID, 0)
	if err := (&messageNotificationSender{db: db}).Send(ctx, notifyChannelInApp, "order.closed",
		order.OrderNo, map[string]interface{}{"order_no": order.OrderNo}); err != nil {
		t.Fatalf("send: %v", err)
	}
	rows, err := db.Message.Query().Where(message.TypeEQ(notificationMessageType)).All(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("notification message type: %d %v", len(rows), err)
	}
}
