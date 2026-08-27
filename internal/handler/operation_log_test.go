package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go-admin/internal/service"

	"github.com/gofiber/fiber/v3"
	jwtlib "github.com/golang-jwt/jwt/v5"
	cmfjwt "github.com/wuwuseo/cmf/jwt"
)

const opLogTestSecret = "oplog-test-secret-42"

// captureLogSvc 捕获写入的 LogEntry 供断言
type captureLogSvc struct {
	mu     sync.Mutex
	entry  *service.LogEntry
	called int
}

func (c *captureLogSvc) Log(_ context.Context, entry *service.LogEntry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.called++
	c.entry = entry
	return nil
}

func (c *captureLogSvc) List(_ context.Context, _ *service.EffectiveDataScope, _ *service.ListOperationLogsRequest) (*service.ListOperationLogsResponse, error) {
	return nil, nil
}

func (c *captureLogSvc) Get(_ context.Context, _ *service.EffectiveDataScope, _ int) (*service.OperationLogResponse, error) {
	return nil, nil
}

func (c *captureLogSvc) captured() (int, *service.LogEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.called, c.entry
}

func makeOpLogToken(t *testing.T, uid, subject string) string {
	t.Helper()
	tok, err := cmfjwt.CreateToken(jwtlib.MapClaims{
		"id":      uid,
		"subject": subject,
		"domain":  "default",
		"exp":     time.Now().Add(time.Hour).Unix(),
	}, opLogTestSecret)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	return tok
}

// TestLogOperation_ExtractsClaimsAndSetsFields 验证从 JWT claims 中正确提取
// 用户信息，并写入 LogEntry 的各字段
func TestLogOperation_ExtractsClaimsAndSetsFields(t *testing.T) {
	capture := &captureLogSvc{}
	app := fiber.New()
	app.Use(cmfjwt.NewJWTMiddleware(opLogTestSecret))
	app.Post("/test-endpoint", func(c fiber.Ctx) error {
		logOperation(c, capture, "user", "创建", "创建用户: alice")
		return c.SendString("done")
	})

	token := makeOpLogToken(t, "uid-999", "alice")
	body := `{"username":"alice","email":"a@b.com"}`
	req := httptest.NewRequest(http.MethodPost, "/test-endpoint", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ua-ponytail/1.0")
	req.RemoteAddr = "10.20.30.40:1234"

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body)

	called, entry := capture.captured()
	if called != 1 {
		t.Fatalf("Log 应调用 1 次，实际 %d 次", called)
	}
	if entry == nil {
		t.Fatal("LogEntry 为 nil")
	}
	if entry.UserUID != "uid-999" {
		t.Errorf("UserUID 期望 uid-999, 实际 %q", entry.UserUID)
	}
	if entry.Username != "alice" {
		t.Errorf("Username 期望 alice, 实际 %q", entry.Username)
	}
	if entry.Module != "user" || entry.Action != "创建" {
		t.Errorf("Module/Action 不匹配: %+v", entry)
	}
	if entry.Description != "创建用户: alice" {
		t.Errorf("Description 不匹配: %q", entry.Description)
	}
	if entry.Method != http.MethodPost {
		t.Errorf("Method 期望 POST, 实际 %q", entry.Method)
	}
	if entry.Path != "/test-endpoint" {
		t.Errorf("Path 不匹配: %q", entry.Path)
	}
	if entry.UserAgent != "ua-ponytail/1.0" {
		t.Errorf("UserAgent 不匹配: %q", entry.UserAgent)
	}
	if entry.StatusCode != 200 {
		t.Errorf("StatusCode 期望 200, 实际 %d", entry.StatusCode)
	}
	// 请求体应被保留（无敏感字段）
	if !strings.Contains(entry.RequestBody, `"username":"alice"`) {
		t.Errorf("请求体未正确保存或被错误修改: %q", entry.RequestBody)
	}
}

// TestLogOperation_PasswordFieldIsRedacted 验证 password 等敏感字段在请求体中被脱敏
func TestLogOperation_PasswordFieldIsRedacted(t *testing.T) {
	capture := &captureLogSvc{}
	app := fiber.New()
	app.Use(cmfjwt.NewJWTMiddleware(opLogTestSecret))
	app.Post("/login", func(c fiber.Ctx) error {
		logOperation(c, capture, "auth", "登录", "登录尝试")
		return c.SendString("ok")
	})
	token := makeOpLogToken(t, "u", "x")
	body := `{"username":"bob","password":"SuperSecret123!","remember":true}`
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, _ := app.Test(req)
	defer resp.Body.Close()
	io.ReadAll(resp.Body)

	_, entry := capture.captured()
	if entry == nil {
		t.Fatal("LogEntry 为 nil")
	}
	if strings.Contains(entry.RequestBody, "SuperSecret123") {
		t.Fatalf("password 应被脱敏, 但请求体中仍保留原始值: %q", entry.RequestBody)
	}
	if !strings.Contains(entry.RequestBody, `***REDACTED***`) {
		t.Fatalf("password 替换标记缺失: %q", entry.RequestBody)
	}
	// 非敏感字段应保留
	if !strings.Contains(entry.RequestBody, `"username":"bob"`) {
		t.Errorf("username 被误修改: %q", entry.RequestBody)
	}
}

