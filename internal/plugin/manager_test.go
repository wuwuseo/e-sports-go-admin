package plugin

import (
	"archive/zip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	pluginent "go-admin/internal/ent/plugin"
	"go-admin/pluginapi"

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
		PluginRoot:  filepath.Join(root, "data", "plugins"),
		PackageRoot: root,
		TempRoot:    filepath.Join(root, "tmp"),
		Registry:    registry,
		Runtime:     runtime,
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
	extensions := registry.List(cmfplugin.ExtensionAdminRoutes)
	if len(extensions) != 1 {
		t.Fatal("expected admin.routes extension to be registered")
	}
	if _, ok := extensions[0].Payload.([]pluginapi.AdminRouteContribution); !ok {
		t.Fatalf("route extension payload = %T, want typed routes", extensions[0].Payload)
	}
	if err := manager.Authorize(ctx, "admin.example-plugin", cmfplugin.ExtensionAdminRoutes, "GET", "/manifest"); err != nil {
		t.Fatalf("Authorize declared wildcard route: %v", err)
	}
	if err := manager.Authorize(ctx, "admin.example-plugin", pluginapi.ExtensionPublicRoutes, "GET", "/public/manifest"); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("admin wildcard must not authorize public request: %v", err)
	}
	if err := manager.Authorize(ctx, "admin.example-plugin", pluginapi.ExtensionUserRoutes, "GET", "/user/auth/manifest"); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("admin wildcard must not authorize user request: %v", err)
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

func TestManagerAuthorizeSeparatesRouteAudiences(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:plugin_route_audiences?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer client.Close()
	registry := cmfplugin.NewMemoryExtensionRegistry()
	manager := NewManager(client, Options{Registry: registry, Runtime: &fakeRuntime{}})
	_, err := client.Plugin.Create().SetPluginID("example.routes").SetName("Routes").
		SetVersion("1.0.0").SetStatus(pluginent.StatusEnabled).SetInstallPath(t.TempDir()).
		SetManifestJSON(map[string]any{}).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for point, routes := range map[string][]pluginapi.AdminRouteContribution{
		cmfplugin.ExtensionAdminRoutes:  {{Method: "*", Path: "/*"}},
		pluginapi.ExtensionPublicRoutes: {{Method: "GET", Path: "/public/feed"}},
		pluginapi.ExtensionUserRoutes:   {{Method: "POST", Path: "/user/auth/orders"}},
	} {
		if err := registry.Register("example.routes", point, routes); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		point, method, path string
		want                bool
	}{
		{pluginapi.ExtensionPublicRoutes, "GET", "/public/feed", true},
		{pluginapi.ExtensionPublicRoutes, "POST", "/public/feed", false},
		{pluginapi.ExtensionPublicRoutes, "GET", "/admin/auth/secret", false},
		{pluginapi.ExtensionPublicRoutes, "GET", "/public/%2e%2e/admin/auth/secret", false},
		{pluginapi.ExtensionPublicRoutes, "GET", "/public/%252e%252e/admin/auth/secret", false},
		{pluginapi.ExtensionUserRoutes, "POST", "/user/auth/orders", true},
		{pluginapi.ExtensionUserRoutes, "GET", "/public/feed", false},
		{cmfplugin.ExtensionAdminRoutes, "GET", "/admin/auth/secret", true},
		{"unknown.routes", "GET", "/public/feed", false},
	} {
		err := manager.Authorize(ctx, "example.routes", test.point, test.method, test.path)
		if (err == nil) != test.want {
			t.Fatalf("Authorize(%q, %q, %q) = %v, want allowed %v", test.point, test.method, test.path, err, test.want)
		}
	}
}

