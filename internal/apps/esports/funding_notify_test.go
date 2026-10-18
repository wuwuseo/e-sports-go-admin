package esports

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"go-admin/internal/ent/esportsoutbox"
)

// assertSingleTargetedRecipient 断言收件人恰好一个、地址非空（不是 user_uid="" 广播）且角色正确。
func assertSingleTargetedRecipient(t *testing.T, eventType string, recipients []notificationRecipient, wantAddress, wantRole string) {
	t.Helper()
	for _, recipient := range recipients {
		if strings.TrimSpace(recipient.Address) == "" {
			t.Fatalf("%s 解析出空地址收件人（广播）: %+v", eventType, recipient)
		}
	}
	if len(recipients) != 1 {
		t.Fatalf("%s 收件人 = %+v, want 恰好 %s", eventType, recipients, wantAddress)
	}
	if recipients[0].Address != wantAddress || recipients[0].Role != wantRole {
		t.Fatalf("%s 收件人 = %+v, want address=%s role=%s", eventType, recipients[0], wantAddress, wantRole)
	}
}

// 任务 1 / 任务 2 验收 1：三个事件类型都必须进入通道表（否则通知消费者不认领、收件人也不解析），
// 并且必须有专属业务文案，不能落到通用兜底或死信。
func TestNotificationChannelTargetsCoverRemindOvertimeAndDurationMismatch(t *testing.T) {
	cases := []struct {
		eventType string
		wantTitle string
	}{
		{"dispatch.remind_player", "派单提醒（陪玩）"},
		{"dispatch.service_overtime", "服务超时待客服确认加钟"},
		{"service.duration_mismatch", "服务时长差异待客服核实"},
	}
	for _, tc := range cases {
		if channels := eventNotifyChannels(tc.eventType); len(channels) == 0 {
			t.Fatalf("%s 未归类为通知事件，通知消费者不会认领", tc.eventType)
		}
		if title := notificationEventTitle(tc.eventType); title != tc.wantTitle {
			t.Fatalf("%s 标题 = %q, want %q", tc.eventType, title, tc.wantTitle)
		}
		summary := notificationEventSummary(tc.eventType, "NO-TEXT-1", map[string]interface{}{"order_no": "NO-TEXT-1"})
		if !strings.Contains(summary, tc.wantTitle) || !strings.Contains(summary, "NO-TEXT-1") {
			t.Fatalf("%s 摘要缺少业务文案/单号: %q", tc.eventType, summary)
		}
	}
}

// 任务 1 验收 2：dispatch.remind_player 必须解析到该订单被提醒的陪玩——
// 优先 payload.player_id，缺失时按 seat_index 回查席位，且只能定向到陪玩一人。
func TestNotificationRemindPlayerResolvesSeatPlayer(t *testing.T) {
	db := openOutboxDB(t, "esports_notify_remind_player_recipients")
	defer db.Close()
	ctx := t.Context()

	customer := seedNotificationCustomer(t, db, "催单客户")
	player := seedNotificationPlayer(t, db, 9201, "催单陪玩")
	order := seedNotificationOrder(t, db, "NO-REMIND-PLAYER-1", customer.ID, 0)
	seedAcceptedSeat(t, db, order.ID, player.ID)

	// 载荷带 player_id（生产写出点第 1 档的正常形态）。
	recipients, err := resolveNotificationRecipients(ctx, db, "dispatch.remind_player", order.OrderNo, map[string]interface{}{
		"order_no": order.OrderNo, "seat_index": 1, "level": 1, "player_id": player.ID,
	})
	if err != nil {
		t.Fatalf("带 player_id 解析失败: %v", err)
	}
	assertSingleTargetedRecipient(t, "dispatch.remind_player", recipients, playerNotificationAddress(player.ID), notificationRolePlayer)

	// 载荷缺 player_id（历史/异常事件）时必须能从订单席位回查，而不是退化为广播。
	recipients, err = resolveNotificationRecipients(ctx, db, "dispatch.remind_player", order.OrderNo, map[string]interface{}{
		"order_no": order.OrderNo, "seat_index": 1, "level": 1,
	})
	if err != nil {
		t.Fatalf("按席位回查陪玩失败: %v", err)
	}
	assertSingleTargetedRecipient(t, "dispatch.remind_player", recipients, playerNotificationAddress(player.ID), notificationRolePlayer)
}

// 任务 1 验收 2（负向）：取不到陪玩时必须报收件人缺失，绝不降级为广播。
func TestNotificationRemindPlayerWithoutPlayerFailsClosed(t *testing.T) {
	db := openOutboxDB(t, "esports_notify_remind_player_missing")
	defer db.Close()
	ctx := t.Context()

	customer := seedNotificationCustomer(t, db, "无陪玩客户")
	order := seedNotificationOrder(t, db, "NO-REMIND-PLAYER-2", customer.ID, 0)

	recipients, err := resolveNotificationRecipients(ctx, db, "dispatch.remind_player", order.OrderNo, map[string]interface{}{
		"order_no": order.OrderNo, "seat_index": 1, "level": 1,
	})
	if !errors.Is(err, errNotificationRecipientMissing) {
		t.Fatalf("err = %v, want errNotificationRecipientMissing (recipients=%+v)", err, recipients)
	}
}

