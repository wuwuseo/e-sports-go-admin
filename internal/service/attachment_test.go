package service

import (
	"errors"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"

	_ "github.com/mattn/go-sqlite3"
)

func setupAttachmentTest(t *testing.T) *ent.Client {
	client := enttest.Open(t, "sqlite3", "file:ent?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	t.Cleanup(func() { client.Close() })
	prevORM := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = prevORM })
	return client
}

func createTestAttachment(t *testing.T, client *ent.Client, name, originalName, path, mimeType, extension string, size int64, uploadedBy string) *ent.Attachment {
	ctx := t.Context()
	u, err := client.Attachment.Create().
		SetName(name).
		SetOriginalName(originalName).
		SetPath(path).
		SetMimeType(mimeType).
		SetExtension(extension).
		SetSize(size).
		SetUploadedBy(uploadedBy).
		Save(ctx)
	if err != nil {
		t.Fatalf("创建测试附件失败: %v", err)
	}
	return u
}

func TestAttachmentService_Delete_NotFound(t *testing.T) {
	client := setupAttachmentTest(t)
	ctx := t.Context()

	svc := &AttachmentService{
		CRUDBase: &CRUDBase{Client: client},
	}

	err := svc.Delete(ctx, 99999)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrAttachmentNotFound) {
		t.Errorf("期望错误为 ErrAttachmentNotFound, 实际错误: %v", err)
	}
}

func TestAttachmentService_BatchDelete_EmptyList(t *testing.T) {
	client := setupAttachmentTest(t)
	ctx := t.Context()

	svc := &AttachmentService{
		CRUDBase: &CRUDBase{Client: client},
	}

	err := svc.BatchDelete(ctx, []int{})
	if err != nil {
		t.Fatalf("空列表批量删除失败: %v", err)
	}
}

func TestAttachmentService_Get_Success(t *testing.T) {
	client := setupAttachmentTest(t)
	ctx := t.Context()

	svc := &AttachmentService{
		CRUDBase: &CRUDBase{Client: client},
	}

	a := createTestAttachment(t, client, "gettest.txt", "original.txt", "/file/path", "text/plain", "txt", 500, "user2")

	resp, err := svc.Get(ctx, a.ID)
	if err != nil {
		t.Fatalf("获取附件失败: %v", err)
	}
	if resp.Name != "gettest.txt" {
		t.Errorf("期望名称为 gettest.txt, 实际为 %s", resp.Name)
	}
	if resp.Path != "/file/path" {
		t.Errorf("期望路径为 /file/path, 实际为 %s", resp.Path)
	}
}

func TestAttachmentService_Get_NotFound(t *testing.T) {
	client := setupAttachmentTest(t)
	ctx := t.Context()

	svc := &AttachmentService{
		CRUDBase: &CRUDBase{Client: client},
	}

	_, err := svc.Get(ctx, 99999)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
}

func TestAttachmentService_List_Empty(t *testing.T) {
	client := setupAttachmentTest(t)
	ctx := t.Context()

	svc := &AttachmentService{
		CRUDBase: &CRUDBase{Client: client},
	}

	resp, err := svc.List(ctx, &ListAttachmentsRequest{
		ListRequest: ListRequest{
			PaginationRequest: PaginationRequest{Page: 1, PageSize: 10},
		},
	})
	if err != nil {
		t.Fatalf("获取空列表失败: %v", err)
	}
	if resp.Total != 0 {
		t.Errorf("期望总数为 0, 实际为 %d", resp.Total)
	}
}

