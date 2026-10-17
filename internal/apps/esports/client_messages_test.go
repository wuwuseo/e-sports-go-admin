package esports

import (
	"strings"
	"testing"

	"go-admin/internal/ent"
)

// ── 客户端站内消息读取：地址即数据边界 ──

func seedClientMessage(t *testing.T, db *ent.Client, address, title, content string) *ent.Message {
	t.Helper()
	return db.Message.Create().SetUserUID(address).SetType(notificationMessageType).
		SetLevel("info").SetTitle(title).SetContent(content).
		SetExtra(map[string]interface{}{
			"event_type": "order.closed",
			"payload":    map[string]interface{}{"internal_note": "不应回给客户端"},
		}).SaveX(t.Context())
}

func messageIDs(views []clientMessageView) []int {
	out := make([]int, 0, len(views))
	for _, view := range views {
		out = append(out, view.ID)
	}
	return out
}

// 客户 A 只读得到 customer:A 的消息，读不到 customer:B 的；列表按 ID 倒序且不含内部载荷。
func TestListClientMessagesScopedByAddress(t *testing.T) {
	db := openOutboxDB(t, "esports_client_messages_scope")
	defer db.Close()
	ctx := t.Context()

	addressA := customerNotificationAddress(11)
	addressB := customerNotificationAddress(22)
	first := seedClientMessage(t, db, addressA, "订单已结单", "订单已结单，订单号 NO-A-1")
	second := seedClientMessage(t, db, addressA, "售后通知", "售后单号 AS-A-1")
	seedClientMessage(t, db, addressB, "他人的消息", "不应被 A 读到")

	views, err := listClientMessages(ctx, db, addressA, 30, 0, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("want only own messages, got %+v", views)
	}
	if views[0].ID != second.ID || views[1].ID != first.ID {
		t.Fatalf("expected newest-first order, got %v", messageIDs(views))
	}
	for _, view := range views {
		if view.Title == "" || view.Summary == "" || view.CreatedAt.IsZero() {
			t.Fatalf("incomplete view: %+v", view)
		}
		if strings.Contains(view.Summary, "internal_note") || strings.Contains(view.Summary, "{") {
			t.Fatalf("view leaked internal payload: %q", view.Summary)
		}
		if view.ReadAt != nil {
			t.Fatalf("new message should be unread: %+v", view)
		}
	}
}

// before_id 游标与 unread_only 过滤。
func TestListClientMessagesCursorAndUnreadFilter(t *testing.T) {
	db := openOutboxDB(t, "esports_client_messages_filter")
	defer db.Close()
	ctx := t.Context()

	address := playerNotificationAddress(33)
	oldest := seedClientMessage(t, db, address, "旧消息", "旧")
	middle := seedClientMessage(t, db, address, "中间消息", "中")
	newest := seedClientMessage(t, db, address, "新消息", "新")

	page, err := listClientMessages(ctx, db, address, 1, 0, false)
	if err != nil || len(page) != 1 || page[0].ID != newest.ID {
		t.Fatalf("first page: %+v %v", page, err)
	}
	page, err = listClientMessages(ctx, db, address, 1, newest.ID, false)
	if err != nil || len(page) != 1 || page[0].ID != middle.ID {
		t.Fatalf("cursor page: %+v %v", page, err)
	}
	page, err = listClientMessages(ctx, db, address, 0, 0, false)
	if err != nil || len(page) != 3 {
		t.Fatalf("default limit page: %+v %v", page, err)
	}
	// 越界游标不会返回任何消息。
	page, err = listClientMessages(ctx, db, address, 30, oldest.ID, false)
	if err != nil || len(page) != 0 {
		t.Fatalf("cursor before oldest: %+v %v", page, err)
	}

	if _, err := markClientMessagesRead(ctx, db, address, []int{middle.ID}); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	unread, err := listClientMessages(ctx, db, address, 30, 0, true)
	if err != nil {
		t.Fatalf("unread list: %v", err)
	}
	if len(unread) != 2 {
		t.Fatalf("want 2 unread, got %+v", messageIDs(unread))
	}
	for _, view := range unread {
		if view.ID == middle.ID {
			t.Fatalf("read message returned by unread_only: %+v", view)
		}
	}
}

// 标记已读只能影响本人地址下的消息。
func TestMarkClientMessagesReadOnlyOwnAddress(t *testing.T) {
	db := openOutboxDB(t, "esports_client_messages_mark")
	defer db.Close()
	ctx := t.Context()

	addressA := customerNotificationAddress(44)
	addressB := customerNotificationAddress(55)
	own := seedClientMessage(t, db, addressA, "本人消息", "本人")
	other := seedClientMessage(t, db, addressB, "他人消息", "他人")

	// 试图通过 ID 顺带标记他人消息：更新条件限定 user_uid，只有本人那条受影响。
	updated, err := markClientMessagesRead(ctx, db, addressA, []int{own.ID, other.ID, 0, own.ID})
	if err != nil {
		t.Fatalf("mark read: %v", err)
	}
	if updated != 1 {
		t.Fatalf("updated = %d, want 1", updated)
	}
	if got := db.Message.GetX(ctx, own.ID); got.ReadAt == nil {
		t.Fatalf("own message not marked read: %+v", got)
	}
	if got := db.Message.GetX(ctx, other.ID); got.ReadAt != nil {
		t.Fatalf("other address message must stay unread: %+v", got)
	}

	// 重复标记已读不再产生更新行。
	updated, err = markClientMessagesRead(ctx, db, addressA, []int{own.ID})
	if err != nil || updated != 0 {
		t.Fatalf("repeat mark read: %d %v", updated, err)
	}
	// 空 ID 列表是合法的 no-op。
	if updated, err = markClientMessagesRead(ctx, db, addressA, nil); err != nil || updated != 0 {
		t.Fatalf("empty ids: %d %v", updated, err)
	}
}

// 空地址（身份解析失败）必须拒绝，避免退化成读取广播/全表。
func TestClientMessagesRejectEmptyAddress(t *testing.T) {
	db := openOutboxDB(t, "esports_client_messages_empty")
	defer db.Close()
	ctx := t.Context()

	seedClientMessage(t, db, "", "全员广播", "广播正文")
	if _, err := listClientMessages(ctx, db, "  ", 30, 0, false); err == nil {
		t.Fatal("empty address must be rejected")
	}
	if _, err := markClientMessagesRead(ctx, db, "", []int{1}); err == nil {
		t.Fatal("empty address must be rejected on mark read")
	}
}

// 摘要截断：超长正文按 rune 截断，不会原样回传整段内容。
func TestClientMessageSummaryTruncated(t *testing.T) {
	db := openOutboxDB(t, "esports_client_messages_truncate")
	defer db.Close()
	ctx := t.Context()

	long := strings.Repeat("超长通知内容", 100)
	seedClientMessage(t, db, customerNotificationAddress(66), "超长消息", long)
	views, err := listClientMessages(ctx, db, customerNotificationAddress(66), 30, 0, false)
	if err != nil || len(views) != 1 {
		t.Fatalf("list: %+v %v", views, err)
	}
	runes := []rune(views[0].Summary)
	if len(runes) != clientMessageSummaryRunes+1 || runes[len(runes)-1] != '…' {
		t.Fatalf("summary not truncated: %d runes", len(runes))
	}
}
