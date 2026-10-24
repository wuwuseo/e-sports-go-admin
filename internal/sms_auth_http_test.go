package internal

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"
	"go-admin/internal/handler"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/bootstrap"
	cmfConfig "github.com/wuwuseo/cmf/config"

	_ "github.com/mattn/go-sqlite3"
)

// ============ HTTP 契约测试用替身（内部包无法引用 service 包的未导出替身） ============

type smsHTTPTestStore struct {
	mu      sync.Mutex
	entries map[string]smsHTTPTestEntry
}

type smsHTTPTestEntry struct {
	value     []byte
	expiresAt time.Time
}

func newSMSHTTPTestStore() *smsHTTPTestStore {
	return &smsHTTPTestStore{entries: make(map[string]smsHTTPTestEntry)}
}

func (s *smsHTTPTestStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok {
		return nil, false, nil
	}
	if time.Now().After(entry.expiresAt) {
		delete(s.entries, key)
		return nil, false, nil
	}
	return append([]byte(nil), entry.value...), true, nil
}

func (s *smsHTTPTestStore) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[key] = smsHTTPTestEntry{value: append([]byte(nil), value...), expiresAt: time.Now().Add(ttl)}
	return nil
}

func (s *smsHTTPTestStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, key)
	return nil
}

type smsHTTPTestNotifier struct {
	mu       sync.Mutex
	contents []string
}

func (n *smsHTTPTestNotifier) Name() string  { return service.ChannelSMS }
func (n *smsHTTPTestNotifier) Enabled() bool { return true }

func (n *smsHTTPTestNotifier) Send(_ context.Context, payload service.NotifyPayload) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.contents = append(n.contents, payload.Content)
	return nil
}

func (n *smsHTTPTestNotifier) lastContent(t *testing.T) string {
	t.Helper()
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.contents) == 0 {
		t.Fatal("短信渠道没有收到投递")
	}
	return n.contents[len(n.contents)-1]
}

// smsHTTPTestCode 从短信正文里取 6 位验证码（服务未导出随机源，只能从投递内容反推）。
func smsHTTPTestCode(t *testing.T, content string) string {
	t.Helper()
	smsHTTPTestCodePattern := regexp.MustCompile(`\d{6}`)
	if code := smsHTTPTestCodePattern.FindString(content); code != "" {
		return code
	}
	t.Fatalf("短信正文中未找到 6 位验证码: %q", content)
	return ""
}

// ============ HTTP 契约 ============

