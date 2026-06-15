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

func GetAttachmentAccessURL(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.AttachmentService").(*service.AttachmentService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的附件ID").ToJSON(c)
	}

	accessURL, err := svc.BuildAccessURL(c.Context(), id, attachmentContentBasePath(c), attachmentAccessTTL(c), attachmentAccessSecret(c))
	if err != nil {
		if err == service.ErrAttachmentNotFound {
			return resp.Fail(global.ErrCodeAttachmentNotFound, "附件不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "生成访问地址失败").ToJSON(c)
	}

	return resp.Success(accessURL, global.ResponseSuccessCode, "生成访问地址成功").ToJSON(c)
}

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
	if err := svc.ValidateAccessToken(id, token, attachmentAccessSecret(c), time.Now()); err != nil {
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

func sendAttachmentData(c fiber.Ctx, data []byte, mimeType string, filename string) error {
	setAttachmentHeaders(c, mimeType, filename)
	return c.Send(data)
}

func setAttachmentHeaders(c fiber.Ctx, mimeType string, filename string) {
	c.Set("Content-Type", mimeType)
	disposition := "attachment"
	if strings.HasPrefix(mimeType, "image/") || mimeType == "application/pdf" {
		disposition = "inline"
	}
	c.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": filename}))
}

func attachmentAccessSecret(c fiber.Ctx) string {
	if secret, ok := c.Locals("attachment.access_secret").(string); ok && secret != "" {
		return secret
	}
	return "attachment-access-secret"
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
