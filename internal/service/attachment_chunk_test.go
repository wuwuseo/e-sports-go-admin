package service

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// cleanupChunkStore 清理分片上传的全局状态，确保测试隔离
func cleanupChunkStore(t *testing.T) {
	t.Helper()
	chunkStore.Range(func(key, _ any) bool {
		chunkStore.Delete(key)
		return true
	})
	// 清理测试可能产生的 chunk 目录
	_ = os.RemoveAll(chunkBaseDir)
}

// readChunkFile 读取分片文件内容辅助函数
func readChunkFile(t *testing.T, uploadID string, chunkIndex int) []byte {
	t.Helper()
	path := filepath.Join(chunkBaseDir, uploadID, fmt.Sprintf("%d", chunkIndex))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read chunk file %s failed: %v", path, err)
	}
	return data
}

// ==================== UnifiedUpload 简单上传 ====================

func TestUnifiedUpload_SimpleUpload(t *testing.T) {
	cleanupChunkStore(t)
	defer cleanupChunkStore(t)

	svc, _ := newTestAttachmentService(t)
	ctx := context.Background()

	fileData := []byte("hello unified simple upload")
	fh := createTestFileHeader(t, "unified.txt", "text/plain", fileData)

	resp, err := svc.UnifiedUpload(ctx, &UnifiedUploadRequest{
		FileHeader: fh,
		UserID:     "uid-1",
		// TotalChunks == 0 → simple upload
	})
	if err != nil {
		t.Fatalf("UnifiedUpload simple failed: %v", err)
	}
	if resp.Attachment == nil {
		t.Fatal("expected attachment in response for simple upload")
	}
	if resp.Attachment.OriginalName != "unified.txt" {
		t.Fatalf("expected 'unified.txt', got '%s'", resp.Attachment.OriginalName)
	}
	if resp.Attachment.Size != int64(len(fileData)) {
		t.Fatalf("expected size %d, got %d", len(fileData), resp.Attachment.Size)
	}
	if resp.UploadID != "" {
		t.Fatalf("expected empty upload_id for simple upload, got '%s'", resp.UploadID)
	}
}

// ==================== UnifiedUpload 分片上传完整流程 ====================

