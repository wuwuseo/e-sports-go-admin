package esports

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportsaftersale"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/message"
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

// ── 通知测试种子 ──

func seedNotificationAdmin(t *testing.T, db *ent.Client, uid string) *ent.Admin {
	t.Helper()
	return db.Admin.Create().SetEmail(uid + "@example.com").SetPassword("test").
		SetUID(uid).SetUsername(uid).SetStatus("active").SaveX(t.Context())
}

func seedNotificationCustomer(t *testing.T, db *ent.Client, name string) *ent.EsportsCustomer {
	t.Helper()
	return db.EsportsCustomer.Create().SetDisplayName(name).SaveX(t.Context())
}

func seedNotificationPlayer(t *testing.T, db *ent.Client, userID int, name string) *ent.EsportsPlayer {
	t.Helper()
	return db.EsportsPlayer.Create().SetUserID(userID).SetDisplayName(name).SaveX(t.Context())
}

// seedNotificationOrder 建一张最小可用的订单；adminID<=0 表示无责任客服。
func seedNotificationOrder(t *testing.T, db *ent.Client, orderNo string, customerID, adminID int) *ent.EsportsOrder {
	t.Helper()
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	create := db.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(customerID).SetSkuID(1).
		SetQuantity(1).SetUnitPriceCents(10000).SetPayableCents(10000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetPaymentDeadline(now.Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusCompleted)
	if adminID > 0 {
		create.SetResponsibleAdminID(adminID)
	}
	return create.SaveX(t.Context())
}

func seedAcceptedSeat(t *testing.T, db *ent.Client, orderID, playerID int) *ent.EsportsDispatchSeat {
	t.Helper()
	return db.EsportsDispatchSeat.Create().SetOrderID(orderID).SetSeatIndex(1).
		SetPlayerID(playerID).SetStatus(esportsdispatchseat.StatusAccepted).SaveX(t.Context())
}

func notificationMessages(t *testing.T, db *ent.Client) []*ent.Message {
	t.Helper()
	return db.Message.Query().Order(ent.Asc(message.FieldID)).AllX(t.Context())
}

func enableNotificationChannel(t *testing.T, db *ent.Client, channels string) {
	t.Helper()
	db.SystemConfig.Create().SetKey(notificationChannelsConfigKey).SetName("通知通道").
		SetValue(channels).SaveX(t.Context())
}

// ── 站内信：按角色寻址 ──

// 订单事件只生成客户 / 责任客服 / 已接单陪玩三条消息，且 user_uid 全部非空、正文不含载荷 JSON。
func TestNotificationOutboxOrderRecipients(t *testing.T) {
	db := openOutboxDB(t, "esports_notify_order_recipients")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)

	admin := seedNotificationAdmin(t, db, "notify-admin")
	customer := seedNotificationCustomer(t, db, "通知客户")
	player := seedNotificationPlayer(t, db, 9001, "通知陪玩")
	order := seedNotificationOrder(t, db, "NO-ORDER-1", customer.ID, admin.ID)
	seedAcceptedSeat(t, db, order.ID, player.ID)
	enableNotificationChannel(t, db, notifyChannelWecomApp)

	db.EsportsOutbox.Create().SetEventKey("notify:order:1").SetEventType("order.closed").
		SetAggregateRef(order.OrderNo).
		SetPayload(map[string]interface{}{
			"order_no": order.OrderNo, "internal_note": "不应出现在正文",
		}).SetNextAttemptAt(now).SaveX(ctx)

	count, err := deliverNotificationOutboxBatch(ctx, db, &messageNotificationSender{db: db}, now, 10)
	if err != nil || count != 1 {
		t.Fatalf("delivery: %d %v", count, err)
	}
	messages := notificationMessages(t, db)
	if len(messages) != 3 {
		t.Fatalf("expected 3 role-addressed messages, got %d: %+v", len(messages), messages)
	}
	want := []string{customerNotificationAddress(customer.ID), admin.UID, playerNotificationAddress(player.ID)}
	for i, msg := range messages {
		if msg.UserUID == "" {
			t.Fatalf("message %d is a broadcast (empty user_uid): %+v", i, msg)
		}
		if msg.UserUID != want[i] {
			t.Fatalf("message %d address = %q, want %q", i, msg.UserUID, want[i])
		}
		if strings.Contains(msg.Content, "internal_note") || strings.Contains(msg.Content, "{") {
			t.Fatalf("message content leaked payload JSON: %q", msg.Content)
		}
		if _, ok := msg.Extra["payload"]; ok {
			t.Fatalf("message extra must not carry full payload: %+v", msg.Extra)
		}
		if msg.Title == "" || msg.Content == "" {
			t.Fatalf("message must have business title/summary: %+v", msg)
		}
	}
	if !strings.Contains(messages[0].Content, order.OrderNo) {
		t.Fatalf("customer summary missing order no: %q", messages[0].Content)
	}
	for i, role := range []string{notificationRoleCustomer, notificationRoleAdmin, notificationRolePlayer} {
		raw, _ := json.Marshal(messages[i].Extra["roles"])
		if !strings.Contains(string(raw), role) {
			t.Fatalf("message %d roles missing %q: %s", i, role, raw)
		}
	}
	stored := db.EsportsOutbox.Query().Where(esportsoutbox.EventKeyEQ("notify:order:1")).OnlyX(ctx)
	if stored.Status != esportsoutbox.StatusDelivered {
		t.Fatalf("notification not delivered: %+v", stored)
	}
}

