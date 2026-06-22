package service

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"

	"github.com/wuwuseo/cmf/bootstrap"
	cmfCache "github.com/wuwuseo/cmf/cache"
	cmfConfig "github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/crypto"
	"github.com/wuwuseo/cmf/filesystem"

	_ "github.com/mattn/go-sqlite3"
)

// ======================== infra ========================

func setupDeepIntegration(t *testing.T) (*ent.Client, func()) {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	prevORM := global.ORM
	global.ORM = client
	cleanup := func() {
		client.Close()
		global.ORM = prevORM
	}
	return client, cleanup
}

func makeMultipartFileHeader(t *testing.T, filename string, data []byte) *multipart.FileHeader {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("Write: %v", err)
	}
	w.Close()

	r := multipart.NewReader(&buf, w.Boundary())
	form, err := r.ReadForm(10 << 20)
	if err != nil {
		t.Fatalf("ReadForm: %v", err)
	}
	return form.File["file"][0]
}

func makeLocalFilesystem(t *testing.T) (*filesystem.Filesystem, string) {
	t.Helper()
	root := t.TempDir()
	cfg := &cmfConfig.Config{}
	cfg.Filesystem.Default = "local"
	cfg.Filesystem.Disks = map[string]struct {
		Driver  string `mapstructure:"driver"`
		Options any    `mapstructure:"options"`
	}{
		"local": {Driver: "local", Options: map[string]any{"root": root}},
	}
	fs, err := filesystem.NewFilesystemFromConfig(cfg)
	if err != nil {
		t.Fatalf("NewFilesystemFromConfig: %v", err)
	}
	return fs, root
}

// ======================== Attachment Upload ========================

func TestIntegration_Upload_Success(t *testing.T) {
	client, cleanup := setupDeepIntegration(t)
	defer cleanup()
	ctx := withPermissionSystemOperator(t.Context())

	fs, _ := makeLocalFilesystem(t)
	fh := makeMultipartFileHeader(t, "report.pdf", []byte("Hello PDF World"))
	fh.Header = textproto.MIMEHeader{"Content-Type": []string{"application/pdf"}}

	svc := &AttachmentService{
		CRUDBase:   &CRUDBase{Client: client},
		filesystem: fs,
	}

	resp, err := svc.Upload(ctx, fh, nil, "user-001")
	if err != nil {
		t.Fatalf("Upload failed: %v", err)
	}
	if resp.Name != "report.pdf" {
		t.Errorf("want report.pdf, got %s", resp.Name)
	}
	if resp.MimeType != "application/pdf" {
		t.Errorf("want application/pdf, got %s", resp.MimeType)
	}
	if resp.UploadedBy != "user-001" {
		t.Errorf("want user-001, got %s", resp.UploadedBy)
	}

	got, err := fs.Get(resp.Path)
	if err != nil {
		t.Fatalf("read stored file: %v", err)
	}
	if string(got) != "Hello PDF World" {
		t.Errorf("want Hello PDF World, got %s", string(got))
	}
}

func TestIntegration_Upload_WithGroup(t *testing.T) {
	client, cleanup := setupDeepIntegration(t)
	defer cleanup()
	ctx := withPermissionSystemOperator(t.Context())

	fs, _ := makeLocalFilesystem(t)
	fh := makeMultipartFileHeader(t, "image.jpg", []byte("JPEG data"))
	fh.Header = textproto.MIMEHeader{"Content-Type": []string{"image/jpeg"}}

	groupID := 7
	svc := &AttachmentService{
		CRUDBase:   &CRUDBase{Client: client},
		filesystem: fs,
	}

	resp, err := svc.Upload(ctx, fh, &groupID, "user-002")
	if err != nil {
		t.Fatalf("Upload with group failed: %v", err)
	}
	if resp.GroupID != 7 {
		t.Errorf("want GroupID=7, got %d", resp.GroupID)
	}
}

