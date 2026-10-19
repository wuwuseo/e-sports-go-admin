package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"

	"github.com/golang-jwt/jwt/v5"
	"github.com/wuwuseo/cmf/bootstrap"
	cmfConfig "github.com/wuwuseo/cmf/config"

	_ "github.com/mattn/go-sqlite3"
)

// ============ 测试替身（§6.1-01 短信登录通道） ============

// smsLoginFakeStore 内存版验证码存储替身：按注入时钟执行 TTL（与真实缓存语义一致），
// 键值全量可见以便断言「不落明文验证码 / 键不含完整手机号」。
type smsLoginFakeStore struct {
	mu      sync.Mutex
	entries map[string]smsLoginFakeEntry
	now     func() time.Time
	setErr  error
	getErr  error
	delErr  error
}

type smsLoginFakeEntry struct {
	value     []byte
	expiresAt time.Time
	hasTTL    bool
}

func newSMSLoginFakeStore() *smsLoginFakeStore {
	return &smsLoginFakeStore{
		entries: make(map[string]smsLoginFakeEntry),
		now:     time.Now,
	}
}

func (s *smsLoginFakeStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return nil, false, s.getErr
	}
	entry, ok := s.entries[key]
	if !ok {
		return nil, false, nil
	}
	if entry.hasTTL && !s.now().Before(entry.expiresAt) {
		delete(s.entries, key)
		return nil, false, nil
	}
	return append([]byte(nil), entry.value...), true, nil
}

func (s *smsLoginFakeStore) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.setErr != nil {
		return s.setErr
	}
	if ttl <= 0 {
		return errors.New("ttl 必须为正数")
	}
	s.entries[key] = smsLoginFakeEntry{
		value:     append([]byte(nil), value...),
		expiresAt: s.now().Add(ttl),
		hasTTL:    true,
	}
	return nil
}

func (s *smsLoginFakeStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.delErr != nil {
		return s.delErr
	}
	delete(s.entries, key)
	return nil
}

func (s *smsLoginFakeStore) snapshot() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.entries))
	for key, entry := range s.entries {
		out[key] = string(entry.value)
	}
	return out
}

func (s *smsLoginFakeStore) has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.entries[key]
	return ok
}

// smsLoginFakeNotifier 短信渠道替身：记录投递载荷，可切换启用状态/注入失败。
type smsLoginFakeNotifier struct {
	mu       sync.Mutex
	enabled  bool
	sendErr  error
	payloads []NotifyPayload
}

func (n *smsLoginFakeNotifier) Name() string  { return ChannelSMS }
func (n *smsLoginFakeNotifier) Enabled() bool { return n.enabled }

func (n *smsLoginFakeNotifier) Send(_ context.Context, payload NotifyPayload) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.sendErr != nil {
		return n.sendErr
	}
	n.payloads = append(n.payloads, payload)
	return nil
}

func (n *smsLoginFakeNotifier) deliveries() []NotifyPayload {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]NotifyPayload(nil), n.payloads...)
}

// ============ 测试环境 ============

type smsLoginTestEnv struct {
	t        *testing.T
	client   *ent.Client
	svc      *SMSCodeService
	store    *smsLoginFakeStore
	notifier *smsLoginFakeNotifier
	clock    *time.Time
}

// smsLoginTestCode 测试用固定验证码（由 svc.generate 注入，避免随机性）。
const smsLoginTestCode = "246810"

func newSMSLoginTestEnv(t *testing.T) *smsLoginTestEnv {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:sms_login_test?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	cfg := &cmfConfig.Config{}
	cfg.App.Name = "sms-login-test"
	cfg.App.Secret = "sms-login-test-secret"
	cfg.App.LoginExpires = 3600
	cfg.App.RefreshExpires = 86400
	bs := bootstrap.NewBootstrap()
	bs.RegisterService("config", cfg)
	prevBootstrap := global.Bootstrap
	global.Bootstrap = bs
	t.Cleanup(func() { global.Bootstrap = prevBootstrap })

	env := &smsLoginTestEnv{
		t:        t,
		client:   client,
		store:    newSMSLoginFakeStore(),
		notifier: &smsLoginFakeNotifier{enabled: true},
	}
	registry := NewNotifierRegistry()
	registry.Register(env.notifier)

	at := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	env.clock = &at
	env.store.now = func() time.Time { return *env.clock }
	env.svc = NewSMSCodeService(client, env.store, registry)
	env.svc.now = func() time.Time { return *env.clock }
	env.svc.generate = func() (string, error) { return smsLoginTestCode, nil }
	return env
}