// 重跑 delivered 事件不再投递，消息数不变。
func TestNotificationOutboxDeliveryAndDedup(t *testing.T) {
	db := openOutboxDB(t, "esports_notify_delivery")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)

	customer := seedNotificationCustomer(t, db, "去重客户")
	order := seedNotificationOrder(t, db, "NO-DEDUP-1", customer.ID, 0)
	enableNotificationChannel(t, db, notifyChannelWecomApp)
	db.EsportsOutbox.Create().SetEventKey("notify:dedup:1").SetEventType("order.closed").
		SetAggregateRef(order.OrderNo).SetPayload(map[string]interface{}{"order_no": order.OrderNo}).
		SetNextAttemptAt(now).SaveX(ctx)

	sender := &messageNotificationSender{db: db}
	if count, err := deliverNotificationOutboxBatch(ctx, db, sender, now, 10); err != nil || count != 1 {
		t.Fatalf("first delivery: %d %v", count, err)
	}
	if count, err := deliverNotificationOutboxBatch(ctx, db, sender, now.Add(time.Minute), 10); err != nil || count != 0 {
		t.Fatalf("re-run delivered: %d %v", count, err)
	}
	if messages := notificationMessages(t, db); len(messages) != 1 {
		t.Fatalf("notification re-sent on re-run: %d", len(messages))
	}
}

// 无责任客服的订单只投客户/陪玩，不报错。
func TestNotificationOutboxOrderWithoutResponsibleAdmin(t *testing.T) {
	db := openOutboxDB(t, "esports_notify_no_admin")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)

	customer := seedNotificationCustomer(t, db, "无客服客户")
	player := seedNotificationPlayer(t, db, 9002, "无客服陪玩")
	order := seedNotificationOrder(t, db, "NO-ADMIN-1", customer.ID, 0)
	seedAcceptedSeat(t, db, order.ID, player.ID)
	enableNotificationChannel(t, db, notifyChannelWecomApp)

	db.EsportsOutbox.Create().SetEventKey("notify:no-admin:1").SetEventType("order.cancelled").
		SetAggregateRef(order.OrderNo).SetPayload(map[string]interface{}{"order_no": order.OrderNo}).
		SetNextAttemptAt(now).SaveX(ctx)

	if count, err := deliverNotificationOutboxBatch(ctx, db, &messageNotificationSender{db: db}, now, 10); err != nil || count != 1 {
		t.Fatalf("delivery without responsible admin: %d %v", count, err)
	}
	messages := notificationMessages(t, db)
	if len(messages) != 2 {
		t.Fatalf("expected customer+player messages, got %d: %+v", len(messages), messages)
	}
	for _, msg := range messages {
		if msg.UserUID == "" || strings.HasPrefix(msg.UserUID, "admin:") {
			t.Fatalf("unexpected admin/broadcast recipient: %+v", msg)
		}
	}
	stored := db.EsportsOutbox.Query().Where(esportsoutbox.EventKeyEQ("notify:no-admin:1")).OnlyX(ctx)
	if stored.Status != esportsoutbox.StatusDelivered {
		t.Fatalf("event should be delivered without responsible admin: %+v", stored)
	}
}

