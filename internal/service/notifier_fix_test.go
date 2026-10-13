package service

import (
	"context"
	"encoding/json"
	"testing"

	cmfqueue "github.com/wuwuseo/cmf/queue"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

// ========== 收件人格式校验 ==========

func TestSMSNotifier_InvalidPhone(t *testing.T) {
	n := &SMSNotifier{cfg: notifierConfig{smsEnabled: true}, sender: &stubSMSSender{}}
	err := n.Send(t.Context(), NotifyPayload{Extra: map[string]any{"phone": "abc"}})
	if err == nil {
		t.Fatal("非手机号应报错")
	}
	// 合法手机号应通过
	err = n.Send(t.Context(), NotifyPayload{Extra: map[string]any{"phone": "13800138000"}})
	if err != nil {
		t.Fatalf("合法手机号不应报错: %v", err)
	}
}

func TestMailNotifier_InvalidEmail(t *testing.T) {
	n := &MailNotifier{cfg: notifierConfig{mailEnabled: true}, sender: &stubMailSender{}}
	err := n.Send(t.Context(), NotifyPayload{Extra: map[string]any{"email": "not-an-email"}})
	if err == nil {
		t.Fatal("非邮箱应报错")
	}
	err = n.Send(t.Context(), NotifyPayload{Extra: map[string]any{"email": "a@b.com"}})
	if err != nil {
		t.Fatalf("合法邮箱不应报错: %v", err)
	}
}

type stubMailSender struct{}

func (s *stubMailSender) Send(_ context.Context, _ string, _ NotifyPayload) error { return nil }

// ========== HTTPS 凭证校验 ==========

func TestIsHTTPS(t *testing.T) {
	cases := map[string]bool{
		"https://api.jpush.cn": true,
		"HTTPS://x.y":          true,
		"http://api.jpush.cn":  false,
		"ftp://x.y":            false,
		"api.jpush.cn":         false,
		"":                     false,
	}
	for in, want := range cases {
		if got := isHTTPS(in); got != want {
			t.Fatalf("isHTTPS(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestJPushSender_RejectsNonHTTPS(t *testing.T) {
	s := &jpushSender{host: "http://api.jpush.cn", httpClient: nil}
	err := s.Send(t.Context(), NotifyPayload{UserUID: "u1"})
	if err == nil {
		t.Fatal("http host 应被拒绝")
	}
}

// ========== GetByUIDs 批量偏好 ==========

func TestMessagePreference_GetByUIDs(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:pref?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	svc := NewMessagePreferenceService()
	ctx := t.Context()

	// u1 配置了偏好，u2 未配置
	_, err := svc.Update(ctx, "u1", &UpdateMessagePreferenceRequest{})
	if err != nil {
		t.Fatal(err)
	}

	prefs, err := svc.GetByUIDs(ctx, []string{"u1", "u2", "u3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(prefs) != 3 {
		t.Fatalf("应返回 3 个用户偏好，实际 %d", len(prefs))
	}
	if _, ok := prefs["u1"]; !ok {
		t.Fatal("缺少 u1")
	}
	// u2/u3 未配置应返回默认值（DesktopNotify=true）
	if prefs["u2"] == nil || !prefs["u2"].DesktopNotify {
		t.Fatal("u2 应返回默认偏好")
	}
	if prefs["u3"] == nil || !prefs["u3"].DesktopNotify {
		t.Fatal("u3 应返回默认偏好")
	}
}

// ========== Clear 分批删除 ==========

func TestMessageService_Clear_BatchDelete(t *testing.T) {
	_, svc, _, cleanup := setupMessageTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	// 造 1200 条消息（超过 clearBatchSize=500，验证分批循环）
	for i := 0; i < 1200; i++ {
		_, err := svc.PublishToUser(ctx, "u1", PublishMessage{Type: MessageTypeBusiness, Title: "t", Content: "c"})
		if err != nil {
			t.Fatal(err)
		}
	}

	n, err := svc.Clear(ctx, "u1", &ClearMessagesRequest{Type: MessageTypeBusiness})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1200 {
		t.Fatalf("应清空 1200 条，实际 %d", n)
	}
	// 确认已清空
	c, err := svc.UnreadCount(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if c.Total != 0 {
		t.Fatalf("清空后未读应为 0，实际 %d", c.Total)
	}
}

// ========== 外部渠道异步投递 handler ==========

// TestExternalDispatchHandler 验证队列处理器能反序列化载荷并投递到目标渠道。
func TestExternalDispatchHandler(t *testing.T) {
	reg := NewNotifierRegistry()
	sms := &stubNotifier{name: ChannelSMS, enabled: true}
	reg.Register(sms)

	handler := NewExternalDispatchHandler(reg)

	payload := NotifyPayload{
		MessageID: 42,
		UserUID:   "u1",
		Type:      MessageTypeSystem,
		Title:     "t",
		Content:   "c",
	}
	raw, err := json.Marshal(externalDispatchPayload{Channel: ChannelSMS, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}

	if err := handler(t.Context(), &cmfqueue.Task{Name: TaskNotifyExternal, Payload: raw}); err != nil {
		t.Fatalf("handler 投递失败: %v", err)
	}
	if sms.count() != 1 {
		t.Fatalf("短信渠道应投递 1 次，实际 %d", sms.count())
	}
}

// TestExternalDispatchHandler_InvalidPayload 非法载荷应返回错误（重试无意义）。
func TestExternalDispatchHandler_InvalidPayload(t *testing.T) {
	reg := NewNotifierRegistry()
	handler := NewExternalDispatchHandler(reg)
	if err := handler(t.Context(), &cmfqueue.Task{Name: TaskNotifyExternal, Payload: []byte("{bad json")}); err == nil {
		t.Fatal("非法 JSON 载荷应返回错误")
	}
}

// TestExternalDispatchHandler_UnknownChannel 未注册渠道应返回错误。
func TestExternalDispatchHandler_UnknownChannel(t *testing.T) {
	reg := NewNotifierRegistry()
	handler := NewExternalDispatchHandler(reg)
	raw, _ := json.Marshal(externalDispatchPayload{Channel: ChannelSMS, Payload: NotifyPayload{}})
	if err := handler(t.Context(), &cmfqueue.Task{Name: TaskNotifyExternal, Payload: raw}); err == nil {
		t.Fatal("未注册渠道应返回错误")
	}
}

// 验证 Ent 的 Contains 谓词已内置 LIKE 通配符转义：
// 输入 % / _ 应被当作字面量匹配，而非通配符导致全表扫描或误匹配。
func TestMessageService_List_KeywordEscapesWildcard(t *testing.T) {
	_, svc, _, cleanup := setupMessageTest(t)
	t.Cleanup(cleanup)
	ctx := t.Context()

	_, _ = svc.PublishToUser(ctx, "u1", PublishMessage{Type: MessageTypeSystem, Title: "100%完成", Content: "进度"})
	_, _ = svc.PublishToUser(ctx, "u1", PublishMessage{Type: MessageTypeSystem, Title: "abc", Content: "x"})

	// "%" 作为字面量应只匹配标题含 100% 的那条，而非匹配所有
	got, err := svc.List(ctx, "u1", &ListMessageRequest{Keyword: "100%"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 1 {
		t.Fatalf("keyword 100%% 应只匹配 1 条，实际 %d", got.Total)
	}

	// "_" 作为字面量应匹配含 100% 那条，而非任意字符
	got2, err := svc.List(ctx, "u1", &ListMessageRequest{Keyword: "100_"})
	if err != nil {
		t.Fatal(err)
	}
	if got2.Total != 0 {
		t.Fatalf("keyword 100_ 应匹配 0 条（_ 是字面量），实际 %d", got2.Total)
	}
}
