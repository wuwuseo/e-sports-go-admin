package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wuwuseo/cmf/sse"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/hook"
	"go-admin/internal/ent/message"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

// setupMessageTest 构造带独立 Hub 的消息服务（模拟生产：每个 app 实例一个 Hub）
func setupMessageTest(t *testing.T) (*ent.Client, *MessageService, *sse.Hub, func()) {
	client := enttest.Open(t, "sqlite3", "file:msg?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	prevORM := global.ORM
	global.ORM = client

	hub := sse.NewHub()
	svc := NewMessageService(hub)

	cleanup := func() {
		hub.Shutdown()
		client.Close()
		global.ORM = prevORM
	}
	return client, svc, hub, cleanup
}

func TestMessageService_PublishToUser_PersistAndPush(t *testing.T) {
	_, svc, hub, cleanup := setupMessageTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	// 先注册一个 SSE 连接
	c := &sse.Client{
		UserUID: "u1",
		ConnID:  "conn-1",
		Send:    make(chan sse.Event, 16),
		Done:    make(chan struct{}),
	}
	hub.Register(c)
	defer hub.Unregister(c)

	resp, err := svc.PublishToUser(ctx, "u1", PublishMessage{
		Type:    MessageTypeBusiness,
		Level:   MessageLevelInfo,
		Title:   "审批通过",
		Content: "您的申请已通过",
		Extra:   map[string]any{"link": "/x"},
	})
	if err != nil {
		t.Fatalf("发布失败: %v", err)
	}
	if resp.ID <= 0 {
		t.Fatalf("响应 ID 异常 %d", resp.ID)
	}

	select {
	case evt := <-c.Send:
		if evt.Event != SSEEventMessage {
			t.Fatalf("事件名错误 %s", evt.Event)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到推送事件")
	}
}

func TestMessageService_PublishToUser_HubNil(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:msg2?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()
	prevORM := global.ORM
	global.ORM = client
	defer func() { global.ORM = prevORM }()

	svc := NewMessageService(nil) // Hub 为 nil
	resp, err := svc.PublishToUser(t.Context(), "u1", PublishMessage{
		Type:    MessageTypeSystem,
		Title:   "t",
		Content: "c",
	})
	if err != nil {
		t.Fatalf("Hub 为 nil 时仍应落库成功 %v", err)
	}
	if resp.ID == 0 {
		t.Fatal("响应 ID 不应为 0")
	}
}

func TestMessageService_List_Pagination(t *testing.T) {
	_, svc, _, cleanup := setupMessageTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	for i := 0; i < 5; i++ {
		_, err := svc.PublishToUser(ctx, "u1", PublishMessage{
			Type: MessageTypeSystem, Title: "t", Content: "c",
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	res, err := svc.List(ctx, "u1", &ListMessageRequest{
		PaginationRequest: PaginationRequest{Page: 1, PageSize: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 5 {
		t.Fatalf("总数应为 5，实际 %d", res.Total)
	}
	if len(res.Data) != 3 {
		t.Fatalf("第一页应有 3 条，实际 %d", len(res.Data))
	}
}

func TestMessageService_MarkRead_OnlyOwn(t *testing.T) {
	_, svc, _, cleanup := setupMessageTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	r1, _ := svc.PublishToUser(ctx, "u1", PublishMessage{Type: MessageTypeSystem, Title: "t", Content: "c"})
	r2, _ := svc.PublishToUser(ctx, "u2", PublishMessage{Type: MessageTypeSystem, Title: "t", Content: "c"})

	// u1 尝试标记 u2 的消息为已读，不应生效
	n, err := svc.MarkRead(ctx, "u1", []int{r1.ID, r2.ID})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("只应标记 1 条（自己的），实际 %d", n)
	}
}

func TestMessageService_MarkAllRead(t *testing.T) {
	_, svc, _, cleanup := setupMessageTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	for i := 0; i < 3; i++ {
		_, _ = svc.PublishToUser(ctx, "u1", PublishMessage{Type: MessageTypeBusiness, Title: "t", Content: "c"})
	}
	n, err := svc.MarkAllRead(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("应标记 3 条，实际 %d", n)
	}
	// 再次调用应为 0
	n, _ = svc.MarkAllRead(ctx, "u1")
	if n != 0 {
		t.Fatalf("应为 0，实际 %d", n)
	}
}

func TestMessageService_UnreadCount(t *testing.T) {
	_, svc, _, cleanup := setupMessageTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	_, _ = svc.PublishToUser(ctx, "u1", PublishMessage{Type: MessageTypeSystem, Title: "t", Content: "c"})
	_, _ = svc.PublishToUser(ctx, "u1", PublishMessage{Type: MessageTypeBusiness, Title: "t", Content: "c"})
	_, _ = svc.PublishToUser(ctx, "u1", PublishMessage{Type: MessageTypeTask, Title: "t", Content: "c"})
	_, _ = svc.Broadcast(ctx, PublishMessage{Type: MessageTypeSystem, Title: "g", Content: "g"})

	c, err := svc.UnreadCount(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if c.Total != 4 {
		t.Fatalf("总未读应为 4，实际 %d", c.Total)
	}
	if c.System != 2 {
		t.Fatalf("系统未读应为 2，实际 %d", c.System)
	}
	if c.Business != 1 || c.Task != 1 {
		t.Fatalf("business/task 未读异常 %+v", c)
	}
}

func TestMessageService_Delete_Forbidden(t *testing.T) {
	_, svc, _, cleanup := setupMessageTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	r, _ := svc.PublishToUser(ctx, "u1", PublishMessage{Type: MessageTypeSystem, Title: "t", Content: "c"})

	if err := svc.Delete(ctx, "u2", r.ID); err != ErrMessageForbidden {
		t.Fatalf("跨用户删除应被拒绝，得到 %v", err)
	}
	if err := svc.Delete(ctx, "u1", r.ID); err != nil {
		t.Fatalf("本人删除失败 %v", err)
	}
	if err := svc.Delete(ctx, "u1", r.ID); err != ErrMessageNotFound {
		t.Fatalf("再次删除应返回 NotFound，得到 %v", err)
	}
}

func TestMessageService_Delete_DoesNotDeleteWhenOwnerChangesBeforeDelete(t *testing.T) {
	client, svc, _, cleanup := setupMessageTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	r, err := svc.PublishToUser(ctx, "u1", PublishMessage{Type: MessageTypeSystem, Title: "t", Content: "c"})
	if err != nil {
		t.Fatal(err)
	}
	moved := false
	client.Message.Use(func(next ent.Mutator) ent.Mutator {
		return hook.MessageFunc(func(ctx context.Context, m *ent.MessageMutation) (ent.Value, error) {
			if m.Op().Is(ent.OpDelete|ent.OpDeleteOne) && !moved {
				moved = true
				if _, err := client.Message.UpdateOneID(r.ID).SetUserUID("u2").Save(ctx); err != nil {
					return nil, err
				}
			}
			return next.Mutate(ctx, m)
		})
	})

	if err := svc.Delete(ctx, "u1", r.ID); err != ErrMessageForbidden {
		t.Fatalf("归属变更后不应删除消息，得到 %v", err)
	}
	_, err = client.Message.Query().Where(message.IDEQ(r.ID), message.UserUIDEQ("u2")).Only(ctx)
	if err != nil {
		t.Fatalf("消息应保留给新归属用户: %v", err)
	}
}

func TestMessageService_Broadcast(t *testing.T) {
	_, svc, hub, cleanup := setupMessageTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	c1 := &sse.Client{UserUID: "u1", ConnID: "c1", Send: make(chan sse.Event, 16), Done: make(chan struct{})}
	c2 := &sse.Client{UserUID: "u2", ConnID: "c2", Send: make(chan sse.Event, 16), Done: make(chan struct{})}
	hub.Register(c1)
	hub.Register(c2)
	defer hub.Unregister(c1)
	defer hub.Unregister(c2)

	_, err := svc.Broadcast(ctx, PublishMessage{Type: MessageTypeSystem, Title: "g", Content: "g"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []*sse.Client{c1, c2} {
		select {
		case <-c.Send:
		case <-time.After(time.Second):
			t.Fatalf("用户 %s 未收到广播", c.UserUID)
		}
	}
}

func TestMessageService_ListSinceID(t *testing.T) {
	_, svc, _, cleanup := setupMessageTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	r1, _ := svc.PublishToUser(ctx, "u1", PublishMessage{Type: MessageTypeSystem, Title: "1", Content: "c"})
	_, _ = svc.PublishToUser(ctx, "u1", PublishMessage{Type: MessageTypeSystem, Title: "2", Content: "c"})
	_, _ = svc.PublishToUser(ctx, "u1", PublishMessage{Type: MessageTypeSystem, Title: "3", Content: "c"})

	list, err := svc.ListSinceID(ctx, "u1", r1.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("应有 2 条补发，实际 %d", len(list))
	}
}

// TestMessageService_CreateSanitizesContentHTML_XSS 验证 Create 路径对 ContentHTML 的 XSS 向量在落库前被净化
func TestMessageService_CreateSanitizesContentHTML_XSS(t *testing.T) {
	client, svc, _, cleanup := setupMessageTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	xssPayloads := []string{
		`<script>alert(1)</script>`,
		`<img src=x onerror=alert(1)>`,
		`<a href="javascript:alert(1)">x</a>`,
		`<svg onload=alert(1)>`,
		`<iframe src="evil"></iframe>`,
		`<p onclick="alert(1)">x</p>`,
	}
	for _, payload := range xssPayloads {
		resp, err := svc.PublishToUser(ctx, "u1", PublishMessage{
			Type:        MessageTypeSystem,
			Title:       "XSS Test",
			Content:     "test",
			ContentHTML: payload,
		})
		if err != nil {
			t.Fatalf("payload %q 创建失败: %v", payload, err)
		}
		entity, err := client.Message.Query().Where(message.IDEQ(resp.ID)).Only(ctx)
		if err != nil {
			t.Fatalf("查询消息 %d 失败: %v", resp.ID, err)
		}
		lower := strings.ToLower(entity.ContentHTML)
		if strings.Contains(lower, "<script") || strings.Contains(lower, "onerror") ||
			strings.Contains(lower, "javascript:") || strings.Contains(lower, "onload") ||
			strings.Contains(lower, "<iframe") || strings.Contains(lower, "onclick") {
			t.Errorf("payload %q 落库后仍包含危险内容: %q", payload, entity.ContentHTML)
		}
	}
}

// TestMessageService_CreateSanitizesContentHTML_PreservesSafeTags 验证 Create 路径保留安全标签
func TestMessageService_CreateSanitizesContentHTML_PreservesSafeTags(t *testing.T) {
	client, svc, _, cleanup := setupMessageTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	resp, err := svc.PublishToUser(ctx, "u1", PublishMessage{
		Type:        MessageTypeSystem,
		Title:       "Safe HTML Test",
		Content:     "test",
		ContentHTML: `<p>hello <strong>world</strong> <a href="https://example.com" target="_blank">link</a></p><br><img src="https://example.com/pic.png" alt="pic">`,
	})
	if err != nil {
		t.Fatal(err)
	}
	entity, err := client.Message.Query().Where(message.IDEQ(resp.ID)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 安全标签应被保留
	for _, tag := range []string{"<p>", "<strong>", "</strong>", `href="https://example.com"`} {
		if !strings.Contains(entity.ContentHTML, tag) {
			t.Errorf("安全标签 %q 应被保留，实际 ContentHTML: %q", tag, entity.ContentHTML)
		}
	}
	// 危险属性应被移除
	lower := strings.ToLower(entity.ContentHTML)
	if strings.Contains(lower, "onclick") || strings.Contains(lower, "javascript:") {
		t.Errorf("危险属性不应保留，实际 ContentHTML: %q", entity.ContentHTML)
	}
}

// TestMessageService_BroadcastToUsersSanitizesContentHTML 验证 BroadcastToUsers 路径对 ContentHTML 的 XSS 向量在落库前被净化
func TestMessageService_BroadcastToUsersSanitizesContentHTML(t *testing.T) {
	client, svc, _, cleanup := setupMessageTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	xssPayload := `<img src=x onerror="alert(1)"><a href="javascript:alert(1)">x</a>`
	n, err := svc.BroadcastToUsers(ctx, []string{"u1", "u2"}, PublishMessage{
		Type:        MessageTypeSystem,
		Title:       "Broadcast XSS Test",
		Content:     "test",
		ContentHTML: xssPayload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("应创建 2 条消息，实际 %d", n)
	}
	entities, err := client.Message.Query().Where(message.TitleEQ("Broadcast XSS Test")).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entities) != 2 {
		t.Fatalf("应查到 2 条消息，实际 %d", len(entities))
	}
	for _, entity := range entities {
		lower := strings.ToLower(entity.ContentHTML)
		if strings.Contains(lower, "onerror") || strings.Contains(lower, "javascript:") {
			t.Errorf("消息 %d 落库后仍包含危险内容: %q", entity.ID, entity.ContentHTML)
		}
	}
}
