package handler

import (
	"errors"
	"fmt"
	"strconv"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/validate"
)

// UploadAttachment 上传附件（统一入口：支持简单上传和分片上传）
// @Summary 上传附件
// @Description 上传文件附件。简单上传仅需 file + group_id；分片上传需额外提供 chunk_index、total_chunks、filename、file_size、chunk_size。首次分片无需 upload_id，后续分片需携带 upload_id
// @Tags 附件管理
// @Accept multipart/form-data
// @Produce json
// @Security BearerAuth
// @Param file formData file true "上传文件"
// @Param group_id formData int false "分组ID"
// @Param chunk_index formData int false "分片索引（0-based，启用分片上传时必填）"
// @Param total_chunks formData int false "总片数（启用分片上传时必填）"
// @Param upload_id formData string false "上传会话ID（后续分片必填，首片不传）"
// @Param filename formData string false "原始文件名（首片必填）"
// @Param file_size formData int false "文件总大小（首片必填）"
// @Param chunk_size formData int false "单片大小（首片必填）"
// @Param mime_type formData string false "MIME类型"
// @Success 200 {object} utils.Response{data=service.UnifiedUploadResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/attachments/upload [post]
func UploadAttachment(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.AttachmentService").(*service.AttachmentService)

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "请选择上传文件").ToJSON(c)
	}

	// 解析分片参数
	chunkIndexStr := c.FormValue("chunk_index")
	totalChunksStr := c.FormValue("total_chunks")

	// 获取用户ID
	uid, ok := c.Locals("uid").(string)
	if !ok || uid == "" {
		return resp.Fail(global.ResponseUnauthorized, "用户身份无效").ToJSON(c)
	}
	userID := uid

	// 构建统一上传请求
	req := &service.UnifiedUploadRequest{
		FileHeader: fileHeader,
		UserID:     userID,
		Directory:  c.FormValue("directory"),
	}

	// 解析可选分组ID
	if gid := c.FormValue("group_id"); gid != "" {
		if id, err := strconv.Atoi(gid); err == nil {
			req.GroupID = &id
		}
	}

	// 分片参数解析
	if chunkIndexStr != "" && totalChunksStr != "" {
		chunkIndex, err := strconv.Atoi(chunkIndexStr)
		if err != nil || chunkIndex < 0 {
			return resp.Fail(global.ResponseErrorCode, "chunk_index 必须为非负整数").ToJSON(c)
		}
		totalChunks, err := strconv.Atoi(totalChunksStr)
		if err != nil || totalChunks < 1 {
			return resp.Fail(global.ResponseErrorCode, "total_chunks 必须为正整数").ToJSON(c)
		}

		req.ChunkIndex = chunkIndex
		req.TotalChunks = totalChunks
		req.UploadID = c.FormValue("upload_id")

		// 首次分片：校验初始化参数
		if req.UploadID == "" {
			req.Filename = c.FormValue("filename")
			if req.Filename == "" {
				return resp.Fail(global.ResponseErrorCode, "首次分片需提供 filename").ToJSON(c)
			}
			fileSizeStr := c.FormValue("file_size")
			if fileSizeStr == "" {
				return resp.Fail(global.ResponseErrorCode, "首次分片需提供 file_size").ToJSON(c)
			}
			fileSize, err := strconv.ParseInt(fileSizeStr, 10, 64)
			if err != nil || fileSize < 1 {
				return resp.Fail(global.ResponseErrorCode, "file_size 必须为正整数").ToJSON(c)
			}
			req.FileSize = fileSize

			chunkSizeStr := c.FormValue("chunk_size")
			if chunkSizeStr == "" {
				return resp.Fail(global.ResponseErrorCode, "首次分片需提供 chunk_size").ToJSON(c)
			}
			chunkSize, err := strconv.ParseInt(chunkSizeStr, 10, 64)
			if err != nil || chunkSize < 1 {
				return resp.Fail(global.ResponseErrorCode, "chunk_size 必须为正整数").ToJSON(c)
			}
			req.ChunkSize = chunkSize
			req.MimeType = c.FormValue("mime_type")
		}
	}

	result, err := svc.UnifiedUpload(c.Context(), req)
	if err != nil {
		// S5: 使用 errors.As 精准映射错误码，替代脆弱的 strings.Contains 字符串匹配
		var chunkErr *service.ChunkUploadError
		if errors.As(err, &chunkErr) {
			return resp.Fail(chunkErr.Code, err.Error()).ToJSON(c)
		}
		// 兜底：未识别的错误
		return resp.Fail(global.ErrCodeAttachmentUploadFailed, "上传失败").ToJSON(c)
	}

	// 操作日志（简单上传和分片合并时记录信息）
	if result.Attachment != nil {
		logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
		logOperation(c, logService, "attachment", "上传", fmt.Sprintf("上传附件: %s", result.Attachment.OriginalName))
		// 直接返回 Attachment 保持向后兼容：data.id 而非 data.attachment.id
		return resp.Success(result.Attachment, global.ResponseSuccessCode, "上传成功").ToJSON(c)
	}

	return resp.Success(result, global.ResponseSuccessCode, "分片上传成功").ToJSON(c)
}