// player.heartbeat_timeout 只投该陪玩。
func TestNotificationOutboxPlayerHeartbeatRecipient(t *testing.T) {
	db := openOutboxDB(t, "esports_notify_player")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)

	player := seedNotificationPlayer(t, db, 9003, "心跳陪玩")
	enableNotificationChannel(t, db, notifyChannelPush) // 推送通道开启但 webhook 未配置，不应阻断站内信。

	db.EsportsOutbox.Create().SetEventKey("player.heartbeat_timeout:1:1").
		SetEventType("player.heartbeat_timeout").
		SetAggregateRef(strconv.Itoa(player.ID)).
		SetPayload(map[string]interface{}{"player_id": player.ID, "presence": "offline"}).
		SetNextAttemptAt(now).SaveX(ctx)

	if count, err := deliverNotificationOutboxBatch(ctx, db, &messageNotificationSender{db: db}, now, 10); err != nil || count != 1 {
		t.Fatalf("player notification: %d %v", count, err)
	}
	messages := notificationMessages(t, db)
	if len(messages) != 1 {
		t.Fatalf("expected exactly one player message, got %d: %+v", len(messages), messages)
	}
	if messages[0].UserUID != playerNotificationAddress(player.ID) {
		t.Fatalf("player address = %q, want %q", messages[0].UserUID, playerNotificationAddress(player.ID))
	}
}

// 售后事件：来源客户 + 责任客服，不包含已接单陪玩。
func TestNotificationOutboxAfterSaleRecipients(t *testing.T) {
	db := openOutboxDB(t, "esports_notify_aftersale")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)

	admin := seedNotificationAdmin(t, db, "aftersale-admin")
	customer := seedNotificationCustomer(t, db, "售后客户")
	order := seedNotificationOrder(t, db, "NO-AFTER-1", customer.ID, admin.ID)
	item := db.EsportsAfterSale.Create().SetAfterSaleNo("AS-1").
		SetSourceType(esportsaftersale.SourceTypeOrder).SetSourceID(order.ID).
		SetKind(esportsaftersale.KindRefundRequest).SetChannel(esportsaftersale.ChannelCustomer).
		SetDemand("退款").SetCustomerID(customer.ID).
		SetStatus(esportsaftersale.StatusPendingAccept).SaveX(ctx)
	enableNotificationChannel(t, db, notifyChannelWecomApp)

	db.EsportsOutbox.Create().SetEventKey("aftersale:sla:1").SetEventType("aftersale.sla_breach").
		SetAggregateRef(item.AfterSaleNo).
		SetPayload(map[string]interface{}{"after_sale_no": item.AfterSaleNo, "source_type": "order", "source_id": order.ID}).
		SetNextAttemptAt(now).SaveX(ctx)

	if count, err := deliverNotificationOutboxBatch(ctx, db, &messageNotificationSender{db: db}, now, 10); err != nil || count != 1 {
		t.Fatalf("aftersale notification: %d %v", count, err)
	}
	messages := notificationMessages(t, db)
	if len(messages) != 2 {
		t.Fatalf("expected customer+admin messages, got %d: %+v", len(messages), messages)
	}
	if messages[0].UserUID != customerNotificationAddress(customer.ID) || messages[1].UserUID != admin.UID {
		t.Fatalf("aftersale addresses = %q/%q", messages[0].UserUID, messages[1].UserUID)
	}
}

// 解析不到收件人：绝不写 user_uid 空记录，事件退避后置 dead。
func TestNotificationOutboxMissingRecipientsGoesDead(t *testing.T) {
	db := openOutboxDB(t, "esports_notify_missing")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	enableNotificationChannel(t, db, notifyChannelWecomApp)

	event := db.EsportsOutbox.Create().SetEventKey("notify:missing:1").SetEventType("order.closed").
		SetAggregateRef("NO-SUCH-ORDER").SetPayload(map[string]interface{}{"order_no": "NO-SUCH-ORDER"}).
		SetNextAttemptAt(now).SaveX(ctx)

	when := now
	for attempt := 1; attempt <= outboxMaxAttempts; attempt++ {
		count, err := deliverNotificationOutboxBatch(ctx, db, &messageNotificationSender{db: db}, when, 10)
		if err != nil || count != 1 {
			t.Fatalf("attempt %d: %d %v", attempt, count, err)
		}
		stored := db.EsportsOutbox.GetX(ctx, event.ID)
		if !strings.Contains(stored.LastError, "收件人") {
			t.Fatalf("attempt %d last_error = %q, want recipient error", attempt, stored.LastError)
		}
		when = stored.NextAttemptAt
	}
	dead := db.EsportsOutbox.GetX(ctx, event.ID)
	if dead.Status != esportsoutbox.StatusDead || dead.Attempts != outboxMaxAttempts {
		t.Fatalf("missing recipients not dead-lettered: %+v", dead)
	}
	if messages := notificationMessages(t, db); len(messages) != 0 {
		t.Fatalf("must not write broadcast/inbox records without recipients: %+v", messages)
	}
}

