package handler

import (
	"mime"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
)

// GetAttachmentAccessURL 生成附件访问地址
// @Summary 生成附件访问地址
// @Description 根据附件ID生成带签名的临时访问地址，用于前端直接访问附件内容
// @Tags 附件管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "附件ID"
// @Success 200 {object} utils.Response{data=string}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/attachments/{id}/access-url [get]
func GetAttachmentAccessURL(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.AttachmentService").(*service.AttachmentService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的附件ID").ToJSON(c)
	}

	secret := attachmentAccessSecret(c)
	if secret == "" {
		return resp.Fail(global.ResponseInternalServerError, "附件访问密钥未配置").ToJSON(c)
	}

	accessURL, err := svc.BuildAccessURL(c.Context(), id, attachmentContentBasePath(c), attachmentAccessTTL(c), secret)
	if err != nil {
		if err == service.ErrAttachmentNotFound {
			return resp.Fail(global.ErrCodeAttachmentNotFound, "附件不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "生成访问地址失败").ToJSON(c)
	}
	return resp.Success(accessURL, global.ResponseSuccessCode, "生成访问地址成功").ToJSON(c)
}

// ServeAttachmentContent 提供附件内容
// @Summary 获取附件内容
// @Description 通过带签名的临时 token 获取附件原始内容，token 由 access-url 接口生成；受 Content-Disposition 策略约束（安全类型内联展示，其余强制下载）
// @Tags 附件管理
// @Produce application/octet-stream
// @Param id path int true "附件ID"
// @Param token query string true "访问凭证"
// @Success 200 {file} binary "附件内容"
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/attachments/{id}/content [get]
func ServeAttachmentContent(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.AttachmentService").(*service.AttachmentService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(resp.Fail(global.ResponseErrorCode, "无效的附件ID"))
	}
	token := c.Query("token")
	if token == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(resp.Fail(global.ResponseUnauthorized, "缺少访问凭证"))
	}
	secret := attachmentAccessSecret(c)
	if secret == "" {
		return c.Status(fiber.StatusInternalServerError).JSON(resp.Fail(global.ResponseInternalServerError, "附件访问密钥未配置"))
	}
	if err := svc.ValidateAccessToken(id, token, secret, time.Now()); err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(resp.Fail(global.ResponseUnauthorized, "访问凭证无效"))
	}

	if attachmentXAccelEnabled(c) {
		redirect, err := svc.BuildXAccelRedirect(c.Context(), id)
		if err != nil {
			if err == service.ErrAttachmentNotFound {
				return c.Status(fiber.StatusNotFound).JSON(resp.Fail(global.ErrCodeAttachmentNotFound, "附件不存在"))
			}
			return c.Status(fiber.StatusInternalServerError).JSON(resp.Fail(global.ResponseInternalServerError, "获取文件失败"))
		}
		if redirect != nil {
			setAttachmentHeaders(c, redirect.MimeType, redirect.Filename)
			c.Set("X-Accel-Redirect", redirect.Path)
			return c.SendStatus(fiber.StatusOK)
		}
	}

	data, mimeType, filename, err := svc.GetFileData(c.Context(), id)
	if err != nil {
		if err == service.ErrAttachmentNotFound {
			return c.Status(fiber.StatusNotFound).JSON(resp.Fail(global.ErrCodeAttachmentNotFound, "附件不存在"))
		}
		return c.Status(fiber.StatusInternalServerError).JSON(resp.Fail(global.ResponseInternalServerError, "获取文件失败"))
	}

	return sendAttachmentData(c, data, mimeType, filename)
}

// ServePublicPageImage serves only images uploaded for page decoration.
// 保留该端点是为了让已保存过 /api/v1/attachments/{id}/public-page-image 的旧草稿
// 仍能显示；新草稿保存附件表同款 public/... 存储路径（或 CDN https），渲染时再转公开地址。
func ServePublicPageImage(c fiber.Ctx) error {
	svc := c.Locals("service.AttachmentService").(*service.AttachmentService)
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return c.SendStatus(fiber.StatusNotFound)
	}
	item, err := svc.Get(c.Context(), id)
	if err != nil {
		return c.SendStatus(fiber.StatusNotFound)
	}
	if !strings.HasPrefix(item.Path, "public/esports-page/") ||
		(item.Directory != "public/esports-page" && !strings.HasPrefix(item.Directory, "public/esports-page/")) ||
		!publicPageImageMimes[item.MimeType] || item.Size < 1 || item.Size > 10<<20 {
		return c.SendStatus(fiber.StatusNotFound)
	}
	data, mimeType, filename, err := svc.GetFileData(c.Context(), id)
	if err != nil {
		return c.SendStatus(fiber.StatusNotFound)
	}
	c.Set("Cache-Control", "public, max-age=31536000, immutable")
	return sendAttachmentData(c, data, mimeType, filename)
}

var publicPageImageMimes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
	"image/gif":  true,
}

func sendAttachmentData(c fiber.Ctx, data []byte, mimeType string, filename string) error {
	setAttachmentHeaders(c, mimeType, filename)
	return c.Send(data)
}

// inlineMimeTypes 允许内联展示的确定性安全类型。
// SVG/HTML/XML 等可承载脚本的类型不在其列——统一强制下载，
// 防止存储型 XSS；配合上传端 rejectBlockedUpload 形成双保险。
var inlineMimeTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
	"image/bmp":  true,
	// "image/tiff": 多数浏览器可内联渲染但非确定性安全，一并强制下载
	"application/pdf": true,
}

func setAttachmentHeaders(c fiber.Ctx, mimeType string, filename string) {
	c.Set("Content-Type", mimeType)
	// 内容端点专用：禁止 MIME 嗅探并将文档沙箱化为唯一源，
	// 即使类型误判也不会以应用源执行脚本
	c.Set("X-Content-Type-Options", "nosniff")
	c.Set("Content-Security-Policy", "sandbox")
	disposition := "attachment"
	if inlineMimeTypes[mimeType] {
		disposition = "inline"
	}
	c.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": filename}))
}

func attachmentAccessSecret(c fiber.Ctx) string {
	if secret, ok := c.Locals("attachment.access_secret").(string); ok && secret != "" {
		return secret
	}
	return ""
}

func attachmentContentBasePath(c fiber.Ctx) string {
	if basePath, ok := c.Locals("attachment.content_base_path").(string); ok && basePath != "" {
		return basePath
	}
	return "/api/v1/attachments"
}

func attachmentAccessTTL(c fiber.Ctx) time.Duration {
	if seconds, ok := c.Locals("attachment.access_ttl").(int); ok && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return service.AttachmentAccessTTL
}

func attachmentXAccelEnabled(c fiber.Ctx) bool {
	enabled, ok := c.Locals("attachment.x_accel_enabled").(bool)
	return ok && enabled
}