func (e *smsLoginTestEnv) advance(d time.Duration) { *e.clock = e.clock.Add(d) }

func (e *smsLoginTestEnv) setConfig(key, value string) {
	e.t.Helper()
	if _, err := e.client.SystemConfig.Create().
		SetKey(key).
		SetName(key).
		SetValue(value).
		SetGroup("user_auth").
		SetType("number").
		SetStatus(1).
		Save(e.t.Context()); err != nil {
		e.t.Fatalf("播种系统配置 %s 失败: %v", key, err)
	}
}

func (e *smsLoginTestEnv) createUser(phone string) *ent.User {
	e.t.Helper()
	u, err := e.client.User.Create().
		SetUID("sms-user-" + phone).
		SetUsername("smsuser" + phone).
		SetEmail("sms" + phone + "@test.com").
		SetPhone(phone).
		SetPassword("hashedpass").
		SetPasswordSalt("salt").
		SetStatus(user.StatusActive).
		Save(e.t.Context())
	if err != nil {
		e.t.Fatalf("创建测试用户失败: %v", err)
	}
	return u
}

func (e *smsLoginTestEnv) send(phone, ip string) (*SendSMSCodeResponse, error) {
	e.t.Helper()
	return e.svc.SendCode(e.t.Context(), phone, ip)
}

func (e *smsLoginTestEnv) lastContent() string {
	e.t.Helper()
	deliveries := e.notifier.deliveries()
	if len(deliveries) == 0 {
		e.t.Fatal("短信渠道没有收到任何投递")
	}
	return deliveries[len(deliveries)-1].Content
}

// ============ 发码：不假成功 ============

// TestSMSCodeService_SendCode_ChannelUnconfiguredFailsExplicitly §6.1-01 不假成功：
// 短信网关未配置时必须明确失败，且**不得**把验证码写进存储（否则等于发了一条永远收不到的码）。
func TestSMSCodeService_SendCode_ChannelUnconfiguredFailsExplicitly(t *testing.T) {
	env := newSMSLoginTestEnv(t)
	env.notifier.enabled = false

	resp, err := env.send("13800000001", "10.0.0.1")
	if !errors.Is(err, ErrSMSChannelUnavailable) {
		t.Fatalf("短信渠道未配置时应返回 ErrSMSChannelUnavailable，实际 err=%v resp=%+v", err, resp)
	}
	if resp != nil {
		t.Fatalf("失败时不应返回成功响应，实际 resp=%+v", resp)
	}
	if len(env.store.snapshot()) != 0 {
		t.Fatalf("渠道不可用时不应写入任何验证码记录，实际存储=%v", env.store.snapshot())
	}
	if len(env.notifier.deliveries()) != 0 {
		t.Fatal("渠道不可用时不应有投递记录")
	}
}

// TestSMSCodeService_SendCode_StoreFailureFailsExplicitly 发送前落库失败必须明确失败且不发短信。
func TestSMSCodeService_SendCode_StoreFailureFailsExplicitly(t *testing.T) {
	env := newSMSLoginTestEnv(t)
	env.store.setErr = errors.New("redis 连接失败")

	if _, err := env.send("13800000002", "10.0.0.2"); !errors.Is(err, ErrSMSStoreUnavailable) {
		t.Fatalf("存储不可用时应返回 ErrSMSStoreUnavailable，实际 err=%v", err)
	}
	if len(env.notifier.deliveries()) != 0 {
		t.Fatal("存储失败时不应发出短信（否则用户收到的码无法校验）")
	}
}

