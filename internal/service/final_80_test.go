package service

import (
	"net/textproto"
	"os"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/role"
	"go-admin/internal/ent/user"
	"go-admin/internal/global"

	cmfCache "github.com/wuwuseo/cmf/cache"
	cmfConfig "github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/filesystem"

	_ "github.com/mattn/go-sqlite3"
)

func setupC80(t *testing.T) *ent.Client {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
	return client
}

func makeMemCache(t *testing.T) *cmfCache.Cache[[]byte] {
	cfg := &cmfConfig.Config{}
	cfg.Cache.Default = "memory"
	cfg.Cache.Stores = map[string]struct {
		Driver     string `mapstructure:"driver"`
		DefaultTTL int    `mapstructure:"default_ttl"`
		Options    any    `mapstructure:"options"`
	}{
		"memory": {Driver: "memory", DefaultTTL: 3600},
	}
	return cmfCache.NewCache(t.Context(), cfg)
}

// ======================== Blacklist Add/Remove with cache ========================

func TestBlacklistService_AddToBlacklist_Cache(t *testing.T) {
	client := setupC80(t)
	ctx := t.Context()
	cache := makeMemCache(t)
	svc := &BlacklistService{client: client, cache: cache}

	u, _ := client.User.Create().
		SetEmail("bl-add@test.com").SetUID("uid-bl-add").SetUsername("bladd").
		SetPassword("p").SetPasswordSalt("s").SetStatus(user.StatusActive).Save(ctx)

	_ = svc.AddToBlacklist(ctx, u.ID, "test reason", "op-uid", "operator")
}

func TestBlacklistService_RemoveFromBlacklist_Cache(t *testing.T) {
	client := setupC80(t)
	ctx := t.Context()
	cache := makeMemCache(t)
	svc := &BlacklistService{client: client, cache: cache}

	u, _ := client.User.Create().
		SetEmail("bl-rm@test.com").SetUID("uid-bl-rm").SetUsername("blrm").
		SetPassword("p").SetPasswordSalt("s").SetStatus(user.StatusActive).Save(ctx)

	_ = svc.AddToBlacklist(ctx, u.ID, "test", "op", "opname")
	_ = svc.RemoveFromBlacklist(ctx, u.ID, "op-uid")
}

func TestBlacklistService_IsBlacklisted_Cached(t *testing.T) {
	client := setupC80(t)
	ctx := t.Context()
	cache := makeMemCache(t)
	svc := &BlacklistService{client: client, cache: cache}

	found, err := svc.IsBlacklisted(ctx, "uid-nonexist")
	if err != nil {
		t.Fatalf("IsBlacklisted: %v", err)
	}
	if found {
		t.Error("expected not blacklisted")
	}
}

// ======================== PermissionService getUserRoleNames ========================

func TestPermissionService_GetUserRoleNames_HasRoles(t *testing.T) {
	client := setupC80(t)
	ctx := t.Context()
	svc := NewPermissionService(nil)

	r, _ := client.Role.Create().SetName("grn-role").SetDisplayName("grn").SetStatus(role.StatusActive).Save(ctx)
	u, _ := client.User.Create().
		SetEmail("grn@test.com").SetUID("uid-grn").SetUsername("grnuser").
		SetPassword("p").SetPasswordSalt("s").SetStatus(user.StatusActive).Save(ctx)
	client.UserRole.Create().SetUserID(u.ID).SetRoleID(r.ID).Save(ctx)

	names, err := svc.getUserRoleNames(ctx, u.UID)
	if err != nil {
		t.Fatalf("getUserRoleNames: %v", err)
	}
	if len(names) != 1 || names[0] != "grn-role" {
		t.Errorf("want [grn-role], got %v", names)
	}
}

// ======================== AttachmentService Delete with filesystem ========================

