package plugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"

	_ "github.com/mattn/go-sqlite3"
	cmfplugin "github.com/wuwuseo/cmf/plugin"
)

// TestInstallRejectsEmptyPackagePath 验证空包路径被拒绝。
// 回归测试：commit d0abc48 限制插件包源目录，防止任意路径安装。
// 空路径必须返回 ErrInvalidPackage 而非被 filepath.Abs 退化为 CWD。
func TestInstallRejectsEmptyPackagePath(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:plugin_empty_path?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()

	root := t.TempDir()
	manager := NewManager(client, Options{
		PluginRoot:  filepath.Join(root, "data", "plugins"),
		PackageRoot: root,
		TempRoot:    filepath.Join(root, "tmp"),
		Registry:    cmfplugin.NewMemoryExtensionRegistry(),
		Runtime:     &fakeRuntime{},
	})

	_, err := manager.Install(ctx, "")
	if !errors.Is(err, ErrInvalidPackage) {
		t.Fatalf("Install('') err = %v, want ErrInvalidPackage", err)
	}
}

// TestInstallRejectsPathTraversal 验证路径遍历攻击被拒绝。
// 回归测试：commit d0abc48 限制插件包源目录，IsWithinRoot 必须阻止
// 包含 ../ 的路径逃逸 PackageRoot。
func TestInstallRejectsPathTraversal(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:plugin_traversal?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()

	root := t.TempDir()
	manager := NewManager(client, Options{
		PluginRoot:  filepath.Join(root, "data", "plugins"),
		PackageRoot: root,
		TempRoot:    filepath.Join(root, "tmp"),
		Registry:    cmfplugin.NewMemoryExtensionRegistry(),
		Runtime:     &fakeRuntime{},
	})

	traversalPaths := []string{
		filepath.Join(root, "..", "..", "etc", "passwd"),
		filepath.Join(root, "..", "secret"),
	}
	for _, pkg := range traversalPaths {
		_, err := manager.Install(ctx, pkg)
		if !errors.Is(err, ErrInvalidPath) {
			t.Errorf("Install(%q) err = %v, want ErrInvalidPath", pkg, err)
		}
	}
}

func TestInstallRejectsPackageSymlinkEscapingRoot(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:plugin_symlink_escape?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()

	root := t.TempDir()
	outsideRoot := t.TempDir()
	outsidePackage := writePluginPackage(t, outsideRoot, "admin.example-plugin", "1.0.0")
	link := filepath.Join(root, "plugin.zip")
	if err := os.Symlink(outsidePackage, link); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	manager := NewManager(client, Options{
		PluginRoot: filepath.Join(root, "plugins"), PackageRoot: root,
		Registry: cmfplugin.NewMemoryExtensionRegistry(), Runtime: &fakeRuntime{},
	})

	if _, err := manager.Install(ctx, link); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("Install symlink error = %v, want ErrInvalidPath", err)
	}
}