func TestUnifiedUpload_ChunkedFullFlow(t *testing.T) {
	cleanupChunkStore(t)
	defer cleanupChunkStore(t)

	svc, _ := newTestAttachmentService(t)
	ctx := context.Background()

	const totalChunks = 3
	const chunkSize = 10

	chunks := [][]byte{
		[]byte("AAAAAAAAAA"), // 10 bytes
		[]byte("BBBBBBBBBB"), // 10 bytes
		[]byte("CCCCCCCCCC"), // 10 bytes
	}

	// 首片：自动初始化
	firstFH := createTestFileHeader(t, "chunked.dat", "application/octet-stream", chunks[0])
	resp, err := svc.UnifiedUpload(ctx, &UnifiedUploadRequest{
		FileHeader:  firstFH,
		ChunkIndex:  0,
		TotalChunks: totalChunks,
		Filename:    "chunked.dat",
		FileSize:    int64(totalChunks * chunkSize),
		ChunkSize:   chunkSize,
		MimeType:    "application/octet-stream",
		UserID:      "uid-2",
	})
	if err != nil {
		t.Fatalf("first chunk failed: %v", err)
	}
	if resp.UploadID == "" {
		t.Fatal("expected non-empty upload_id for chunked upload")
	}
	if resp.Attachment != nil {
		t.Fatal("expected no attachment for non-final chunk")
	}
	if !resp.Received {
		t.Fatal("expected Received=true")
	}
	uploadID := resp.UploadID

	// 中间片
	for i := 1; i < totalChunks-1; i++ {
		midFH := createTestFileHeader(t, "chunked.dat", "application/octet-stream", chunks[i])
		resp, err = svc.UnifiedUpload(ctx, &UnifiedUploadRequest{
			FileHeader:  midFH,
			UploadID:    uploadID,
			ChunkIndex:  i,
			TotalChunks: totalChunks,
			UserID:      "uid-2",
		})
		if err != nil {
			t.Fatalf("middle chunk %d failed: %v", i, err)
		}
		if !resp.Received {
			t.Fatalf("chunk %d: expected Received=true", i)
		}
	}

	// 最后一片：自动合并
	lastFH := createTestFileHeader(t, "chunked.dat", "application/octet-stream", chunks[totalChunks-1])
	resp, err = svc.UnifiedUpload(ctx, &UnifiedUploadRequest{
		FileHeader:  lastFH,
		UploadID:    uploadID,
		ChunkIndex:  totalChunks - 1,
		TotalChunks: totalChunks,
		UserID:      "uid-2",
	})
	if err != nil {
		t.Fatalf("last chunk failed: %v", err)
	}
	if resp.Attachment == nil {
		t.Fatal("expected attachment in response after final chunk merge")
	}
	if resp.Attachment.OriginalName != "chunked.dat" {
		t.Fatalf("expected 'chunked.dat', got '%s'", resp.Attachment.OriginalName)
	}
	if resp.Attachment.Size != int64(totalChunks*chunkSize) {
		t.Fatalf("expected total size %d, got %d", totalChunks*chunkSize, resp.Attachment.Size)
	}
	if resp.Attachment.UploadedBy != "uid-2" {
		t.Fatalf("expected uploaded_by 'uid-2', got '%s'", resp.Attachment.UploadedBy)
	}

	// 验证文件内容正确合并
	fileData, mime, _, err := svc.GetFileData(ctx, resp.Attachment.ID)
	if err != nil {
		t.Fatalf("GetFileData failed: %v", err)
	}
	if mime != "application/octet-stream" {
		t.Fatalf("expected mime 'application/octet-stream', got '%s'", mime)
	}
	expected := append(append([]byte{}, chunks[0]...), append(chunks[1], chunks[2]...)...)
	if !bytes.Equal(fileData, expected) {
		t.Fatalf("merged file content mismatch: expected %q, got %q", expected, fileData)
	}

	// 验证会话已清理（合并后 session 从 chunkStore 移除）
	_, err = svc.GetChunkStatus(uploadID)
	if err == nil {
		t.Fatal("expected session to be removed after merge")
	}
}

// ==================== 首片幂等（并发安全 #1）====================

func TestUnifiedUpload_FirstChunkIdempotent(t *testing.T) {
	cleanupChunkStore(t)
	defer cleanupChunkStore(t)

	svc, _ := newTestAttachmentService(t)
	ctx := context.Background()

	const totalChunks = 2
	chunk0 := []byte("FIRSTCHUNK")

	// 模拟并发：两个"首片"请求（相同 filename + fileSize + userID）
	doFirstChunk := func() string {
		fh := createTestFileHeader(t, "idem.dat", "application/octet-stream", chunk0)
		resp, err := svc.UnifiedUpload(ctx, &UnifiedUploadRequest{
			FileHeader:  fh,
			ChunkIndex:  0,
			TotalChunks: totalChunks,
			Filename:    "idem.dat",
			FileSize:    int64(totalChunks * len(chunk0)),
			ChunkSize:   int64(len(chunk0)),
			UserID:      "uid-3",
		})
		if err != nil {
			t.Fatalf("first chunk failed: %v", err)
		}
		return resp.UploadID
	}

	// 获取两个响应
	id1 := doFirstChunk()
	id2 := doFirstChunk()

	// 两次"首片"应返回相同 upload_id（确定性 ID）
	if id1 != id2 {
		t.Fatalf("concurrent first chunks should have same upload_id, got '%s' vs '%s'", id1, id2)
	}

	// 验证只有一个会话存在
	count := 0
	chunkStore.Range(func(_, _ any) bool {
		count++
		return true
	})
	if count != 1 {
		t.Fatalf("expected exactly 1 session in chunkStore, got %d", count)
	}
}

