package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wuwuseo/cmf/sse"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

// ========== 测试替身 ==========

// stubNotifier 记录投递次数的渠道替身
type stubNotifier struct {
	name    string
	enabled bool
	mu      sync.Mutex
	calls   []NotifyPayload
	failErr error
}

func (s *stubNotifier) Name() string  { return s.name }
func (s *stubNotifier) Enabled() bool { return s.enabled }

func (s *stubNotifier) Send(_ context.Context, p NotifyPayload) error {
	if s.failErr != nil {
		return s.failErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, p)
	return nil
}

func (s *stubNotifier) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// stubSMSSender 短信投递替身（签名与 smsSender 一致，含收件人参数）
type stubSMSSender struct {
	mu       sync.Mutex
	phones   []string
	payloads []NotifyPayload
}

func (s *stubSMSSender) Send(_ context.Context, phone string, p NotifyPayload) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phones = append(s.phones, phone)
	s.payloads = append(s.payloads, p)
	return nil
}

func (s *stubSMSSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.phones)
}

// ========== ShouldDeliver 决策 ==========

func TestShouldDeliver_NoPreference_OnlyInApp(t *testing.T) {
	reg := NewNotifierRegistry()
	reg.Register(&stubNotifier{name: ChannelInApp, enabled: true})
	reg.Register(&stubNotifier{name: ChannelSMS, enabled: true})
	reg.Register(&stubNotifier{name: ChannelMail, enabled: true})
	reg.Register(&stubNotifier{name: ChannelPush, enabled: true})

	reg.Register(NewInAppNotifier(sse.NewHub()))

	now := time.Date(2026, 10, 13, 10, 0, 0, 0, time.Local)
	// 无偏好记录：站内信投递，外部渠道不投递（避免无差别打扰）
	for ch, want := range map[string]bool{
		ChannelInApp: true,
		ChannelSMS:   false,
		ChannelMail:  false,
		ChannelPush:  false,
	} {
		if got := reg.ShouldDeliver(nil, MessageTypeSystem, ch, now); got != want {
			t.Errorf("无偏好时渠道 %s 期望 %v，实际 %v", ch, want, got)
		}
	}
}

func TestShouldDeliver_ChannelDisabledNotifier(t *testing.T) {
	reg := NewNotifierRegistry()
	reg.Register(&stubNotifier{name: ChannelSMS, enabled: false}) // 未配置凭证

	pref := &MessagePreferenceResponse{ChannelSMS: true}
	if reg.ShouldDeliver(pref, MessageTypeSystem, ChannelSMS, time.Now()) {
		t.Error("渠道未启用时不应投递")
	}
}

func TestShouldDeliver_UnsubscribedType(t *testing.T) {
	reg := NewNotifierRegistry()
	reg.Register(&stubNotifier{name: ChannelSMS, enabled: true})

	pref := &MessagePreferenceResponse{ChannelSMS: true, SubscribeSystem: false}
	if reg.ShouldDeliver(pref, MessageTypeSystem, ChannelSMS, time.Now()) {
		t.Error("未订阅 system 类型时不应投递")
	}
	// 其他类型仍可投递
	pref.SubscribeBusiness = true
	if !reg.ShouldDeliver(pref, MessageTypeBusiness, ChannelSMS, time.Now()) {
		t.Error("已订阅 business 类型时应投递")
	}
}

func TestShouldDeliver_ChannelSwitchOff(t *testing.T) {
	reg := NewNotifierRegistry()
	reg.Register(&stubNotifier{name: ChannelMail, enabled: true})

	// 关闭渠道时必须同时关闭类型订阅，否则「全部退订」语义会打开所有渠道
	pref := &MessagePreferenceResponse{
		SubscribeSystem: true,
		ChannelMail:     false,
	}
	if reg.ShouldDeliver(pref, MessageTypeSystem, ChannelMail, time.Now()) {
		t.Error("用户关闭邮件渠道时不应投递")
	}
	pref.ChannelMail = true
	if !reg.ShouldDeliver(pref, MessageTypeSystem, ChannelMail, time.Now()) {
		t.Error("用户开启邮件渠道时应投递")
	}
}

