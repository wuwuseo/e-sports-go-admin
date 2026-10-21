package service

import (
	"os"
	"path/filepath"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
	cmfConfig "github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/filesystem"
)

// setupDiskAttachmentService 构造 local（默认）+ archive（第二块本地磁盘）配置，
// 用于验证附件按记录中的 disk 字段读写，而不是永远读写默认磁盘。
func setupDiskAttachmentService(t *testing.T) (*AttachmentService, *ent.Client, string, string) {
	t.Helper()

	client := enttest.Open(t, "sqlite3", "file:attachment_disk?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	defaultRoot := t.TempDir()
	archiveRoot := t.TempDir()
	cfg := cmfConfig.Config{}
	// 驱动实例按 App.Name + 磁盘名缓存，测试使用唯一名称避免复用其它用例的临时目录
	cfg.App.Name = "attachment-disk-" + t.Name()
	cfg.Filesystem.Default = "local"
	cfg.Filesystem.Disks = map[string]cmfConfig.DiskConfig{
		"local":   {Driver: "local", Options: map[string]any{"root": defaultRoot}},
		"archive": {Driver: "local", Options: map[string]any{"root": archiveRoot}},
	}

	fs, err := filesystem.NewFilesystemFromConfig(&cfg)
	if err != nil {
		t.Fatalf("NewFilesystemFromConfig: %v", err)
	}
	return NewAttachmentService(fs), client, defaultRoot, archiveRoot
}

func TestAttachmentServiceReadsFromRecordDisk(t *testing.T) {
	svc, client, _, archiveRoot := setupDiskAttachmentService(t)
	ctx := t.Context()

	key := filepath.Join("attachments", "2026", "10", "archive.txt")
	if err := os.MkdirAll(filepath.Join(archiveRoot, filepath.Dir(key)), 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(archiveRoot, key), []byte("from-archive-disk"), 0o644); err != nil {
		t.Fatalf("写入文件失败: %v", err)
	}

	a, err := client.Attachment.Create().
		SetName("archive.txt").
		SetOriginalName("archive.txt").
		SetPath(key).
		SetDisk("archive").
		SetDriver("local").
		SetDirectory("attachments/2026/10").
		SetMimeType("text/plain").
		SetExtension("txt").
		SetSize(17).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建附件失败: %v", err)
	}

	data, _, _, err := svc.GetFileData(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetFileData 失败: %v", err)
	}
	if string(data) != "from-archive-disk" {
		t.Fatalf("读取到了错误磁盘的内容: %q", data)
	}

	if err := svc.Delete(ctx, a.ID); err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(archiveRoot, key)); !os.IsNotExist(err) {
		t.Fatalf("删除记录后 archive 磁盘上的文件仍存在: %v", err)
	}
}

func TestAttachmentDiskDriverLegacyRecordFallsBackToLocal(t *testing.T) {
	// 默认磁盘已切到 S3 时，没有任何 disk/driver 的历史记录仍应按本地文件处理
	cfg := cmfConfig.Config{}
	cfg.Filesystem.Default = "s3"
	cfg.Filesystem.Disks = map[string]cmfConfig.DiskConfig{
		"local": {Driver: "local", Options: map[string]any{"root": t.TempDir()}},
		"s3":    {Driver: "s3", Options: map[string]any{"bucket": "bucket"}},
	}
	svc := NewAttachmentService(&filesystem.Filesystem{Config: cfg})

	disk, driver := svc.attachmentDiskDriver(&ent.Attachment{})
	if disk != "local" || driver != "local" {
		t.Fatalf("历史空记录应回落到本地磁盘，got disk=%q driver=%q", disk, driver)
	}
}