// ListAttachments 获取附件列表
// @Summary 获取附件列表
// @Description 获取附件列表，支持分页、关键字搜索、分组筛选和MIME类型筛选
// @Tags 附件管理
// @Produce json
// @Security BearerAuth
// @Param page query int false "页码，默认1"
// @Param page_size query int false "每页数量，默认10"
// @Param keyword query string false "按名称搜索"
// @Param group_id query int false "分组ID筛选"
// @Param mime_type query string false "MIME类型筛选"
// @Success 200 {object} utils.Response{data=service.ListAttachmentsResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/attachments [get]
func ListAttachments(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.AttachmentService").(*service.AttachmentService)

	req, err := validate.ParseQueryAndValidate[service.ListAttachmentsRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	if req.Page == 0 {
		req.Page = 1
	}
	if req.PageSize == 0 {
		req.PageSize = 10
	}

	attachments, err := svc.List(c.Context(), req)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取附件列表失败").ToJSON(c)
	}

	return resp.Success(attachments, global.ResponseSuccessCode, "获取附件列表成功").ToJSON(c)
}

// GetAttachment 获取附件详情
// @Summary 获取附件详情
// @Description 根据ID获取附件详细信息
// @Tags 附件管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "附件ID"
// @Success 200 {object} utils.Response{data=service.AttachmentResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/attachments/{id} [get]
func GetAttachment(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.AttachmentService").(*service.AttachmentService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的附件ID").ToJSON(c)
	}

	attachment, err := svc.Get(c.Context(), id)
	if err != nil {
		if err == service.ErrAttachmentNotFound {
			return resp.Fail(global.ErrCodeAttachmentNotFound, "附件不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "获取附件失败").ToJSON(c)
	}

	return resp.Success(attachment, global.ResponseSuccessCode, "获取附件成功").ToJSON(c)
}

// PreviewAttachment 预览/下载附件
// @Summary 预览/下载附件
// @Description 根据ID预览或下载附件。图片和PDF直接预览，其他类型作为下载
// @Tags 附件管理
// @Produce octet-stream
// @Security BearerAuth
// @Param id path int true "附件ID"
// @Success 200 {file} file "文件内容"
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/attachments/{id}/preview [get]
func PreviewAttachment(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.AttachmentService").(*service.AttachmentService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的附件ID").ToJSON(c)
	}

	data, mimeType, filename, err := svc.GetFileData(c.Context(), id)
	if err != nil {
		if err == service.ErrAttachmentStorageUnavailable {
			return c.Status(fiber.StatusInternalServerError).JSON(resp.Fail(global.ResponseInternalServerError, "filesystem unavailable"))
		}
		if err == service.ErrAttachmentNotFound {
			return c.Status(fiber.StatusNotFound).JSON(resp.Fail(global.ErrCodeAttachmentNotFound, "attachment not found"))
		}
		return resp.Fail(global.ResponseInternalServerError, "获取文件失败").ToJSON(c)
	}

	return sendAttachmentData(c, data, mimeType, filename)
}

// DeleteAttachment 删除附件
// @Summary 删除附件
// @Description 根据ID删除附件
// @Tags 附件管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "附件ID"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/attachments/{id} [delete]
func DeleteAttachment(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.AttachmentService").(*service.AttachmentService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的附件ID").ToJSON(c)
	}

	if err := svc.Delete(c.Context(), id); err != nil {
		if err == service.ErrAttachmentNotFound {
			return resp.Fail(global.ErrCodeAttachmentNotFound, "附件不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "删除附件失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "attachment", "删除", fmt.Sprintf("删除附件 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "附件删除成功").ToJSON(c)
}

// BatchDeleteAttachments 批量删除附件
// @Summary 批量删除附件
// @Description 批量删除多个附件
// @Tags 附件管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body service.BatchDeleteRequest true "附件ID列表"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/attachments/batch [delete]
func BatchDeleteAttachments(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.AttachmentService").(*service.AttachmentService)

	req, err := validate.ParseAndValidate[service.BatchDeleteRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	if err := svc.BatchDelete(c.Context(), req.IDs); err != nil {
		return resp.Fail(global.ResponseInternalServerError, "批量删除附件失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "attachment", "批量删除", fmt.Sprintf("批量删除 %d 个附件", len(req.IDs)))

	return resp.Success(nil, global.ResponseSuccessCode, "批量删除成功").ToJSON(c)
}

// MoveAttachment 移动附件到分组
// @Summary 移动附件到分组
// @Description 将附件移动到指定分组，group_id为null时移出分组
// @Tags 附件管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "附件ID"
// @Param body body service.MoveAttachmentRequest true "目标分组信息"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/attachments/{id}/move [put]
func MoveAttachment(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.AttachmentService").(*service.AttachmentService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的附件ID").ToJSON(c)
	}

	req, err := validate.ParseAndValidate[service.MoveAttachmentRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	if err := svc.MoveToGroup(c.Context(), id, req.GroupID); err != nil {
		if err == service.ErrAttachmentNotFound {
			return resp.Fail(global.ErrCodeAttachmentNotFound, "附件不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "移动附件失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "attachment", "移动分组", fmt.Sprintf("移动附件 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "移动成功").ToJSON(c)
}
