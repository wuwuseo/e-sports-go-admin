package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
)

func TestProxyPluginForwardsPublicRequestAndWritesPluginResponse(t *testing.T) {
	pluginService := newFakePluginService()
	app := newProxyPluginTestApp(pluginService)
	app.Post("/api/v1/plugin/:plugin_id/*", ProxyPlugin)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/plugin/admin.example-plugin/api/manifest?debug=1", strings.NewReader(`{"ping":true}`))
	req.Header.Set("X-Test", "yes")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	assertProxyResponse(t, pluginService, resp, body, "/api/manifest")
}

func TestProxyPluginForwardsAdminAuthRequestAndWritesPluginResponse(t *testing.T) {
	pluginService := newFakePluginService()
	app := newProxyPluginTestApp(pluginService)
	app.Post("/api/v1/plugin/:plugin_id/admin/auth/*", func(c fiber.Ctx) error {
		c.Locals("plugin.proxy_path_prefix", "/admin/auth")
		return ProxyPlugin(c)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/plugin/admin.example-plugin/admin/auth/api/manifest?debug=1", strings.NewReader(`{"ping":true}`))
	req.Header.Set("X-Test", "yes")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	assertProxyResponse(t, pluginService, resp, body, "/admin/auth/api/manifest")
}

func TestProxyPluginRejectsAdminAuthPathWithoutAuthPrefix(t *testing.T) {
	pluginService := newFakePluginService()
	app := newProxyPluginTestApp(pluginService)
	app.Post("/api/v1/plugin/:plugin_id/*", ProxyPlugin)

	resp, err := app.Test(httptest.NewRequest(http.MethodPost, "/api/v1/plugin/admin.example-plugin/admin/auth", nil))
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if pluginService.proxyRequest != nil {
		t.Fatalf("proxy request should not be forwarded")
	}
}

func TestProxyPluginFiltersSensitiveRequestHeaders(t *testing.T) {
	pluginService := newFakePluginService()
	app := newProxyPluginTestApp(pluginService)
	app.Get("/api/v1/plugin/:plugin_id/*", ProxyPlugin)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/plugin/admin.example-plugin/manifest", nil)
	req.Header.Set("X-Test", "yes")
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Cookie", "admin_token=secret")
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	req.Header.Set("Connection", "keep-alive")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}
	defer resp.Body.Close()
	if pluginService.proxyRequest.Header.Get("X-Test") != "yes" {
		t.Fatalf("X-Test = %q, want yes", pluginService.proxyRequest.Header.Get("X-Test"))
	}
	for _, key := range []string{"Authorization", "Cookie", "X-Forwarded-For", "Connection"} {
		if got := pluginService.proxyRequest.Header.Get(key); got != "" {
			t.Fatalf("%s should be filtered, got %q", key, got)
		}
	}
}

func newFakePluginService() *fakePluginService {
	return &fakePluginService{
		proxyResponse: &service.PluginProxyResponse{
			StatusCode: http.StatusAccepted,
			Header:     http.Header{"X-Plugin": []string{"demo"}},
			Body:       []byte(`{"ok":true}`),
		},
	}
}

func newProxyPluginTestApp(pluginService *fakePluginService) *fiber.App {
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("utils.Response", utils.NewResponse())
		c.Locals("service.PluginService", pluginService)
		return c.Next()
	})
	return app
}

func assertProxyResponse(t *testing.T, pluginService *fakePluginService, resp *http.Response, body []byte, wantPath string) {
	t.Helper()
	if pluginService.proxyRequest.PluginID != "admin.example-plugin" {
		t.Fatalf("plugin id = %q, want admin.example-plugin", pluginService.proxyRequest.PluginID)
	}
	if pluginService.proxyRequest.Method != http.MethodPost {
		t.Fatalf("method = %q, want POST", pluginService.proxyRequest.Method)
	}
	if pluginService.proxyRequest.Path != wantPath {
		t.Fatalf("path = %q, want %s", pluginService.proxyRequest.Path, wantPath)
	}
	if pluginService.proxyRequest.Query != "debug=1" {
		t.Fatalf("query = %q, want debug=1", pluginService.proxyRequest.Query)
	}
	if pluginService.proxyRequest.Header.Get("X-Test") != "yes" {
		t.Fatalf("X-Test = %q, want yes", pluginService.proxyRequest.Header.Get("X-Test"))
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	if resp.Header.Get("X-Plugin") != "demo" {
		t.Fatalf("X-Plugin = %q, want demo", resp.Header.Get("X-Plugin"))
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("body = %s", string(body))
	}
}

type fakePluginService struct {
	proxyRequest  *service.PluginProxyRequest
	proxyResponse *service.PluginProxyResponse
}

func (f *fakePluginService) Install(ctx context.Context, req *service.InstallPluginRequest) (*service.PluginResponse, error) {
	return nil, nil
}
func (f *fakePluginService) Enable(ctx context.Context, pluginID string) error  { return nil }
func (f *fakePluginService) Disable(ctx context.Context, pluginID string) error { return nil }
func (f *fakePluginService) Uninstall(ctx context.Context, pluginID string) error {
	return nil
}
func (f *fakePluginService) List(ctx context.Context) ([]service.PluginResponse, error) {
	return nil, nil
}
func (f *fakePluginService) Get(ctx context.Context, pluginID string) (*service.PluginResponse, error) {
	return nil, nil
}
func (f *fakePluginService) Proxy(ctx context.Context, req *service.PluginProxyRequest) (*service.PluginProxyResponse, error) {
	f.proxyRequest = req
	return f.proxyResponse, nil
}