func TestIntegration_Upload_DeduplicatesByHashSizeAndOriginalName(t *testing.T) {
	client, cleanup := setupDeepIntegration(t)
	defer cleanup()
	ctx := withPermissionSystemOperator(t.Context())

	fs, _ := makeLocalFilesystem(t)
	svc := &AttachmentService{
		CRUDBase:   &CRUDBase{Client: client},
		filesystem: fs,
	}

	fh1 := makeMultipartFileHeader(t, "avatar.png", []byte("same image bytes"))
	fh1.Header = textproto.MIMEHeader{"Content-Type": []string{"image/png"}}
	first, err := svc.Upload(ctx, fh1, nil, "user-001", "avatars")
	if err != nil {
		t.Fatalf("first Upload failed: %v", err)
	}

	fh2 := makeMultipartFileHeader(t, "avatar.png", []byte("same image bytes"))
	fh2.Header = textproto.MIMEHeader{"Content-Type": []string{"image/png"}}
	second, err := svc.Upload(ctx, fh2, nil, "user-002", "avatars")
	if err != nil {
		t.Fatalf("second Upload failed: %v", err)
	}

	if second.ID != first.ID {
		t.Fatalf("expected duplicate upload to return attachment %d, got %d", first.ID, second.ID)
	}
	if first.ContentHash == "" {
		t.Fatal("expected content hash to be set")
	}
	if first.Directory != "avatars" {
		t.Fatalf("expected directory avatars, got %q", first.Directory)
	}
	if first.Disk != "local" || first.Driver != "local" {
		t.Fatalf("expected local disk info, got disk=%q driver=%q", first.Disk, first.Driver)
	}

	count, err := client.Attachment.Query().Count(ctx)
	if err != nil {
		t.Fatalf("count attachments: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one attachment record, got %d", count)
	}
}

func TestIntegration_Upload_SameContentDifferentOriginalNameCreatesRecord(t *testing.T) {
	client, cleanup := setupDeepIntegration(t)
	defer cleanup()
	ctx := withPermissionSystemOperator(t.Context())

	fs, _ := makeLocalFilesystem(t)
	svc := &AttachmentService{
		CRUDBase:   &CRUDBase{Client: client},
		filesystem: fs,
	}

	fh1 := makeMultipartFileHeader(t, "avatar.png", []byte("same image bytes"))
	fh1.Header = textproto.MIMEHeader{"Content-Type": []string{"image/png"}}
	first, err := svc.Upload(ctx, fh1, nil, "user-001", "avatars")
	if err != nil {
		t.Fatalf("first Upload failed: %v", err)
	}

	fh2 := makeMultipartFileHeader(t, "logo.png", []byte("same image bytes"))
	fh2.Header = textproto.MIMEHeader{"Content-Type": []string{"image/png"}}
	second, err := svc.Upload(ctx, fh2, nil, "user-002", "avatars")
	if err != nil {
		t.Fatalf("second Upload failed: %v", err)
	}

	if second.ID == first.ID {
		t.Fatal("expected different original_name to create a separate attachment record")
	}

	count, err := client.Attachment.Query().Count(ctx)
	if err != nil {
		t.Fatalf("count attachments: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected two attachment records, got %d", count)
	}
}

func TestIntegration_Upload_RejectsUnsafeDirectory(t *testing.T) {
	client, cleanup := setupDeepIntegration(t)
	defer cleanup()
	ctx := withPermissionSystemOperator(t.Context())

	fs, _ := makeLocalFilesystem(t)
	svc := &AttachmentService{
		CRUDBase:   &CRUDBase{Client: client},
		filesystem: fs,
	}

	fh := makeMultipartFileHeader(t, "avatar.png", []byte("image bytes"))
	fh.Header = textproto.MIMEHeader{"Content-Type": []string{"image/png"}}
	if _, err := svc.Upload(ctx, fh, nil, "user-001", "../avatars"); err == nil {
		t.Fatal("expected unsafe directory to be rejected")
	}
}

// ======================== UploadChunk ========================