// TestShouldDeliver_AllSubscriptionsOff_SilencesExternalChannels 回归测试：
// 用户关闭全部类型订阅时，外部渠道必须一并静默（由 MessageService.dispatch 统一决策）。
func TestShouldDeliver_AllSubscriptionsOff_SilencesExternalChannels(t *testing.T) {
	reg := NewNotifierRegistry()
	reg.Register(NewInAppNotifier(sse.NewHub()))
	reg.Register(&stubNotifier{name: ChannelSMS, enabled: true})
	reg.Register(&stubNotifier{name: ChannelMail, enabled: true})

	pref := &MessagePreferenceResponse{
		DesktopNotify:     true,
		SubscribeSystem:   false,
		SubscribeBusiness: false,
		SubscribeTask:     false,
		ChannelSMS:        true, // 渠道开关为 true，但类型订阅全关
		ChannelMail:       true,
	}
	now := time.Now()
	for _, ch := range []string{ChannelSMS, ChannelMail} {
		if reg.ShouldDeliver(pref, MessageTypeSystem, ch, now) {
			t.Errorf("类型订阅全关时渠道 %s 不应投递", ch)
		}
	}
}

func TestShouldDeliver_DoNotDisturbBlocksExternalOnly(t *testing.T) {
	reg := NewNotifierRegistry()
	reg.Register(NewInAppNotifier(sse.NewHub()))
	reg.Register(&stubNotifier{name: ChannelSMS, enabled: true})

	pref := &MessagePreferenceResponse{
		DesktopNotify:     true,
		SubscribeSystem:   true,
		ChannelSMS:        true,
		DoNotDisturbStart: "22:00",
		DoNotDisturbEnd:   "07:00",
	}

	inWindow := time.Date(2026, 10, 13, 23, 30, 0, 0, time.Local)
	if reg.ShouldDeliver(pref, MessageTypeSystem, ChannelSMS, inWindow) {
		t.Error("免打扰时段内不应投递短信")
	}
	// 站内信不受免打扰影响（消息仍需实时抵达 APP/网页）
	if !reg.ShouldDeliver(pref, MessageTypeSystem, ChannelInApp, inWindow) {
		t.Error("站内信不应被免打扰拦截")
	}

	outWindow := time.Date(2026, 10, 13, 12, 0, 0, 0, time.Local)
	if !reg.ShouldDeliver(pref, MessageTypeSystem, ChannelSMS, outWindow) {
		t.Error("免打扰时段外应正常投递短信")
	}
}

// ========== 免打扰时段解析 ==========

func TestIsDoNotDisturb(t *testing.T) {
	cases := []struct {
		name       string
		start, end string
		at         time.Time
		wantDND    bool
	}{
		{"未配置", "", "", time.Date(2026, 10, 13, 23, 0, 0, 0, time.Local), false},
		{"仅配置开始", "22:00", "", time.Date(2026, 10, 13, 23, 0, 0, 0, time.Local), false},
		{"同日区间-内", "09:00", "18:00", time.Date(2026, 10, 13, 12, 0, 0, 0, time.Local), true},
		{"同日区间-外", "09:00", "18:00", time.Date(2026, 10, 13, 20, 0, 0, 0, time.Local), false},
		{"同日区间-左闭", "09:00", "18:00", time.Date(2026, 10, 13, 9, 0, 0, 0, time.Local), true},
		{"同日区间-右开", "09:00", "18:00", time.Date(2026, 10, 13, 18, 0, 0, 0, time.Local), false},
		{"跨零点-前半夜", "22:00", "07:00", time.Date(2026, 10, 13, 23, 30, 0, 0, time.Local), true},
		{"跨零点-后半夜", "22:00", "07:00", time.Date(2026, 10, 13, 3, 0, 0, 0, time.Local), true},
		{"跨零点-白天", "22:00", "07:00", time.Date(2026, 10, 13, 12, 0, 0, 0, time.Local), false},
		{"非法格式", "25:99", "07:00", time.Date(2026, 10, 13, 3, 0, 0, 0, time.Local), false},
		{"起止相同", "10:00", "10:00", time.Date(2026, 10, 13, 10, 0, 0, 0, time.Local), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &MessagePreferenceResponse{DoNotDisturbStart: tc.start, DoNotDisturbEnd: tc.end}
			if got := p.IsDoNotDisturb(tc.at); got != tc.wantDND {
				t.Errorf("期望 %v，实际 %v", tc.wantDND, got)
			}
		})
	}
}

