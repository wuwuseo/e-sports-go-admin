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

	url, err := svc.CreateAccessURL(c.Context(), id, attachmentContentBasePath(c), service.AttachmentAccessTTL, attachmentAccessSecret(c))
	if err != nil {
		if err == service.ErrAttachmentNotFound {
			return resp.Fail(global.ErrCodeAttachmentNotFound, "附件不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "生成访问地址失败").ToJSON(c)
	}

	return resp.Success(fiber.Map{
		"url":        url,
		"expires_in": int64(service.AttachmentAccessTTL / time.Second),
	}, global.ResponseSuccessCode, "生成访问地址成功").ToJSON(c)
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
	c.Set("Content-Type", mimeType)
	disposition := "attachment"
	if strings.HasPrefix(mimeType, "image/") || mimeType == "application/pdf" {
		disposition = "inline"
	}
	c.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": filename}))
	return c.Send(data)
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