func TestIntegration_UploadChunk_Success(t *testing.T) {
	client, cleanup := setupDeepIntegration(t)
	defer cleanup()
	ctx := withPermissionSystemOperator(t.Context())

	_ = client
	t.Cleanup(func() { os.RemoveAll("./data") })
	os.MkdirAll("./data/storage/chunks", 0755)

	svc := &AttachmentService{}

	// init session
	_, err := svc.initChunkSession("upload-001", &ChunkInitRequest{
		Filename:    "bigfile.bin",
		FileSize:    1024 * 1024 * 3,
		ChunkSize:   1024 * 1024,
		TotalChunks: 3,
		MimeType:    "application/octet-stream",
	})
	if err != nil {
		t.Fatalf("initChunkSession: %v", err)
	}

	// upload first chunk (1MB)
	chunkData := bytes.Repeat([]byte("A"), 1024*1024)
	fh := makeMultipartFileHeader(t, "chunk1", chunkData)

	resp, err := svc.UploadChunk(ctx, "upload-001", 0, fh)
	if err != nil {
		t.Fatalf("UploadChunk[0] failed: %v", err)
	}
	if !resp.Received {
		t.Error("expected Received=true")
	}

	// verify chunk file on disk
	chunkPath := filepath.Join("./data/storage/chunks", "upload-001", "0")
	if _, err := os.Stat(chunkPath); os.IsNotExist(err) {
		t.Error("chunk file not written to disk")
	}
}

func TestIntegration_UploadChunk_SessionNotFound(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll("./data") })
	os.MkdirAll("./data/storage/chunks", 0755)

	svc := &AttachmentService{}
	ctx := context.Background()
	fh := makeMultipartFileHeader(t, "chunk", []byte("data"))

	_, err := svc.UploadChunk(ctx, "nonexistent-id", 0, fh)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestIntegration_UploadChunk_InvalidIndex(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll("./data") })
	os.MkdirAll("./data/storage/chunks", 0755)

	svc := &AttachmentService{}
	ctx := context.Background()

	_, err := svc.initChunkSession("idx-test", &ChunkInitRequest{
		Filename:    "test.bin",
		FileSize:    2048,
		ChunkSize:   1024,
		TotalChunks: 2,
		MimeType:    "text/plain",
	})
	if err != nil {
		t.Fatalf("initChunkSession: %v", err)
	}

	fh := makeMultipartFileHeader(t, "chunk", []byte("data"))
	_, err = svc.UploadChunk(ctx, "idx-test", 99, fh)
	if err == nil {
		t.Fatal("expected error for invalid index, got nil")
	}
}

func TestIntegration_UploadChunk_Expired(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll("./data") })
	os.MkdirAll("./data/storage/chunks", 0755)

	svc := &AttachmentService{}
	ctx := context.Background()

	// manually create an expired session
	session := &chunkSession{
		UploadID:    "expired-session",
		Filename:    "expired.bin",
		FileSize:    1024,
		ChunkSize:   1024,
		TotalChunks: 1,
		MimeType:    "text/plain",
		Uploaded:    make(map[int]bool),
		CreatedAt:   time.Now().Add(-2 * time.Hour),
		ExpiresAt:   time.Now().Add(-1 * time.Hour),
		sem:         make(chan struct{}, maxConcurrentChunksPerSession),
	}
	chunkStore.Store("expired-session", session)

	fh := makeMultipartFileHeader(t, "chunk", []byte("data"))
	_, err := svc.UploadChunk(ctx, "expired-session", 0, fh)
	if err == nil {
		t.Fatal("expected expired error, got nil")
	}
}

// ======================== MergeChunks ========================