// 通道全关时站内信仍然投递（本实现口径：站内信不受 notification_channels 控制）。
func TestNotificationOutboxAllChannelsDisabledStillInApp(t *testing.T) {
	db := openOutboxDB(t, "esports_notify_all_off")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)

	customer := seedNotificationCustomer(t, db, "全关客户")
	order := seedNotificationOrder(t, db, "NO-ALLOFF-1", customer.ID, 0)
	db.EsportsOutbox.Create().SetEventKey("notify:all-off:1").SetEventType("order.closed").
		SetAggregateRef(order.OrderNo).SetPayload(map[string]interface{}{"order_no": order.OrderNo}).
		SetNextAttemptAt(now).SaveX(ctx)

	if count, err := deliverNotificationOutboxBatch(ctx, db, &messageNotificationSender{db: db}, now, 10); err != nil || count != 1 {
		t.Fatalf("all channels off delivery: %d %v", count, err)
	}
	messages := notificationMessages(t, db)
	if len(messages) != 1 || messages[0].UserUID != customerNotificationAddress(customer.ID) {
		t.Fatalf("in-app delivery must ignore channel switches: %+v", messages)
	}
	stored := db.EsportsOutbox.Query().Where(esportsoutbox.EventKeyEQ("notify:all-off:1")).OnlyX(ctx)
	if stored.Status != esportsoutbox.StatusDelivered {
		t.Fatalf("in-app success should mark delivered: %+v", stored)
	}
}

// ── webhook 推送 ──

// 组合投递器在通道开启且 webhook 已配置时真实调用推送，并与站内信并存。
func TestNotificationOutboxCompositePushesWebhook(t *testing.T) {
	db := openOutboxDB(t, "esports_notify_composite")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)

	customer := seedNotificationCustomer(t, db, "组合投递客户")
	order := seedNotificationOrder(t, db, "NO-COMPOSITE-1", customer.ID, 0)
	enableNotificationChannel(t, db, notifyChannelPush)

	hits := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body notificationWebhookRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		hits <- body.Channel
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	cfg, _ := json.Marshal(map[string]interface{}{"url": server.URL, "secret": "s", "timeout_seconds": 2})
	db.SystemConfig.Create().SetKey(notificationPushWebhookConfigKey).SetName("推送网关").
		SetValue(string(cfg)).SaveX(ctx)

	db.EsportsOutbox.Create().SetEventKey("notify:composite:1").SetEventType("order.closed").
		SetAggregateRef(order.OrderNo).SetPayload(map[string]interface{}{"order_no": order.OrderNo}).
		SetNextAttemptAt(now).SaveX(ctx)

	if count, err := deliverNotificationOutboxBatch(ctx, db, &messageNotificationSender{db: db}, now, 10); err != nil || count != 1 {
		t.Fatalf("composite delivery: %d %v", count, err)
	}
	select {
	case channel := <-hits:
		if channel != notifyChannelPush {
			t.Fatalf("webhook channel = %q", channel)
		}
	default:
		t.Fatal("webhook was not called")
	}
	if messages := notificationMessages(t, db); len(messages) != 1 {
		t.Fatalf("in-app message missing: %+v", messages)
	}
	stored := db.EsportsOutbox.Query().Where(esportsoutbox.EventKeyEQ("notify:composite:1")).OnlyX(ctx)
	if stored.Status != esportsoutbox.StatusDelivered {
		t.Fatalf("composite event not delivered: %+v", stored)
	}
}

