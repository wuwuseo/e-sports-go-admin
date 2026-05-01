package service

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"

	"github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/filesystem"

	_ "github.com/mattn/go-sqlite3"
)

// testStorage is a simple in-memory storage for testing that avoids filesystem directory issues
type testStorage struct {
	basePath string
}

func (s *testStorage) Get(key string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(s.basePath, key))
	if os.IsNotExist(err) {
		return nil, nil
	}
	return data, err
}

func (s *testStorage) Set(key string, val []byte, exp time.Duration) error {
	fp := filepath.Join(s.basePath, key)
	if err := os.MkdirAll(filepath.Dir(fp), 0755); err != nil {
		return err
	}
	return os.WriteFile(fp, val, 0644)
}

func (s *testStorage) Delete(key string) error {
	err := os.Remove(filepath.Join(s.basePath, key))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s *testStorage) Reset() error { return nil }
func (s *testStorage) Close() error { return nil }

// newTestAttachmentGroupService creates an AttachmentGroupService with test DB
func newTestAttachmentGroupService(t *testing.T) (*AttachmentGroupService, *ent.Client) {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	svc := &AttachmentGroupService{client: client}
	return svc, client
}

// newTestAttachmentService creates an AttachmentService with test DB and temp filesystem
func newTestAttachmentService(t *testing.T) (*AttachmentService, *ent.Client) {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })

	tmpDir := t.TempDir()
	storage := &testStorage{basePath: tmpDir}
	fs := filesystem.NewFilesystem(storage, config.Config{})

	svc := &AttachmentService{client: client, filesystem: fs}
	return svc, client
}

// createTestFileHeader creates a multipart.FileHeader for testing
func createTestFileHeader(t *testing.T, filename, contentType string, data []byte) *multipart.FileHeader {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filename))
	h.Set("Content-Type", contentType)
	part, err := writer.CreatePart(h)
	if err != nil {
		t.Fatalf("create multipart part failed: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write multipart data failed: %v", err)
	}
	writer.Close()

	reader := multipart.NewReader(body, writer.Boundary())
	form, err := reader.ReadForm(int64(len(data) + 1024))
	if err != nil {
		t.Fatalf("read multipart form failed: %v", err)
	}
	files := form.File["file"]
	if len(files) == 0 {
		t.Fatal("no file in multipart form")
	}
	return files[0]
}

// --- AttachmentGroup Tests ---

func TestCreateAttachmentGroup(t *testing.T) {
	svc, _ := newTestAttachmentGroupService(t)
	ctx := context.Background()

	resp, err := svc.Create(ctx, &CreateGroupRequest{
		Name:        "测试分组",
		Description: "这是测试分组",
		Sort:        1,
	})
	if err != nil {
		t.Fatalf("Create group failed: %v", err)
	}
	if resp.Name != "测试分组" {
		t.Fatalf("expected name '测试分组', got '%s'", resp.Name)
	}
	if resp.Description != "这是测试分组" {
		t.Fatalf("expected description '这是测试分组', got '%s'", resp.Description)
	}
	if resp.Sort != 1 {
		t.Fatalf("expected sort 1, got %d", resp.Sort)
	}
	if resp.ID == 0 {
		t.Fatal("expected non-zero ID")
	}
}

func TestCreateAttachmentGroup_DuplicateName(t *testing.T) {
	svc, _ := newTestAttachmentGroupService(t)
	ctx := context.Background()

	_, err := svc.Create(ctx, &CreateGroupRequest{Name: "重复分组"})
	if err != nil {
		t.Fatalf("first create failed: %v", err)
	}

	_, err = svc.Create(ctx, &CreateGroupRequest{Name: "重复分组"})
	if err == nil {
		t.Fatal("expected error for duplicate name, got nil")
	}
	if err != ErrAttachmentGroupExists {
		t.Fatalf("expected ErrAttachmentGroupExists, got: %v", err)
	}
}