func TestIntegration_MergeChunks_Success(t *testing.T) {
	client, cleanup := setupDeepIntegration(t)
	defer cleanup()
	ctx := withPermissionSystemOperator(t.Context())

	t.Cleanup(func() { os.RemoveAll("./data") })

	fs, _ := makeLocalFilesystem(t)
	svc := &AttachmentService{
		CRUDBase:   &CRUDBase{Client: client},
		filesystem: fs,
	}

	// create session and write chunks
	uploadID := "merge-001"
	chunkDir := filepath.Join("./data/storage/chunks", uploadID)
	os.MkdirAll(chunkDir, 0755)

	chunk1Data := []byte("Hello from chunk 1\n")
	chunk2Data := []byte("Hello from chunk 2\n")
	os.WriteFile(filepath.Join(chunkDir, "0"), chunk1Data, 0644)
	os.WriteFile(filepath.Join(chunkDir, "1"), chunk2Data, 0644)

	session := &chunkSession{
		UploadID:    uploadID,
		Filename:    "merged.txt",
		FileSize:    int64(len(chunk1Data) + len(chunk2Data)),
		ChunkSize:   1024,
		TotalChunks: 2,
		MimeType:    "text/plain",
		GroupID:     nil,
		Uploaded:    map[int]bool{0: true, 1: true},
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(1 * time.Hour),
		sem:         make(chan struct{}, maxConcurrentChunksPerSession),
	}
	chunkStore.Store(uploadID, session)

	resp, err := svc.MergeChunks(ctx, uploadID, "user-merge")
	if err != nil {
		t.Fatalf("MergeChunks failed: %v", err)
	}
	if resp.Name != "merged.txt" {
		t.Errorf("want merged.txt, got %s", resp.Name)
	}
	if resp.UploadedBy != "user-merge" {
		t.Errorf("want user-merge, got %s", resp.UploadedBy)
	}

	// verify merged file content
	got, err := fs.Get(resp.Path)
	if err != nil {
		t.Fatalf("read merged file: %v", err)
	}
	expected := string(chunk1Data) + string(chunk2Data)
	if string(got) != expected {
		t.Errorf("content mismatch: got len=%d, want len=%d", len(got), len(expected))
	}
}