func TestAttachmentService_Delete_Success(t *testing.T) {
	client := setupC80(t)
	ctx := t.Context()

	// Create temp filesystem
	cfg := &cmfConfig.Config{}
	cfg.Filesystem.Default = "local"
	cfg.Filesystem.Disks = map[string]struct {
		Driver  string `mapstructure:"driver"`
		Options any    `mapstructure:"options"`
	}{
		"local": {Driver: "local", Options: map[string]any{"root": t.TempDir()}},
	}
	fs, err := filesystem.NewFilesystemFromConfig(cfg)
	if err != nil {
		t.Fatalf("fs: %v", err)
	}

	// Store a file
	_ = fs.Set("/test/del.txt", []byte("data"), 0)

	a, _ := client.Attachment.Create().
		SetName("del.txt").SetOriginalName("del.txt").
		SetPath("/test/del.txt").SetMimeType("text/plain").
		SetExtension("txt").SetSize(4).SetUploadedBy("user").Save(ctx)

	svc := &AttachmentService{CRUDBase: &CRUDBase{Client: client}, filesystem: fs}
	err = svc.Delete(ctx, a.ID)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// ======================== AttachmentGroup Delete success ========================

func TestAttachmentGroupService_Delete_NoAttachments(t *testing.T) {
	client := setupC80(t)
	ctx := t.Context()
	svc := NewAttachmentGroupService()

	ag, _ := client.AttachmentGroup.Create().SetName("del-group").SetSort(1).Save(ctx)

	err := svc.Delete(ctx, ag.ID)
	if err != nil {
		t.Fatalf("Delete group: %v", err)
	}
}

// ======================== BlacklistService List more paths ========================

func TestBlacklistService_ListBlacklist_CachedPath(t *testing.T) {
	client := setupC80(t)
	ctx := t.Context()
	cache := makeMemCache(t)
	svc := &BlacklistService{client: client, cache: cache}

	u, _ := client.User.Create().
		SetEmail("bl-list@test.com").SetUID("uid-bl-list").SetUsername("bllist").
		SetPassword("p").SetPasswordSalt("s").SetStatus(user.StatusActive).Save(ctx)

	_ = svc.AddToBlacklist(ctx, u.ID, "list test", "op", "opname")

	resp, err := svc.ListBlacklist(ctx, 1, 10)
	if err != nil {
		t.Fatalf("ListBlacklist: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("want 1, got %d", resp.Total)
	}
}

// ======================== AttachmentGroup Get ========================

func TestAttachmentGroupService_Get_WithData(t *testing.T) {
	client := setupC80(t)
	ctx := t.Context()
	svc := NewAttachmentGroupService()

	ag, _ := client.AttachmentGroup.Create().SetName("get-group").SetSort(1).Save(ctx)

	resp, err := svc.Get(ctx, ag.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if resp.Name != "get-group" {
		t.Errorf("want get-group, got %s", resp.Name)
	}
}

// ======================== AttachmentGroup List ========================

func TestAttachmentGroupService_List_WithItems(t *testing.T) {
	client := setupC80(t)
	ctx := t.Context()
	svc := NewAttachmentGroupService()

	client.AttachmentGroup.Create().SetName("a").SetSort(1).Save(ctx)
	client.AttachmentGroup.Create().SetName("b").SetSort(2).Save(ctx)

	resp, err := svc.List(ctx, &ListGroupsRequest{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("want 2, got %d", resp.Total)
	}
}

// ======================== AttachmentGroup Create with description ========================

func TestAttachmentGroupService_Create_WithDescription(t *testing.T) {
	_ = setupC80(t)
	ctx := t.Context()
	svc := NewAttachmentGroupService()

	resp, err := svc.Create(ctx, &CreateGroupRequest{
		Name:        "desc-group",
		Description: "has description",
		Sort:        10,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if resp.Description != "has description" {
		t.Errorf("want 'has description', got '%s'", resp.Description)
	}
}

// ======================== AttachmentGroup Update with description ========================

func TestAttachmentGroupService_Update_WithDescription(t *testing.T) {
	client := setupC80(t)
	ctx := t.Context()
	svc := NewAttachmentGroupService()

	ag, _ := client.AttachmentGroup.Create().SetName("upd-group").SetSort(1).Save(ctx)

	sort := 5
	err := svc.Update(ctx, ag.ID, &UpdateGroupRequest{
		Name:        "updated-group",
		Description: "updated desc",
		Sort:        &sort,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	updated, _ := client.AttachmentGroup.Get(ctx, ag.ID)
	if updated.Name != "updated-group" {
		t.Errorf("want updated-group, got %s", updated.Name)
	}
	if updated.Description != "updated desc" {
		t.Errorf("want 'updated desc', got '%s'", updated.Description)
	}
}

// ======================== UploadChunk size exceeded ========================

func TestUploadChunk_SizeExceeded(t *testing.T) {
	cleanup := func() { os.RemoveAll("./data") }
	cleanup()
	t.Cleanup(cleanup)
	os.MkdirAll("./data/storage/chunks", 0755)
	_ = setupC80(t)

	svc := &AttachmentService{}
	ctx := t.Context()

	_, err := svc.initChunkSession("size-test", &ChunkInitRequest{
		Filename:    "big.bin",
		FileSize:    1024 * 1024 * 100,
		ChunkSize:   1024 * 1024 * 100,
		TotalChunks: 1,
		MimeType:    "application/octet-stream",
	})
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	// Create a chunk larger than maxChunkSize (110MB limit, use even bigger to be safe)
	bigData := make([]byte, 1024*1024*200) // 200MB - way beyond max
	fh := makeMultipartFileHeader(t, "bigchunk", bigData)
	// Override size
	fh.Size = maxChunkSize + 1

	_, err = svc.UploadChunk(ctx, "", "size-test", 0, fh)
	if err == nil {
		t.Fatal("expected size exceeded error")
	}
}

// ======================== UnifiedUpload size exceeded ========================

func TestUnifiedUpload_SimpleSizeExceeded(t *testing.T) {
	client := setupC80(t)
	ctx := t.Context()
	fs, _ := makeLocalFilesystem(t)
	svc := &AttachmentService{CRUDBase: &CRUDBase{Client: client}, filesystem: fs}

	bigData := make([]byte, maxSimpleFileSize+1)
	fh := makeMultipartFileHeader(t, "bigfile.bin", bigData)

	_, err := svc.UnifiedUpload(ctx, &UnifiedUploadRequest{
		FileHeader:  fh,
		Filename:    "bigfile.bin",
		UserID:      "user",
		TotalChunks: 0,
	})
	if err == nil {
		t.Fatal("expected size exceeded error")
	}
}

func TestUnifiedUpload_ChunkedSizeExceeded(t *testing.T) {
	client := setupC80(t)
	ctx := t.Context()
	fs, _ := makeLocalFilesystem(t)
	svc := &AttachmentService{CRUDBase: &CRUDBase{Client: client}, filesystem: fs}

	fh := makeMultipartFileHeader(t, "huge.bin", []byte("data"))
	fh.Header = textproto.MIMEHeader{"Content-Type": []string{"application/octet-stream"}}

	_, err := svc.UnifiedUpload(ctx, &UnifiedUploadRequest{
		FileHeader:  fh,
		Filename:    "huge.bin",
		FileSize:    maxChunkedFileSize + 1,
		ChunkSize:   1024,
		TotalChunks: 1,
		ChunkIndex:  0,
		UserID:      "user",
	})
	if err == nil {
		t.Fatal("expected chunked file size exceeded error")
	}
}

func TestUnifiedUpload_ChunkSizeExceeded(t *testing.T) {
	client := setupC80(t)
	ctx := t.Context()
	fs, _ := makeLocalFilesystem(t)
	svc := &AttachmentService{CRUDBase: &CRUDBase{Client: client}, filesystem: fs}

	fh := makeMultipartFileHeader(t, "badchunk.bin", []byte("data"))

	_, err := svc.UnifiedUpload(ctx, &UnifiedUploadRequest{
		FileHeader:  fh,
		Filename:    "badchunk.bin",
		FileSize:    1024,
		ChunkSize:   maxChunkSize + 1,
		TotalChunks: 1,
		ChunkIndex:  0,
		UserID:      "user",
	})
	if err == nil {
		t.Fatal("expected chunk size exceeded error")
	}
}