// TestSMSCodeService_SendCode_ResponseAndStoreCarryNoPlainCode 安全红线：
// 验证码不得出现在响应里，也不得以明文落在存储里（存储只允许哈希）。
func TestSMSCodeService_SendCode_ResponseAndStoreCarryNoPlainCode(t *testing.T) {
	env := newSMSLoginTestEnv(t)

	resp, err := env.send("13800000003", "10.0.0.3")
	if err != nil {
		t.Fatalf("发送验证码失败: %v", err)
	}
	if resp == nil || !resp.Sent || resp.ExpiresIn != smsCodeTTLDefault {
		t.Fatalf("响应口径应为 sent=true / expires_in=%d，实际 %+v", smsCodeTTLDefault, resp)
	}

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("序列化响应失败: %v", err)
	}
	if strings.Contains(string(raw), smsLoginTestCode) {
		t.Fatalf("响应体不得包含验证码，实际 %s", raw)
	}
	if strings.Contains(string(raw), `"code"`) {
		t.Fatalf("响应体只允许 sent / expires_in 两个字段，实际 %s", raw)
	}

	for key, value := range env.store.snapshot() {
		if strings.Contains(value, smsLoginTestCode) {
			t.Fatalf("存储 %s 不得包含明文验证码，实际 %s", key, value)
		}
		if strings.Contains(key, "13800000003") {
			t.Fatalf("存储键不得包含完整手机号，实际 %s", key)
		}
	}

	content := env.lastContent()
	if !strings.Contains(content, smsLoginTestCode) {
		t.Fatalf("短信正文应包含验证码，实际 %q", content)
	}
}

// ============ 发码：防刷 ============

// TestSMSCodeService_SendCode_ThrottledByInterval 同一手机号在间隔内重复发码必须被拒，过间隔后恢复。
func TestSMSCodeService_SendCode_ThrottledByInterval(t *testing.T) {
	env := newSMSLoginTestEnv(t)

	if _, err := env.send("13800000004", "10.0.0.4"); err != nil {
		t.Fatalf("首次发码应成功: %v", err)
	}
	if _, err := env.send("13800000004", "10.0.0.4"); !errors.Is(err, ErrSMSCodeTooFrequent) {
		t.Fatalf("间隔内重复发码应返回 ErrSMSCodeTooFrequent，实际 err=%v", err)
	}
	if got := len(env.notifier.deliveries()); got != 1 {
		t.Fatalf("被限流的请求不得投递短信，实际投递 %d 次", got)
	}

	env.advance(time.Duration(smsCodeIntervalDefault) * time.Second)
	if _, err := env.send("13800000004", "10.0.0.4"); err != nil {
		t.Fatalf("超过间隔后应可再次发码，实际 err=%v", err)
	}
}

// TestSMSCodeService_SendCode_EnforcesPhoneAndIPDailyLimits 手机号与 IP 的当日上限。
func TestSMSCodeService_SendCode_EnforcesPhoneAndIPDailyLimits(t *testing.T) {
	env := newSMSLoginTestEnv(t)
	env.setConfig(smsCodePhoneDailyConfigKey, "2")
	env.setConfig(smsCodeIPDailyConfigKey, "3")
	interval := time.Duration(smsCodeIntervalDefault) * time.Second

	for i := 0; i < 2; i++ {
		if _, err := env.send("13800000005", "10.0.0.5"); err != nil {
			t.Fatalf("第 %d 次发码应成功: %v", i+1, err)
		}
		env.advance(interval)
	}
	if _, err := env.send("13800000005", "10.0.0.5"); !errors.Is(err, ErrSMSCodeSendLimit) {
		t.Fatalf("达到手机号当日上限后应返回 ErrSMSCodeSendLimit，实际 err=%v", err)
	}

	// 同一 IP 换手机号：10.0.0.5 已用掉 2 次，IP 上限 3，再发 1 次应成功、第 4 次应被拒
	if _, err := env.send("13800000006", "10.0.0.5"); err != nil {
		t.Fatalf("同 IP 第 3 次发码应成功: %v", err)
	}
	env.advance(interval)
	if _, err := env.send("13800000007", "10.0.0.5"); !errors.Is(err, ErrSMSCodeSendLimit) {
		t.Fatalf("达到 IP 当日上限后应返回 ErrSMSCodeSendLimit，实际 err=%v", err)
	}
}

// ============ 码登录：一次性 / 短时效 / 失败次数 ============

// TestSMSCodeService_Login_ReplayFails 一次性：使用过的验证码重放必须失败。
func TestSMSCodeService_Login_ReplayFails(t *testing.T) {
	env := newSMSLoginTestEnv(t)
	u := env.createUser("13800000010")
	if _, err := env.send("13800000010", "10.0.1.1"); err != nil {
		t.Fatalf("发码失败: %v", err)
	}

	token, err := env.svc.LoginBySMS(t.Context(), "13800000010", smsLoginTestCode)
	if err != nil || token == nil || token.AccessToken == "" {
		t.Fatalf("首次码登录应成功，实际 token=%+v err=%v", token, err)
	}
	if env.store.has(smsCodeKey("13800000010")) {
		t.Fatal("验证码使用后必须立即失效（存储中不应残留）")
	}

	replay, err := env.svc.LoginBySMS(t.Context(), "13800000010", smsLoginTestCode)
	if !errors.Is(err, ErrSMSCodeInvalid) {
		t.Fatalf("重放必须失败并返回 ErrSMSCodeInvalid，实际 token=%+v err=%v", replay, err)
	}
	if replay != nil {
		t.Fatalf("重放不得签发令牌，实际 %+v", replay)
	}

	updated, err := env.client.User.Get(t.Context(), u.ID)
	if err != nil {
		t.Fatalf("查询用户失败: %v", err)
	}
	if updated.LastLogin.IsZero() {
		t.Fatal("码登录成功后应更新最近登录时间")
	}
}