func TestIntegration_MergeChunks_SessionNotFound(t *testing.T) {
	client, cleanup := setupDeepIntegration(t)
	defer cleanup()
	t.Cleanup(func() { os.RemoveAll("./data") })
	os.MkdirAll("./data/storage/chunks", 0755)

	svc := &AttachmentService{CRUDBase: &CRUDBase{Client: client}}
	ctx := withPermissionSystemOperator(t.Context())

	_, err := svc.MergeChunks(ctx, "nonexistent-merge", "user-1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestIntegration_MergeChunks_Incomplete(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll("./data") })
	os.MkdirAll("./data/storage/chunks", 0755)

	client, cleanup := setupDeepIntegration(t)
	defer cleanup()

	svc := &AttachmentService{CRUDBase: &CRUDBase{Client: client}}
	ctx := withPermissionSystemOperator(t.Context())

	uploadID := "incomplete-001"
	chunkDir := filepath.Join("./data/storage/chunks", uploadID)
	os.MkdirAll(chunkDir, 0755)
	os.WriteFile(filepath.Join(chunkDir, "0"), []byte("chunk0"), 0644)

	session := &chunkSession{
		UploadID:    uploadID,
		Filename:    "incomplete.txt",
		FileSize:    2048,
		ChunkSize:   1024,
		TotalChunks: 2,
		MimeType:    "text/plain",
		Uploaded:    map[int]bool{0: true},
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(1 * time.Hour),
		sem:         make(chan struct{}, maxConcurrentChunksPerSession),
	}
	chunkStore.Store(uploadID, session)

	_, err := svc.MergeChunks(ctx, uploadID, "user-1")
	if err == nil {
		t.Fatal("expected incomplete error, got nil")
	}
}

// ======================== UnifiedUpload ========================

func TestIntegration_UnifiedUpload_Simple(t *testing.T) {
	client, cleanup := setupDeepIntegration(t)
	defer cleanup()
	ctx := withPermissionSystemOperator(t.Context())

	fs, _ := makeLocalFilesystem(t)
	svc := &AttachmentService{
		CRUDBase:   &CRUDBase{Client: client},
		filesystem: fs,
	}

	fh := makeMultipartFileHeader(t, "simple.txt", []byte("simple content"))
	fh.Header = textproto.MIMEHeader{"Content-Type": []string{"text/plain"}}

	resp, err := svc.UnifiedUpload(ctx, &UnifiedUploadRequest{
		FileHeader:  fh,
		Filename:    "simple.txt",
		UserID:      "user-simple",
		MimeType:    "text/plain",
		TotalChunks: 0,
	})
	if err != nil {
		t.Fatalf("UnifiedUpload simple failed: %v", err)
	}
	if resp.Attachment == nil {
		t.Fatal("expected Attachment response")
	}
	if resp.Attachment.Name != "simple.txt" {
		t.Errorf("want simple.txt, got %s", resp.Attachment.Name)
	}
}

func TestIntegration_UnifiedUpload_Sharded(t *testing.T) {
	client, cleanup := setupDeepIntegration(t)
	defer cleanup()
	ctx := withPermissionSystemOperator(t.Context())

	t.Cleanup(func() { os.RemoveAll("./data") })

	fs, _ := makeLocalFilesystem(t)
	svc := &AttachmentService{
		CRUDBase:   &CRUDBase{Client: client},
		filesystem: fs,
	}

	uploadID := ""

	// === first chunk ===
	chunkData0 := []byte("AAAA")
	fh0 := makeMultipartFileHeader(t, "sharded.bin", chunkData0)
	fh0.Header = textproto.MIMEHeader{"Content-Type": []string{"application/octet-stream"}}

	resp0, err := svc.UnifiedUpload(ctx, &UnifiedUploadRequest{
		FileHeader:  fh0,
		Filename:    "sharded.bin",
		FileSize:    8,
		ChunkSize:   4,
		TotalChunks: 2,
		ChunkIndex:  0,
		UploadID:    uploadID,
		UserID:      "user-sharded",
		MimeType:    "application/octet-stream",
	})
	if err != nil {
		t.Fatalf("UnifiedUpload chunk 0 failed: %v", err)
	}
	if !resp0.Received {
		t.Error("first chunk expected Received=true")
	}
	uploadID = resp0.UploadID
	if uploadID == "" {
		t.Fatal("first chunk should return UploadID")
	}

	// === last chunk (auto merge) ===
	chunkData1 := []byte("BBBB")
	fh1 := makeMultipartFileHeader(t, "sharded.bin", chunkData1)
	fh1.Header = textproto.MIMEHeader{"Content-Type": []string{"application/octet-stream"}}

	resp1, err := svc.UnifiedUpload(ctx, &UnifiedUploadRequest{
		FileHeader:  fh1,
		Filename:    "sharded.bin",
		FileSize:    8,
		ChunkSize:   4,
		TotalChunks: 2,
		ChunkIndex:  1,
		UploadID:    uploadID,
		UserID:      "user-sharded",
	})
	if err != nil {
		t.Fatalf("UnifiedUpload chunk 1 (last) failed: %v", err)
	}
	if resp1.Attachment == nil {
		t.Fatal("last chunk expected Attachment")
	}
	if resp1.Attachment.Name != "sharded.bin" {
		t.Errorf("want sharded.bin, got %s", resp1.Attachment.Name)
	}

	// verify merged content
	got, err := fs.Get(resp1.Attachment.Path)
	if err != nil {
		t.Fatalf("read merged file: %v", err)
	}
	if string(got) != "AAAABBBB" {
		t.Errorf("want AAAABBBB, got %s", string(got))
	}
}

func TestIntegration_UnifiedUpload_ShardedDirectoryAndDuplicate(t *testing.T) {
	client, cleanup := setupDeepIntegration(t)
	defer cleanup()
	ctx := withPermissionSystemOperator(t.Context())

	t.Cleanup(func() { os.RemoveAll("./data") })
	fs, _ := makeLocalFilesystem(t)
	svc := &AttachmentService{
		CRUDBase:   &CRUDBase{Client: client},
		filesystem: fs,
	}

	uploadSharded := func(userID string) *AttachmentResponse {
		t.Helper()
		firstChunk := makeMultipartFileHeader(t, "notes.txt", []byte("hello "))
		firstChunk.Header = textproto.MIMEHeader{"Content-Type": []string{"text/plain"}}
		resp0, err := svc.UnifiedUpload(ctx, &UnifiedUploadRequest{
			FileHeader:  firstChunk,
			ChunkIndex:  0,
			TotalChunks: 2,
			Filename:    "notes.txt",
			FileSize:    int64(len("hello world")),
			ChunkSize:   int64(len("hello ")),
			MimeType:    "text/plain",
			UserID:      userID,
			Directory:   "docs/chunks",
		})
		if err != nil {
			t.Fatalf("first chunk failed: %v", err)
		}
		if resp0.UploadID == "" {
			t.Fatal("expected upload id")
		}

		secondChunk := makeMultipartFileHeader(t, "notes.txt", []byte("world"))
		secondChunk.Header = textproto.MIMEHeader{"Content-Type": []string{"text/plain"}}
		resp1, err := svc.UnifiedUpload(ctx, &UnifiedUploadRequest{
			FileHeader:  secondChunk,
			UploadID:    resp0.UploadID,
			ChunkIndex:  1,
			TotalChunks: 2,
			Filename:    "notes.txt",
			FileSize:    int64(len("hello world")),
			ChunkSize:   int64(len("world")),
			MimeType:    "text/plain",
			UserID:      userID,
			Directory:   "ignored/after/start",
		})
		if err != nil {
			t.Fatalf("second chunk failed: %v", err)
		}
		if resp1.Attachment == nil {
			t.Fatal("expected merged attachment")
		}
		return resp1.Attachment
	}

	first := uploadSharded("user-chunk-1")
	if first.Directory != "docs/chunks" {
		t.Fatalf("expected directory docs/chunks, got %q", first.Directory)
	}
	if first.ContentHash == "" {
		t.Fatal("expected content hash")
	}

	second := uploadSharded("user-chunk-2")
	if second.ID != first.ID {
		t.Fatalf("expected duplicate sharded upload to return attachment %d, got %d", first.ID, second.ID)
	}
}

// ======================== Cache integration (Blacklist/Permission/Role) ========================

func TestIntegration_CacheAllPaths(t *testing.T) {
	client, cleanup := setupDeepIntegration(t)
	defer cleanup()
	ctx := withPermissionSystemOperator(t.Context())

	// === create memory cache ===
	cfg := &cmfConfig.Config{}
	cfg.Cache.Default = "memory"
	cfg.Cache.Stores = map[string]struct {
		Driver     string `mapstructure:"driver"`
		DefaultTTL int    `mapstructure:"default_ttl"`
		Options    any    `mapstructure:"options"`
	}{
		"memory": {Driver: "memory", DefaultTTL: 3600},
	}
	cacheInst := cmfCache.NewCache(ctx, cfg)

	// === Blacklist with cache ===
	bs := &BlacklistService{cache: cacheInst, client: client}
	err := bs.RecordLogout(ctx, "uid-logout-test", 10*time.Minute)
	if err != nil {
		t.Fatalf("RecordLogout with cache: %v", err)
	}
	loggedOut, err := bs.IsLoggedOut(ctx, "uid-logout-test")
	if err != nil {
		t.Fatalf("IsLoggedOut with cache: %v", err)
	}
	if !loggedOut {
		t.Error("expected logged-out user returns true")
	}
	notLoggedOut, err := bs.IsLoggedOut(ctx, "uid-never-logged-in")
	if err != nil {
		t.Fatalf("IsLoggedOut: %v", err)
	}
	if notLoggedOut {
		t.Error("expected never-logged-in user returns false")
	}

	// === Permission cache ===
	permSvc := NewPermissionService(cacheInst)
	permSvc.clearUserPermissionsCache(ctx, "uid-cache-perm", "default")

	_, err = client.Role.Create().
		SetName("perm-cache-role").
		SetDisplayName("perm cache role").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role: %v", err)
	}

	err = permSvc.AssignPermissionsToRole(ctx, "", "perm-cache-role", "default", []PermissionRule{
		{Object: "/api/cached", Action: "GET"},
	})
	if err != nil {
		t.Fatalf("assign permission: %v", err)
	}

	perms, err := permSvc.GetRolePermissions(ctx, "perm-cache-role", "default")
	if err != nil {
		t.Fatalf("get permissions: %v", err)
	}
	if len(perms) != 1 {
		t.Errorf("want 1 perm, got %d", len(perms))
	}

	// second call (cache hit)
	perms2, err := permSvc.GetRolePermissions(ctx, "perm-cache-role", "default")
	if err != nil {
		t.Fatalf("second get permissions: %v", err)
	}
	if len(perms2) != 1 {
		t.Errorf("want 1 perm, got %d", len(perms2))
	}

	// === Role cache ===
	roleSvc := NewRoleService(cacheInst)
	roleCache, err := client.Role.Create().
		SetName("role-cache-role").
		SetDisplayName("role cache role").
		SetStatus(role.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create role: %v", err)
	}

	u, err := client.User.Create().
		SetEmail("rolecache@test.com").
		SetUID("uid-role-cache").
		SetUsername("rolecacheuser").
		SetPassword("pass").
		SetPasswordSalt("salt").
		SetStatus(user.StatusActive).
		Save(ctx)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	client.UserRole.Create().
		SetUserID(u.ID).
		SetRoleID(roleCache.ID).
		Save(ctx)

	roleSvc.clearUserPermissionsByRoleID(ctx, roleCache.ID, "default")
	roleSvc.clearUserPermissionsByRoleID(ctx, 99999, "default")

	_ = cfg
}

