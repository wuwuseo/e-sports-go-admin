package service

import (
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"

	"github.com/wuwuseo/cmf/crypto"
	"github.com/wuwuseo/cmf/filesystem"
	cmfConfig "github.com/wuwuseo/cmf/config"
	_ "github.com/mattn/go-sqlite3"
)

// -------------------------------------------------------------------
// BuildXAccelRedirect 单元测试
// -------------------------------------------------------------------

func setupBuildXAccelRedirectTest(t *testing.T) (*AttachmentService, *ent.Client) {
	client := enttest.Open(t, "sqlite3", "file:xaccel?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	// 创建带 local driver 的附件记录
	cfg := cmfConfig.Config{}
	cfg.Filesystem.Default = "local"
	cfg.Filesystem.Disks = map[string]struct {
		Driver  string `mapstructure:"driver"`
		Options any    `mapstructure:"options"`
	}{
		"local": {
			Driver: "local",
			Options: map[string]any{
				"root":            t.TempDir(),
				"public_base_url": "https://cdn.example.com",
			},
		},
	}
	cfg.Attachment.XAccelPrefix = "/_protected_attachments"
	fs, err := filesystem.NewFilesystemFromConfig(&cfg)
	if err != nil {
		fs = &filesystem.Filesystem{Config: cfg}
	}
	svc := NewAttachmentService(fs)
	return svc, client
}

func TestBuildXAccelRedirect_ReturnsNilForNonLocalDriver(t *testing.T) {
	svc, client := setupBuildXAccelRedirectTest(t)
	ctx := t.Context()

	a, err := client.Attachment.Create().
		SetName("test.txt").
		SetPath("files/test.txt").
		SetDisk("s3").
		SetDriver("s3").
		SetMimeType("text/plain").
		SetOriginalName("test.txt").
		SetContentHash("hash").
		SetExtension("txt").
		SetSize(100).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建附件失败: %v", err)
	}

	redirect, err := svc.BuildXAccelRedirect(ctx, a.ID)
	if err != nil {
		t.Fatalf("BuildXAccelRedirect 返回错误: %v", err)
	}
	if redirect != nil {
		t.Errorf("非 local driver 应返回 nil，got %+v", redirect)
	}
}

func TestBuildXAccelRedirect_ReturnsNilForMissingAttachment(t *testing.T) {
	svc, _ := setupBuildXAccelRedirectTest(t)
	ctx := t.Context()

	_, err := svc.BuildXAccelRedirect(ctx, 9999)
	if err == nil {
		t.Error("不存在的附件应返回错误")
	}
}

func TestBuildXAccelRedirect_ReturnsRedirectForLocalDriver(t *testing.T) {
	svc, client := setupBuildXAccelRedirectTest(t)
	ctx := t.Context()

	a, err := client.Attachment.Create().
		SetName("test.txt").
		SetPath("attachments/file.txt").
		SetDisk("local").
		SetDriver("local").
		SetMimeType("text/plain").
		SetOriginalName("原始文件.txt").
		SetContentHash("hash").
		SetExtension("txt").
		SetSize(100).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建附件失败: %v", err)
	}

	redirect, err := svc.BuildXAccelRedirect(ctx, a.ID)
	if err != nil {
		t.Fatalf("BuildXAccelRedirect 返回错误: %v", err)
	}
	if redirect == nil {
		t.Fatal("local driver 应返回非 nil redirect")
	}
	if redirect.Path == "" {
		t.Error("redirect.Path 不应为空")
	}
	if redirect.MimeType != "text/plain" {
		t.Errorf("MimeType 不匹配，期望 'text/plain'，got %q", redirect.MimeType)
	}
	if redirect.Filename != "原始文件.txt" {
		t.Errorf("Filename 不匹配，期望 '原始文件.txt'，got %q", redirect.Filename)
	}
}

func TestBuildXAccelRedirect_ReturnsNilWhenFilesystemUnavailable(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:xaccel2?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	// 创建 Filesystem 为 nil 的服务
	svc := NewAttachmentService(nil)
	ctx := t.Context()

	a, err := client.Attachment.Create().
		SetName("test.txt").
		SetPath("file.txt").
		SetDisk("local").
		SetDriver("local").
		SetMimeType("text/plain").
		SetOriginalName("test.txt").
		SetContentHash("hash").
		SetExtension("txt").
		SetSize(100).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建附件失败: %v", err)
	}

	_, err = svc.BuildXAccelRedirect(ctx, a.ID)
	if err == nil {
		t.Error("Filesystem 不可用时应返回错误")
	}
}

// -------------------------------------------------------------------
// ValidateAccessToken 扩展测试
// -------------------------------------------------------------------

func TestValidateAccessToken_WrongSecretReturnsError(t *testing.T) {
	svc, client := setupBuildXAccelRedirectTest(t)
	ctx := t.Context()

	a, err := client.Attachment.Create().
		SetName("test.txt").
		SetPath("files/test.txt").
		SetDisk("local").
		SetDriver("local").
		SetMimeType("text/plain").
		SetOriginalName("test.txt").
		SetContentHash("hash").
		SetExtension("txt").
		SetSize(100).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建附件失败: %v", err)
	}

	// 使用正确 secret 生成 token
	token, err := crypto.SignExpiringToken("correct-secret", attachmentAccessSubject(a.ID), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}

	// 用错误 secret 验证会失败
	err = svc.ValidateAccessToken(a.ID, token, "wrong-secret", time.Now())
	if err == nil {
		t.Error("错误 secret 验证应返回错误")
	}
}

func TestValidateAccessToken_ExpiredTokenReturnsError(t *testing.T) {
	svc, client := setupBuildXAccelRedirectTest(t)
	ctx := t.Context()

	a, err := client.Attachment.Create().
		SetName("test.txt").
		SetPath("files/test.txt").
		SetDisk("local").
		SetDriver("local").
		SetMimeType("text/plain").
		SetOriginalName("test.txt").
		SetContentHash("hash").
		SetExtension("txt").
		SetSize(100).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建附件失败: %v", err)
	}

	// 生成已过期的 token
	token, err := crypto.SignExpiringToken("secret", attachmentAccessSubject(a.ID), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("生成过期 token 失败: %v", err)
	}

	err = svc.ValidateAccessToken(a.ID, token, "secret", time.Now())
	if err == nil {
		t.Error("过期 token 验证应返回错误")
	}
}

func TestValidateAccessToken_ValidTokenReturnsNil(t *testing.T) {
	svc, client := setupBuildXAccelRedirectTest(t)
	ctx := t.Context()
	secret := "my-secret-key"

	a, err := client.Attachment.Create().
		SetName("test.txt").
		SetPath("files/test.txt").
		SetDisk("local").
		SetDriver("local").
		SetMimeType("text/plain").
		SetOriginalName("test.txt").
		SetContentHash("hash").
		SetExtension("txt").
		SetSize(100).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建附件失败: %v", err)
	}

	// 生成有效的 token
	token, err := crypto.SignExpiringToken(secret, attachmentAccessSubject(a.ID), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("生成 token 失败: %v", err)
	}

	err = svc.ValidateAccessToken(a.ID, token, secret, time.Now())
	if err != nil {
		t.Errorf("有效 token 验证不应返回错误，got: %v", err)
	}
}


