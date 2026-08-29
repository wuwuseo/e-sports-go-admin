package service

import (
	"errors"
	"mime/multipart"
	"net/textproto"
	"testing"
)

// -------------------------------------------------------------------
// M1 回归：SVG 存储型 XSS 上传拦截
// -------------------------------------------------------------------

func TestIsMimeTypeAllowed_SVGRemovedFromWhitelist(t *testing.T) {
	if isMimeTypeAllowed("image/svg+xml") {
		t.Fatal("image/svg+xml 应已从上传白名单移除（SVG 可内嵌脚本）")
	}
}

func TestRejectBlockedUpload(t *testing.T) {
	cases := []struct {
		name         string
		original     string
		declaredMime string
		wantBlocked  bool
	}{
		{"svg扩展名拒绝", "evil.svg", "", true},
		{"大写SVG扩展名拒绝", "evil.SVG", "text/xml", true},
		{"声明svg MIME拒绝", "evil.txt", "image/svg+xml", true},
		{"声明svg参数化MIME拒绝", "evil.xml", "image/svg+xml; charset=utf-8", true},
		{"声明text/xml但无svg特征放行", "evil.xml", "text/xml", false},
		{"普通图片放行", "photo.png", "image/png", false},
		{"普通文档放行", "doc.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := rejectBlockedUpload(tc.original, tc.declaredMime)
			if tc.wantBlocked && err == nil {
				t.Fatalf("expected %q/%q to be blocked", tc.original, tc.declaredMime)
			}
			if !tc.wantBlocked && err != nil {
				t.Fatalf("expected %q/%q to pass, got: %v", tc.original, tc.declaredMime, err)
			}
		})
	}
}

func TestUpload_RejectsSVGFile(t *testing.T) {
	svc, _ := setupAttachmentService(t)

	cases := []struct {
		name     string
		filename string
		mime     string
	}{
		{"扩展名svg+声明svg MIME", "evil.svg", "image/svg+xml"},
		{"扩展名svg+伪装text/xml", "evil.svg", "text/xml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			header := &multipart.FileHeader{
				Filename: tc.filename,
				Header:   textproto.MIMEHeader{"Content-Type": []string{tc.mime}},
				Size:     10,
			}
			_, err := svc.Upload(t.Context(), header, nil, "operator-uid")
			if !errors.Is(err, ErrSVGUploadBlocked) {
				t.Fatalf("expected ErrSVGUploadBlocked, got: %v", err)
			}
		})
	}
}

func TestUnifiedUpload_RejectsSVGFirstChunk(t *testing.T) {
	svc, _ := setupAttachmentService(t)

	// 声明 text/xml 可通过 MIME 白名单（text/xml 在白名单内），
	// 必须由首片扩展名拦截兜底；拦截发生在会话创建之前
	req := &UnifiedUploadRequest{
		TotalChunks: 2,
		ChunkIndex:  0,
		UploadID:    "",
		Filename:    "evil.svg",
		FileSize:    100,
		ChunkSize:   50,
		MimeType:    "text/xml",
	}
	_, err := svc.UnifiedUpload(t.Context(), req)
	if !errors.Is(err, ErrSVGUploadBlocked) {
		t.Fatalf("expected ErrSVGUploadBlocked for first chunk, got: %v", err)
	}

	// 拦截在会话创建前：不应残留分片会话
	if _, ok := chunkStore.Load("anything"); ok {
		t.Fatal("no chunk session should be created for blocked upload")
	}
}