func TestUpdateAttachmentGroup(t *testing.T) {
	svc, _ := newTestAttachmentGroupService(t)
	ctx := context.Background()

	resp, err := svc.Create(ctx, &CreateGroupRequest{Name: "原始名称", Sort: 0})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	newSort := 5
	err = svc.Update(ctx, resp.ID, &UpdateGroupRequest{
		Name:        "更新名称",
		Description: "更新描述",
		Sort:        &newSort,
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	updated, err := svc.Get(ctx, resp.ID)
	if err != nil {
		t.Fatalf("Get after update failed: %v", err)
	}
	if updated.Name != "更新名称" {
		t.Fatalf("expected name '更新名称', got '%s'", updated.Name)
	}
	if updated.Sort != 5 {
		t.Fatalf("expected sort 5, got %d", updated.Sort)
	}
}

func TestDeleteAttachmentGroup_Empty(t *testing.T) {
	svc, _ := newTestAttachmentGroupService(t)
	ctx := context.Background()

	resp, err := svc.Create(ctx, &CreateGroupRequest{Name: "待删分组"})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	err = svc.Delete(ctx, resp.ID)
	if err != nil {
		t.Fatalf("Delete empty group failed: %v", err)
	}

	_, err = svc.Get(ctx, resp.ID)
	if err != ErrAttachmentGroupNotFound {
		t.Fatalf("expected ErrAttachmentGroupNotFound after delete, got: %v", err)
	}
}

func TestDeleteAttachmentGroup_NotEmpty(t *testing.T) {
	svc, client := newTestAttachmentGroupService(t)
	ctx := context.Background()

	resp, err := svc.Create(ctx, &CreateGroupRequest{Name: "非空分组"})
	if err != nil {
		t.Fatalf("Create group failed: %v", err)
	}

	// Create an attachment in this group
	_, err = client.Attachment.Create().
		SetName("test.txt").
		SetOriginalName("test.txt").
		SetPath("attachments/test.txt").
		SetMimeType("text/plain").
		SetExtension("txt").
		SetSize(100).
		SetGroupID(resp.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("Create attachment failed: %v", err)
	}

	err = svc.Delete(ctx, resp.ID)
	if err == nil {
		t.Fatal("expected error for non-empty group, got nil")
	}
	if err != ErrAttachmentGroupNotEmpty {
		t.Fatalf("expected ErrAttachmentGroupNotEmpty, got: %v", err)
	}
}

func TestListAttachmentGroups(t *testing.T) {
	svc, _ := newTestAttachmentGroupService(t)
	ctx := context.Background()

	// Create multiple groups
	for i := 1; i <= 3; i++ {
		_, err := svc.Create(ctx, &CreateGroupRequest{
			Name: fmt.Sprintf("分组%d", i),
			Sort: i,
		})
		if err != nil {
			t.Fatalf("Create group %d failed: %v", i, err)
		}
	}

	// List all
	listResp, err := svc.List(ctx, &ListGroupsRequest{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if listResp.Total != 3 {
		t.Fatalf("expected total 3, got %d", listResp.Total)
	}
	if len(listResp.Data) != 3 {
		t.Fatalf("expected 3 items, got %d", len(listResp.Data))
	}

	// Search by keyword
	searchResp, err := svc.List(ctx, &ListGroupsRequest{Page: 1, PageSize: 10, Keyword: "分组1"})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if searchResp.Total != 1 {
		t.Fatalf("expected search total 1, got %d", searchResp.Total)
	}
}

func TestUploadAttachment(t *testing.T) {
	svc, _ := newTestAttachmentService(t)
	ctx := context.Background()

	fileData := []byte("hello world test file content")
	fh := createTestFileHeader(t, "test-doc.txt", "text/plain", fileData)

	resp, err := svc.Upload(ctx, fh, nil, "uid-1")
	if err != nil {
		t.Fatalf("Upload failed: %v", err)
	}
	if resp.OriginalName != "test-doc.txt" {
		t.Fatalf("expected original_name 'test-doc.txt', got '%s'", resp.OriginalName)
	}
	if resp.MimeType != "text/plain" {
		t.Fatalf("expected mime_type 'text/plain', got '%s'", resp.MimeType)
	}
	if resp.Extension != "txt" {
		t.Fatalf("expected extension 'txt', got '%s'", resp.Extension)
	}
	if resp.Size != int64(len(fileData)) {
		t.Fatalf("expected size %d, got %d", len(fileData), resp.Size)
	}
	if resp.UploadedBy != "uid-1" {
		t.Fatalf("expected uploaded_by 'uid-1', got '%s'", resp.UploadedBy)
	}
}

func TestDeleteAttachment(t *testing.T) {
	svc, client := newTestAttachmentService(t)
	ctx := context.Background()

	// Create attachment directly in DB
	a, err := client.Attachment.Create().
		SetName("delete-test.txt").
		SetOriginalName("delete-test.txt").
		SetPath("attachments/delete-test.txt").
		SetMimeType("text/plain").
		SetExtension("txt").
		SetSize(50).
		Save(ctx)
	if err != nil {
		t.Fatalf("Create attachment failed: %v", err)
	}

	err = svc.Delete(ctx, a.ID)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	_, err = svc.Get(ctx, a.ID)
	if err != ErrAttachmentNotFound {
		t.Fatalf("expected ErrAttachmentNotFound after delete, got: %v", err)
	}
}

func TestBatchDeleteAttachments(t *testing.T) {
	svc, client := newTestAttachmentService(t)
	ctx := context.Background()

	ids := make([]int, 0, 3)
	for i := 0; i < 3; i++ {
		a, err := client.Attachment.Create().
			SetName(fmt.Sprintf("batch-%d.txt", i)).
			SetOriginalName(fmt.Sprintf("batch-%d.txt", i)).
			SetPath(fmt.Sprintf("attachments/batch-%d.txt", i)).
			SetMimeType("text/plain").
			SetExtension("txt").
			SetSize(10).
			Save(ctx)
		if err != nil {
			t.Fatalf("Create attachment %d failed: %v", i, err)
		}
		ids = append(ids, a.ID)
	}

	err := svc.BatchDelete(ctx, ids)
	if err != nil {
		t.Fatalf("BatchDelete failed: %v", err)
	}

	// Verify all deleted
	for _, id := range ids {
		_, err := svc.Get(ctx, id)
		if err != ErrAttachmentNotFound {
			t.Fatalf("expected ErrAttachmentNotFound for id %d, got: %v", id, err)
		}
	}
}

func TestListAttachments_Search(t *testing.T) {
	svc, client := newTestAttachmentService(t)
	ctx := context.Background()

	// Create a group
	g, err := client.AttachmentGroup.Create().
		SetName("图片分组").
		Save(ctx)
	if err != nil {
		t.Fatalf("Create group failed: %v", err)
	}

	// Create attachments with different types and groups
	attachments := []struct {
		name     string
		mimeType string
		groupID  *int
	}{
		{"photo.jpg", "image/jpeg", &g.ID},
		{"doc.pdf", "application/pdf", nil},
		{"logo.png", "image/png", &g.ID},
	}

	for _, att := range attachments {
		builder := client.Attachment.Create().
			SetName(att.name).
			SetOriginalName(att.name).
			SetPath("attachments/" + att.name).
			SetMimeType(att.mimeType).
			SetExtension("ext").
			SetSize(100)
		if att.groupID != nil {
			builder.SetGroupID(*att.groupID)
		}
		if _, err := builder.Save(ctx); err != nil {
			t.Fatalf("Create attachment '%s' failed: %v", att.name, err)
		}
	}

	// Search by keyword
	resp, err := svc.List(ctx, &ListAttachmentsRequest{Page: 1, PageSize: 10, Keyword: "photo"})
	if err != nil {
		t.Fatalf("List by keyword failed: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("expected 1 result for keyword 'photo', got %d", resp.Total)
	}

	// Filter by group
	resp, err = svc.List(ctx, &ListAttachmentsRequest{Page: 1, PageSize: 10, GroupID: &g.ID})
	if err != nil {
		t.Fatalf("List by group failed: %v", err)
	}
	if resp.Total != 2 {
		t.Fatalf("expected 2 results for group, got %d", resp.Total)
	}

	// Filter by MIME type prefix
	resp, err = svc.List(ctx, &ListAttachmentsRequest{Page: 1, PageSize: 10, MimeType: "image/"})
	if err != nil {
		t.Fatalf("List by mime type failed: %v", err)
	}
	if resp.Total != 2 {
		t.Fatalf("expected 2 results for mime type 'image/', got %d", resp.Total)
	}

	// List all
	resp, err = svc.List(ctx, &ListAttachmentsRequest{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("List all failed: %v", err)
	}
	if resp.Total != 3 {
		t.Fatalf("expected 3 total, got %d", resp.Total)
	}
}

func TestMoveAttachment(t *testing.T) {
	svc, client := newTestAttachmentService(t)
	ctx := context.Background()

	// Create two groups
	g1, err := client.AttachmentGroup.Create().SetName("分组A").Save(ctx)
	if err != nil {
		t.Fatalf("Create group1 failed: %v", err)
	}
	g2, err := client.AttachmentGroup.Create().SetName("分组B").Save(ctx)
	if err != nil {
		t.Fatalf("Create group2 failed: %v", err)
	}

	// Create attachment in group1
	a, err := client.Attachment.Create().
		SetName("movable.txt").
		SetOriginalName("movable.txt").
		SetPath("attachments/movable.txt").
		SetMimeType("text/plain").
		SetExtension("txt").
		SetSize(10).
		SetGroupID(g1.ID).
		Save(ctx)
	if err != nil {
		t.Fatalf("Create attachment failed: %v", err)
	}

	// Move to group2
	err = svc.MoveToGroup(ctx, a.ID, &g2.ID)
	if err != nil {
		t.Fatalf("MoveToGroup failed: %v", err)
	}

	moved, err := svc.Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("Get after move failed: %v", err)
	}
	if moved.GroupID != g2.ID {
		t.Fatalf("expected group_id %d after move, got %d", g2.ID, moved.GroupID)
	}

	// Move to no group (clear group)
	err = svc.MoveToGroup(ctx, a.ID, nil)
	if err != nil {
		t.Fatalf("MoveToGroup to nil failed: %v", err)
	}

	cleared, err := svc.Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("Get after clear group failed: %v", err)
	}
	if cleared.GroupID != 0 {
		t.Fatalf("expected group_id 0 after clear, got %d", cleared.GroupID)
	}
}
