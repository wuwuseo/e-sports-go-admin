package plugin

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
	if !filepath.IsAbs(record.InstallPath) {
		t.Fatalf("install path = %q, want absolute path", record.InstallPath)
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

func TestManagerInstallStoresAbsolutePathWithRelativePluginRoot(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:plugin_relative_root?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()

	root := t.TempDir()
	pkg := writePluginPackage(t, root, "admin.example-plugin", "1.0.0")
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatal(err)
		}
	}()
	manager := NewManager(client, Options{
		PluginRoot: filepath.Join("data", "plugins"),
		TempRoot:   filepath.Join("tmp"),
		Registry:   cmfplugin.NewMemoryExtensionRegistry(),
		Runtime:    &fakeRuntime{},
	})

	if _, err := manager.Install(ctx, pkg); err != nil {
		t.Fatalf("Install returned error: %v", err)
	}
	record, err := client.Plugin.Query().Where(pluginent.PluginIDEQ("admin.example-plugin")).Only(ctx)
	if err != nil {
		t.Fatalf("query plugin: %v", err)
	}
	if !filepath.IsAbs(record.InstallPath) {
		t.Fatalf("install path = %q, want absolute path", record.InstallPath)
	}
}

func TestManagerEnableRollsBackExtensionsWhenRuntimeStartFails(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:plugin_enable_failure?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()

	root := t.TempDir()
	pkg := writePluginPackage(t, root, "admin.example-plugin", "1.0.0")
	registry := cmfplugin.NewMemoryExtensionRegistry()
	manager := NewManager(client, Options{
		PluginRoot: filepath.Join(root, "data", "plugins"),
		TempRoot:   filepath.Join(root, "tmp"),
		Registry:   registry,
		Runtime:    &fakeRuntime{startErr: errors.New("boom")},
	})
	if _, err := manager.Install(ctx, pkg); err != nil {
		t.Fatalf("Install returned error: %v", err)
	}

	if err := manager.Enable(ctx, "admin.example-plugin"); err != ErrEnableFailed {
		t.Fatalf("Enable err = %v, want ErrEnableFailed", err)
	}
	if len(registry.List(cmfplugin.ExtensionAdminRoutes)) != 0 {
		t.Fatal("expected extensions to be rolled back after failed enable")
	}
	record, err := client.Plugin.Query().Where(pluginent.PluginIDEQ("admin.example-plugin")).Only(ctx)
	if err != nil {
		t.Fatalf("query plugin: %v", err)
	}
	if record.Status != pluginent.StatusError {
		t.Fatalf("status = %s, want error", record.Status)
	}
}

func TestManagerRestoreEnabledStartsRuntimeAndRegistersExtensions(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:plugin_restore?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()

	root := t.TempDir()
	pkg := writePluginPackage(t, root, "admin.example-plugin", "1.0.0")
	manager := NewManager(client, Options{
		PluginRoot: filepath.Join(root, "data", "plugins"),
		TempRoot:   filepath.Join(root, "tmp"),
		Registry:   cmfplugin.NewMemoryExtensionRegistry(),
		Runtime:    &fakeRuntime{},
	})
	if _, err := manager.Install(ctx, pkg); err != nil {
		t.Fatalf("Install returned error: %v", err)
	}
	if err := manager.Enable(ctx, "admin.example-plugin"); err != nil {
		t.Fatalf("Enable returned error: %v", err)
	}

	registry := cmfplugin.NewMemoryExtensionRegistry()
	runtime := &fakeRuntime{}
	restoredManager := NewManager(client, Options{
		PluginRoot: filepath.Join(root, "data", "plugins"),
		TempRoot:   filepath.Join(root, "tmp"),
		Registry:   registry,
		Runtime:    runtime,
	})
	if err := restoredManager.RestoreEnabled(ctx); err != nil {
		t.Fatalf("RestoreEnabled returned error: %v", err)
	}
	if !runtime.started {
		t.Fatal("expected enabled plugin runtime to start during restore")
	}
	if len(registry.List(cmfplugin.ExtensionAdminRoutes)) != 1 {
		t.Fatal("expected enabled plugin extensions to be registered during restore")
	}
}