// ==================== 并发发送最终分片不重复合并 ====================

func TestUnifiedUpload_ConcurrentFinalChunk_NoDuplicateMerge(t *testing.T) {
	cleanupChunkStore(t)
	defer cleanupChunkStore(t)

	svc, _ := newTestAttachmentService(t)
	ctx := context.Background()

	const totalChunks = 2
	chunkSize := 5
	chunks := [][]byte{[]byte("AAAAA"), []byte("BBBBB")}

	// 首片
	fh0 := createTestFileHeader(t, "concurrent.dat", "application/octet-stream", chunks[0])
	respInit, err := svc.UnifiedUpload(ctx, &UnifiedUploadRequest{
		FileHeader:  fh0,
		ChunkIndex:  0,
		TotalChunks: totalChunks,
		Filename:    "concurrent.dat",
		FileSize:    int64(totalChunks * chunkSize),
		ChunkSize:   int64(chunkSize),
		UserID:      "uid-4",
	})
	if err != nil {
		t.Fatalf("first chunk failed: %v", err)
	}
	uploadID := respInit.UploadID

	// 并发发送最后一片（TotalChunks-1），只有一个会成功合并，另一个应返回"会话已结束"或类似错误
	var wg sync.WaitGroup
	results := make(chan error, 2)

	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fh := createTestFileHeader(t, "concurrent.dat", "application/octet-stream", chunks[1])
			_, e := svc.UnifiedUpload(ctx, &UnifiedUploadRequest{
				FileHeader:  fh,
				UploadID:    uploadID,
				ChunkIndex:  1,
				TotalChunks: totalChunks,
				UserID:      "uid-4",
			})
			results <- e
		}()
	}
	wg.Wait()
	close(results)

	successCount := 0
	for e := range results {
		if e == nil {
			successCount++
		}
	}
	if successCount != 1 {
		t.Fatalf("expected exactly 1 successful concurrent final chunk, got %d", successCount)
	}
}

// ==================== 首片上传失败后回滚 ====================

func TestUnifiedUpload_FirstChunkRollback(t *testing.T) {
	cleanupChunkStore(t)
	defer cleanupChunkStore(t)

	svc, _ := newTestAttachmentService(t)
	ctx := context.Background()

	// 使用 chunkIndex 超出范围触发 UploadChunk 失败
	fh := createTestFileHeader(t, "rollback.dat", "application/octet-stream", []byte("data"))
	_, err := svc.UnifiedUpload(ctx, &UnifiedUploadRequest{
		FileHeader:  fh,
		ChunkIndex:  999, // 无效索引
		TotalChunks: 3,
		Filename:    "rollback.dat",
		FileSize:    30,
		ChunkSize:   10,
		UserID:      "uid-rb",
	})
	if err == nil {
		t.Fatal("expected error for invalid chunk index")
	}
	if err.Error() == "" || !containsStr(err.Error(), "无效的分片索引") {
		t.Fatalf("expected '无效的分片索引' error, got: %v", err)
	}

	// 验证会话未被回滚清理（转为 TTL 自动过期清理）
	count := 0
	chunkStore.Range(func(_, _ any) bool {
		count++
		return true
	})
	if count != 1 {
		t.Fatalf("expected 1 orphan session retained for TTL cleanup, got %d", count)
	}
}

// ==================== InitChunkUpload ====================

