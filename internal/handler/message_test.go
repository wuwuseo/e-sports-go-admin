package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/golang-jwt/jwt/v5"
	cmfjwt "github.com/wuwuseo/cmf/jwt"
)

const messageHandlerTestSecret = "message-handler-test-secret"

func TestBroadcastMessage_AllowsRBACAuthorizedNonSuperAdmin(t *testing.T) {
	broadcastService := &fakeMessageBroadcastService{
		response: &service.BroadcastResponse{Delivered: 3},
	}
	app := newBroadcastMessageTestApp(broadcastService)

	token := newMessageHandlerTestToken(t, jwt.MapClaims{
		"id":     "tttt",
		"domain": "default",
		"exp":    time.Now().Add(time.Hour).Unix(),
	})

	body := `{
		"type": "system",
		"level": "info",
		"title": "notice",
		"content": "hello",
		"target": {"scope": "all"}
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/messages/broadcast", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", resp.StatusCode, string(respBody))
	}

	var decoded utils.Response
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if decoded.Code != global.ResponseSuccessCode {
		t.Fatalf("response code = %d, want %d, body = %s", decoded.Code, global.ResponseSuccessCode, string(respBody))
	}
	if !broadcastService.called {
		t.Fatal("broadcast service was not called")
	}
	if broadcastService.domain != "default" {
		t.Fatalf("domain = %q, want default", broadcastService.domain)
	}
	if broadcastService.request == nil || broadcastService.request.Target.Scope != service.BroadcastScopeAll {
		t.Fatalf("request target scope = %#v, want all", broadcastService.request)
	}
}

func newBroadcastMessageTestApp(broadcastService *fakeMessageBroadcastService) *fiber.App {
	app := fiber.New()
	app.Use(cmfjwt.NewJWTMiddleware(messageHandlerTestSecret))
	app.Use(func(c fiber.Ctx) error {
		c.Locals("utils.Response", utils.NewResponse())
		c.Locals("domain", "default")
		c.Locals("isSuperAdmin", false)
		c.Locals("service.MessageBroadcastService", broadcastService)
		return c.Next()
	})
	app.Post("/api/v1/admin/auth/messages/broadcast", BroadcastMessage)
	return app
}

func newMessageHandlerTestToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	token, err := cmfjwt.CreateToken(claims, messageHandlerTestSecret)
	if err != nil {
		t.Fatalf("create token failed: %v", err)
	}
	return token
}

type fakeMessageBroadcastService struct {
	called   bool
	domain   string
	request  *service.BroadcastRequest
	response *service.BroadcastResponse
	err      error
}

func (f *fakeMessageBroadcastService) Broadcast(ctx context.Context, domain string, req *service.BroadcastRequest) (*service.BroadcastResponse, error) {
	f.called = true
	f.domain = domain
	f.request = req
	return f.response, f.err
}