func TestNotificationWebhookSignatureAndRecipients(t *testing.T) {
	db := openOutboxDB(t, "esports_notify_webhook")
	defer db.Close()
	ctx := t.Context()

	customer := seedNotificationCustomer(t, db, "推送客户")
	admin := seedNotificationAdmin(t, db, "webhook-admin")
	order := seedNotificationOrder(t, db, "NO-HOOK-1", customer.ID, admin.ID)

	type captured struct {
		signature string
		body      []byte
	}
	got := make(chan captured, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- captured{signature: r.Header.Get("X-Esports-Signature"), body: body}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	secret := "test-secret"
	cfg, _ := json.Marshal(map[string]interface{}{
		"url": server.URL, "secret": secret, "timeout_seconds": 2,
	})
	db.SystemConfig.Create().SetKey(notificationPushWebhookConfigKey).SetName("推送网关").
		SetValue(string(cfg)).SaveX(ctx)

	sender := newWebhookNotificationSender(db)
	err := sender.Send(ctx, notifyChannelPush, "order.closed", order.OrderNo, map[string]interface{}{
		"order_no": order.OrderNo, "internal_note": "不应进入推送体",
	})
	if err != nil {
		t.Fatalf("webhook send: %v", err)
	}
	result := <-got
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(result.body)
	wantSignature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if result.signature != wantSignature {
		t.Fatalf("signature = %q, want %q", result.signature, wantSignature)
	}
	var body notificationWebhookRequest
	if err := json.Unmarshal(result.body, &body); err != nil {
		t.Fatalf("decode body: %v (%s)", err, result.body)
	}
	if body.Channel != notifyChannelPush || body.EventType != "order.closed" || body.OrderNo != order.OrderNo {
		t.Fatalf("unexpected webhook body: %+v", body)
	}
	if len(body.Recipients) != 2 {
		t.Fatalf("expected customer+admin recipients, got %+v", body.Recipients)
	}
	if body.Recipients[0].Role != notificationRoleCustomer || body.Recipients[0].Address != customerNotificationAddress(customer.ID) {
		t.Fatalf("customer recipient = %+v", body.Recipients[0])
	}
	if body.Recipients[1].Role != notificationRoleAdmin || body.Recipients[1].Address != admin.UID {
		t.Fatalf("admin recipient = %+v", body.Recipients[1])
	}
	if strings.Contains(string(result.body), "internal_note") {
		t.Fatalf("webhook body leaked full payload: %s", result.body)
	}
}

func TestNotificationWebhookNon2xx(t *testing.T) {
	db := openOutboxDB(t, "esports_notify_webhook_401")
	defer db.Close()
	ctx := t.Context()

	customer := seedNotificationCustomer(t, db, "401 客户")
	order := seedNotificationOrder(t, db, "NO-HOOK-401", customer.ID, 0)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	cfg, _ := json.Marshal(map[string]interface{}{"url": server.URL, "secret": "s", "timeout_seconds": 2})
	db.SystemConfig.Create().SetKey(notificationPushWebhookConfigKey).SetName("推送网关").
		SetValue(string(cfg)).SaveX(ctx)

	sender := newWebhookNotificationSender(db)
	err := sender.Send(ctx, notifyChannelWecomApp, "order.closed", order.OrderNo,
		map[string]interface{}{"order_no": order.OrderNo})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("non-2xx should fail with status, got %v", err)
	}
}

// ── 既有 outbox 语义 ──

func TestNotificationOutboxFailureDeadLetterAndTodo(t *testing.T) {
	db := openOutboxDB(t, "esports_notify_dead")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	enableNotificationChannel(t, db, notifyChannelWecomApp)
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
	customer := seedNotificationCustomer(t, db, "租约客户")
	order := seedNotificationOrder(t, db, "NO-LEASE-1", customer.ID, 0)
	enableNotificationChannel(t, db, notifyChannelWecomApp)
	stale := db.EsportsOutbox.Create().SetEventKey("notify:stale:1").SetEventType("order.closed").
		SetAggregateRef(order.OrderNo).SetPayload(map[string]interface{}{"order_no": order.OrderNo}).
		SetStatus(esportsoutbox.StatusProcessing).SetAttempts(1).
		SetLeaseUntil(now.Add(-time.Minute)).SetNextAttemptAt(now).SaveX(ctx)
	count, err := deliverNotificationOutboxBatch(ctx, db, &messageNotificationSender{db: db}, now, 10)
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
	enableNotificationChannel(t, db, notifyChannelWecomApp)
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
