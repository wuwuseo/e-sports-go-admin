package handler

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// -------------------------------------------------------------------
// M1 回归：附件回显响应头安全收敛（内容端点与预览端点共用
// setAttachmentHeaders，此处以预览端点验证）
// -------------------------------------------------------------------

func TestPreviewAttachmentForceDownloadsSVG(t *testing.T) {
	app, client, _, fs := setupAttachmentHandlerTest(t)
	a := createHandlerAttachmentFile(t, client, fs, "docs/evil.svg", "evil.svg", "image/svg+xml", "svg", []byte("<svg onload=\"alert(1)\"></svg>"), true)
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
		t.Fatalf("SVG 必须强制下载（attachment），got %q", disposition)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("expected X-Content-Type-Options: nosniff")
	}
	if resp.Header.Get("Content-Security-Policy") != "sandbox" {
		t.Fatal("expected Content-Security-Policy: sandbox")
	}
}

func TestPreviewAttachmentInlineWhitelist(t *testing.T) {
	cases := []struct {
		name       string
		mimeType   string
		wantInline bool
	}{
		{"PNG保持内联", "image/png", true},
		{"JPEG保持内联", "image/jpeg", true},
		{"PDF保持内联", "application/pdf", true},
		{"TIFF强制下载", "image/tiff", false},
		{"HTML强制下载", "text/html", false},
		{"XML强制下载", "text/xml", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, client, _, fs := setupAttachmentHandlerTest(t)
			a := createHandlerAttachmentFile(t, client, fs, "docs/file", "file", tc.mimeType, "bin", []byte("data"), true)
			app.Get("/attachments/:id/preview", PreviewAttachment)

			req := httptest.NewRequest(http.MethodGet, "/attachments/"+strconv.Itoa(a.ID)+"/preview", nil)
			resp, err := app.Test(req)
			if err != nil {
				t.Fatalf("app.Test returned error: %v", err)
			}
			disposition := resp.Header.Get("Content-Disposition")
			if tc.wantInline && !strings.HasPrefix(disposition, "inline;") {
				t.Fatalf("%s 应内联展示，got %q", tc.mimeType, disposition)
			}
			if !tc.wantInline && !strings.HasPrefix(disposition, "attachment;") {
				t.Fatalf("%s 应强制下载，got %q", tc.mimeType, disposition)
			}
		})
	}
}