// TestLogOperation_ExtraRedactKeysApplied 验证 extraRedactKeys 参数生效
func TestLogOperation_ExtraRedactKeysApplied(t *testing.T) {
	capture := &captureLogSvc{}
	app := fiber.New()
	app.Use(cmfjwt.NewJWTMiddleware(opLogTestSecret))
	app.Post("/configs", func(c fiber.Ctx) error {
		// 动态配置的 value 字段通常也包含凭据，需要显式传入 extraRedactKeys
		logOperation(c, capture, "system", "创建", "创建配置: smtp", "value")
		return c.SendString("ok")
	})
	token := makeOpLogToken(t, "admin", "admin")
	body := `{"key":"smtp.password","value":"MailSecretPass","group":"email"}`
	req := httptest.NewRequest(http.MethodPost, "/configs", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, _ := app.Test(req)
	defer resp.Body.Close()
	io.ReadAll(resp.Body)

	_, entry := capture.captured()
	if entry == nil {
		t.Fatal("LogEntry 为 nil")
	}
	if strings.Contains(entry.RequestBody, "MailSecretPass") {
		t.Fatalf("通过 extraRedactKeys 指定的 value 字段应被脱敏: %q", entry.RequestBody)
	}
	if !strings.Contains(entry.RequestBody, `"value":"***REDACTED***"`) {
		t.Fatalf("value 替换标记缺失: %q", entry.RequestBody)
	}
}

// TestLogOperation_NoJWTClaimsStillLogs 验证无 JWT 时仍然写入日志，但用户字段为空
func TestLogOperation_NoJWTClaimsStillLogs(t *testing.T) {
	capture := &captureLogSvc{}
	app := fiber.New()
	// 不使用 JWT 中间件，模拟公开接口
	app.Post("/public-log", func(c fiber.Ctx) error {
		logOperation(c, capture, "public", "访问", "公开接口调用")
		return c.SendString("ok")
	})
	body := `{"data":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/public-log", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	defer resp.Body.Close()
	io.ReadAll(resp.Body)

	called, entry := capture.captured()
	if called != 1 {
		t.Fatalf("无 JWT 时也应写日志，调用次数=%d", called)
	}
	if entry.UserUID != "" || entry.Username != "" {
		t.Errorf("无 JWT 时用户字段应为空, 实际 UID=%q Username=%q",
			entry.UserUID, entry.Username)
	}
	if entry.Module != "public" {
		t.Errorf("Module 字段缺失: %+v", entry)
	}
}

// TestLogOperation_FormEncodingBodyPreserved 验证 form 编码请求体保留并正确脱敏
func TestLogOperation_FormEncodingBodyPreserved(t *testing.T) {
	capture := &captureLogSvc{}
	app := fiber.New()
	app.Use(cmfjwt.NewJWTMiddleware(opLogTestSecret))
	app.Post("/form", func(c fiber.Ctx) error {
		logOperation(c, capture, "auth", "表单", "表单提交")
		return c.SendString("ok")
	})
	token := makeOpLogToken(t, "u1", "x1")
	form := "username=charlie&password=MyPlainPass&submit=1"
	req := httptest.NewRequest(http.MethodPost, "/form", strings.NewReader(form))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, _ := app.Test(req)
	defer resp.Body.Close()
	io.ReadAll(resp.Body)

	_, entry := capture.captured()
	if strings.Contains(entry.RequestBody, "MyPlainPass") {
		t.Fatalf("表单编码中的 password 也应被脱敏: %q", entry.RequestBody)
	}
	if !strings.Contains(entry.RequestBody, "username=charlie") {
		t.Errorf("非敏感表单字段应保留: %q", entry.RequestBody)
	}
}

// TestLogOperation_RequestBodyTruncated 验证超大请求体被截断（不允许操作日志无限增长）
func TestLogOperation_RequestBodyTruncated(t *testing.T) {
	capture := &captureLogSvc{}
	app := fiber.New()
	app.Use(cmfjwt.NewJWTMiddleware(opLogTestSecret))
	app.Post("/huge", func(c fiber.Ctx) error {
		logOperation(c, capture, "bulk", "批量", "大批量提交")
		return c.SendString("ok")
	})
	// 构造超过 2048 字节的合法 JSON
	payload := map[string]string{}
	for i := 0; i < 300; i++ {
		payload["k"+string(rune('A'+i%26))+itoa(i)] = strings.Repeat("v", 8)
	}
	buf, _ := json.Marshal(payload)
	if len(buf) <= 2048 {
		t.Fatalf("测试构造失败: payload 太小 (%d)", len(buf))
	}
	token := makeOpLogToken(t, "a", "b")
	req := httptest.NewRequest(http.MethodPost, "/huge", strings.NewReader(string(buf)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	defer resp.Body.Close()
	io.ReadAll(resp.Body)

	_, entry := capture.captured()
	// SafeGetBody 限制在 2048 + "..." => 最多 2051 字符左右
	const maxAllowed = 2100
	if len(entry.RequestBody) > maxAllowed {
		t.Errorf("请求体应被截断，实际长度=%d 上限~%d",
			len(entry.RequestBody), maxAllowed)
	}
	if !strings.HasSuffix(entry.RequestBody, "...") {
		t.Errorf("截断后应以 '...' 结尾，实际结尾: %q",
			entry.RequestBody[len(entry.RequestBody)-min(20, len(entry.RequestBody)):])
	}
}

func itoa(i int) string {
	// ponytail: 小工具，避免 strconv 导入
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	n := i
	for n > 0 {
		pos--
		b[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(b[pos:])
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
