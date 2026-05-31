package plugin

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	pluginent "go-admin/internal/ent/plugin"

	_ "github.com/mattn/go-sqlite3"
	cmfplugin "github.com/wuwuseo/cmf/plugin"
)

func TestManagerInstallEnableDisableUninstall(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:plugin_manager?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()

	root := t.TempDir()
	pkg := writePluginPackage(t, root, "admin.example-plugin", "1.0.0")
	registry := cmfplugin.NewMemoryExtensionRegistry()
	runtime := &fakeRuntime{}
	manager := NewManager(client, Options{
		PluginRoot: filepath.Join(root, "data", "plugins"),
		TempRoot:   filepath.Join(root, "tmp"),
		Registry:   registry,
		Runtime:    runtime,
	})

	if _, err := manager.Install(ctx, pkg); err != nil {
		t.Fatalf("Install returned error: %v", err)
	}
	record, err := client.Plugin.Query().Where(pluginent.PluginIDEQ("admin.example-plugin")).Only(ctx)
	if err != nil {
		t.Fatalf("query plugin: %v", err)
	}
	if record.Status != pluginent.StatusInstalled {
		t.Fatalf("status = %s, want installed", record.Status)
	}
	if _, err := manager.Install(ctx, pkg); err != ErrAlreadyInstalled {
		t.Fatalf("duplicate install err = %v, want ErrAlreadyInstalled", err)
	}
	if err := manager.Enable(ctx, "admin.example-plugin"); err != nil {
		t.Fatalf("Enable returned error: %v", err)
	}
	if !runtime.started {
		t.Fatal("expected runtime to start")
	}
	enabled, _ := client.Plugin.Query().Where(pluginent.PluginIDEQ("admin.example-plugin")).Only(ctx)
	if enabled.Status != pluginent.StatusEnabled {
		t.Fatalf("status = %s, want enabled", enabled.Status)
	}
	if len(registry.List(cmfplugin.ExtensionAdminRoutes)) != 1 {
		t.Fatal("expected admin.routes extension to be registered")
	}
	if err := manager.Disable(ctx, "admin.example-plugin"); err != nil {
		t.Fatalf("Disable returned error: %v", err)
	}
	disabled, _ := client.Plugin.Query().Where(pluginent.PluginIDEQ("admin.example-plugin")).Only(ctx)
	if disabled.Status != pluginent.StatusDisabled {
		t.Fatalf("status = %s, want disabled", disabled.Status)
	}
	if len(registry.List(cmfplugin.ExtensionAdminRoutes)) != 0 {
		t.Fatal("expected extensions to be unregistered")
	}
	if err := manager.Uninstall(ctx, "admin.example-plugin"); err != nil {
		t.Fatalf("Uninstall returned error: %v", err)
	}
	exists, err := client.Plugin.Query().Where(pluginent.PluginIDEQ("admin.example-plugin")).Exist(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("expected plugin record to be deleted")
	}
}

func TestManagerInstallRejectsMissingPackage(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:plugin_missing?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()
	manager := NewManager(client, Options{
		PluginRoot: filepath.Join(t.TempDir(), "plugins"),
		TempRoot:   filepath.Join(t.TempDir(), "tmp"),
		Registry:   cmfplugin.NewMemoryExtensionRegistry(),
		Runtime:    &fakeRuntime{},
	})
	if _, err := manager.Install(ctx, filepath.Join(t.TempDir(), "missing.zip")); err != ErrInvalidPackage {
		t.Fatalf("err = %v, want ErrInvalidPackage", err)
	}
}

func writePluginPackage(t *testing.T, root string, id string, version string) string {
	t.Helper()
	zipPath := filepath.Join(root, id+".zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	defer zw.Close()
	files := map[string]string{
		"plugin.yaml": `id: ` + id + `
name: Example Plugin
version: ` + version + `
author: example
description: Example plugin
engine:
  type: process
  protocol: http
  entry:
    windows-amd64: bin/windows-amd64/plugin.exe
    linux-amd64: bin/linux-amd64/plugin
compatibility:
  app: ">=1.0.0 <2.0.0"
  api: "v1"
permissions:
  - route:register
extension_points:
  - admin.routes
`,
		"bin/windows-amd64/plugin.exe": "",
		"bin/linux-amd64/plugin":       "",
	}
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	return zipPath
}

type fakeRuntime struct {
	started bool
	stopped bool
}

func (f *fakeRuntime) Start(ctx context.Context, info cmfplugin.PluginInfo) error {
	f.started = true
	return nil
}

func (f *fakeRuntime) Stop(ctx context.Context, pluginID string) error {
	f.stopped = true
	return nil
}

func (f *fakeRuntime) Health(ctx context.Context, pluginID string) error {
	return nil
}
