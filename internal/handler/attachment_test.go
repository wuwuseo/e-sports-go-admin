package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	cmfConfig "github.com/wuwuseo/cmf/config"
	cmfCrypto "github.com/wuwuseo/cmf/crypto"
	"github.com/wuwuseo/cmf/filesystem"

	_ "github.com/mattn/go-sqlite3"
)

func setupAttachmentHandlerTest(t *testing.T) (*fiber.App, *ent.Client, *service.AttachmentService, *filesystem.Filesystem) {
	t.Helper()
	cfg := newAttachmentHandlerConfig(t)
	fs, err := filesystem.NewFilesystemFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewFilesystemFromConfig: %v", err)
	}
	return setupAttachmentHandlerTestWithFilesystem(t, cfg, fs)
}

func newAttachmentHandlerConfig(t *testing.T) *cmfConfig.Config {
	t.Helper()
	cfg := &cmfConfig.Config{}
	cfg.Attachment.PublicBaseURL = "/uploads"
	cfg.Attachment.XAccelEnabled = true
	cfg.Attachment.XAccelPrefix = "/_protected_attachments"
	cfg.Attachment.AccessURLTTL = 600
	cfg.Filesystem.Default = "local"
	cfg.Filesystem.Disks = map[string]struct {
		Driver  string `mapstructure:"driver"`
		Options any    `mapstructure:"options"`
	}{
		"local": {
			Driver: "local",
			Options: map[string]any{
				"root":            t.TempDir(),
				"public_base_url": "https://local-cdn.example.com/uploads",
			},
		},
	}
	return cfg
}

func setupAttachmentHandlerTestWithFilesystem(t *testing.T, cfg *cmfConfig.Config, fs *filesystem.Filesystem) (*fiber.App, *ent.Client, *service.AttachmentService, *filesystem.Filesystem) {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:handler_attachment?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })

	svc := service.NewAttachmentService(fs)

	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("utils.Response", utils.NewResponse())
		c.Locals("service.AttachmentService", svc)
		c.Locals("attachment.access_secret", "test-secret")
		c.Locals("attachment.content_base_path", "/api/v1/attachments")
		c.Locals("attachment.access_ttl", cfg.Attachment.AccessURLTTL)
		c.Locals("attachment.x_accel_enabled", cfg.Attachment.XAccelEnabled)
		return c.Next()
	})
	return app, client, svc, fs
}

func createHandlerAttachment(t *testing.T, client *ent.Client, fs *filesystem.Filesystem) *ent.Attachment {
	t.Helper()
	return createHandlerAttachmentFile(t, client, fs, "avatars/test.png", "avatar.png", "image/png", "png", []byte("png-bytes"), true)
}

func createHandlerAttachmentFile(t *testing.T, client *ent.Client, fs *filesystem.Filesystem, key, originalName, mimeType, extension string, data []byte, writeFile bool) *ent.Attachment {
	t.Helper()
	return createHandlerAttachmentFileWithStorage(t, client, fs, "local", "local", key, originalName, mimeType, extension, data, writeFile)
}

func createHandlerAttachmentFileWithStorage(t *testing.T, client *ent.Client, fs *filesystem.Filesystem, disk, driver, key, originalName, mimeType, extension string, data []byte, writeFile bool) *ent.Attachment {
	t.Helper()
	if writeFile {
		if err := fs.Set(key, data, 0); err != nil {
			t.Fatalf("write test file: %v", err)
		}
	}
	directory := ""
	if index := strings.LastIndex(key, "/"); index >= 0 {
		directory = key[:index]
	}
	a, err := client.Attachment.Create().
		SetName(originalName).
		SetOriginalName(originalName).
		SetPath(key).
		SetDisk(disk).
		SetDriver(driver).
		SetDirectory(directory).
		SetContentHash(filesystem.HashString(key)).
		SetOriginalNameHash(filesystem.HashString(originalName)).
		SetMimeType(mimeType).
		SetExtension(extension).
		SetSize(int64(len(data))).
		SetUploadedBy("tester").
		Save(t.Context())
	if err != nil {
		t.Fatalf("create attachment: %v", err)
	}
	return a
}

