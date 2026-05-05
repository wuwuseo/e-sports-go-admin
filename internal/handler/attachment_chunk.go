package handler

import (
	"strings"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
)

// GetChunkUploadStatus 获取分片上传状态
// @Summary 获取分片上传状态
// @Description 查询分片上传进度
// @Tags 附件管理
// @Produce json
// @Security BearerAuth
// @Param upload_id path string true "上传会话ID"
// @Success 200 {object} utils.Response{data=service.ChunkStatusResponse}
// @Failure 400 {object} utils.Response
// @Router /api/v1/admin/auth/attachments/chunk/status/{upload_id} [get]
func GetChunkUploadStatus(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.AttachmentService").(*service.AttachmentService)

	uploadID := c.Params("upload_id")
	if uploadID == "" {
		return resp.Fail(global.ResponseErrorCode, "缺少 upload_id 参数").ToJSON(c)
	}

	result, err := svc.GetChunkStatus(uploadID)
	if err != nil {
		if strings.Contains(err.Error(), "不存在") {
			return resp.Fail(global.ErrCodeChunkUploadNotFound, "分片上传会话不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "获取分片状态失败").ToJSON(c)
	}

	return resp.Success(result, global.ResponseSuccessCode, "获取分片上传状态成功").ToJSON(c)
}

// CancelChunkUpload 取消分片上传
// @Summary 取消分片上传
// @Description 取消分片上传并清理已上传的分片数据
// @Tags 附件管理
// @Produce json
// @Security BearerAuth
// @Param upload_id path string true "上传会话ID"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Router /api/v1/admin/auth/attachments/chunk/{upload_id} [delete]
func CancelChunkUpload(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.AttachmentService").(*service.AttachmentService)

	uploadID := c.Params("upload_id")
	if uploadID == "" {
		return resp.Fail(global.ResponseErrorCode, "缺少 upload_id 参数").ToJSON(c)
	}

	if err := svc.CancelChunkUpload(uploadID); err != nil {
		return resp.Fail(global.ResponseInternalServerError, "取消分片上传失败").ToJSON(c)
	}

	return resp.Success(nil, global.ResponseSuccessCode, "已取消分片上传").ToJSON(c)
}
