package handler

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"go-admin/internal/global"
	"go-admin/internal/service"

	"github.com/gofiber/fiber/v3"
)

// -------------------------------------------------------------------
// 前台统一上传路由（/api/v1/auth/attachments/upload）行为回归：
// 同一 UploadAttachment/分片 handler 复用下的身份注入、uploaded_by
// 域前缀、缺身份拒绝与 SVG 拦截。
// -------------------------------------------------------------------

func setupFrontUploadApp(t *testing.T) (*fiber.App, *service.AttachmentService) {
	t.Helper()
	app, _, svc, _ := setupAttachmentHandlerTest(t)
	// 模拟前台路由组：jwt → UserIdentity 之后注入 uid 与 upload_subject。
	// 通过请求头模拟不同登录用户，缺省头即未注入身份。
	app.Use(func(c fiber.Ctx) error {
		if uid := c.Get("X-Test-UID"); uid != "" {
			c.Locals("uid", uid)
		}
		if subject := c.Get("X-Test-Subject"); subject != "" {
			c.Locals("attachment.upload_subject", subject)
		}
		return c.Next()
	})
	app.Post("/auth/attachments/upload", UploadAttachment)
	app.Get("/auth/attachments/chunk/status/:upload_id", GetChunkUploadStatus)
	app.Delete("/auth/attachments/chunk/:upload_id", CancelChunkUpload)
	t.Cleanup(func() { os.RemoveAll("./data") })
	return app, svc
}

// buildMultipartFileHeader 构造与真实 multipart 解析一致的文件头，供直连 service 的用例使用
func buildMultipartFileHeader(t *testing.T, filename string, content []byte) *multipart.FileHeader {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	form, err := multipart.NewReader(body, writer.Boundary()).ReadForm(10 << 20)
	if err != nil {
		t.Fatalf("ReadForm: %v", err)
	}
	return form.File["file"][0]
}

func buildFrontUploadRequest(t *testing.T, filename string, content []byte, fields map[string]string) *http.Request {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	for k, v := range fields {
		if err := writer.WriteField(k, v); err != nil {
			t.Fatalf("write field %s: %v", k, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/auth/attachments/upload", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

type frontUploadBody struct {
	Code    int                        `json:"code"`
	Message string                     `json:"message"`
	Data    service.AttachmentResponse `json:"data"`
}

func TestFrontUploadAttachment_NamespacedUploadedBy(t *testing.T) {
	app, _ := setupFrontUploadApp(t)

	req := buildFrontUploadRequest(t, "hello.txt", []byte("hello front upload"), nil)
	req.Header.Set("X-Test-UID", "5")
	req.Header.Set("X-Test-Subject", "user")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var body frontUploadBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Code != global.ResponseSuccessCode {
		t.Fatalf("expected success code %d, got %d (%s)", global.ResponseSuccessCode, body.Code, body.Message)
	}
	if body.Data.UploadedBy != "user:5" {
		t.Fatalf("uploaded_by 期望带前台域前缀 user:5, got %q", body.Data.UploadedBy)
	}
}

func TestFrontUploadAttachment_AdminStyleBareUID(t *testing.T) {
	app, _ := setupFrontUploadApp(t)

	// 未设置 upload_subject 的路由组（管理端）保持裸 uid 语义
	req := buildFrontUploadRequest(t, "admin.txt", []byte("admin upload content"), nil)
	req.Header.Set("X-Test-UID", "7")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}

	var body frontUploadBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Code != global.ResponseSuccessCode {
		t.Fatalf("expected success code %d, got %d (%s)", global.ResponseSuccessCode, body.Code, body.Message)
	}
	if body.Data.UploadedBy != "7" {
		t.Fatalf("管理端 uploaded_by 应保持裸 uid, got %q", body.Data.UploadedBy)
	}
}

func TestFrontUploadAttachment_MissingIdentity(t *testing.T) {
	app, _ := setupFrontUploadApp(t)

	// 未注入 uid（身份中间件缺失/失效）必须 401 拒绝
	req := buildFrontUploadRequest(t, "hello.txt", []byte("no identity"), nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}

	var body frontUploadBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Code != global.ResponseUnauthorized {
		t.Fatalf("expected unauthorized code %d, got %d (%s)", global.ResponseUnauthorized, body.Code, body.Message)
	}
}

func TestFrontUploadAttachment_SVGBlocked(t *testing.T) {
	app, _ := setupFrontUploadApp(t)

	req := buildFrontUploadRequest(t, "evil.svg", []byte(`<svg onload="alert(1)"></svg>`), nil)
	req.Header.Set("X-Test-UID", "5")
	req.Header.Set("X-Test-Subject", "user")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}

	var body frontUploadBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Code != global.ErrCodeMimeTypeNotAllowed {
		t.Fatalf("SVG 应被类型白名单拦截（code %d）, got %d (%s)", global.ErrCodeMimeTypeNotAllowed, body.Code, body.Message)
	}
}

func TestFrontChunkStatus_OwnerOnly(t *testing.T) {
	app, svc := setupFrontUploadApp(t)

	// 直连 service 创建带属主的分片会话
	req := &service.UnifiedUploadRequest{
		FileHeader:  buildMultipartFileHeader(t, "front-status.txt", []byte("AAAA")),
		ChunkIndex:  0,
		TotalChunks: 2,
		Filename:    "front-status.txt",
		FileSize:    8,
		ChunkSize:   4,
		MimeType:    "text/plain",
		UserID:      "user:5",
	}
	first, err := svc.UnifiedUpload(t.Context(), req)
	if err != nil {
		t.Fatalf("创建分片会话失败: %v", err)
	}
	if first.UploadID == "" {
		t.Fatal("首片应返回 upload_id")
	}

	// 属主查询成功
	ownerReq := httptest.NewRequest(http.MethodGet, "/auth/attachments/chunk/status/"+first.UploadID, nil)
	ownerReq.Header.Set("X-Test-UID", "5")
	ownerReq.Header.Set("X-Test-Subject", "user")
	ownerResp, err := app.Test(ownerReq)
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}
	var ownerBody struct {
		Code    int `json:"code"`
		Message string
		Data    service.ChunkStatusResponse `json:"data"`
	}
	if err := json.NewDecoder(ownerResp.Body).Decode(&ownerBody); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if ownerBody.Code != global.ResponseSuccessCode {
		t.Fatalf("属主查询应成功, got code %d (%s)", ownerBody.Code, ownerBody.Message)
	}

	// 他人查询表现为会话不存在
	otherReq := httptest.NewRequest(http.MethodGet, "/auth/attachments/chunk/status/"+first.UploadID, nil)
	otherReq.Header.Set("X-Test-UID", "6")
	otherReq.Header.Set("X-Test-Subject", "user")
	otherResp, err := app.Test(otherReq)
	if err != nil {
		t.Fatalf("app.Test returned error: %v", err)
	}
	var otherBody struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(otherResp.Body).Decode(&otherBody); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if otherBody.Code != global.ErrCodeChunkUploadNotFound {
		t.Fatalf("他人查询应返回会话不存在（code %d）, got %d (%s)", global.ErrCodeChunkUploadNotFound, otherBody.Code, otherBody.Message)
	}
}