func TestGetAttachmentAccessURL(t *testing.T) {
	app, client, _, fs := setupAttachmentHandlerTest(t)
	a := createHandlerAttachment(t, client, fs)
	app.Get("/attachments/:id/access-url", GetAttachmentAccessURL)

	req := httptest.NewRequest(http.MethodGet, "/attachments/"+strconv.Itoa(a.ID)+"/access-url", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var body struct {
		Code int `json:"code"`
		Data struct {
			URL       string `json:"url"`
			ExpiresIn int64  `json:"expires_in"`
			Mode      string `json:"mode"`
			Public    bool   `json:"public"`
			Disk      string `json:"disk"`
			Driver    string `json:"driver"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.URL == "" || !strings.Contains(body.Data.URL, "/api/v1/attachments/"+strconv.Itoa(a.ID)+"/content?token=") {
		t.Fatalf("unexpected access url: %q", body.Data.URL)
	}
	if body.Data.ExpiresIn != 600 || body.Data.Mode != "private_local" || body.Data.Public {
		t.Fatalf("unexpected private local access metadata: %+v", body.Data)
	}
	if body.Data.Disk != "local" || body.Data.Driver != "local" {
		t.Fatalf("unexpected disk metadata: %+v", body.Data)
	}
}

func TestGetAttachmentAccessURLReturnsLocalPublicCDN(t *testing.T) {
	app, client, _, fs := setupAttachmentHandlerTest(t)
	a := createHandlerAttachmentFile(t, client, fs, "public/avatar/image.png", "image.png", "image/png", "png", []byte("image"), true)
	app.Get("/attachments/:id/access-url", GetAttachmentAccessURL)

	req := httptest.NewRequest(http.MethodGet, "/attachments/"+strconv.Itoa(a.ID)+"/access-url", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var body struct {
		Data struct {
			URL       string `json:"url"`
			ExpiresIn int64  `json:"expires_in"`
			Mode      string `json:"mode"`
			Public    bool   `json:"public"`
			Disk      string `json:"disk"`
			Driver    string `json:"driver"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.URL != "https://local-cdn.example.com/uploads/avatar/image.png" {
		t.Fatalf("unexpected public local url: %q", body.Data.URL)
	}
	if body.Data.ExpiresIn != 0 || body.Data.Mode != "public_local" || !body.Data.Public {
		t.Fatalf("unexpected public local access metadata: %+v", body.Data)
	}
	if body.Data.Disk != "local" || body.Data.Driver != "local" {
		t.Fatalf("unexpected disk metadata: %+v", body.Data)
	}
}

func TestServeAttachmentContentRequiresValidToken(t *testing.T) {
	app, client, svc, fs := setupAttachmentHandlerTest(t)
	a := createHandlerAttachment(t, client, fs)
	app.Get("/attachments/:id/content", ServeAttachmentContent)

	accessURL, err := svc.CreateAccessURL(t.Context(), a.ID, "/attachments", service.AttachmentAccessTTL, "test-secret")
	if err != nil {
		t.Fatalf("CreateAccessURL: %v", err)
	}
	token := accessURL.URL[strings.LastIndex(accessURL.URL, "token=")+len("token="):]

	req := httptest.NewRequest(http.MethodGet, "/attachments/"+strconv.Itoa(a.ID)+"/content?token="+token, nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
	if redirect := resp.Header.Get("X-Accel-Redirect"); redirect != "/_protected_attachments/avatars/test.png" {
		t.Fatalf("expected X-Accel-Redirect header, got %q", redirect)
	}

	badReq := httptest.NewRequest(http.MethodGet, "/attachments/"+strconv.Itoa(a.ID)+"/content?token=bad", nil)
	badResp, err := app.Test(badReq)
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}
	if badResp.StatusCode == http.StatusOK {
		t.Fatal("expected invalid token to be rejected")
	}

	expiredToken, err := cmfCrypto.SignExpiringToken("test-secret", "attachment:"+strconv.Itoa(a.ID), time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("SignExpiringToken: %v", err)
	}
	expiredReq := httptest.NewRequest(http.MethodGet, "/attachments/"+strconv.Itoa(a.ID)+"/content?token="+expiredToken, nil)
	expiredResp, err := app.Test(expiredReq)
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}
	if expiredResp.StatusCode == http.StatusOK {
		t.Fatal("expected expired token to be rejected")
	}
}

func TestGetAttachmentAccessURLReturnsS3PublicCDNWhenDefaultDiskIsS3AndLocalCopyExists(t *testing.T) {
	cfg := newAttachmentHandlerConfig(t)
	cfg.Filesystem.Default = "s3"
	cfg.Filesystem.IsAndLocal = true
	cfg.Filesystem.Disks["s3"] = struct {
		Driver  string `mapstructure:"driver"`
		Options any    `mapstructure:"options"`
	}{
		Driver: "s3",
		Options: map[string]any{
			"access_key":      "AKIA_TEST",
			"secret_key":      "secret",
			"region":          "us-east-1",
			"bucket":          "bucket",
			"endpoint":        "https://s3.example.com",
			"public_base_url": "https://s3-cdn.example.com",
			"presign_expires": 600,
			"use_path_style":  true,
		},
	}
	fs := &filesystem.Filesystem{Config: *cfg, Adapter: noopStorage{}}
	app, client, _, _ := setupAttachmentHandlerTestWithFilesystem(t, cfg, fs)
	a := createHandlerAttachmentFileWithStorage(t, client, fs, "s3", "s3", "public/avatar/image.png", "image.png", "image/png", "png", []byte("image"), false)
	app.Get("/attachments/:id/access-url", GetAttachmentAccessURL)

	req := httptest.NewRequest(http.MethodGet, "/attachments/"+strconv.Itoa(a.ID)+"/access-url", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var body struct {
		Data struct {
			URL       string `json:"url"`
			ExpiresIn int64  `json:"expires_in"`
			Mode      string `json:"mode"`
			Public    bool   `json:"public"`
			Disk      string `json:"disk"`
			Driver    string `json:"driver"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Data.URL != "https://s3-cdn.example.com/public/avatar/image.png" {
		t.Fatalf("unexpected s3 public url: %q", body.Data.URL)
	}
	if body.Data.ExpiresIn != 0 || body.Data.Mode != "public_s3" || !body.Data.Public {
		t.Fatalf("unexpected s3 public metadata: %+v", body.Data)
	}
	if body.Data.Disk != "s3" || body.Data.Driver != "s3" {
		t.Fatalf("expected primary disk s3, got %+v", body.Data)
	}
}

func TestGetAttachmentAccessURLReturnsS3PresignedPrivateURL(t *testing.T) {
	cfg := newAttachmentHandlerConfig(t)
	cfg.Filesystem.Default = "s3"
	cfg.Filesystem.Disks["s3"] = struct {
		Driver  string `mapstructure:"driver"`
		Options any    `mapstructure:"options"`
	}{
		Driver: "s3",
		Options: map[string]any{
			"access_key":      "AKIA_TEST",
			"secret_key":      "secret",
			"region":          "us-east-1",
			"bucket":          "bucket",
			"endpoint":        "https://s3.example.com",
			"public_base_url": "https://s3-cdn.example.com",
			"presign_expires": 600,
			"use_path_style":  true,
		},
	}
	fs := &filesystem.Filesystem{Config: *cfg, Adapter: noopStorage{}}
	app, client, _, _ := setupAttachmentHandlerTestWithFilesystem(t, cfg, fs)
	a := createHandlerAttachmentFileWithStorage(t, client, fs, "s3", "s3", "attachments/2026/06/image.png", "image.png", "image/png", "png", []byte("image"), false)
	app.Get("/attachments/:id/access-url", GetAttachmentAccessURL)

	req := httptest.NewRequest(http.MethodGet, "/attachments/"+strconv.Itoa(a.ID)+"/access-url", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var body struct {
		Data struct {
			URL       string `json:"url"`
			ExpiresIn int64  `json:"expires_in"`
			Mode      string `json:"mode"`
			Public    bool   `json:"public"`
			Disk      string `json:"disk"`
			Driver    string `json:"driver"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.Contains(body.Data.URL, "X-Amz-Signature=") {
		t.Fatalf("expected presigned s3 url, got %q", body.Data.URL)
	}
	if body.Data.ExpiresIn != 600 || body.Data.Mode != "private_s3" || body.Data.Public {
		t.Fatalf("unexpected s3 private metadata: %+v", body.Data)
	}
}

type noopStorage struct{}

func (noopStorage) Get(string) ([]byte, error)                 { return nil, nil }
func (noopStorage) Set(string, []byte, time.Duration) error    { return nil }
func (noopStorage) Delete(string) error                        { return nil }
func (noopStorage) Reset() error                               { return nil }
func (noopStorage) Close() error                               { return nil }

func TestPreviewAttachmentSendsInlineForImages(t *testing.T) {
	app, client, _, fs := setupAttachmentHandlerTest(t)
	a := createHandlerAttachment(t, client, fs)
	app.Get("/attachments/:id/preview", PreviewAttachment)

	req := httptest.NewRequest(http.MethodGet, "/attachments/"+strconv.Itoa(a.ID)+"/preview", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
	if disposition := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(disposition, "inline;") {
		t.Fatalf("expected inline content disposition, got %q", disposition)
	}
}

func TestPreviewAttachmentDownloadsOtherFiles(t *testing.T) {
	app, client, _, fs := setupAttachmentHandlerTest(t)
	a := createHandlerAttachmentFile(t, client, fs, "docs/report.txt", "report 1.txt", "text/plain", "txt", []byte("hello"), true)
	app.Get("/attachments/:id/preview", PreviewAttachment)

	req := httptest.NewRequest(http.MethodGet, "/attachments/"+strconv.Itoa(a.ID)+"/preview", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
	if disposition := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(disposition, "attachment;") {
		t.Fatalf("expected attachment content disposition, got %q", disposition)
	}
}

func TestPreviewAttachmentMissingStoredFileReturnsNotFound(t *testing.T) {
	app, client, _, fs := setupAttachmentHandlerTest(t)
	a := createHandlerAttachmentFile(t, client, fs, "docs/missing.txt", "missing.txt", "text/plain", "txt", []byte("missing"), false)
	app.Get("/attachments/:id/preview", PreviewAttachment)

	req := httptest.NewRequest(http.MethodGet, "/attachments/"+strconv.Itoa(a.ID)+"/preview", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", resp.StatusCode)
	}
}