// TestSMSCodeService_Login_ConcurrentReplayOnlyOneSucceeds 并发重放同一验证码只允许一个成功。
func TestSMSCodeService_Login_ConcurrentReplayOnlyOneSucceeds(t *testing.T) {
	env := newSMSLoginTestEnv(t)
	env.createUser("13800000011")
	if _, err := env.send("13800000011", "10.0.1.2"); err != nil {
		t.Fatalf("发码失败: %v", err)
	}

	const workers = 8
	var wg sync.WaitGroup
	results := make([]error, workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			_, err := env.svc.LoginBySMS(context.Background(), "13800000011", smsLoginTestCode)
			results[idx] = err
		}(i)
	}
	close(start)
	wg.Wait()

	success := 0
	for _, err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("并发重放同一验证码只允许 1 次成功，实际 %d 次", success)
	}
}

// TestSMSCodeService_Login_ExpiredCodeFails 短时效：过期验证码必须失败。
func TestSMSCodeService_Login_ExpiredCodeFails(t *testing.T) {
	env := newSMSLoginTestEnv(t)
	env.createUser("13800000012")
	if _, err := env.send("13800000012", "10.0.1.3"); err != nil {
		t.Fatalf("发码失败: %v", err)
	}
	env.advance(time.Duration(smsCodeTTLDefault)*time.Second + time.Second)

	if _, err := env.svc.LoginBySMS(t.Context(), "13800000012", smsLoginTestCode); !errors.Is(err, ErrSMSCodeInvalid) {
		t.Fatalf("过期验证码应返回 ErrSMSCodeInvalid，实际 err=%v", err)
	}
}

// TestSMSCodeService_Login_AttemptsExhaustedInvalidatesCode 失败次数限制：
// 连续错误达到上限后，即使给出正确验证码也必须失败（记录已作废）。
func TestSMSCodeService_Login_AttemptsExhaustedInvalidatesCode(t *testing.T) {
	env := newSMSLoginTestEnv(t)
	env.createUser("13800000013")
	env.setConfig(smsCodeMaxAttemptsConfigKey, "3")
	if _, err := env.send("13800000013", "10.0.1.4"); err != nil {
		t.Fatalf("发码失败: %v", err)
	}

	for i := 0; i < 3; i++ {
		if _, err := env.svc.LoginBySMS(t.Context(), "13800000013", "000000"); !errors.Is(err, ErrSMSCodeInvalid) {
			t.Fatalf("第 %d 次错误验证码应返回 ErrSMSCodeInvalid，实际 err=%v", i+1, err)
		}
	}
	if _, err := env.svc.LoginBySMS(t.Context(), "13800000013", smsLoginTestCode); !errors.Is(err, ErrSMSCodeInvalid) {
		t.Fatalf("失败次数用尽后正确验证码也必须失败，实际 err=%v", err)
	}
}

// ============ 码登录：不泄露「手机号是否注册」 ============