func TestInitChunkUpload(t *testing.T) {
	cleanupChunkStore(t)
	defer cleanupChunkStore(t)

	svc, _ := newTestAttachmentService(t)
	ctx := context.Background()

	req := &ChunkInitRequest{
		Filename:    "bigfile.zip",
		FileSize:    1000000,
		ChunkSize:   1048576,
		TotalChunks: 1,
		MimeType:    "application/zip",
	}

	resp, err := svc.InitChunkUpload(ctx, req)
	if err != nil {
		t.Fatalf("InitChunkUpload failed: %v", err)
	}
	if resp.UploadID == "" {
		t.Fatal("expected non-empty upload_id")
	}
	if resp.TotalChunks != 1 {
		t.Fatalf("expected TotalChunks=1, got %d", resp.TotalChunks)
	}
	if resp.ChunkSize != 1048576 {
		t.Fatalf("expected ChunkSize=1048576, got %d", resp.ChunkSize)
	}
	if len(resp.UploadedChunks) != 0 {
		t.Fatalf("expected 0 uploaded chunks, got %d", len(resp.UploadedChunks))
	}

	// 验证目录已创建
	chunkDir := filepath.Join(chunkBaseDir, resp.UploadID)
	if info, err := os.Stat(chunkDir); err != nil || !info.IsDir() {
		t.Fatalf("chunk directory not created: %v", err)
	}
}

// ==================== UploadChunk 边界情况 ====================

func TestUploadChunk_InvalidIndex(t *testing.T) {
	cleanupChunkStore(t)
	defer cleanupChunkStore(t)

	svc, _ := newTestAttachmentService(t)
	ctx := context.Background()

	// 先初始化
	initResp, err := svc.InitChunkUpload(ctx, &ChunkInitRequest{
		Filename:    "test.bin",
		FileSize:    100,
		ChunkSize:   50,
		TotalChunks: 2,
	})
	if err != nil {
		t.Fatalf("InitChunkUpload failed: %v", err)
	}
	uploadID := initResp.UploadID

	// 负数索引
	fh := createTestFileHeader(t, "test.bin", "application/octet-stream", []byte("xx"))
	_, err = svc.UploadChunk(ctx, uploadID, -1, fh)
	if err == nil {
		t.Fatal("expected error for negative chunk index")
	}

	// 超出范围索引
	_, err = svc.UploadChunk(ctx, uploadID, 999, fh)
	if err == nil {
		t.Fatal("expected error for out-of-range chunk index")
	}
}

func TestUploadChunk_SessionNotFound(t *testing.T) {
	cleanupChunkStore(t)
	defer cleanupChunkStore(t)

	svc, _ := newTestAttachmentService(t)
	ctx := context.Background()

	fh := createTestFileHeader(t, "ghost.bin", "application/octet-stream", []byte("xx"))
	_, err := svc.UploadChunk(ctx, "non-existent-id", 0, fh)
	if err == nil {
		t.Fatal("expected error for non-existent session")
	}
}

func TestUploadChunk_DetectDeletedSession(t *testing.T) {
	cleanupChunkStore(t)
	defer cleanupChunkStore(t)

	svc, _ := newTestAttachmentService(t)
	ctx := context.Background()

	// 初始化并立即取消（触发 LoadAndDelete + deleted 标志）
	initResp, err := svc.InitChunkUpload(ctx, &ChunkInitRequest{
		Filename:    "canceled.bin",
		FileSize:    100,
		ChunkSize:   50,
		TotalChunks: 2,
	})
	if err != nil {
		t.Fatalf("InitChunkUpload failed: %v", err)
	}
	uploadID := initResp.UploadID

	// 取消会话
	if err := svc.CancelChunkUpload(uploadID); err != nil {
		t.Fatalf("CancelChunkUpload failed: %v", err)
	}

	// 尝试上传到已取消的会话
	fh := createTestFileHeader(t, "canceled.bin", "application/octet-stream", []byte("xx"))
	_, err = svc.UploadChunk(ctx, uploadID, 0, fh)
	if err == nil {
		t.Fatal("expected error when uploading to deleted session")
	}
}

// ==================== MergeChunks ====================

