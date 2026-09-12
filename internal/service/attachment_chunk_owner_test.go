package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// 分片会话属主隔离回归测试：前台上传复用分片会话后，
// 非属主的一切会话操作必须表现为"会话不存在"，且不得消耗/破坏属主的会话。

func TestChunkSessionOwnership_DeniesOtherUsers(t *testing.T) {
	ensureChunkDir(t)
	svc := &AttachmentService{}
	ctx := context.Background()

	_, err := svc.initChunkSession("owner-test-001", &ChunkInitRequest{
		Filename:    "owner.bin",
		FileSize:    8,
		ChunkSize:   4,
		TotalChunks: 2,
		MimeType:    "text/plain",
		UserID:      "user:5",
	})
	if err != nil {
		t.Fatalf("initChunkSession: %v", err)
	}

	fh := makeMultipartFileHeader(t, "chunk", []byte("data"))

	// 他人查询/追片/合并/取消均拒绝，且语义为"会话不存在"（不泄露存在性）
	if _, err := svc.GetChunkStatus("user:6", "owner-test-001"); !errors.Is(err, ErrChunkSessionNotFound) {
		t.Fatalf("他人查询应返回会话不存在, got %v", err)
	}
	if _, err := svc.UploadChunk(ctx, "user:6", "owner-test-001", 0, fh); !errors.Is(err, ErrChunkSessionNotFound) {
		t.Fatalf("他人追片应返回会话不存在, got %v", err)
	}
	if _, err := svc.MergeChunks(ctx, "owner-test-001", "user:6"); !errors.Is(err, ErrChunkSessionNotFound) {
		t.Fatalf("他人合并应返回会话不存在, got %v", err)
	}
	if err := svc.CancelChunkUpload("user:6", "owner-test-001"); !errors.Is(err, ErrChunkSessionNotFound) {
		t.Fatalf("他人取消应返回会话不存在, got %v", err)
	}

	// 拒绝路径不得消耗/破坏会话：属主仍可查询并追片
	if _, err := svc.GetChunkStatus("user:5", "owner-test-001"); err != nil {
		t.Fatalf("属主查询应成功, got %v", err)
	}
	if _, err := svc.UploadChunk(ctx, "user:5", "owner-test-001", 0, fh); err != nil {
		t.Fatalf("属主追片应成功, got %v", err)
	}
	if err := svc.CancelChunkUpload("user:5", "owner-test-001"); err != nil {
		t.Fatalf("属主取消应成功, got %v", err)
	}
}

func TestChunkSessionOwnership_LegacyUnownedSessionAllowed(t *testing.T) {
	ensureChunkDir(t)
	svc := &AttachmentService{}
	ctx := context.Background()

	// 遗留路径（UserID 为空）创建无属主会话，保持原行为：任意调用者可用
	_, err := svc.initChunkSession("legacy-owner-001", &ChunkInitRequest{
		Filename:    "legacy.bin",
		FileSize:    4,
		ChunkSize:   4,
		TotalChunks: 1,
		MimeType:    "text/plain",
	})
	if err != nil {
		t.Fatalf("initChunkSession: %v", err)
	}

	if _, err := svc.GetChunkStatus("", "legacy-owner-001"); err != nil {
		t.Fatalf("无属主会话应可查询, got %v", err)
	}
	fh := makeMultipartFileHeader(t, "chunk", []byte("data"))
	if _, err := svc.UploadChunk(ctx, "anyone", "legacy-owner-001", 0, fh); err != nil {
		t.Fatalf("无属主会话应可追片, got %v", err)
	}
}

func TestUnifiedUpload_ChunkOwnership_EndToEnd(t *testing.T) {
	client, cleanup := setupDeepIntegration(t)
	defer cleanup()
	ctx := withPermissionSystemOperator(t.Context())
	_ = client
	t.Cleanup(func() { os.RemoveAll("./data") })
	os.MkdirAll(filepath.Join(".", "data", "storage", "chunks"), 0755)

	fs, _ := makeLocalFilesystem(t)
	svc := &AttachmentService{
		CRUDBase:   &CRUDBase{Client: client},
		filesystem: fs,
	}

	uploadChunk := func(userID string, chunkIndex int, uploadID string, content []byte) (*UnifiedUploadResponse, error) {
		req := &UnifiedUploadRequest{
			FileHeader:  makeMultipartFileHeader(t, "e2e.txt", content),
			ChunkIndex:  chunkIndex,
			TotalChunks: 2,
			Filename:    "e2e.txt",
			FileSize:    8,
			ChunkSize:   4,
			MimeType:    "text/plain",
			UserID:      userID,
		}
		if uploadID != "" {
			req.UploadID = uploadID
		}
		return svc.UnifiedUpload(ctx, req)
	}

	// 首片：user:5 创建带属主的会话
	first, err := uploadChunk("user:5", 0, "", []byte("AAAA"))
	if err != nil {
		t.Fatalf("首片上传失败: %v", err)
	}
	if first.UploadID == "" {
		t.Fatal("首片应返回 upload_id")
	}

	// 他人拿同一 upload_id 续传必须被拒
	if _, err := uploadChunk("user:6", 1, first.UploadID, []byte("BBBB")); !errors.Is(err, ErrChunkSessionNotFound) {
		t.Fatalf("他人续传应返回会话不存在, got %v", err)
	}

	// 属主续传最后一片，自动合并，uploaded_by 记录为属主
	final, err := uploadChunk("user:5", 1, first.UploadID, []byte("BBBB"))
	if err != nil {
		t.Fatalf("属主续传失败: %v", err)
	}
	if final.Attachment == nil {
		t.Fatal("最后一片应返回附件")
	}
	if final.Attachment.UploadedBy != "user:5" {
		t.Fatalf("uploaded_by 期望 user:5, got %q", final.Attachment.UploadedBy)
	}
}
