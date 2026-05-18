package service

import (
	"testing"
	"time"

	"go-admin/internal/global"
)

func TestIsMimeTypeAllowed_ExactMatch(t *testing.T) {
	tests := []struct {
		mimeType string
		expected bool
	}{
		{"image/jpeg", true},
		{"image/png", true},
		{"application/pdf", true},
		{"application/msword", true},
		{"application/zip", true},
		{"audio/mpeg", true},
		{"video/mp4", true},
		{"application/octet-stream", false}, // 已从白名单移除
		{"application/x-shockwave-flash", false},
		{"application/x-msdownload", false},
	}

	for _, tt := range tests {
		t.Run(tt.mimeType, func(t *testing.T) {
			result := isMimeTypeAllowed(tt.mimeType)
			if result != tt.expected {
				t.Errorf("isMimeTypeAllowed(%q) = %v, want %v", tt.mimeType, result, tt.expected)
			}
		})
	}
}

func TestIsMimeTypeAllowed_WithParameters(t *testing.T) {
	result := isMimeTypeAllowed("text/plain; charset=utf-8")
	if !result {
		t.Errorf("isMimeTypeAllowed(%q) = %v, want true", "text/plain; charset=utf-8", result)
	}
}

func TestIsMimeTypeAllowed_WildcardMatch(t *testing.T) {
	result := isMimeTypeAllowed("image/png") // 精确匹配
	if !result {
		t.Errorf("isMimeTypeAllowed(%q) = %v, want true", "image/png", result)
	}
}

func TestIsMimeTypeAllowed_Empty(t *testing.T) {
	result := isMimeTypeAllowed("")
	if !result {
		t.Errorf("isMimeTypeAllowed(%q) = %v, want true (empty means allowed)", "", result)
	}
}

func TestIsMimeTypeAllowed_NotAllowed(t *testing.T) {
	result := isMimeTypeAllowed("application/x-shockwave-flash")
	if result {
		t.Errorf("isMimeTypeAllowed(%q) = %v, want false", "application/x-shockwave-flash", result)
	}
}

func TestGetExt_WithExtension(t *testing.T) {
	tests := []struct {
		filename string
		expected string
	}{
		{"document.pdf", "pdf"},
		{"image.png", "png"},
		{"archive.tar.gz", "gz"},
		{"file.TXT", "TXT"},
		{".hidden", "hidden"}, // dotfile 有扩展名
		{"noextension", ""},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			result := getExt(tt.filename)
			if result != tt.expected {
				t.Errorf("getExt(%q) = %q, want %q", tt.filename, result, tt.expected)
			}
		})
	}
}

func TestGetExt_NoExtension(t *testing.T) {
	result := getExt("justfilename")
	if result != "" {
		t.Errorf("getExt(%q) = %q, want empty string", "justfilename", result)
	}
}

func TestGetExt_DotFile(t *testing.T) {
	result := getExt(".gitignore")
	if result != "gitignore" {
		t.Errorf("getExt(%q) = %q, want 'gitignore'", ".gitignore", result)
	}
}

func TestGenerateDeterministicUploadID(t *testing.T) {
	id1 := generateDeterministicUploadID("test.pdf", 1024, "user123")
	id2 := generateDeterministicUploadID("test.pdf", 1024, "user123")
	id3 := generateDeterministicUploadID("test.pdf", 2048, "user123")
	id4 := generateDeterministicUploadID("other.pdf", 1024, "user123")

	// 相同参数应生成相同ID
	if id1 != id2 {
		t.Errorf("相同参数生成了不同ID: %s != %s", id1, id2)
	}

	// 不同参数应生成不同ID
	if id1 == id3 {
		t.Error("不同文件大小生成了相同ID")
	}
	if id1 == id4 {
		t.Error("不同文件名生成了相同ID")
	}

	// ID长度应为40（SHA1十六进制）
	if len(id1) != 40 {
		t.Errorf("ID长度错误: got %d, want 40", len(id1))
	}
}

func TestChunkUploadError_Error(t *testing.T) {
	err := &ChunkUploadError{
		Code:    1001,
		Message: "测试错误消息",
	}

	if err.Error() != "测试错误消息" {
		t.Errorf("Error() = %q, want %q", err.Error(), "测试错误消息")
	}
}

func TestPredefinedErrors(t *testing.T) {
	tests := []struct {
		err      error
		wantCode int
		wantMsg  string
	}{
		{ErrChunkSessionNotFound, global.ErrCodeChunkUploadNotFound, "分片上传会话不存在"},
		{ErrChunkSessionEnded, global.ErrCodeChunkUploadNotFound, "分片上传会话已结束"},
		{ErrChunkSessionExpired, global.ErrCodeChunkUploadExpired, "分片上传会话已过期"},
		{ErrChunkInvalidIndex, global.ErrCodeChunkUploadInvalidChunk, "无效的分片索引"},
		{ErrChunkMergeIncomplete, global.ErrCodeChunkUploadMergeFailed, "部分分片尚未上传"},
		{ErrChunkConcurrencyLimit, global.ErrCodeUploadConcurrencyLimit, "上传并发数超限，请稍后重试"},
		{ErrFileSizeExceeded, global.ErrCodeFileSizeExceeded, "文件大小超过限制"},
		{ErrChunkedFileSizeExceeded, global.ErrCodeFileSizeExceeded, "文件总大小超过限制"},
		{ErrChunkSizeExceeded, global.ErrCodeChunkSizeExceeded, "单分片大小超过限制"},
		{ErrMimeTypeNotAllowed, global.ErrCodeMimeTypeNotAllowed, "不支持的文件类型"},
	}

	for _, tt := range tests {
		t.Run(tt.wantMsg, func(t *testing.T) {
			chunkErr, ok := tt.err.(*ChunkUploadError)
			if !ok {
				t.Errorf("错误类型转换失败: %v", tt.err)
				return
			}
			if chunkErr.Code != tt.wantCode {
				t.Errorf("错误码: got %d, want %d", chunkErr.Code, tt.wantCode)
			}
			if chunkErr.Message != tt.wantMsg {
				t.Errorf("错误消息: got %q, want %q", chunkErr.Message, tt.wantMsg)
			}
		})
	}
}

func TestConstants(t *testing.T) {
	// 验证常量定义正确
	if maxSimpleFileSize != 100*1024*1024 {
		t.Errorf("maxSimpleFileSize = %d, want %d", maxSimpleFileSize, 100*1024*1024)
	}
	if maxChunkedFileSize != 5*1024*1024*1024 {
		t.Errorf("maxChunkedFileSize = %d, want %d", maxChunkedFileSize, 5*1024*1024*1024)
	}
	if maxChunkSize != 50*1024*1024 {
		t.Errorf("maxChunkSize = %d, want %d", maxChunkSize, 50*1024*1024)
	}
	if maxConcurrentChunksPerSession != 3 {
		t.Errorf("maxConcurrentChunksPerSession = %d, want 3", maxConcurrentChunksPerSession)
	}
	if sessionTTL != 24*time.Hour {
		t.Errorf("sessionTTL = %v, want 24h", sessionTTL)
	}
	if cleanupInterval != 1*time.Hour {
		t.Errorf("cleanupInterval = %v, want 1h", cleanupInterval)
	}
}