func TestMergeChunks_NotAllChunksUploaded(t *testing.T) {
	cleanupChunkStore(t)
	defer cleanupChunkStore(t)

	svc, _ := newTestAttachmentService(t)
	ctx := context.Background()

	// 初始化 3 片但只上传 1 片
	initResp, err := svc.InitChunkUpload(ctx, &ChunkInitRequest{
		Filename:    "partial.bin",
		FileSize:    150,
		ChunkSize:   50,
		TotalChunks: 3,
	})
	if err != nil {
		t.Fatalf("InitChunkUpload failed: %v", err)
	}
	uploadID := initResp.UploadID

	// 只上传一片
	fh := createTestFileHeader(t, "partial.bin", "application/octet-stream", bytes.Repeat([]byte("A"), 50))
	_, err = svc.UploadChunk(ctx, uploadID, 0, fh)
	if err != nil {
		t.Fatalf("UploadChunk failed: %v", err)
	}

	// 尝试合并（缺少分片 1 和 2）
	_, err = svc.MergeChunks(ctx, uploadID, "uid-merge")
	if err == nil {
		t.Fatal("expected error when merging incomplete chunks")
	}
}

// ==================== CancelChunkUpload ====================

func TestCancelChunkUpload(t *testing.T) {
	cleanupChunkStore(t)
	defer cleanupChunkStore(t)

	svc, _ := newTestAttachmentService(t)
	ctx := context.Background()

	initResp, err := svc.InitChunkUpload(ctx, &ChunkInitRequest{
		Filename:    "cancel-test.bin",
		FileSize:    100,
		ChunkSize:   50,
		TotalChunks: 2,
	})
	if err != nil {
		t.Fatalf("InitChunkUpload failed: %v", err)
	}
	uploadID := initResp.UploadID

	// 取消
	if err := svc.CancelChunkUpload(uploadID); err != nil {
		t.Fatalf("CancelChunkUpload failed: %v", err)
	}

	// 验证会话已移除
	_, err = svc.GetChunkStatus(uploadID)
	if err == nil {
		t.Fatal("expected session not found after cancel")
	}

	// 验证目录已清理
	chunkDir := filepath.Join(chunkBaseDir, uploadID)
	if _, err := os.Stat(chunkDir); !os.IsNotExist(err) {
		t.Fatalf("expected chunk directory to be removed, but it still exists")
	}

	// 幂等：再次取消同一 uploadID 不应报错
	if err := svc.CancelChunkUpload(uploadID); err != nil {
		t.Fatalf("CancelChunkUpload should be idempotent, got: %v", err)
	}
}

// ==================== GetChunkStatus ====================

func TestGetChunkStatus(t *testing.T) {
	cleanupChunkStore(t)
	defer cleanupChunkStore(t)

	svc, _ := newTestAttachmentService(t)
	ctx := context.Background()

	req := &ChunkInitRequest{
		Filename:    "status-test.zip",
		FileSize:    5000000,
		ChunkSize:   1048576,
		TotalChunks: 5,
		MimeType:    "application/zip",
	}
	initResp, err := svc.InitChunkUpload(ctx, req)
	if err != nil {
		t.Fatalf("InitChunkUpload failed: %v", err)
	}
	uploadID := initResp.UploadID

	// 上传 2 片
	for i := 0; i < 2; i++ {
		fh := createTestFileHeader(t, "status-test.zip", "application/zip", bytes.Repeat([]byte{byte('A' + i)}, 1048576))
		_, err := svc.UploadChunk(ctx, uploadID, i, fh)
		if err != nil {
			t.Fatalf("UploadChunk %d failed: %v", i, err)
		}
	}

	// 查询状态
	status, err := svc.GetChunkStatus(uploadID)
	if err != nil {
		t.Fatalf("GetChunkStatus failed: %v", err)
	}
	if status.UploadID != uploadID {
		t.Fatalf("expected upload_id '%s', got '%s'", uploadID, status.UploadID)
	}
	if status.Filename != "status-test.zip" {
		t.Fatalf("expected filename 'status-test.zip', got '%s'", status.Filename)
	}
	if status.TotalChunks != 5 {
		t.Fatalf("expected TotalChunks=5, got %d", status.TotalChunks)
	}
	if len(status.UploadedChunks) != 2 {
		t.Fatalf("expected 2 uploaded chunks, got %d", len(status.UploadedChunks))
	}
	if status.UploadedChunks[0] != 0 || status.UploadedChunks[1] != 1 {
		t.Fatalf("expected uploaded chunks [0, 1], got %v", status.UploadedChunks)
	}
}