func TestManagerEnableRestartsEnabledPluginWhenRuntimeIsNotRunning(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:plugin_enabled_runtime_missing?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()

	root := t.TempDir()
	pkg := writePluginPackage(t, root, "admin.example-plugin", "1.0.0")
	manager := NewManager(client, Options{
		PluginRoot: filepath.Join(root, "data", "plugins"),
		TempRoot:   filepath.Join(root, "tmp"),
		Registry:   cmfplugin.NewMemoryExtensionRegistry(),
		Runtime:    &fakeRuntime{},
	})
	if _, err := manager.Install(ctx, pkg); err != nil {
		t.Fatalf("Install returned error: %v", err)
	}
	record, err := client.Plugin.Query().Where(pluginent.PluginIDEQ("admin.example-plugin")).Only(ctx)
	if err != nil {
		t.Fatalf("query plugin: %v", err)
	}
	if _, err := client.Plugin.UpdateOne(record).SetStatus(pluginent.StatusEnabled).Save(ctx); err != nil {
		t.Fatalf("mark plugin enabled: %v", err)
	}

	registry := cmfplugin.NewMemoryExtensionRegistry()
	runtime := &fakeRuntime{healthErr: cmfplugin.ErrRuntimeNotStarted}
	restoredManager := NewManager(client, Options{
		PluginRoot: filepath.Join(root, "data", "plugins"),
		TempRoot:   filepath.Join(root, "tmp"),
		Registry:   registry,
		Runtime:    runtime,
	})
	if err := restoredManager.Enable(ctx, "admin.example-plugin"); err != nil {
		t.Fatalf("Enable returned error: %v", err)
	}
	if !runtime.started {
		t.Fatal("expected Enable to start an enabled plugin when runtime is missing")
	}
	if len(registry.List(cmfplugin.ExtensionAdminRoutes)) != 1 {
		t.Fatal("expected extensions to be registered when enabled plugin is restarted")
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

func TestManagerResolveEndpointReturnsTCPAddressForEnabledPlugin(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:plugin_endpoint_tcp?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()
	root := t.TempDir()
	pkg := writePluginPackageWithEngine(t, root, "admin.example-plugin", "1.0.0", `  transport:
    `+runtime.GOOS+`-`+runtime.GOARCH+`: tcp
  address:
    `+runtime.GOOS+`-`+runtime.GOARCH+`: 127.0.0.1:19090
  entry:
    `+runtime.GOOS+`-`+runtime.GOARCH+`: bin/plugin
`)
	manager := NewManager(client, Options{
		PluginRoot: filepath.Join(root, "data", "plugins"),
		TempRoot:   filepath.Join(root, "tmp"),
		Registry:   cmfplugin.NewMemoryExtensionRegistry(),
		Runtime:    &fakeRuntime{},
	})
	if _, err := manager.Install(ctx, pkg); err != nil {
		t.Fatalf("Install returned error: %v", err)
	}
	if err := manager.Enable(ctx, "admin.example-plugin"); err != nil {
		t.Fatalf("Enable returned error: %v", err)
	}
	endpoint, err := manager.ResolveEndpoint(ctx, "admin.example-plugin")
	if err != nil {
		t.Fatalf("ResolveEndpoint returned error: %v", err)
	}
	if endpoint.Transport != cmfplugin.TransportTCP {
		t.Fatalf("transport = %q, want tcp", endpoint.Transport)
	}
	if endpoint.Address != "127.0.0.1:19090" {
		t.Fatalf("address = %q, want 127.0.0.1:19090", endpoint.Address)
	}
}

func TestManagerResolveEndpointRejectsDisabledPlugin(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:plugin_endpoint_disabled?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()
	root := t.TempDir()
	pkg := writePluginPackage(t, root, "admin.example-plugin", "1.0.0")
	manager := NewManager(client, Options{
		PluginRoot: filepath.Join(root, "data", "plugins"),
		TempRoot:   filepath.Join(root, "tmp"),
		Registry:   cmfplugin.NewMemoryExtensionRegistry(),
		Runtime:    &fakeRuntime{},
	})
	if _, err := manager.Install(ctx, pkg); err != nil {
		t.Fatalf("Install returned error: %v", err)
	}
	if _, err := manager.ResolveEndpoint(ctx, "admin.example-plugin"); !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("ResolveEndpoint err = %v, want ErrNotEnabled", err)
	}
}

func TestManagerResolveEndpointReturnsUnixSocketInsideInstallPath(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:plugin_endpoint_unix?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()
	root := t.TempDir()
	pkg := writePluginPackageWithEngine(t, root, "admin.example-plugin", "1.0.0", `  transport:
    `+runtime.GOOS+`-`+runtime.GOARCH+`: unix
  socket:
    `+runtime.GOOS+`-`+runtime.GOARCH+`: runtime/plugin.sock
  entry:
    `+runtime.GOOS+`-`+runtime.GOARCH+`: bin/plugin
`)
	manager := NewManager(client, Options{
		PluginRoot: filepath.Join(root, "data", "plugins"),
		TempRoot:   filepath.Join(root, "tmp"),
		Registry:   cmfplugin.NewMemoryExtensionRegistry(),
		Runtime:    &fakeRuntime{},
	})
	if _, err := manager.Install(ctx, pkg); err != nil {
		t.Fatalf("Install returned error: %v", err)
	}
	if err := manager.Enable(ctx, "admin.example-plugin"); err != nil {
		t.Fatalf("Enable returned error: %v", err)
	}
	endpoint, err := manager.ResolveEndpoint(ctx, "admin.example-plugin")
	if err != nil {
		t.Fatalf("ResolveEndpoint returned error: %v", err)
	}
	wantSocket := filepath.Join(root, "data", "plugins", "admin.example-plugin", "1.0.0", "runtime", "plugin.sock")
	if endpoint.Transport != cmfplugin.TransportUnix {
		t.Fatalf("transport = %q, want unix", endpoint.Transport)
	}
	if endpoint.Socket != wantSocket {
		t.Fatalf("socket = %q, want %q", endpoint.Socket, wantSocket)
	}
}

func writePluginPackage(t *testing.T, root string, id string, version string) string {
	t.Helper()
	return writePluginPackageWithEngine(t, root, id, version, `  entry:
    windows-amd64: bin/windows-amd64/plugin.exe
    linux-amd64: bin/linux-amd64/plugin
`)
}

func writePluginPackageWithEngine(t *testing.T, root string, id string, version string, engineBody string) string {
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
` + engineBody + `compatibility:
  app: ">=1.0.0 <2.0.0"
  api: "v1"
permissions:
  - route:register
extension_points:
  - admin.routes
`,
		"bin/plugin":                   "",
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
	started   bool
	stopped   bool
	startErr  error
	stopErr   error
	healthErr error
}

func (f *fakeRuntime) Start(ctx context.Context, info cmfplugin.PluginInfo) error {
	f.started = true
	if f.startErr == nil {
		f.healthErr = nil
	}
	return f.startErr
}

func (f *fakeRuntime) Stop(ctx context.Context, pluginID string) error {
	f.stopped = true
	return f.stopErr
}

func (f *fakeRuntime) Health(ctx context.Context, pluginID string) error {
	return f.healthErr
}