func TestAttachmentService_List_ReturnsAll(t *testing.T) {
	client := setupAttachmentTest(t)
	ctx := t.Context()

	svc := &AttachmentService{
		CRUDBase: &CRUDBase{Client: client},
	}

	createTestAttachment(t, client, "file1.txt", "file1.txt", "/path1", "text/plain", "txt", 100, "u1")
	createTestAttachment(t, client, "file2.jpg", "file2.jpg", "/path2", "image/jpeg", "jpg", 200, "u2")

	resp, err := svc.List(ctx, &ListAttachmentsRequest{
		ListRequest: ListRequest{
			PaginationRequest: PaginationRequest{Page: 1, PageSize: 10},
		},
	})
	if err != nil {
		t.Fatalf("获取列表失败: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("期望总数为 2, 实际为 %d", resp.Total)
	}
}

func TestAttachmentService_List_KeywordSearch(t *testing.T) {
	client := setupAttachmentTest(t)
	ctx := t.Context()

	svc := &AttachmentService{
		CRUDBase: &CRUDBase{Client: client},
	}

	createTestAttachment(t, client, "report.pdf", "report.pdf", "/path1", "application/pdf", "pdf", 100, "u1")
	createTestAttachment(t, client, "report2.txt", "report2.txt", "/path2", "text/plain", "txt", 200, "u2")
	createTestAttachment(t, client, "notmatch.txt", "notmatch.txt", "/path3", "text/plain", "txt", 300, "u3")

	resp, err := svc.List(ctx, &ListAttachmentsRequest{
		ListRequest: ListRequest{
			PaginationRequest: PaginationRequest{Page: 1, PageSize: 10},
			Keyword:           "report",
		},
	})
	if err != nil {
		t.Fatalf("搜索失败: %v", err)
	}
	if resp.Total != 2 {
		t.Errorf("期望搜索结果为 2, 实际为 %d", resp.Total)
	}
}

func TestAttachmentService_List_MimeTypeFilter(t *testing.T) {
	client := setupAttachmentTest(t)
	ctx := t.Context()

	svc := &AttachmentService{
		CRUDBase: &CRUDBase{Client: client},
	}

	createTestAttachment(t, client, "a.jpg", "a.jpg", "/1", "image/jpeg", "jpg", 100, "u")
	createTestAttachment(t, client, "b.pdf", "b.pdf", "/2", "application/pdf", "pdf", 200, "u")

	_, err := svc.List(ctx, &ListAttachmentsRequest{
		ListRequest: ListRequest{
			PaginationRequest: PaginationRequest{Page: 1, PageSize: 10},
		},
		MimeType: "image/",
	})
	if err != nil {
		t.Fatalf("按MIME类型筛选失败: %v", err)
	}
}

func TestAttachmentService_List_Pagination(t *testing.T) {
	client := setupAttachmentTest(t)
	ctx := t.Context()

	svc := &AttachmentService{
		CRUDBase: &CRUDBase{Client: client},
	}

	for i := 0; i < 5; i++ {
		name := "file" + string(rune('0'+i))
		createTestAttachment(t, client, name, name, "/path", "text/plain", "txt", 100, "u")
	}

	resp, err := svc.List(ctx, &ListAttachmentsRequest{
		ListRequest: ListRequest{
			PaginationRequest: PaginationRequest{Page: 2, PageSize: 2},
		},
	})
	if err != nil {
		t.Fatalf("分页查询失败: %v", err)
	}
	if resp.Total != 5 {
		t.Errorf("期望总数为 5, 实际为 %d", resp.Total)
	}
}

func TestAttachmentService_GetFileData_NotFound(t *testing.T) {
	client := setupAttachmentTest(t)
	ctx := t.Context()

	svc := &AttachmentService{
		CRUDBase: &CRUDBase{Client: client},
	}

	_, _, _, err := svc.GetFileData(ctx, 99999)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
	if !errors.Is(err, ErrAttachmentNotFound) {
		t.Errorf("期望 ErrAttachmentNotFound, 实际: %v", err)
	}
}

func TestAttachmentService_MoveToGroup_Success(t *testing.T) {
	client := setupAttachmentTest(t)
	ctx := t.Context()

	svc := &AttachmentService{
		CRUDBase: &CRUDBase{Client: client},
	}

	a := createTestAttachment(t, client, "moveme.txt", "moveme.txt", "/move", "text/plain", "txt", 100, "user")

	newGroupID := 10
	err := svc.MoveToGroup(ctx, a.ID, &newGroupID)
	if err != nil {
		t.Fatalf("移动附件失败: %v", err)
	}

	updated, _ := client.Attachment.Get(ctx, a.ID)
	if updated.GroupID != 10 {
		t.Errorf("期望 GroupID 为 10, 实际为 %d", updated.GroupID)
	}
}

func TestAttachmentService_MoveToGroup_RemoveGroup(t *testing.T) {
	client := setupAttachmentTest(t)
	ctx := t.Context()

	svc := &AttachmentService{
		CRUDBase: &CRUDBase{Client: client},
	}

	a, err := client.Attachment.Create().
		SetName("cleargroup.txt").
		SetOriginalName("cleargroup.txt").
		SetPath("/cleargroup").
		SetMimeType("text/plain").
		SetExtension("txt").
		SetSize(100).
		SetGroupID(20).
		SetUploadedBy("user").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建带分组的附件失败: %v", err)
	}

	err = svc.MoveToGroup(ctx, a.ID, nil)
	if err != nil {
		t.Fatalf("移除分组失败: %v", err)
	}

	updated, _ := client.Attachment.Get(ctx, a.ID)
	if updated.GroupID != 0 {
		t.Errorf("期望 GroupID 为 0, 实际为 %d", updated.GroupID)
	}
}

func TestAttachmentService_MoveToGroup_NotFound(t *testing.T) {
	client := setupAttachmentTest(t)
	ctx := t.Context()

	svc := &AttachmentService{
		CRUDBase: &CRUDBase{Client: client},
	}

	groupID := 5
	err := svc.MoveToGroup(ctx, 99999, &groupID)
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
}

func TestAttachmentService_ToAttachmentResponse(t *testing.T) {
	client := setupAttachmentTest(t)
	ctx := t.Context()

	svc := &AttachmentService{
		CRUDBase: &CRUDBase{Client: client},
	}

	a, err := client.Attachment.Create().
		SetName("tresp.txt").
		SetOriginalName("tresp-ori.txt").
		SetPath("/tresp").
		SetMimeType("text/plain").
		SetExtension("txt").
		SetSize(12345).
		SetGroupID(99).
		SetUploadedBy("testuser").
		Save(ctx)
	if err != nil {
		t.Fatalf("创建附件失败: %v", err)
	}

	resp := svc.toAttachmentResponse(a)

	if resp.ID != a.ID {
		t.Errorf("期望 ID 为 %d, 实际为 %d", a.ID, resp.ID)
	}
	if resp.Name != "tresp.txt" {
		t.Errorf("期望名称为 tresp.txt, 实际为 %s", resp.Name)
	}
	if resp.GroupID != 99 {
		t.Errorf("期望 GroupID 为 99, 实际为 %d", resp.GroupID)
	}
	if resp.UploadedBy != "testuser" {
		t.Errorf("期望 UploadedBy 为 testuser, 实际为 %s", resp.UploadedBy)
	}
	if resp.CreatedAt == "" {
		t.Error("期望 CreatedAt 不为空")
	}
	if resp.UpdatedAt == "" {
		t.Error("期望 UpdatedAt 不为空")
	}
}