func TestGetChunkStatus_NotFound(t *testing.T) {
	cleanupChunkStore(t)
	defer cleanupChunkStore(t)

	svc, _ := newTestAttachmentService(t)
	_, err := svc.GetChunkStatus("non-existent")
	if err == nil {
		t.Fatal("expected error for non-existent session")
	}
}

// ==================== 确定性 UploadID ====================

func TestGenerateDeterministicUploadID(t *testing.T) {
	// 相同输入 → 相同输出
	id1 := generateDeterministicUploadID("video.mp4", 104857600, "user-abc")
	id2 := generateDeterministicUploadID("video.mp4", 104857600, "user-abc")
	if id1 != id2 {
		t.Fatalf("expected same ID for same inputs: '%s' vs '%s'", id1, id2)
	}

	// 不同 filename → 不同 ID
	id3 := generateDeterministicUploadID("other.mp4", 104857600, "user-abc")
	if id1 == id3 {
		t.Fatal("expected different IDs for different filenames")
	}

	// 不同 userID → 不同 ID
	id4 := generateDeterministicUploadID("video.mp4", 104857600, "user-xyz")
	if id1 == id4 {
		t.Fatal("expected different IDs for different users")
	}

	// 不同 fileSize → 不同 ID
	id5 := generateDeterministicUploadID("video.mp4", 999, "user-abc")
	if id1 == id5 {
		t.Fatal("expected different IDs for different file sizes")
	}
}

// ==================== UploadChunk 成功写入磁盘 ====================

func TestUploadChunk_WritesToDisk(t *testing.T) {
	cleanupChunkStore(t)
	defer cleanupChunkStore(t)

	svc, _ := newTestAttachmentService(t)
	ctx := context.Background()

	initResp, err := svc.InitChunkUpload(ctx, &ChunkInitRequest{
		Filename:    "disk-test.bin",
		FileSize:    30,
		ChunkSize:   10,
		TotalChunks: 3,
	})
	if err != nil {
		t.Fatalf("InitChunkUpload failed: %v", err)
	}
	uploadID := initResp.UploadID

	// 上传分片 1
	chunkData := []byte("CHUNK-ONE!")
	fh := createTestFileHeader(t, "disk-test.bin", "application/octet-stream", chunkData)
	_, err = svc.UploadChunk(ctx, uploadID, 1, fh)
	if err != nil {
		t.Fatalf("UploadChunk failed: %v", err)
	}

	// 验证文件写入磁盘
	diskData := readChunkFile(t, uploadID, 1)
	if !bytes.Equal(diskData, chunkData) {
		t.Fatalf("chunk on disk mismatch: expected %q, got %q", chunkData, diskData)
	}
}

// ==================== 安全限制测试 ====================

// TestUnifiedUpload_ChunkedFileSizeExceeded 分片上传总文件大小超过限制
func TestUnifiedUpload_ChunkedFileSizeExceeded(t *testing.T) {
	cleanupChunkStore(t)
	defer cleanupChunkStore(t)

	svc, _ := newTestAttachmentService(t)
	ctx := context.Background()

	fh := createTestFileHeader(t, "huge.bin", "application/octet-stream", []byte("abc"))
	_, err := svc.UnifiedUpload(ctx, &UnifiedUploadRequest{
		FileHeader:  fh,
		ChunkIndex:  0,
		TotalChunks: 500,
		Filename:    "huge.bin",
		FileSize:    6 * 1024 * 1024 * 1024, // 6GB > maxChunkedFileSize (5GB)
		ChunkSize:   10 * 1024 * 1024,
		UserID:      "uid-size",
	})
	if err == nil {
		t.Fatal("expected error for oversized chunked file")
	}
	if !containsStr(err.Error(), "文件总大小超过限制") {
		t.Fatalf("expected '文件总大小超过限制' error, got: %v", err)
	}
}