// ======================== Bootstrap auth (Login/RefreshToken/ChangePassword) ========================

func TestIntegration_BootstrapBasedAuth(t *testing.T) {
	client, cleanup := setupDeepIntegration(t)
	defer cleanup()

	cfg := &cmfConfig.Config{}
	cfg.App.Name = "test-admin"
	cfg.App.Secret = "test-secret-for-auth"
	cfg.App.LoginExpires = 86400
	cfg.App.RefreshExpires = 604800

	bs := bootstrap.NewBootstrap()
	bs.RegisterService("config", cfg)
	prevBS := global.Bootstrap
	global.Bootstrap = bs
	t.Cleanup(func() { global.Bootstrap = prevBS })

	ctx := withPermissionSystemOperator(t.Context())
	hashedPassword, salt, _ := crypto.HashPassword("Tt!12345")
	client.User.Create().
		SetEmail("authtest@example.com").
		SetUID("uid-auth-test").
		SetUsername("authtest").
		SetPassword(hashedPassword).
		SetPasswordSalt(salt).
		SetStatus(user.StatusActive).
		Save(ctx)

	svc := NewUserService()

	// === Login ===
	resp, err := svc.Login(ctx, &LoginRequest{
		Email:    "authtest@example.com",
		Password: "Tt!12345",
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if resp.AccessToken == "" {
		t.Fatal("expected access token")
	}
	if resp.RefreshToken == "" {
		t.Fatal("expected refresh token")
	}

	// === RefreshToken ===
	refreshResp, err := svc.RefreshToken(ctx, resp.RefreshToken)
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if refreshResp.AccessToken == "" {
		t.Fatal("expected new access token after refresh")
	}

	// === ChangePassword ===
	err = svc.ChangePassword(ctx, "uid-auth-test", "Tt!12345", "Tt!67890")
	if err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	// === login with new password ===
	_, err = svc.Login(ctx, &LoginRequest{
		Email:    "authtest@example.com",
		Password: "Tt!67890",
	})
	if err != nil {
		t.Fatalf("new password login: %v", err)
	}
}

// ======================== ConfigService cache paths ========================

func TestIntegration_ConfigService_CachePaths(t *testing.T) {
	client, cleanup := setupDeepIntegration(t)
	defer cleanup()
	ctx := withPermissionSystemOperator(t.Context())

	cfg := &cmfConfig.Config{}
	cfg.Cache.Default = "memory"
	cfg.Cache.Stores = map[string]struct {
		Driver     string `mapstructure:"driver"`
		DefaultTTL int    `mapstructure:"default_ttl"`
		Options    any    `mapstructure:"options"`
	}{
		"memory": {Driver: "memory", DefaultTTL: 3600},
	}
	cacheInst := cmfCache.NewCache(ctx, cfg)

	_, err := client.SystemConfig.Create().
		SetName("cache config").SetKey("cache_config").
		SetValue("cached_val").SetType("string").SetGroup("cache_test").
		SetStatus(1).Save(ctx)
	if err != nil {
		t.Fatalf("create config: %v", err)
	}

	svc := NewConfigService(cacheInst)

	// first fetch (writes cache)
	resp1, err := svc.GetByKey(ctx, "cache_config")
	if err != nil {
		t.Fatalf("GetByKey: %v", err)
	}
	if resp1.Value != "cached_val" {
		t.Errorf("want cached_val, got %s", resp1.Value)
	}

	// second fetch (cache hit)
	resp2, err := svc.GetByKey(ctx, "cache_config")
	if err != nil {
		t.Fatalf("second GetByKey: %v", err)
	}
	if resp2.Value != "cached_val" {
		t.Errorf("want cached_val, got %s", resp2.Value)
	}

	// clear cache and re-fetch
	svc.clearConfigCache(ctx, "cache_config")
	resp3, err := svc.GetByKey(ctx, "cache_config")
	if err != nil {
		t.Fatalf("after cache clear: %v", err)
	}
	if resp3.Value != "cached_val" {
		t.Errorf("want cached_val, got %s", resp3.Value)
	}

	// GetByGroup
	cfgs, err := svc.GetByGroup(ctx, "cache_test")
	if err != nil {
		t.Fatalf("GetByGroup: %v", err)
	}
	if len(cfgs) != 1 {
		t.Errorf("want 1, got %d", len(cfgs))
	}
}

// ======================== full flow: init + 2 chunks + merge ========================

func TestIntegration_UploadChunk_FullFlow(t *testing.T) {
	t.Cleanup(func() { os.RemoveAll("./data") })
	os.MkdirAll("./data/storage/chunks", 0755)

	client, cleanup := setupDeepIntegration(t)
	defer cleanup()
	ctx := withPermissionSystemOperator(t.Context())

	_ = client

	fs, _ := makeLocalFilesystem(t)
	svc := &AttachmentService{
		CRUDBase:   &CRUDBase{Client: client},
		filesystem: fs,
	}

	// init
	_, err := svc.initChunkSession("full-flow-001", &ChunkInitRequest{
		Filename:    "full_flow.dat",
		FileSize:    2048,
		ChunkSize:   1024,
		TotalChunks: 2,
		MimeType:    "text/plain",
	})
	if err != nil {
		t.Fatalf("initChunkSession: %v", err)
	}

	// upload chunk 0
	hdr0 := makeMultipartFileHeader(t, "chunk0", bytes.Repeat([]byte{0x30}, 1024))
	_, err = svc.UploadChunk(ctx, "full-flow-001", 0, hdr0)
	if err != nil {
		t.Fatalf("UploadChunk 0: %v", err)
	}

	// upload chunk 1
	hdr1 := makeMultipartFileHeader(t, "chunk1", bytes.Repeat([]byte{0x31}, 1024))
	_, err = svc.UploadChunk(ctx, "full-flow-001", 1, hdr1)
	if err != nil {
		t.Fatalf("UploadChunk 1: %v", err)
	}

	// merge
	resp, err := svc.MergeChunks(ctx, "full-flow-001", "user-full-flow")
	if err != nil {
		t.Fatalf("MergeChunks: %v", err)
	}
	if resp.Name != "full_flow.dat" {
		t.Errorf("want full_flow.dat, got %s", resp.Name)
	}
	if resp.Size != 2048 {
		t.Errorf("want Size=2048, got %d", resp.Size)
	}

	// verify content
	got, err := fs.Get(resp.Path)
	if err != nil {
		t.Fatalf("read merged file: %v", err)
	}
	if len(got) != 2048 {
		t.Errorf("want len=2048, got len=%d", len(got))
	}
}