func TestManagerUpgradesEnabledPluginWithoutLosingAvailabilityState(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:plugin_upgrade_success?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()

	root := t.TempDir()
	runtimeManager := &versionRuntime{starts: make(chan string, 4), stops: make(chan struct{}, 2)}
	manager := NewManager(client, Options{
		PluginRoot:  filepath.Join(root, "data", "plugins"),
		PackageRoot: root,
		TempRoot:    filepath.Join(root, "tmp"),
		Registry:    cmfplugin.NewMemoryExtensionRegistry(),
		Runtime:     runtimeManager,
	})
	if _, err := manager.Install(ctx, writePluginPackage(t, root, "admin.example-plugin", "1.0.0")); err != nil {
		t.Fatal(err)
	}
	if err := manager.Enable(ctx, "admin.example-plugin"); err != nil {
		t.Fatal(err)
	}
	waitForRuntimeVersion(t, runtimeManager.starts, "1.0.0")

	upgraded, err := manager.Install(ctx, writePluginPackage(t, root, "admin.example-plugin", "2.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	waitForRuntimeSignal(t, runtimeManager.stops, "old runtime stop")
	waitForRuntimeVersion(t, runtimeManager.starts, "2.0.0")
	if upgraded.Version != "2.0.0" || upgraded.Status != cmfplugin.PluginStatusEnabled {
		t.Fatalf("upgraded plugin = version %s status %s", upgraded.Version, upgraded.Status)
	}
	for _, version := range []string{"1.0.0", "2.0.0"} {
		if _, err := os.Stat(filepath.Join(root, "data", "plugins", "admin.example-plugin", version)); err != nil {
			t.Fatalf("version %s directory: %v", version, err)
		}
	}
	if err := manager.Uninstall(ctx, "admin.example-plugin"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "data", "plugins", "admin.example-plugin")); !os.IsNotExist(err) {
		t.Fatalf("plugin version root still exists after uninstall: %v", err)
	}
}

func TestManagerRollsBackFailedEnabledPluginUpgrade(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:plugin_upgrade_rollback?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()

	root := t.TempDir()
	runtimeManager := &versionRuntime{starts: make(chan string, 4), stops: make(chan struct{}, 2), failVersion: "2.0.0"}
	manager := NewManager(client, Options{
		PluginRoot:  filepath.Join(root, "data", "plugins"),
		PackageRoot: root,
		TempRoot:    filepath.Join(root, "tmp"),
		Registry:    cmfplugin.NewMemoryExtensionRegistry(),
		Runtime:     runtimeManager,
	})
	if _, err := manager.Install(ctx, writePluginPackage(t, root, "admin.example-plugin", "1.0.0")); err != nil {
		t.Fatal(err)
	}
	if err := manager.Enable(ctx, "admin.example-plugin"); err != nil {
		t.Fatal(err)
	}
	waitForRuntimeVersion(t, runtimeManager.starts, "1.0.0")

	_, err := manager.Install(ctx, writePluginPackage(t, root, "admin.example-plugin", "2.0.0"))
	if !errors.Is(err, ErrUpgradeFailed) {
		t.Fatalf("upgrade error = %v, want ErrUpgradeFailed", err)
	}
	waitForRuntimeSignal(t, runtimeManager.stops, "old runtime stop")
	waitForRuntimeVersion(t, runtimeManager.starts, "2.0.0")
	waitForRuntimeVersion(t, runtimeManager.starts, "1.0.0")
	record, err := client.Plugin.Query().Where(pluginent.PluginIDEQ("admin.example-plugin")).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if record.Version != "1.0.0" || record.Status != pluginent.StatusEnabled {
		t.Fatalf("rolled back plugin = version %s status %s", record.Version, record.Status)
	}
	if _, err := os.Stat(filepath.Join(root, "data", "plugins", "admin.example-plugin", "2.0.0")); !os.IsNotExist(err) {
		t.Fatalf("failed version directory was not removed: %v", err)
	}
}

func TestManagerRequiresSignaturesAcrossVersionUpgrade(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:plugin_signed_upgrade?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	manager := NewManager(client, Options{
		PluginRoot:        filepath.Join(root, "data", "plugins"),
		PackageRoot:       root,
		TempRoot:          filepath.Join(root, "tmp"),
		RequireSignature:  true,
		TrustedPublicKeys: []ed25519.PublicKey{publicKey},
		Registry:          cmfplugin.NewMemoryExtensionRegistry(),
		Runtime:           &fakeRuntime{},
	})
	versionOne := writePluginPackage(t, root, "admin.example-plugin", "1.0.0")
	signPluginPackage(t, versionOne, privateKey)
	if _, err := manager.Install(ctx, versionOne); err != nil {
		t.Fatal(err)
	}
	versionTwo := writePluginPackage(t, root, "admin.example-plugin", "2.0.0")
	signPluginPackage(t, versionTwo, privateKey)
	upgraded, err := manager.Install(ctx, versionTwo)
	if err != nil {
		t.Fatal(err)
	}
	if upgraded.Version != "2.0.0" {
		t.Fatalf("upgraded version = %q, want 2.0.0", upgraded.Version)
	}
	unsigned := writePluginPackage(t, root, "admin.example-plugin", "3.0.0")
	if err := os.Remove(unsigned + signatureFileSuffix); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := manager.Install(ctx, unsigned); !errors.Is(err, ErrInvalidPackage) {
		t.Fatalf("unsigned upgrade error = %v, want ErrInvalidPackage", err)
	}
	record, err := client.Plugin.Query().Where(pluginent.PluginIDEQ("admin.example-plugin")).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if record.Version != "2.0.0" {
		t.Fatalf("unsigned upgrade changed active version to %q", record.Version)
	}
}

func TestManagerRunsSignedInstalledProcessEndToEnd(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:plugin_process_e2e?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	pkg := writeExecutablePluginPackage(t, root, executable)
	signPluginPackage(t, pkg, privateKey)
	if err := validatePluginArchive(pkg); err != nil {
		t.Fatalf("validate end-to-end package: %v", err)
	}
	if _, _, err := verifyPluginPackage(pkg, pkg+signatureFileSuffix, Options{
		PackageRoot: root, RequireSignature: true, TrustedPublicKeys: []ed25519.PublicKey{publicKey},
	}); err != nil {
		t.Fatalf("verify end-to-end package: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	manager := NewManager(client, Options{
		PluginRoot:        filepath.Join(root, "plugins"),
		PackageRoot:       root,
		TempRoot:          filepath.Join(root, "runtime"),
		RequireSignature:  true,
		TrustedPublicKeys: []ed25519.PublicKey{publicKey},
	})
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		if err := manager.Close(closeCtx); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	info, err := manager.Install(ctx, pkg)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Enable(ctx, info.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.Authorize(ctx, info.ID, cmfplugin.ExtensionAdminRoutes, http.MethodPost, "/echo"); err != nil {
		t.Fatal(err)
	}
	response, err := manager.Invoke(ctx, info.ID, &pluginapi.ProxyRequest{
		Method: http.MethodPost, Path: "/echo", Query: "e2e=1", Body: []byte("signed"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.GetStatusCode() != http.StatusCreated || string(response.GetBody()) != "POST /echo?e2e=1 signed" {
		t.Fatalf("response = %d %q", response.GetStatusCode(), response.GetBody())
	}
}

func TestNewManagerUsesProtocolRuntimeByDefault(t *testing.T) {
	manager := NewManager(nil, Options{})
	if _, ok := manager.opts.Runtime.(*ProcessRuntime); !ok {
		t.Fatalf("default runtime = %T, want *ProcessRuntime", manager.opts.Runtime)
	}
}

func TestManagerEnableRejectsInstallPathOutsidePluginRoot(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:plugin_install_path_escape?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()

	root := t.TempDir()
	pkg := writePluginPackage(t, root, "admin.example-plugin", "1.0.0")
	runtime := &fakeRuntime{}
	manager := NewManager(client, Options{
		PluginRoot: filepath.Join(root, "plugins"), PackageRoot: root,
		Registry: cmfplugin.NewMemoryExtensionRegistry(), Runtime: runtime,
	})
	if _, err := manager.Install(ctx, pkg); err != nil {
		t.Fatal(err)
	}
	record, err := client.Plugin.Query().Where(pluginent.PluginIDEQ("admin.example-plugin")).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Plugin.UpdateOne(record).SetInstallPath(t.TempDir()).Save(ctx); err != nil {
		t.Fatal(err)
	}
	if err := manager.Enable(ctx, "admin.example-plugin"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("Enable error = %v, want ErrInvalidPath", err)
	}
	if runtime.started {
		t.Fatal("runtime started for an install path outside PluginRoot")
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
		PluginRoot:  filepath.Join("data", "plugins"),
		PackageRoot: root,
		TempRoot:    filepath.Join("tmp"),
		Registry:    cmfplugin.NewMemoryExtensionRegistry(),
		Runtime:     &fakeRuntime{},
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
		PluginRoot:  filepath.Join(root, "data", "plugins"),
		PackageRoot: root,
		TempRoot:    filepath.Join(root, "tmp"),
		Registry:    registry,
		Runtime:     &fakeRuntime{startErr: errors.New("boom")},
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

func TestManagerEnableRejectsModifiedInstalledFiles(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:plugin_integrity_failure?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()

	root := t.TempDir()
	pkg := writePluginPackage(t, root, "admin.example-plugin", "1.0.0")
	runtimeManager := &fakeRuntime{}
	manager := NewManager(client, Options{
		PluginRoot:  filepath.Join(root, "data", "plugins"),
		PackageRoot: root,
		TempRoot:    filepath.Join(root, "tmp"),
		Registry:    cmfplugin.NewMemoryExtensionRegistry(),
		Runtime:     runtimeManager,
	})
	info, err := manager.Install(ctx, pkg)
	if err != nil {
		t.Fatal(err)
	}
	entry := info.Manifest.Engine.Entry[runtime.GOOS+"-"+runtime.GOARCH]
	if err := os.WriteFile(filepath.Join(info.InstallPath, entry), []byte("tampered"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := manager.Enable(ctx, info.ID); !errors.Is(err, ErrEnableFailed) {
		t.Fatalf("Enable error = %v, want ErrEnableFailed", err)
	}
	if runtimeManager.started {
		t.Fatal("runtime started after installed files were modified")
	}
}

func TestManagerEnableRejectsUnexpectedInstalledFiles(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:plugin_integrity_extra_file?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()

	root := t.TempDir()
	pkg := writePluginPackage(t, root, "admin.example-plugin", "1.0.0")
	runtimeManager := &fakeRuntime{}
	manager := NewManager(client, Options{
		PluginRoot:  filepath.Join(root, "data", "plugins"),
		PackageRoot: root,
		TempRoot:    filepath.Join(root, "tmp"),
		Registry:    cmfplugin.NewMemoryExtensionRegistry(),
		Runtime:     runtimeManager,
	})
	info, err := manager.Install(ctx, pkg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(info.InstallPath, "injected.so"), []byte("unexpected"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Enable(ctx, info.ID); !errors.Is(err, ErrEnableFailed) {
		t.Fatalf("Enable error = %v, want ErrEnableFailed", err)
	}
	if runtimeManager.started {
		t.Fatal("runtime started after an unexpected file was added")
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
		PluginRoot:  filepath.Join(root, "data", "plugins"),
		PackageRoot: root,
		TempRoot:    filepath.Join(root, "tmp"),
		Registry:    cmfplugin.NewMemoryExtensionRegistry(),
		Runtime:     &fakeRuntime{},
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

func TestManagerRecoversEnabledPluginAfterUnexpectedExit(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:plugin_runtime_recovery?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()

	root := t.TempDir()
	pkg := writePluginPackage(t, root, "admin.example-plugin", "1.0.0")
	runtime := &recoveryRuntime{starts: make(chan struct{}, 4), stops: make(chan struct{}, 1)}
	manager := NewManager(client, Options{
		PluginRoot:  filepath.Join(root, "data", "plugins"),
		PackageRoot: root,
		TempRoot:    filepath.Join(root, "tmp"),
		Registry:    cmfplugin.NewMemoryExtensionRegistry(),
		Runtime:     runtime,
	})
	if _, err := manager.Install(ctx, pkg); err != nil {
		t.Fatal(err)
	}
	if err := manager.Enable(ctx, "admin.example-plugin"); err != nil {
		t.Fatal(err)
	}
	waitForRuntimeSignal(t, runtime.starts, "initial runtime start")

	manager.markRuntimeExited("admin.example-plugin", errors.New("process exited unexpectedly"))
	waitForRuntimeSignal(t, runtime.starts, "recovery runtime start")

	deadline := time.Now().Add(2 * time.Second)
	for {
		record, err := client.Plugin.Query().Where(pluginent.PluginIDEQ("admin.example-plugin")).Only(ctx)
		if err != nil {
			if time.Now().After(deadline) {
				t.Fatal(err)
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if record.Status == pluginent.StatusEnabled && record.ErrorMessage == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("plugin did not return to enabled state: status=%s error=%q", record.Status, record.ErrorMessage)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestManagerCloseCancelsPendingRuntimeRecovery(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:plugin_runtime_close?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()

	root := t.TempDir()
	pkg := writePluginPackage(t, root, "admin.example-plugin", "1.0.0")
	runtimeManager := &recoveryRuntime{starts: make(chan struct{}, 2), stops: make(chan struct{}, 1)}
	manager := NewManager(client, Options{
		PluginRoot:  filepath.Join(root, "data", "plugins"),
		PackageRoot: root,
		TempRoot:    filepath.Join(root, "tmp"),
		Registry:    cmfplugin.NewMemoryExtensionRegistry(),
		Runtime:     runtimeManager,
	})
	if _, err := manager.Install(ctx, pkg); err != nil {
		t.Fatal(err)
	}
	if err := manager.Enable(ctx, "admin.example-plugin"); err != nil {
		t.Fatal(err)
	}
	waitForRuntimeSignal(t, runtimeManager.starts, "initial runtime start")
	manager.markRuntimeExited("admin.example-plugin", errors.New("process exited unexpectedly"))
	diagnostics, err := manager.Diagnostics(ctx, "admin.example-plugin")
	if err != nil {
		t.Fatal(err)
	}
	if !diagnostics.Restarting || diagnostics.ErrorMessage != "process exited unexpectedly" {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}

	closeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := manager.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	waitForRuntimeSignal(t, runtimeManager.stops, "runtime stop-all")
	select {
	case <-runtimeManager.starts:
		t.Fatal("plugin restarted after manager shutdown began")
	case <-time.After(2 * runtimeRestartBaseDelay):
	}
}

func TestManagerCanDisablePluginInErrorState(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:plugin_disable_error?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()

	root := t.TempDir()
	pkg := writePluginPackage(t, root, "admin.example-plugin", "1.0.0")
	runtime := &recoveryRuntime{starts: make(chan struct{}, 1), stops: make(chan struct{}, 1)}
	manager := NewManager(client, Options{
		PluginRoot:  filepath.Join(root, "data", "plugins"),
		PackageRoot: root,
		TempRoot:    filepath.Join(root, "tmp"),
		Registry:    cmfplugin.NewMemoryExtensionRegistry(),
		Runtime:     runtime,
	})
	if _, err := manager.Install(ctx, pkg); err != nil {
		t.Fatal(err)
	}
	record, err := client.Plugin.Query().Where(pluginent.PluginIDEQ("admin.example-plugin")).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Plugin.UpdateOne(record).SetStatus(pluginent.StatusError).SetErrorMessage("crashed").Save(ctx); err != nil {
		t.Fatal(err)
	}

	if err := manager.Disable(ctx, "admin.example-plugin"); err != nil {
		t.Fatal(err)
	}
	waitForRuntimeSignal(t, runtime.stops, "runtime stop")
	record, err = client.Plugin.Query().Where(pluginent.PluginIDEQ("admin.example-plugin")).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != pluginent.StatusDisabled || record.EnabledAt != nil {
		t.Fatalf("disabled plugin = status %s enabled_at %v", record.Status, record.EnabledAt)
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
		PluginRoot:  filepath.Join(root, "data", "plugins"),
		PackageRoot: root,
		TempRoot:    filepath.Join(root, "tmp"),
		Registry:    cmfplugin.NewMemoryExtensionRegistry(),
		Runtime:     &fakeRuntime{},
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
	root := t.TempDir()
	manager := NewManager(client, Options{
		PluginRoot:  filepath.Join(root, "plugins"),
		PackageRoot: root,
		TempRoot:    filepath.Join(root, "tmp"),
		Registry:    cmfplugin.NewMemoryExtensionRegistry(),
		Runtime:     &fakeRuntime{},
	})
	if _, err := manager.Install(ctx, filepath.Join(root, "missing.zip")); !errors.Is(err, ErrInvalidPackage) {
		t.Fatalf("err = %v, want ErrInvalidPackage", err)
	}
}

func TestManagerInstallRejectsUnsafeVersionWithoutDeletingPluginRoot(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:plugin_unsafe_version?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()
	root := t.TempDir()
	pluginRoot := filepath.Join(root, "plugins")
	if err := os.MkdirAll(pluginRoot, 0755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(pluginRoot, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	packagePath := writePluginPackage(t, root, "admin.example-plugin", "..")
	manager := NewManager(client, Options{
		PluginRoot: pluginRoot, PackageRoot: root,
		Registry: cmfplugin.NewMemoryExtensionRegistry(), Runtime: &fakeRuntime{},
	})

	if _, err := manager.Install(ctx, packagePath); !errors.Is(err, ErrInvalidPackage) {
		t.Fatalf("Install unsafe version error = %v, want ErrInvalidPackage", err)
	}
	if body, err := os.ReadFile(sentinel); err != nil || string(body) != "keep" {
		t.Fatalf("plugin root sentinel was changed: body=%q err=%v", body, err)
	}
}

func TestValidateAdminManifestProtocolMatrix(t *testing.T) {
	platform := runtime.GOOS + "-" + runtime.GOARCH
	for _, test := range []struct {
		name      string
		protocol  string
		transport string
		wantError bool
	}{
		{name: "http tcp", protocol: cmfplugin.ProtocolHTTP, transport: cmfplugin.TransportTCP},
		{name: "grpc tcp", protocol: cmfplugin.ProtocolGRPC, transport: cmfplugin.TransportTCP},
		{name: "stdio", protocol: cmfplugin.ProtocolStdio, transport: cmfplugin.TransportStdio},
		{name: "http stdio", protocol: cmfplugin.ProtocolHTTP, transport: cmfplugin.TransportStdio, wantError: true},
		{name: "grpc stdio", protocol: cmfplugin.ProtocolGRPC, transport: cmfplugin.TransportStdio, wantError: true},
		{name: "stdio tcp", protocol: cmfplugin.ProtocolStdio, transport: cmfplugin.TransportTCP, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := &cmfplugin.PluginManifest{
				ID: "admin.example-plugin", Name: "Example", Version: "1.0.0",
				Compatibility:   cmfplugin.PluginCompatibility{App: ">=1.0.0 <2.0.0", API: "v1"},
				Permissions:     []string{"route:register"},
				ExtensionPoints: []string{cmfplugin.ExtensionAdminRoutes},
				Engine: cmfplugin.PluginEngine{
					Type: cmfplugin.EngineTypeProcess, Protocol: test.protocol,
					Transport: map[string]string{platform: test.transport},
					Entry:     map[string]string{platform: "bin/plugin"},
				},
			}
			err := validateAdminManifest(manifest)
			if (err != nil) != test.wantError {
				t.Fatalf("validateAdminManifest() error = %v, wantError=%v", err, test.wantError)
			}
		})
	}
}

func TestValidateAdminManifestRejectsIncompatibleHostVersion(t *testing.T) {
	platform := runtime.GOOS + "-" + runtime.GOARCH
	manifest := &cmfplugin.PluginManifest{
		ID: "admin.example-plugin", Name: "Example", Version: "1.0.0",
		Compatibility:   cmfplugin.PluginCompatibility{App: ">=2.0.0", API: "v1"},
		Permissions:     []string{"route:register"},
		ExtensionPoints: []string{cmfplugin.ExtensionAdminRoutes},
		Engine: cmfplugin.PluginEngine{
			Type: cmfplugin.EngineTypeProcess, Protocol: cmfplugin.ProtocolGRPC,
			Transport: map[string]string{platform: cmfplugin.TransportTCP},
			Entry:     map[string]string{platform: "bin/plugin"},
		},
	}
	if err := validateAdminManifest(manifest); err == nil {
		t.Fatal("manifest requiring an incompatible host version was accepted")
	}
}

func TestValidateAdminManifestAcceptsCommaAndWhitespaceConstraints(t *testing.T) {
	platform := runtime.GOOS + "-" + runtime.GOARCH
	for _, constraint := range []string{">=1.0.0 <2.0.0", ">=1.0.0, <2.0.0", ">=1.0.0, !=1.5.0 <2.0.0"} {
		manifest := &cmfplugin.PluginManifest{
			ID: "admin.example-plugin", Name: "Example", Version: "1.0.0",
			Compatibility:   cmfplugin.PluginCompatibility{App: constraint, API: "v1"},
			Permissions:     []string{"route:register"},
			ExtensionPoints: []string{cmfplugin.ExtensionAdminRoutes},
			Engine: cmfplugin.PluginEngine{
				Type: cmfplugin.EngineTypeProcess, Protocol: cmfplugin.ProtocolGRPC,
				Transport: map[string]string{platform: cmfplugin.TransportTCP},
				Entry:     map[string]string{platform: "bin/plugin"},
			},
		}
		if err := validateAdminManifest(manifest); err != nil {
			t.Fatalf("constraint %q: %v", constraint, err)
		}
	}
}

func TestManagerInstallRejectsPathOutsidePackageRoot(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:plugin_outside_pkgroot?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()
	root := t.TempDir()
	outsideRoot := t.TempDir()
	pkg := writePluginPackage(t, outsideRoot, "admin.example-plugin", "1.0.0")
	manager := NewManager(client, Options{
		PluginRoot:  filepath.Join(root, "data", "plugins"),
		PackageRoot: root,
		TempRoot:    filepath.Join(root, "tmp"),
		Registry:    cmfplugin.NewMemoryExtensionRegistry(),
		Runtime:     &fakeRuntime{},
	})
	if _, err := manager.Install(ctx, pkg); err != ErrInvalidPath {
		t.Fatalf("err = %v, want ErrInvalidPath", err)
	}
}

func TestManagerInstallRejectsPathWhenPackageRootNotConfigured(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:plugin_no_pkgrt?mode=memory&cache=shared&_fk=1",
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
	if _, err := manager.Install(ctx, pkg); err != ErrInvalidPath {
		t.Fatalf("err = %v, want ErrInvalidPath when PackageRoot is not configured", err)
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
		PluginRoot:  filepath.Join(root, "data", "plugins"),
		PackageRoot: root,
		TempRoot:    filepath.Join(root, "tmp"),
		Registry:    cmfplugin.NewMemoryExtensionRegistry(),
		Runtime:     &fakeRuntime{},
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
		PluginRoot:  filepath.Join(root, "data", "plugins"),
		PackageRoot: root,
		TempRoot:    filepath.Join(root, "tmp"),
		Registry:    cmfplugin.NewMemoryExtensionRegistry(),
		Runtime:     &fakeRuntime{},
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
		PluginRoot:  filepath.Join(root, "data", "plugins"),
		PackageRoot: root,
		TempRoot:    filepath.Join(root, "tmp"),
		Registry:    cmfplugin.NewMemoryExtensionRegistry(),
		Runtime:     &fakeRuntime{},
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

func writeExecutablePluginPackage(t *testing.T, root, executable string) string {
	t.Helper()
	platform := runtime.GOOS + "-" + runtime.GOARCH
	entry := "bin/" + platform + "/plugin"
	if runtime.GOOS == "windows" {
		entry += ".exe"
	}
	zipPath := filepath.Join(root, runtimeHelperPluginID+".zip")
	file, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	manifest := `id: ` + runtimeHelperPluginID + `
name: Runtime helper
version: 1.0.0
engine:
  type: process
  protocol: grpc
  transport:
    ` + platform + `: tcp
  entry:
    ` + platform + `: ` + entry + `
compatibility:
  app: ">=1.0.0, <2.0.0"
  api: v1
permissions:
  - route:register
extension_points:
  - admin.routes
`
	for name, body := range map[string]string{
		"plugin.yaml":        manifest,
		"contributions.json": `{"admin.routes":[{"method":"POST","path":"/echo"}]}`,
	} {
		writer, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	header := &zip.FileHeader{Name: entry, Method: zip.Deflate}
	header.SetMode(0755)
	writer, err := archive.CreateHeader(header)
	if err != nil {
		_ = source.Close()
		t.Fatal(err)
	}
	if _, err := io.Copy(writer, source); err != nil {
		_ = source.Close()
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return zipPath
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
		"contributions.json":           `{"admin.routes":[{"method":"*","path":"/*"}]}`,
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

func signPluginPackage(t *testing.T, packagePath string, privateKey ed25519.PrivateKey) {
	t.Helper()
	digest, err := hashFile(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	signature := base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, digest))
	if err := os.WriteFile(packagePath+signatureFileSuffix, []byte(signature), 0600); err != nil {
		t.Fatal(err)
	}
}

type fakeRuntime struct {
	started   bool
	stopped   bool
	startErr  error
	stopErr   error
	healthErr error
}

type recoveryRuntime struct {
	starts chan struct{}
	stops  chan struct{}
}

type versionRuntime struct {
	starts      chan string
	stops       chan struct{}
	failVersion string
}

func (runtime *versionRuntime) Start(_ context.Context, info cmfplugin.PluginInfo) error {
	runtime.starts <- info.Version
	if info.Version == runtime.failVersion {
		return errors.New("version start failed")
	}
	return nil
}

func (runtime *versionRuntime) Stop(context.Context, string) error {
	runtime.stops <- struct{}{}
	return nil
}

func (*versionRuntime) Health(context.Context, string) error { return nil }

func (runtime *recoveryRuntime) Start(context.Context, cmfplugin.PluginInfo) error {
	runtime.starts <- struct{}{}
	return nil
}

func (runtime *recoveryRuntime) Stop(context.Context, string) error {
	runtime.stops <- struct{}{}
	return nil
}

func (runtime *recoveryRuntime) StopAll(context.Context) error {
	runtime.stops <- struct{}{}
	return nil
}

func (*recoveryRuntime) Health(context.Context, string) error { return nil }

func waitForRuntimeSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitForRuntimeVersion(t *testing.T, versions <-chan string, want string) {
	t.Helper()
	select {
	case got := <-versions:
		if got != want {
			t.Fatalf("runtime version = %q, want %q", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for runtime version %s", want)
	}
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