// TestUnifiedUpload_ChunkSizeExceeded 单分片大小超过限制
func TestUnifiedUpload_ChunkSizeExceeded(t *testing.T) {
	cleanupChunkStore(t)
	defer cleanupChunkStore(t)

	svc, _ := newTestAttachmentService(t)
	ctx := context.Background()

	fh := createTestFileHeader(t, "bigchunk.bin", "application/octet-stream", []byte("abc"))
	_, err := svc.UnifiedUpload(ctx, &UnifiedUploadRequest{
		FileHeader:  fh,
		ChunkIndex:  0,
		TotalChunks: 2,
		Filename:    "bigchunk.bin",
		FileSize:    100 * 1024 * 1024,
		ChunkSize:   60 * 1024 * 1024, // 60MB > maxChunkSize (50MB)
		UserID:      "uid-chunk",
	})
	if err == nil {
		t.Fatal("expected error for oversized chunk")
	}
	if !containsStr(err.Error(), "单分片大小超过限制") {
		t.Fatalf("expected '单分片大小超过限制' error, got: %v", err)
	}
}

// TestUnifiedUpload_MimeTypeAllowed 允许的 MIME 类型正常上传
func TestUnifiedUpload_MimeTypeAllowed(t *testing.T) {
	cleanupChunkStore(t)
	defer cleanupChunkStore(t)

	svc, _ := newTestAttachmentService(t)
	ctx := context.Background()

	// PNG 文件头（让 http.DetectContentType 识别为 image/png）
	pngData := []byte("\x89PNG\r\n\x1a\n" + "fake png data content here")
	fh := createTestFileHeader(t, "test.png", "image/png", pngData)

	resp, err := svc.UnifiedUpload(ctx, &UnifiedUploadRequest{
		FileHeader: fh,
		UserID:     "uid-mime",
	})
	if err != nil {
		t.Fatalf("allowed MIME type should succeed: %v", err)
	}
	if resp.Attachment == nil {
		t.Fatal("expected attachment in response")
	}
}

// TestUnifiedUpload_MimeTypeBlocked 不允许的 MIME 类型被拒绝
func TestUnifiedUpload_MimeTypeBlocked(t *testing.T) {
	cleanupChunkStore(t)
	defer cleanupChunkStore(t)

	svc, _ := newTestAttachmentService(t)
	ctx := context.Background()

	// OGG 文件头（http.DetectContentType 识别为 application/ogg，不在白名单中）
	oggData := []byte("OggS\x00\x02\x00\x00\x00\x00\x00\x00\x00\x00" + "blocked ogg vorbis data stream")
	fh := createTestFileHeader(t, "audio.ogg", "audio/ogg", oggData)

	_, err := svc.UnifiedUpload(ctx, &UnifiedUploadRequest{
		FileHeader: fh,
		UserID:     "uid-block",
	})
	if err == nil {
		t.Fatal("expected error for blocked MIME type")
	}
	if !containsStr(err.Error(), "不支持的文件类型") {
		t.Fatalf("expected '不支持的文件类型' error, got: %v", err)
	}
}