// 任务 1 / 任务 2 验收 2 + 3：服务超时与服务时长差异都定向责任客服；remind_admin 回归不变。
func TestNotificationAdminRoutedEventRecipients(t *testing.T) {
	db := openOutboxDB(t, "esports_notify_admin_routed")
	defer db.Close()
	ctx := t.Context()

	admin := seedNotificationAdmin(t, db, "routed-admin")
	customer := seedNotificationCustomer(t, db, "定向客户")
	player := seedNotificationPlayer(t, db, 9301, "定向陪玩")
	order := seedNotificationOrder(t, db, "NO-ROUTED-1", customer.ID, admin.ID)
	seedAcceptedSeat(t, db, order.ID, player.ID)

	cases := []struct {
		eventType string
		payload   map[string]interface{}
	}{
		{"dispatch.service_overtime", map[string]interface{}{
			"order_no": order.OrderNo, "order_id": order.ID, "seat_index": 1,
			"player_id": player.ID, "overtime_minutes": 60, "admin_id": admin.ID,
		}},
		{"service.duration_mismatch", map[string]interface{}{
			"order_no": order.OrderNo, "order_id": order.ID, "seat_index": 1,
			"player_id": player.ID, "reported_seconds": 600, "system_seconds": 1800, "admin_id": admin.ID,
		}},
		{"dispatch.remind_admin", map[string]interface{}{
			"order_no": order.OrderNo, "seat_index": 1, "level": 2, "admin_id": admin.ID,
		}},
	}
	for _, tc := range cases {
		recipients, err := resolveNotificationRecipients(ctx, db, tc.eventType, order.OrderNo, tc.payload)
		if err != nil {
			t.Fatalf("%s 解析失败: %v", tc.eventType, err)
		}
		assertSingleTargetedRecipient(t, tc.eventType, recipients, admin.UID, notificationRoleAdmin)
	}
}

// 端到端回归：三类事件必须被通知消费者认领并投递到定向收件人（不写 user_uid="" 广播），
// 业务消费者不得抢占。
func TestNotificationOutboxDeliversRemindOvertimeAndDurationMismatch(t *testing.T) {
	db := openOutboxDB(t, "esports_notify_remind_overtime_delivery")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

	admin := seedNotificationAdmin(t, db, "deliver-admin")
	customer := seedNotificationCustomer(t, db, "投递客户")
	player := seedNotificationPlayer(t, db, 9401, "投递陪玩")
	order := seedNotificationOrder(t, db, "NO-DELIVER-1", customer.ID, admin.ID)
	seedAcceptedSeat(t, db, order.ID, player.ID)
	enableNotificationChannel(t, db, notifyChannelWecomApp)

	events := []struct {
		key       string
		eventType string
		payload   map[string]interface{}
	}{
		{"dispatch_remind:" + strconv.Itoa(order.ID) + ":1:1", "dispatch.remind_player", map[string]interface{}{
			"order_no": order.OrderNo, "seat_index": 1, "level": 1,
		}},
		{"service_overtime:" + strconv.Itoa(order.ID) + ":1:60", "dispatch.service_overtime", map[string]interface{}{
			"order_no": order.OrderNo, "seat_index": 1, "player_id": player.ID,
			"overtime_minutes": 60, "admin_id": admin.ID,
		}},
		{"service.duration_mismatch:1", "service.duration_mismatch", map[string]interface{}{
			"order_no": order.OrderNo, "order_id": order.ID, "player_id": player.ID,
			"seat_index": 1, "reported_seconds": 600, "system_seconds": 1800, "admin_id": admin.ID,
		}},
	}
	for _, event := range events {
		db.EsportsOutbox.Create().SetEventKey(event.key).SetEventType(event.eventType).
			SetAggregateRef(order.OrderNo).SetPayload(event.payload).
			SetNextAttemptAt(now).SaveX(ctx)
	}

	business := &recordingPublisher{}
	if count, err := deliverOutboxBatch(ctx, db, business, now, 10); err != nil || count != 0 {
		t.Fatalf("业务消费者抢占了通知事件: count=%d err=%v keys=%+v", count, err, business.keys)
	}
	if count, err := deliverNotificationOutboxBatch(ctx, db, &messageNotificationSender{db: db}, now, 10); err != nil || count != 3 {
		t.Fatalf("通知投递: count=%d err=%v", count, err)
	}

	messages := notificationMessages(t, db)
	want := []string{playerNotificationAddress(player.ID), admin.UID, admin.UID}
	if len(messages) != len(want) {
		t.Fatalf("站内信数量 = %d, want %d: %+v", len(messages), len(want), messages)
	}
	for i, msg := range messages {
		if msg.UserUID == "" {
			t.Fatalf("第 %d 条为广播（user_uid 为空）: %+v", i, msg)
		}
		if msg.UserUID != want[i] {
			t.Fatalf("第 %d 条地址 = %q, want %q", i, msg.UserUID, want[i])
		}
		if msg.Title == "" || msg.Content == "" {
			t.Fatalf("第 %d 条缺标题/摘要: %+v", i, msg)
		}
	}
	rows := db.EsportsOutbox.Query().AllX(ctx)
	if len(rows) != len(events) {
		t.Fatalf("外盒事件数 = %d, want %d", len(rows), len(events))
	}
	for _, row := range rows {
		if row.Status != esportsoutbox.StatusDelivered {
			t.Fatalf("事件 %s 未投递: status=%s last_error=%s", row.EventType, row.Status, row.LastError)
		}
	}
}