// ========== 渠道分发 ==========

func TestDispatcher_MultiChannel(t *testing.T) {
	reg := NewNotifierRegistry()
	inApp := &stubNotifier{name: ChannelInApp, enabled: true}
	sms := &stubNotifier{name: ChannelSMS, enabled: true}
	mail := &stubNotifier{name: ChannelMail, enabled: true}
	reg.Register(inApp)
	reg.Register(sms)
	reg.Register(mail)

	pref := &MessagePreferenceResponse{
		DesktopNotify:   true,
		SubscribeSystem: true,
		ChannelSMS:      true,
		ChannelMail:     false, // 关闭邮件
	}

	now := time.Now()
	// 决策层：按偏好判定哪些渠道应投递
	if !reg.ShouldDeliver(pref, MessageTypeSystem, ChannelInApp, now) {
		t.Fatal("站内信应投递")
	}
	if !reg.ShouldDeliver(pref, MessageTypeSystem, ChannelSMS, now) {
		t.Fatal("短信应投递")
	}
	if reg.ShouldDeliver(pref, MessageTypeSystem, ChannelMail, now) {
		t.Fatal("邮件已关闭不应投递")
	}

	// 投递层：Dispatch 只负责「投出去」，偏好过滤由调用方（MessageService.dispatch）完成
	payload := NotifyPayload{MessageID: 1, UserUID: "u1", Type: MessageTypeSystem, Title: "t", Content: "c"}
	delivered := make([]string, 0, 3)
	for _, ch := range []string{ChannelInApp, ChannelSMS, ChannelMail} {
		if !reg.ShouldDeliver(pref, MessageTypeSystem, ch, now) {
			continue
		}
		if err := reg.Dispatch(context.Background(), ch, payload); err == nil {
			delivered = append(delivered, ch)
		}
	}

	if inApp.count() != 1 || sms.count() != 1 {
		t.Errorf("站内信/短信应各投递 1 次，实际 %d/%d", inApp.count(), sms.count())
	}
	if mail.count() != 0 {
		t.Errorf("邮件渠道已被偏好关闭，不应投递，实际 %d 次", mail.count())
	}
	if len(delivered) != 2 {
		t.Errorf("应投递 2 个渠道，实际 %v", delivered)
	}
}

func TestDispatcher_ChannelFailureIsolated(t *testing.T) {
	reg := NewNotifierRegistry()
	bad := &stubNotifier{name: ChannelSMS, enabled: true, failErr: errors.New("网关超时")}
	good := &stubNotifier{name: ChannelMail, enabled: true}
	reg.Register(bad)
	reg.Register(good)

	payload := NotifyPayload{MessageID: 1, UserUID: "u1"}
	if err := reg.Dispatch(context.Background(), ChannelSMS, payload); err == nil {
		t.Error("失败渠道应返回错误")
	}
	// 单渠道失败不影响其他渠道
	if err := reg.Dispatch(context.Background(), ChannelMail, payload); err != nil {
		t.Errorf("其他渠道不应受失败渠道影响: %v", err)
	}
	if good.count() != 1 {
		t.Errorf("正常渠道应投递成功，实际 %d 次", good.count())
	}
}