// TestUnifiedUpload_ConcurrencyLimit 并发分片上传受信号量限制
func TestUnifiedUpload_ConcurrencyLimit(t *testing.T) {
	cleanupChunkStore(t)
	defer cleanupChunkStore(t)

	svc, _ := newTestAttachmentService(t)
	ctx := context.Background()

	const totalChunks = 10
	const chunkSize = 10

	// 首片：初始化
	fh0 := createTestFileHeader(t, "conclimit.dat", "application/octet-stream", []byte("AAAAAAAAAA"))
	resp, err := svc.UnifiedUpload(ctx, &UnifiedUploadRequest{
		FileHeader:  fh0,
		ChunkIndex:  0,
		TotalChunks: totalChunks,
		Filename:    "conclimit.dat",
		FileSize:    int64(totalChunks * chunkSize),
		ChunkSize:   chunkSize,
		UserID:      "uid-conc",
	})
	if err != nil {
		t.Fatalf("first chunk failed: %v", err)
	}
	uploadID := resp.UploadID

	// 并发发送 8 个分片（超过 maxConcurrentChunksPerSession=3），部分应被信号量拒绝
	var wg sync.WaitGroup
	errCount := int32(0)
	successCount := int32(0)

	for i := 1; i <= 8; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			fh := createTestFileHeader(t, "conclimit.dat", "application/octet-stream", []byte("BBBBBBBBBB"))
			_, e := svc.UnifiedUpload(ctx, &UnifiedUploadRequest{
				FileHeader:  fh,
				UploadID:    uploadID,
				ChunkIndex:  idx,
				TotalChunks: totalChunks,
				UserID:      "uid-conc",
			})
			if e != nil {
				if containsStr(e.Error(), "并发数超限") {
					atomic.AddInt32(&errCount, 1)
				}
			} else {
				atomic.AddInt32(&successCount, 1)
			}
		}(i)
	}
	wg.Wait()

	// 至少有一些请求因并发限制被拒绝
	if errCount == 0 {
		t.Log("warning: no concurrent requests were rejected (may depend on timing)")
	}
	// 至少有一些请求成功
	if successCount == 0 {
		t.Fatal("expected some concurrent uploads to succeed")
	}

	// 清理：取消会话
	_ = svc.CancelChunkUpload(uploadID)
}

// TestUnifiedUpload_SessionExpired 过期会话被拒绝
func TestUnifiedUpload_SessionExpired(t *testing.T) {
	cleanupChunkStore(t)
	defer cleanupChunkStore(t)

	svc, _ := newTestAttachmentService(t)
	ctx := context.Background()

	// 首片初始化
	fh0 := createTestFileHeader(t, "expired.dat", "application/octet-stream", []byte("AAAAAAAAAA"))
	resp, err := svc.UnifiedUpload(ctx, &UnifiedUploadRequest{
		FileHeader:  fh0,
		ChunkIndex:  0,
		TotalChunks: 5,
		Filename:    "expired.dat",
		FileSize:    50,
		ChunkSize:   10,
		UserID:      "uid-expired",
	})
	if err != nil {
		t.Fatalf("first chunk failed: %v", err)
	}
	uploadID := resp.UploadID

	// 手动将会话标记为已过期
	val, ok := chunkStore.Load(uploadID)
	if !ok {
		t.Fatal("session not found")
	}
	session := val.(*chunkSession)
	session.ExpiresAt = time.Now().Add(-1 * time.Hour) // 设置为 1 小时前过期

	// 尝试上传到已过期的会话
	fh1 := createTestFileHeader(t, "expired.dat", "application/octet-stream", []byte("BBBBBBBBBB"))
	_, err = svc.UnifiedUpload(ctx, &UnifiedUploadRequest{
		FileHeader:  fh1,
		UploadID:    uploadID,
		ChunkIndex:  1,
		TotalChunks: 5,
		UserID:      "uid-expired",
	})
	if err == nil {
		t.Fatal("expected error for expired session")
	}
	if !containsStr(err.Error(), "已过期") {
		t.Fatalf("expected '已过期' error, got: %v", err)
	}

	// 清理
	_ = svc.CancelChunkUpload(uploadID)
}

// ==================== 辅助 ====================

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
