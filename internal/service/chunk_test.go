package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func ensureChunkDir(t *testing.T) {
	t.Helper()
	dir := filepath.Join(".", "data", "storage", "chunks")
	os.MkdirAll(dir, 0755)
	t.Cleanup(func() { os.RemoveAll("./data") })
}

func TestChunkInitSession_Success(t *testing.T) {
	ensureChunkDir(t)
	svc := &AttachmentService{}

	resp, err := svc.initChunkSession("test-upload-001", &ChunkInitRequest{
		Filename:    "testfile.bin",
		FileSize:    1024 * 1024 * 5,
		ChunkSize:   1024 * 1024,
		TotalChunks: 5,
		MimeType:    "application/octet-stream",
		GroupID:     nil,
	})
	if err != nil {
		t.Fatalf("初始化分片会话失败: %v", err)
	}
	if resp.UploadID != "test-upload-001" {
		t.Errorf("期望UploadID=test-upload-001, 实际=%s", resp.UploadID)
	}
	if resp.TotalChunks != 5 {
		t.Errorf("期望TotalChunks=5, 实际=%d", resp.TotalChunks)
	}
}

func TestChunkInitSession_ResumeExisting(t *testing.T) {
	ensureChunkDir(t)
	svc := &AttachmentService{}
	uploadID := "resume-001"

	_, err := svc.initChunkSession(uploadID, &ChunkInitRequest{
		Filename:    "resume.bin",
		FileSize:    1024 * 1024 * 10,
		ChunkSize:   1024 * 1024,
		TotalChunks: 10,
		MimeType:    "application/octet-stream",
	})
	if err != nil {
		t.Fatalf("首次初始化失败: %v", err)
	}

	// 创建模拟分片文件
	chunkDir := filepath.Join(".", "data", "storage", "chunks", uploadID)
	for i := range 3 {
		os.WriteFile(filepath.Join(chunkDir, string(rune('0'+i))), []byte("chunk"), 0644)
	}

	resp, err := svc.initChunkSession(uploadID, &ChunkInitRequest{
		Filename:    "resume.bin",
		FileSize:    1024 * 1024 * 10,
		ChunkSize:   1024 * 1024,
		TotalChunks: 10,
		MimeType:    "application/octet-stream",
	})
	if err != nil {
		t.Fatalf("恢复初始化失败: %v", err)
	}
	if len(resp.UploadedChunks) != 3 {
		t.Errorf("期望3个已上传分片, 实际为 %d", len(resp.UploadedChunks))
	}
}

func TestInitChunkUpload_Integration(t *testing.T) {
	ensureChunkDir(t)
	svc := &AttachmentService{}
	ctx := context.Background()

	resp, err := svc.InitChunkUpload(ctx, &ChunkInitRequest{
		Filename:    "upload.bin",
		FileSize:    1024 * 1024 * 5,
		ChunkSize:   1024 * 1024,
		TotalChunks: 5,
		MimeType:    "application/octet-stream",
	})
	if err != nil {
		t.Fatalf("初始化分片上传失败: %v", err)
	}
	if resp.UploadID == "" {
		t.Fatal("期望UploadID不为空")
	}
}

func TestInitChunkUpload_WithGroupID(t *testing.T) {
	ensureChunkDir(t)
	svc := &AttachmentService{}
	ctx := context.Background()

	groupID := 5
	resp, err := svc.InitChunkUpload(ctx, &ChunkInitRequest{
		Filename:    "group.bin",
		FileSize:    2048,
		ChunkSize:   1024,
		TotalChunks: 2,
		MimeType:    "application/pdf",
		GroupID:     &groupID,
	})
	if err != nil {
		t.Fatalf("带分组初始化上传失败: %v", err)
	}
	if resp.UploadID == "" {
		t.Fatal("期望UploadID不为空")
	}
}

func TestGetChunkStatus_NotFound(t *testing.T) {
	ensureChunkDir(t)
	svc := &AttachmentService{}

	_, err := svc.GetChunkStatus("", "nonexistent-id")
	if err == nil {
		t.Fatal("期望返回错误，但未返回")
	}
}

func TestGetChunkStatus_Found(t *testing.T) {
	ensureChunkDir(t)
	svc := &AttachmentService{}

	_, err := svc.initChunkSession("status-001", &ChunkInitRequest{
		Filename:    "status.bin",
		FileSize:    1024,
		ChunkSize:   1024,
		TotalChunks: 1,
		MimeType:    "text/plain",
	})
	if err != nil {
		t.Fatalf("初始化失败: %v", err)
	}

	resp, err := svc.GetChunkStatus("", "status-001")
	if err != nil {
		t.Fatalf("获取分片状态失败: %v", err)
	}
	if resp.UploadID != "status-001" {
		t.Errorf("期望UploadID=status-001, 实际=%s", resp.UploadID)
	}
}

func TestCancelChunkUpload_NotFound(t *testing.T) {
	ensureChunkDir(t)
	svc := &AttachmentService{}

	err := svc.CancelChunkUpload("", "nonexistent-id")
	if err != nil {
		t.Fatalf("取消不存在的会话应返回nil: %v", err)
	}
}

func TestCancelChunkUpload_Success(t *testing.T) {
	ensureChunkDir(t)
	svc := &AttachmentService{}

	_, err := svc.initChunkSession("cancel-001", &ChunkInitRequest{
		Filename:    "cancel.bin",
		FileSize:    1024,
		ChunkSize:   1024,
		TotalChunks: 1,
		MimeType:    "text/plain",
	})
	if err != nil {
		t.Fatalf("初始化失败: %v", err)
	}

	err = svc.CancelChunkUpload("", "cancel-001")
	if err != nil {
		t.Fatalf("取消分片上传失败: %v", err)
	}

	_, err = svc.GetChunkStatus("", "cancel-001")
	if err == nil {
		t.Fatal("已取消的会话查询应返回错误")
	}
}

func TestStartStopChunkSessionCleanup(t *testing.T) {
	ensureChunkDir(t)

	StartChunkSessionCleanup()

	svc := &AttachmentService{}
	_, err := svc.initChunkSession("cleanup-test", &ChunkInitRequest{
		Filename:    "cleanup.bin",
		FileSize:    1024,
		ChunkSize:   1024,
		TotalChunks: 1,
		MimeType:    "text/plain",
	})
	if err != nil {
		t.Fatalf("初始化失败: %v", err)
	}

	CleanupExpiredSessions()
	StopChunkSessionCleanup()

	if val, ok := chunkStore.Load("cleanup-test"); !ok || val == nil {
		t.Fatal("未过期的会话不应被清理")
	}
}

func TestStopChunkSessionCleanup_WithoutStart(t *testing.T) {
	StopChunkSessionCleanup()
}