func TestDispatcher_UnknownOrDisabledChannel(t *testing.T) {
	reg := NewNotifierRegistry()
	reg.Register(&stubNotifier{name: ChannelSMS, enabled: false})

	// 未注册渠道：静默跳过
	if err := reg.Dispatch(context.Background(), "not-exist", NotifyPayload{}); err != nil {
		t.Errorf("未注册渠道应静默跳过，实际 %v", err)
	}
	// 已注册但未启用：静默跳过
	if err := reg.Dispatch(context.Background(), ChannelSMS, NotifyPayload{}); err != nil {
		t.Errorf("未启用渠道应静默跳过，实际 %v", err)
	}
}

// ========== 渠道实现在未配置时的行为 ==========

func TestSMSNotifier_DisabledWhenUnconfigured(t *testing.T) {
	n := NewSMSNotifier(notifierConfig{smsEnabled: true, smsEndpoint: ""})
	if n.Enabled() {
		t.Error("缺少 endpoint 时短信渠道应不可用")
	}
	if err := n.Send(context.Background(), NotifyPayload{UserUID: "u1"}); !errors.Is(err, ErrNotifierDisabled) {
		t.Errorf("期望 ErrNotifierDisabled，实际 %v", err)
	}

	off := NewSMSNotifier(notifierConfig{smsEnabled: false, smsEndpoint: "http://x"})
	if off.Enabled() {
		t.Error("开关关闭时短信渠道应不可用")
	}
}

func TestSMSNotifier_MissingPhone(t *testing.T) {
	n := NewSMSNotifier(notifierConfig{smsEnabled: true, smsEndpoint: "http://example.invalid"})
	n.sender = &stubSMSSender{} // 绕过网络
	if err := n.Send(context.Background(), NotifyPayload{UserUID: "u1"}); !errors.Is(err, ErrNotifierNoRecipient) {
		t.Errorf("缺少手机号应返回 ErrNotifierNoRecipient，实际 %v", err)
	}
	// 从 Extra 中取到手机号
	sender := &stubSMSSender{}
	n.sender = sender
	err := n.Send(context.Background(), NotifyPayload{Extra: map[string]any{"phone": "13800000000"}})
	if err != nil {
		t.Errorf("提供手机号后应投递成功，实际 %v", err)
	}
	if sender.count() != 1 || sender.phones[0] != "13800000000" {
		t.Errorf("应使用 Extra.phone 作为收件号码，实际 %v", sender.phones)
	}
}

func TestMailNotifier_DisabledWhenUnconfigured(t *testing.T) {
	n := NewMailNotifier(notifierConfig{mailEnabled: true, mailEndpoint: ""})
	if n.Enabled() {
		t.Error("缺少 endpoint 时邮件渠道应不可用")
	}
}

func TestPushNotifier_DisabledWhenUnconfigured(t *testing.T) {
	// 缺少 AppKey / MasterSecret 时不可用
	n := NewPushNotifier(notifierConfig{pushEnabled: true, jpushAppKey: "k"})
	if n.Enabled() {
		t.Error("缺少 master_secret 时推送渠道应不可用")
	}
	// 具备凭证则可用
	ready := NewPushNotifier(notifierConfig{pushEnabled: true, jpushAppKey: "k", jpushMaster: "s", jpushHost: defaultJPushHost, pushTimeout: time.Second})
	if !ready.Enabled() {
		t.Error("凭证齐全时推送渠道应可用")
	}
}