// TestSMSCodeService_Login_UnknownPhoneNotDistinguishable 安全红线：
// 未注册手机号与「验证码错误」的返回必须完全一致，不通过响应差异暴露注册状态。
func TestSMSCodeService_Login_UnknownPhoneNotDistinguishable(t *testing.T) {
	env := newSMSLoginTestEnv(t)
	env.createUser("13800000014") // 已注册
	const unknown = "13800000015"

	for _, phone := range []string{"13800000014", unknown} {
		if _, err := env.send(phone, "10.0.2.1"); err != nil {
			t.Fatalf("向 %s 发码不应因未注册而失败: %v", phone, err)
		}
	}

	_, registeredErr := env.svc.LoginBySMS(t.Context(), "13800000014", "000000")
	if !errors.Is(registeredErr, ErrSMSCodeInvalid) {
		t.Fatalf("已注册手机号错误验证码应返回 ErrSMSCodeInvalid，实际 err=%v", registeredErr)
	}
	_, unknownErr := env.svc.LoginBySMS(t.Context(), unknown, "000000")
	if !errors.Is(unknownErr, ErrSMSCodeInvalid) {
		t.Fatalf("未注册手机号错误验证码应返回 ErrSMSCodeInvalid，实际 err=%v", unknownErr)
	}
	if registeredErr.Error() != unknownErr.Error() {
		t.Fatalf("已注册/未注册的错误文案必须一致：%q vs %q", registeredErr, unknownErr)
	}

	// 未注册手机号即使给出正确验证码，也必须与「验证码错误」同一口径（不签发、不暴露原因）
	token, err := env.svc.LoginBySMS(t.Context(), unknown, smsLoginTestCode)
	if !errors.Is(err, ErrSMSCodeInvalid) {
		t.Fatalf("未注册手机号正确验证码必须返回同一错误，实际 token=%+v err=%v", token, err)
	}
	if token != nil {
		t.Fatal("未注册手机号不得签发令牌")
	}
}

// ============ 码登录：复用同一套令牌签发 ============

// TestSMSCodeService_Login_IssuesSameTokenAsPasswordLogin 码登录必须签发与密码登录同一套令牌。
func TestSMSCodeService_Login_IssuesSameTokenAsPasswordLogin(t *testing.T) {
	env := newSMSLoginTestEnv(t)
	u := env.createUser("13800000016")
	if _, err := env.send("13800000016", "10.0.2.2"); err != nil {
		t.Fatalf("发码失败: %v", err)
	}

	token, err := env.svc.LoginBySMS(t.Context(), "13800000016", smsLoginTestCode)
	if err != nil || token == nil {
		t.Fatalf("码登录失败: token=%+v err=%v", token, err)
	}
	claims, err := parseSMSLoginToken(token.AccessToken, "sms-login-test-secret")
	if err != nil {
		t.Fatalf("访问令牌必须能用用户端密钥验签（同一套签发逻辑）: %v", err)
	}
	if claims["id"] != u.UID || claims["subject"] != u.Username {
		t.Fatalf("令牌主体口径与密码登录不一致: %+v", claims)
	}
	if claims["token_type"] != "access" || claims["domain"] != defaultPermissionDomain {
		t.Fatalf("令牌类型/域口径不一致: %+v", claims)
	}
	if token.TokenType != "Bearer" || token.ExpiresIn != 3600 || token.RefreshToken == "" {
		t.Fatalf("令牌响应口径与密码登录不一致: %+v", token)
	}
	if _, err := parseSMSLoginToken(token.RefreshToken, "sms-login-test-secret"); err != nil {
		t.Fatalf("刷新令牌必须能用同一密钥验签: %v", err)
	}
}

// TestSMSCodeService_Login_StoreUnavailableFailsExplicitly 存储不可用时必须明确失败，而不是放行。
func TestSMSCodeService_Login_StoreUnavailableFailsExplicitly(t *testing.T) {
	env := newSMSLoginTestEnv(t)
	env.createUser("13800000017")
	env.store.getErr = errors.New("redis 连接失败")

	token, err := env.svc.LoginBySMS(t.Context(), "13800000017", smsLoginTestCode)
	if !errors.Is(err, ErrSMSStoreUnavailable) {
		t.Fatalf("存储不可用时应返回 ErrSMSStoreUnavailable，实际 err=%v", err)
	}
	if token != nil {
		t.Fatal("存储不可用时不得签发令牌")
	}
}

// TestSMSCodeService_MaskPhone 日志口径：不得输出完整手机号。
func TestSMSCodeService_MaskPhone(t *testing.T) {
	if got := maskSMSLoginPhone("13800000018"); got != "138****0018" {
		t.Fatalf("手机号脱敏结果 = %q", got)
	}
	if got := maskSMSLoginPhone("abc"); strings.Contains(got, "abc") {
		t.Fatalf("非法手机号也不应原样输出，实际 %q", got)
	}
}

func parseSMSLoginToken(raw, secret string) (jwt.MapClaims, error) {
	parsed, err := jwt.Parse(raw, func(*jwt.Token) (any, error) {
		return []byte(secret), nil
	}, jwt.WithoutClaimsValidation())
	if err != nil {
		return nil, err
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return nil, errors.New("unexpected claims type")
	}
	return claims, nil
}
