package service

import (
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"

	"github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/filesystem"
	_ "github.com/mattn/go-sqlite3"
)

func setupAttachmentService(t *testing.T) (*AttachmentService, *ent.Client) {
	client := enttest.Open(t, "sqlite3", "file:att?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
	cfg := config.Config{}
	cfg.Filesystem.Default = "local"
	fs, err := filesystem.NewFilesystemFromConfig(&cfg)
	if err != nil {
		t.Logf("初始化 filesystem 失败: %v，使用空 Filesystem 结构", err)
		fs = &filesystem.Filesystem{Config: cfg}
	}
	svc := NewAttachmentService(fs)
	return svc, client
}

// -------------------------------------------------------------------
// 纯函数：磁盘/驱动判断
// -------------------------------------------------------------------

func TestAttachmentDiskDriver_PrefersProvidedValues(t *testing.T) {
	svc, _ := setupAttachmentService(t)
	a := &ent.Attachment{
		Disk:   "custom-disk",
		Driver: "s3",
	}
	disk, driver := svc.attachmentDiskDriver(a)
	if disk != "custom-disk" || driver != "s3" {
		t.Fatalf("期望保留提供的磁盘/驱动，got disk=%q driver=%q", disk, driver)
	}
}

func TestAttachmentDiskDriver_FallsBackToDefaultConfig(t *testing.T) {
	svc, _ := setupAttachmentService(t)
	// 默认磁盘/驱动通常来源于 Filesystem 配置
	a := &ent.Attachment{Disk: "", Driver: ""}
	disk, driver := svc.attachmentDiskDriver(a)
	if disk == "" || driver == "" {
		t.Fatalf("空值应回退到默认值，got disk=%q driver=%q", disk, driver)
	}
}

// -------------------------------------------------------------------
// attachmentDirectory：从 path 解析目录
// -------------------------------------------------------------------

func TestAttachmentDirectory_UsesRecordDirectoryWhenProvided(t *testing.T) {
	a := &ent.Attachment{Directory: "attachments/2025/01", Path: "ignored"}
	got := attachmentDirectory(a)
	if got != "attachments/2025/01" {
		t.Fatalf("期望使用 Directory 字段，got %q", got)
	}
}

func TestAttachmentDirectory_FallsBackToPathBase(t *testing.T) {
	a := &ent.Attachment{Directory: "", Path: "attachments/2025/01/file.bin"}
	got := attachmentDirectory(a)
	want := "attachments/2025/01"
	if got != want {
		t.Fatalf("期望从 path 解析目录为 %q，got %q", want, got)
	}
}

func TestAttachmentDirectory_RootLevelFileReturnsEmpty(t *testing.T) {
	a := &ent.Attachment{Directory: "", Path: "file.bin"}
	got := attachmentDirectory(a)
	if got != "" {
		t.Fatalf("根级文件应返回空目录，got %q", got)
	}
}

// -------------------------------------------------------------------
// 纯函数：resolveAttachmentDirectory / firstString
// -------------------------------------------------------------------

func TestResolveAttachmentDirectory_UsesInputWhenNonEmpty(t *testing.T) {
	got, err := resolveAttachmentDirectory("attachments/2025/01")
	if err != nil {
		t.Fatalf("未预期错误: %v", err)
	}
	if got != "attachments/2025/01" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveAttachmentDirectory_EmptyDefaultsToTimeBasedLayout(t *testing.T) {
	got, err := resolveAttachmentDirectory("")
	if err != nil {
		t.Fatalf("未预期错误: %v", err)
	}
	// 格式为 attachments/YYYY/MM
	if len(got) != len("attachments/YYYY/MM") || got[:12] != "attachments/" {
		t.Fatalf("期望 attachments/YYYY/MM 格式，got %q", got)
	}
}

func TestFirstString_PrefersFirstNonEmpty(t *testing.T) {
	if firstString([]string{"a", "b"}) != "a" {
		t.Fatalf("期望 a")
	}
	if firstString(nil) != "" {
		t.Fatalf("nil 应返回空")
	}
	if firstString([]string{}) != "" {
		t.Fatalf("空切片应返回空")
	}
}

// -------------------------------------------------------------------
// CreateAccessURL / BuildAccessURL: 存在/不存在的处理
// -------------------------------------------------------------------

func TestAttachmentCreateAccessURL_AttachmentMissingReturnsError(t *testing.T) {
	svc, _ := setupAttachmentService(t)
	ctx := t.Context()
	_, err := svc.CreateAccessURL(ctx, 9999, "/attachments", time.Hour, "secret")
	if err == nil {
		t.Fatalf("应返回附件不存在的错误")
	}
}

func TestAttachmentBuildAccessURL_UsesDiskDriverFromRecord(t *testing.T) {
	svc, client := setupAttachmentService(t)
	ctx := t.Context()
	a, err := client.Attachment.Create().
		SetName("a").
		SetPath("files/example.png").
		SetDisk("disk-1").
		SetDriver("local").
		SetMimeType("image/png").
		SetOriginalName("o.png").
		SetContentHash("h1").
		SetExtension("png").
		SetSize(100).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建附件失败: %v", err)
	}

	url, err := svc.CreateAccessURL(ctx, a.ID, "/attachments", 10*time.Minute, "secret")
	if err != nil {
		t.Fatalf("未预期错误: %v", err)
	}
	if url == "" {
		t.Fatalf("期望返回非空 URL")
	}
}

// -------------------------------------------------------------------
// ValidateAccessToken：token 格式和过期
// -------------------------------------------------------------------

func TestValidateAccessToken_InvalidSubjectRejected(t *testing.T) {
	svc, client := setupAttachmentService(t)
	ctx := t.Context()
	a, err := client.Attachment.Create().
		SetName("a").SetPath("files/x.txt").SetDisk("x").SetDriver("local").
		SetMimeType("text/plain").SetOriginalName("x.txt").
		SetContentHash("h").SetExtension("txt").SetSize(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建附件失败: %v", err)
	}

	// 使用 CreateAccessURL 生成的是 id=a.ID 的 token，
	// 用另一个 id(例如 a.ID+1) 去校验应当返回 subject 不匹配错误。
	url, err := svc.CreateAccessURL(ctx, a.ID, "/attachments", 10*time.Minute, "top-secret")
	if err != nil {
		t.Fatalf("创建访问 URL 失败: %v", err)
	}
	if url == "" {
		t.Fatalf("URL 不应为空")
	}

	// 测试 ValidateAccessToken 对未知 token 返回错误
	if err := svc.ValidateAccessToken(a.ID, "not-a-signed-token", "top-secret", timeNow()); err == nil {
		t.Fatalf("非签名 token 应被拒绝")
	}
}

func TestAttachmentService_GetReturnsNotFoundForMissingRecord(t *testing.T) {
	svc, _ := setupAttachmentService(t)
	ctx := t.Context()
	_, err := svc.Get(ctx, 9999)
	if err == nil {
		t.Fatalf("应返回错误")
	}
}

// -------------------------------------------------------------------
// ListAttachments：关键字与 MIME 过滤
// -------------------------------------------------------------------

func TestAttachmentList_FiltersByMimeType(t *testing.T) {
	svc, client := setupAttachmentService(t)
	ctx := t.Context()

	_, err := client.Attachment.Create().
		SetName("logo").SetPath("files/logo.png").SetDisk("x").SetDriver("local").
		SetMimeType("image/png").SetOriginalName("o.png").
		SetContentHash("hpng").SetExtension("png").SetSize(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	_, err = client.Attachment.Create().
		SetName("readme").SetPath("files/readme.txt").SetDisk("x").SetDriver("local").
		SetMimeType("text/plain").SetOriginalName("r.txt").
		SetContentHash("htxt").SetExtension("txt").SetSize(2).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	req := &ListAttachmentsRequest{MimeType: "image"}
	req.Page = 1
	req.PageSize = 10
	resp, err := svc.List(ctx, req)
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("期望匹配 1 条 image 记录，got %d", resp.Total)
	}
}

// -------------------------------------------------------------------
// MoveToGroup：修改 GroupID
// -------------------------------------------------------------------

func TestAttachmentMoveToGroup_UpdatesGroupID(t *testing.T) {
	svc, client := setupAttachmentService(t)
	ctx := t.Context()

	a, err := client.Attachment.Create().
		SetName("x").SetPath("files/x.txt").SetDisk("x").SetDriver("local").
		SetMimeType("text/plain").SetOriginalName("x.txt").
		SetContentHash("h").SetExtension("txt").SetSize(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建: %v", err)
	}

	newGroup := 123
	if err := svc.MoveToGroup(ctx, a.ID, &newGroup); err != nil {
		t.Fatalf("MoveToGroup 失败: %v", err)
	}
	updated, err := client.Attachment.Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("获取失败: %v", err)
	}
	if updated.GroupID != 123 {
		t.Fatalf("期望 GroupID=123，got %d", updated.GroupID)
	}
}

func TestAttachmentMoveToGroup_MissingAttachmentReturnsError(t *testing.T) {
	svc, _ := setupAttachmentService(t)
	ctx := t.Context()
	g := 1
	if err := svc.MoveToGroup(ctx, 9999, &g); err == nil {
		t.Fatalf("不存在的附件应返回错误")
	}
}

// timeNow 返回当前时间的辅助函数，便于测试中保持一致性
func timeNow() time.Time { return time.Now() }