func TestBuildJPushAudience_Priority(t *testing.T) {
	// registration_id 优先
	a := buildJPushAudience(NotifyPayload{UserUID: "u1", Extra: map[string]any{"registration_id": "r1,r2"}})
	if len(a.RegistrationID) != 2 || len(a.Alias) != 0 {
		t.Errorf("应优先使用 registration_id，实际 %+v", a)
	}
	// alias 次之
	a = buildJPushAudience(NotifyPayload{UserUID: "u1", Extra: map[string]any{"alias": "alice"}})
	if len(a.Alias) != 1 || a.Alias[0] != "alice" {
		t.Errorf("应使用 alias，实际 %+v", a)
	}
	// 回退到 UserUID（约定为 alias）
	a = buildJPushAudience(NotifyPayload{UserUID: "u9"})
	if len(a.Alias) != 1 || a.Alias[0] != "u9" {
		t.Errorf("应回退到 UserUID 作为 alias，实际 %+v", a)
	}
	// 全部缺失
	a = buildJPushAudience(NotifyPayload{})
	if len(a.RegistrationID)+len(a.Alias)+len(a.Tag) != 0 {
		t.Errorf("无收件人信息时 audience 应为空，实际 %+v", a)
	}
}

func TestBuildSMSContent_Truncation(t *testing.T) {
	long := strings.Repeat("测", 300)
	got := buildSMSContent(NotifyPayload{Title: "标题", Content: long})
	if runes := []rune(got); len(runes) > smsContentMaxRunes {
		t.Errorf("短信正文应截断到 %d 字，实际 %d", smsContentMaxRunes, len(runes))
	}
	short := buildSMSContent(NotifyPayload{Title: "标题", Content: "正文"})
	if !strings.Contains(short, "标题") || !strings.Contains(short, "正文") {
		t.Errorf("短正文应包含标题与内容，实际 %q", short)
	}
}

// ========== 端到端：MessageService 多渠道分发 ==========

func TestMessageService_MultiChannelDispatch(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:multich?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer client.Close()
	prevORM := global.ORM
	global.ORM = client
	defer func() { global.ORM = prevORM }()

	hub := sse.NewHub()
	defer hub.Shutdown()

	reg := NewNotifierRegistry()
	reg.Register(NewInAppNotifier(hub))
	sms := &stubNotifier{name: ChannelSMS, enabled: true}
	mail := &stubNotifier{name: ChannelMail, enabled: true}
	reg.Register(sms)
	reg.Register(mail)

	prefSvc := NewMessagePreferenceService()
	svc := NewMessageServiceWithNotifiers(reg, prefSvc)
	ctx := t.Context()

	// 用户开启短信与邮件渠道
	if _, err := prefSvc.Update(ctx, "u1", &UpdateMessagePreferenceRequest{
		ChannelSMS:  boolPtr(true),
		ChannelMail: boolPtr(true),
	}); err != nil {
		t.Fatalf("更新偏好失败: %v", err)
	}

	_, err := svc.PublishToUser(ctx, "u1", PublishMessage{
		Type: MessageTypeSystem, Title: "审批通知", Content: "您的申请已通过",
	})
	if err != nil {
		t.Fatalf("发布失败: %v", err)
	}

	if sms.count() != 1 {
		t.Errorf("短信渠道应投递 1 次，实际 %d", sms.count())
	}
	if mail.count() != 1 {
		t.Errorf("邮件渠道应投递 1 次，实际 %d", mail.count())
	}
}

func TestMessageService_ExternalChannelRespectsUserToggle(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:multich2?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer client.Close()
	prevORM := global.ORM
	global.ORM = client
	defer func() { global.ORM = prevORM }()

	hub := sse.NewHub()
	defer hub.Shutdown()

	reg := NewNotifierRegistry()
	reg.Register(NewInAppNotifier(hub))
	sms := &stubNotifier{name: ChannelSMS, enabled: true}
	reg.Register(sms)

	prefSvc := NewMessagePreferenceService()
	svc := NewMessageServiceWithNotifiers(reg, prefSvc)
	ctx := t.Context()

	// 用户未开启短信渠道（默认关闭）
	_, err := svc.PublishToUser(ctx, "u2", PublishMessage{
		Type: MessageTypeSystem, Title: "t", Content: "c",
	})
	if err != nil {
		t.Fatalf("发布失败: %v", err)
	}
	if sms.count() != 0 {
		t.Errorf("用户未开启短信渠道时不应投递，实际 %d 次", sms.count())
	}
}

