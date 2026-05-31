package service

import (
	"context"
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

type fakePluginManager struct {
	installedPath string
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