// TestUserAuthSMSLoginHTTPContract 锁定对外契约与安全红线：
//  1. 发码响应只有 sent / expires_in，**不含验证码**；
//  2. 码登录成功返回与密码登录同一形状的令牌响应；
//  3. 同一验证码重放必须失败；
//  4. 「已注册手机号 + 错误验证码」与「未注册手机号 + 错误验证码」的响应**逐字节一致**（不可枚举）。
func TestUserAuthSMSLoginHTTPContract(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:router_sms_http_contract?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer client.Close()
	global.ORM = client

	cfg := &cmfConfig.Config{}
	cfg.App.Name = "sms-http-test"
	cfg.App.Secret = "sms-http-secret"
	cfg.App.LoginExpires = 3600
	cfg.App.RefreshExpires = 86400
	bs := bootstrap.NewBootstrap()
	bs.RegisterService("config", cfg)
	prevBootstrap := global.Bootstrap
	global.Bootstrap = bs
	t.Cleanup(func() { global.Bootstrap = prevBootstrap })

	const registered = "13900000001"
	if _, err := client.User.Create().
		SetUID("sms-http-user").
		SetUsername("smshttpuser").
		SetEmail("smshttp@test.com").
		SetPhone(registered).
		SetStatus(user.StatusActive).
		Save(t.Context()); err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}

	store := newSMSHTTPTestStore()
	notifier := &smsHTTPTestNotifier{}
	registry := service.NewNotifierRegistry()
	registry.Register(notifier)
	svc := service.NewSMSCodeService(client, store, registry)

	app := fiber.New()
	v1 := app.Group("/api/v1", func(c fiber.Ctx) error {
		c.Locals("utils.Response", utils.NewResponse())
		return c.Next()
	})
	userAuth := v1.Group("/user/auth", func(c fiber.Ctx) error {
		c.Locals("service.SMSCodeService", svc)
		return c.Next()
	})
	userAuth.Post("/sms-code", handler.SendUserSMSCode)
	userAuth.Post("/login-by-sms", handler.LoginUserBySMS)

	post := func(path, body string) (int, string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response, err := app.Test(request)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		raw, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		return response.StatusCode, string(raw)
	}

	// 1) 发码：成功但绝不回验证码
	_, sendBody := post("/api/v1/user/auth/sms-code", `{"phone":"`+registered+`"}`)
	var sendEnvelope struct {
		Code int `json:"code"`
		Data struct {
			Sent      bool `json:"sent"`
			ExpiresIn int  `json:"expires_in"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(sendBody), &sendEnvelope); err != nil {
		t.Fatalf("发码响应解析失败: %v body=%s", err, sendBody)
	}
	if sendEnvelope.Code != global.ResponseSuccessCode || !sendEnvelope.Data.Sent || sendEnvelope.Data.ExpiresIn <= 0 {
		t.Fatalf("发码响应口径异常: %s", sendBody)
	}
	code := smsHTTPTestCode(t, notifier.lastContent(t))
	if strings.Contains(sendBody, code) {
		t.Fatalf("发码响应不得包含验证码: %s", sendBody)
	}

	// 2) 码登录：一次成功，签发与密码登录同形状的令牌
	_, loginBody := post("/api/v1/user/auth/login-by-sms", `{"phone":"`+registered+`","code":"`+code+`"}`)
	var loginEnvelope struct {
		Code int `json:"code"`
		Data struct {
			AccessToken  string `json:"access_token"`
			TokenType    string `json:"token_type"`
			ExpiresIn    int64  `json:"expires_in"`
			RefreshToken string `json:"refresh_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(loginBody), &loginEnvelope); err != nil {
		t.Fatalf("码登录响应解析失败: %v body=%s", err, loginBody)
	}
	if loginEnvelope.Code != global.ResponseSuccessCode || loginEnvelope.Data.AccessToken == "" ||
		loginEnvelope.Data.TokenType != "Bearer" || loginEnvelope.Data.RefreshToken == "" {
		t.Fatalf("码登录应签发与密码登录同一套令牌，实际 %s", loginBody)
	}

	// 3) 重放：同一验证码必须失败
	replayStatus, replayBody := post("/api/v1/user/auth/login-by-sms", `{"phone":"`+registered+`","code":"`+code+`"}`)
	if !strings.Contains(replayBody, "手机号或验证码错误") {
		t.Fatalf("重放必须失败并给出统一中文提示，实际 status=%d body=%s", replayStatus, replayBody)
	}
	if strings.Contains(replayBody, "access_token") {
		t.Fatalf("重放不得签发令牌，实际 %s", replayBody)
	}

	// 4) 不可枚举：另取一对「已注册 / 未注册」手机号都先发码，再用错误验证码登录 → 响应体逐字节一致
	const registered2 = "13900000003"
	const unknown2 = "13900000004"
	if _, err := client.User.Create().
		SetUID("sms-http-user-2").
		SetUsername("smshttpuser2").
		SetEmail("smshttp2@test.com").
		SetPhone(registered2).
		SetStatus(user.StatusActive).
		Save(t.Context()); err != nil {
		t.Fatalf("创建第二个测试用户失败: %v", err)
	}
	for _, phone := range []string{registered2, unknown2} {
		if _, body := post("/api/v1/user/auth/sms-code", `{"phone":"`+phone+`"}`); !strings.Contains(body, `"sent":true`) {
			t.Fatalf("向 %s 发码应成功（不因未注册而差异对待），实际 %s", phone, body)
		}
	}
	_, registeredWrong := post("/api/v1/user/auth/login-by-sms", `{"phone":"`+registered2+`","code":"000000"}`)
	_, unknownWrong := post("/api/v1/user/auth/login-by-sms", `{"phone":"`+unknown2+`","code":"000000"}`)
	if registeredWrong != unknownWrong {
		t.Fatalf("已注册/未注册的错误响应必须完全一致：\n已注册=%s\n未注册=%s", registeredWrong, unknownWrong)
	}
}
