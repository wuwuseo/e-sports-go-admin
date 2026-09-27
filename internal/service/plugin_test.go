package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	adminplugin "go-admin/internal/plugin"
	"go-admin/pluginapi"

	cmfplugin "github.com/wuwuseo/cmf/plugin"
)

func TestPluginServiceInstallDelegatesToManager(t *testing.T) {
	manager := &fakePluginManager{diagnostics: &adminplugin.Diagnostics{ErrorMessage: "last crash", Restarting: true}}
	service := NewPluginService(manager)
	resp, err := service.Install(context.Background(), &InstallPluginRequest{PackagePath: "D:/tmp/plugin.zip"})
	if err != nil {
		t.Fatalf("Install returned error: %v", err)
	}
	if manager.installedPath != "D:/tmp/plugin.zip" {
		t.Fatalf("installed path = %q", manager.installedPath)
	}
	if resp.ID != "admin.example-plugin" {
		t.Fatalf("response ID = %q", resp.ID)
	}
	if resp.ErrorMessage != "last crash" || !resp.Restarting {
		t.Fatalf("diagnostics were not included: %+v", resp)
	}
}

func TestPluginServiceProxyForwardsTCPRequest(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/manifest" {
			t.Fatalf("path = %s, want /manifest", r.URL.Path)
		}
		if r.URL.RawQuery != "debug=1" {
			t.Fatalf("query = %s, want debug=1", r.URL.RawQuery)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != `{"ping":true}` {
			t.Fatalf("body = %s", string(body))
		}
		if r.Header.Get("X-Test") != "yes" {
			t.Fatalf("X-Test = %q, want yes", r.Header.Get("X-Test"))
		}
		w.Header().Set("X-Plugin", "demo")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer backend.Close()
	address := strings.TrimPrefix(backend.URL, "http://")
	manager := &fakePluginManager{endpoint: &adminplugin.Endpoint{
		PluginID:  "admin.example-plugin",
		Protocol:  cmfplugin.ProtocolHTTP,
		Transport: cmfplugin.TransportTCP,
		Address:   address,
	}}
	service := NewPluginService(manager)
	resp, err := service.Proxy(context.Background(), &PluginProxyRequest{
		PluginID: "admin.example-plugin",
		Method:   http.MethodPost,
		Path:     "/manifest",
		Query:    "debug=1",
		Header:   http.Header{"X-Test": []string{"yes"}},
		Body:     strings.NewReader(`{"ping":true}`),
	})
	if err != nil {
		t.Fatalf("Proxy returned error: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	if resp.Header.Get("X-Plugin") != "demo" {
		t.Fatalf("X-Plugin = %q, want demo", resp.Header.Get("X-Plugin"))
	}
	if string(resp.Body) != `{"ok":true}` {
		t.Fatalf("body = %s", string(resp.Body))
	}
}

func TestPluginServiceProxyPassesOnlyTrustedUserIdentity(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(pluginapi.UserIDHeader); got != "trusted-user" {
			t.Errorf("user ID = %q, want trusted-user", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()
	manager := &fakePluginManager{endpoint: &adminplugin.Endpoint{
		PluginID: "admin.example-plugin", Protocol: cmfplugin.ProtocolHTTP,
		Transport: cmfplugin.TransportTCP, Address: strings.TrimPrefix(backend.URL, "http://"),
	}}
	pluginService := NewPluginService(manager)
	_, err := pluginService.Proxy(context.Background(), &PluginProxyRequest{
		PluginID: "admin.example-plugin", Point: pluginapi.ExtensionUserRoutes, UserID: "trusted-user",
		Method: http.MethodGet, Path: "/user/auth/profile",
		Header: http.Header{"x-go-admin-plugin-user-id": []string{"spoofed-user"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if manager.authorizePoint != pluginapi.ExtensionUserRoutes {
		t.Fatalf("authorized point = %q", manager.authorizePoint)
	}
	_, err = pluginService.Proxy(context.Background(), &PluginProxyRequest{
		PluginID: "admin.example-plugin", Point: pluginapi.ExtensionUserRoutes, UserID: "trusted-user",
		Method: http.MethodGet, Path: "/user/auth/profile",
	})
	if err != nil {
		t.Fatalf("user request without client headers: %v", err)
	}
	_, err = pluginService.Proxy(context.Background(), &PluginProxyRequest{
		PluginID: "admin.example-plugin", Point: pluginapi.ExtensionUserRoutes,
		Method: http.MethodGet, Path: "/user/auth/profile",
	})
	if !errors.Is(err, adminplugin.ErrPermissionDenied) {
		t.Fatalf("missing trusted user ID error = %v", err)
	}
}

func TestPluginServiceProxyInvokesGRPCAndStdio(t *testing.T) {
	for _, protocol := range []string{cmfplugin.ProtocolGRPC, cmfplugin.ProtocolStdio} {
		t.Run(protocol, func(t *testing.T) {
			manager := &fakePluginManager{
				endpoint: &adminplugin.Endpoint{PluginID: "admin.example-plugin", Protocol: protocol},
				invokeResponse: &pluginapi.ProxyResponse{
					StatusCode: http.StatusCreated,
					Headers:    []*pluginapi.Header{{Name: "X-Plugin", Values: []string{"demo"}}},
					Body:       []byte(`{"ok":true}`),
				},
			}
			pluginService := NewPluginService(manager)
			response, err := pluginService.Proxy(context.Background(), &PluginProxyRequest{
				PluginID: "admin.example-plugin",
				Method:   http.MethodPost,
				Path:     "manifest",
				Query:    "debug=1",
				Header:   http.Header{"X-Test": []string{"yes"}},
				Body:     strings.NewReader(`{"ping":true}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			if manager.invokeRequest.GetPath() != "/manifest" || manager.invokeRequest.GetQuery() != "debug=1" {
				t.Fatalf("invoke request = %+v", manager.invokeRequest)
			}
			if string(manager.invokeRequest.GetBody()) != `{"ping":true}` {
				t.Fatalf("invoke body = %q", manager.invokeRequest.GetBody())
			}
			if !manager.invokeHasDeadline {
				t.Fatal("plugin invocation did not receive a deadline")
			}
			if response.StatusCode != http.StatusCreated || response.Header.Get("X-Plugin") != "demo" || string(response.Body) != `{"ok":true}` {
				t.Fatalf("proxy response = %+v", response)
			}
		})
	}
}

func TestPluginServiceProxyRejectsUndeclaredCapabilityBeforeResolvingEndpoint(t *testing.T) {
	manager := &fakePluginManager{
		authorizeErr: adminplugin.ErrPermissionDenied,
		endpoint:     &adminplugin.Endpoint{PluginID: "admin.example-plugin", Protocol: cmfplugin.ProtocolHTTP},
	}
	service := NewPluginService(manager)
	_, err := service.Proxy(context.Background(), &PluginProxyRequest{
		PluginID: "admin.example-plugin", Method: http.MethodDelete, Path: "/undeclared",
	})
	if !errors.Is(err, adminplugin.ErrPermissionDenied) {
		t.Fatalf("Proxy error = %v, want ErrPermissionDenied", err)
	}
	if manager.resolveCalled {
		t.Fatal("endpoint was resolved before capability authorization")
	}
}

func TestPluginServiceProxyRejectsEncodedRuntimeControlPath(t *testing.T) {
	manager := &fakePluginManager{
		endpoint: &adminplugin.Endpoint{PluginID: "admin.example-plugin", Protocol: cmfplugin.ProtocolHTTP},
	}
	service := NewPluginService(manager)
	for _, path := range []string{"/health", "/_go-admin/shutdown", "/%5fgo-admin%2fshutdown", "/%255fgo-admin/shutdown"} {
		_, err := service.Proxy(context.Background(), &PluginProxyRequest{
			PluginID: "admin.example-plugin", Method: http.MethodPost, Path: path,
		})
		if !errors.Is(err, adminplugin.ErrPermissionDenied) {
			t.Fatalf("Proxy(%q) error = %v, want ErrPermissionDenied", path, err)
		}
	}
	if manager.resolveCalled {
		t.Fatal("runtime control path reached endpoint resolution")
	}
}

type fakePluginManager struct {
	installedPath     string
	endpoint          *adminplugin.Endpoint
	invokeRequest     *pluginapi.ProxyRequest
	invokeResponse    *pluginapi.ProxyResponse
	invokeErr         error
	invokeHasDeadline bool
	authorizeErr      error
	authorizePoint    string
	resolveCalled     bool
	diagnostics       *adminplugin.Diagnostics
	diagnosticsErr    error
}

func (*fakePluginManager) Close(context.Context) error { return nil }

func (f *fakePluginManager) Diagnostics(context.Context, string) (*adminplugin.Diagnostics, error) {
	if f.diagnostics == nil {
		f.diagnostics = &adminplugin.Diagnostics{}
	}
	return f.diagnostics, f.diagnosticsErr
}

func (f *fakePluginManager) Install(ctx context.Context, packagePath string) (*cmfplugin.PluginInfo, error) {
	f.installedPath = packagePath
	return samplePluginInfo(), nil
}
func (f *fakePluginManager) Enable(ctx context.Context, pluginID string) error    { return nil }
func (f *fakePluginManager) Disable(ctx context.Context, pluginID string) error   { return nil }
func (f *fakePluginManager) Uninstall(ctx context.Context, pluginID string) error { return nil }
func (f *fakePluginManager) List(ctx context.Context) ([]cmfplugin.PluginInfo, error) {
	return []cmfplugin.PluginInfo{*samplePluginInfo()}, nil
}
func (f *fakePluginManager) Get(ctx context.Context, pluginID string) (*cmfplugin.PluginInfo, error) {
	if pluginID == "missing" {
		return nil, adminplugin.ErrNotFound
	}
	return samplePluginInfo(), nil
}
func (f *fakePluginManager) Authorize(_ context.Context, _ string, point string, _ string, _ string) error {
	f.authorizePoint = point
	return f.authorizeErr
}
func (f *fakePluginManager) ResolveEndpoint(ctx context.Context, pluginID string) (*adminplugin.Endpoint, error) {
	f.resolveCalled = true
	if f.endpoint == nil {
		return nil, adminplugin.ErrNotFound
	}
	return f.endpoint, nil
}
func (f *fakePluginManager) Invoke(ctx context.Context, pluginID string, request *pluginapi.ProxyRequest) (*pluginapi.ProxyResponse, error) {
	f.invokeRequest = request
	_, f.invokeHasDeadline = ctx.Deadline()
	return f.invokeResponse, f.invokeErr
}

func samplePluginInfo() *cmfplugin.PluginInfo {
	return &cmfplugin.PluginInfo{
		ID:          "admin.example-plugin",
		Name:        "Example Plugin",
		Version:     "1.0.0",
		Author:      "example",
		Description: "Example",
		Status:      cmfplugin.PluginStatusInstalled,
		InstallPath: "data/plugins/admin.example-plugin/1.0.0",
	}
}