// ========== 端到端：全员广播不群发外部渠道 ==========

func TestMessageService_Broadcast_SkipsExternalChannels(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:multich3?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer client.Close()
	prevORM := global.ORM
	global.ORM = client
	defer func() { global.ORM = prevORM }()

	hub := sse.NewHub()
	defer hub.Shutdown()

	reg := NewNotifierRegistry()
	reg.Register(NewInAppNotifier(hub))
	sms := &stubNotifier{name: ChannelSMS, enabled: true}
	reg.Register(sms)

	svc := NewMessageServiceWithNotifiers(reg, NewMessagePreferenceService())

	_, err := svc.Broadcast(t.Context(), PublishMessage{
		Type: MessageTypeSystem, Title: "全员公告", Content: "系统维护通知",
	})
	if err != nil {
		t.Fatalf("广播失败: %v", err)
	}
	// 全员广播只走站内信，避免对全部用户群发短信
	if sms.count() != 0 {
		t.Errorf("全员广播不应触发短信群发，实际 %d 次", sms.count())
	}
}

// ========== 偏好渠道字段读写 ==========

func TestMessagePreference_ChannelFieldsRoundTrip(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:prefch?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer client.Close()
	prevORM := global.ORM
	global.ORM = client
	defer func() { global.ORM = prevORM }()

	svc := NewMessagePreferenceService()
	ctx := t.Context()

	// 默认值：外部渠道全关
	def, err := svc.GetOrDefault(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if def.ChannelSMS || def.ChannelMail || def.ChannelPush {
		t.Errorf("默认外部渠道应为关闭，实际 sms=%v mail=%v push=%v", def.ChannelSMS, def.ChannelMail, def.ChannelPush)
	}
	if !def.DesktopNotify {
		t.Error("默认站内信应为开启")
	}

	// 开启后持久化
	updated, err := svc.Update(ctx, "u1", &UpdateMessagePreferenceRequest{
		ChannelSMS:  boolPtr(true),
		ChannelPush: boolPtr(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.ChannelSMS || !updated.ChannelPush || updated.ChannelMail {
		t.Errorf("渠道开关未正确持久化: %+v", updated)
	}

	// 重新读取应一致
	reread, err := svc.GetOrDefault(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if !reread.ChannelSMS || !reread.ChannelPush || reread.ChannelMail {
		t.Errorf("重新读取渠道开关不一致: %+v", reread)
	}
}

// ========== 端到端：并发投递 ==========

// slowStubNotifier 模拟慢速外部渠道（如短信网关高延迟）
type slowStubNotifier struct {
	name string
	mu   sync.Mutex
	done []time.Time
}

func (s *slowStubNotifier) Name() string  { return s.name }
func (s *slowStubNotifier) Enabled() bool { return s.enabledOrTrue() }

func (s *slowStubNotifier) enabledOrTrue() bool { return true }

func (s *slowStubNotifier) Send(_ context.Context, _ NotifyPayload) error {
	time.Sleep(150 * time.Millisecond)
	s.mu.Lock()
	s.done = append(s.done, time.Now())
	s.mu.Unlock()
	return nil
}

func (s *slowStubNotifier) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.done)
}

// TestMessageService_Dispatch_Concurrent 外部渠道应并发投递：
// 两个各耗时 150ms 的渠道，总耗时应远小于串行的 300ms。
func TestMessageService_Dispatch_Concurrent(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:conc?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer client.Close()
	prevORM := global.ORM
	global.ORM = client
	defer func() { global.ORM = prevORM }()

	hub := sse.NewHub()
	defer hub.Shutdown()

	c := &sse.Client{UserUID: "u1", ConnID: "c1", Send: make(chan sse.Event, 16), Done: make(chan struct{})}
	hub.Register(c)

	reg := NewNotifierRegistry()
	reg.Register(NewInAppNotifier(hub))
	slowSMS := &slowStubNotifier{name: ChannelSMS}
	slowMail := &slowStubNotifier{name: ChannelMail}
	reg.Register(slowSMS)
	reg.Register(slowMail)

	prefSvc := NewMessagePreferenceService()
	svc := NewMessageServiceWithNotifiers(reg, prefSvc)
	ctx := t.Context()

	if _, err := prefSvc.Update(ctx, "u1", &UpdateMessagePreferenceRequest{
		ChannelSMS:  boolPtr(true),
		ChannelMail: boolPtr(true),
	}); err != nil {
		t.Fatalf("更新偏好失败: %v", err)
	}

	start := time.Now()
	_, err := svc.PublishToUser(ctx, "u1", PublishMessage{
		Type: MessageTypeSystem, Title: "并发测试", Content: "c",
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("发布失败: %v", err)
	}

	// 站内信应立即送达（不等外部渠道）
	select {
	case <-c.Send:
	default:
		t.Error("站内信应在外部渠道完成前先行送达")
	}

	if slowSMS.count() != 1 || slowMail.count() != 1 {
		t.Fatalf("两个慢渠道应各投递 1 次，实际 sms=%d mail=%d", slowSMS.count(), slowMail.count())
	}

	// 并发下总耗时 ≈ 单渠道耗时（150ms），串行则需要 300ms
	if elapsed >= 290*time.Millisecond {
		t.Errorf("外部渠道应并发投递，总耗时 %v 接近串行时间，疑似未并发", elapsed)
	}
	if elapsed < 140*time.Millisecond {
		t.Errorf("总耗时 %v 短于单个渠道耗时，渠道可能未真正执行", elapsed)
	}
}

// TestMessageService_Dispatch_InAppNotBlockedBySlowChannel 站内信送达时间
// 不应受慢速外部渠道影响（同步先行策略）。
func TestMessageService_Dispatch_InAppNotBlockedBySlowChannel(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:conc2?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer client.Close()
	prevORM := global.ORM
	global.ORM = client
	defer func() { global.ORM = prevORM }()

	hub := sse.NewHub()
	defer hub.Shutdown()

	c := &sse.Client{UserUID: "u1", ConnID: "c1", Send: make(chan sse.Event, 16), Done: make(chan struct{})}
	hub.Register(c)

	reg := NewNotifierRegistry()
	reg.Register(NewInAppNotifier(hub))
	reg.Register(&slowStubNotifier{name: ChannelSMS})

	prefSvc := NewMessagePreferenceService()
	svc := NewMessageServiceWithNotifiers(reg, prefSvc)
	ctx := t.Context()
	if _, err := prefSvc.Update(ctx, "u1", &UpdateMessagePreferenceRequest{ChannelSMS: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}

	// 用独立 goroutine 记录站内信事件的真实到达时刻。
	// 不能在 PublishToUser 返回后才计时：发布主流程会 wg.Wait() 等待外部渠道
	// （150ms），而站内信事件在 dispatch 内同步先行、0ms 即写入 c.Send 缓冲区。
	start := time.Now()
	arrived := make(chan time.Duration, 1)
	go func() {
		select {
		case evt := <-c.Send:
			if evt.Event != SSEEventMessage {
				arrived <- -1 // 第一个事件应是站内信消息而非未读数
				return
			}
			arrived <- time.Since(start)
		case <-time.After(2 * time.Second):
			arrived <- -1
		}
	}()

	if _, err := svc.PublishToUser(ctx, "u1", PublishMessage{
		Type: MessageTypeSystem, Title: "t", Content: "c",
	}); err != nil {
		t.Fatal(err)
	}

	since := <-arrived
	if since < 0 {
		t.Fatal("站内信事件未送达或事件类型不对")
	}
	// 站内信应在远小于 150ms（慢渠道耗时）内送达
	if since > 120*time.Millisecond {
		t.Errorf("站内信被慢渠道阻塞，送达耗时 %v", since)
	}
}
