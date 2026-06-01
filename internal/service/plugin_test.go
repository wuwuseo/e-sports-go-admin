package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	adminplugin "go-admin/internal/plugin"

	cmfplugin "github.com/wuwuseo/cmf/plugin"
)

func TestPluginServiceInstallDelegatesToManager(t *testing.T) {
	manager := &fakePluginManager{}
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

type fakePluginManager struct {
	installedPath string
	endpoint      *adminplugin.Endpoint
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
func (f *fakePluginManager) ResolveEndpoint(ctx context.Context, pluginID string) (*adminplugin.Endpoint, error) {
	if f.endpoint == nil {
		return nil, adminplugin.ErrNotFound
	}
	return f.endpoint, nil
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
