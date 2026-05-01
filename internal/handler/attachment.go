package handler

import (
	"fmt"
	"strconv"
	"strings"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v2"
	"github.com/wuwuseo/cmf/validate"
)

// UploadAttachment 上传附件
// @Summary 上传附件
// @Description 上传文件附件，支持指定分组
// @Tags 附件管理
// @Accept multipart/form-data
// @Produce json
// @Security BearerAuth
// @Param file formData file true "上传文件"
// @Param group_id formData int false "分组ID"
// @Success 200 {object} utils.Response{data=service.AttachmentResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/attachments/upload [post]
func UploadAttachment(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.AttachmentService").(*service.AttachmentService)

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "请选择上传文件").ToJSON(c)
	}

	// 获取可选的分组ID
	var groupID *int
	if gid := c.FormValue("group_id"); gid != "" {
		id, err := strconv.Atoi(gid)
		if err == nil {
			groupID = &id
		}
	}

	// 从JWT获取用户ID
	userID := c.Locals("uid").(string)

	attachment, err := svc.Upload(c.Context(), fileHeader, groupID, userID)
	if err != nil {
		return resp.Fail(global.ErrCodeAttachmentUploadFailed, "上传失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "附件管理", "上传", "上传附件: "+fileHeader.Filename)

	return resp.Success(attachment, global.ResponseSuccessCode, "上传成功").ToJSON(c)
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
func ListAttachments(c *fiber.Ctx) error {
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
func GetAttachment(c *fiber.Ctx) error {
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
func PreviewAttachment(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.AttachmentService").(*service.AttachmentService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的附件ID").ToJSON(c)
	}

	data, mimeType, filename, err := svc.GetFileData(c.Context(), id)
	if err != nil {
		if err == service.ErrAttachmentNotFound {
			return resp.Fail(global.ErrCodeAttachmentNotFound, "附件不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "获取文件失败").ToJSON(c)
	}

	c.Set("Content-Type", mimeType)

	// 图片和PDF直接预览（inline），其他类型作为下载（attachment）
	if strings.HasPrefix(mimeType, "image/") || mimeType == "application/pdf" {
		c.Set("Content-Disposition", "inline; filename=\""+filename+"\"")
	} else {
		c.Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	}

	return c.Send(data)
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
func DeleteAttachment(c *fiber.Ctx) error {
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
	logOperation(c, logService, "附件管理", "删除", fmt.Sprintf("删除附件 ID:%d", id))

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
func BatchDeleteAttachments(c *fiber.Ctx) error {
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
	logOperation(c, logService, "附件管理", "批量删除", fmt.Sprintf("批量删除 %d 个附件", len(req.IDs)))

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
func MoveAttachment(c *fiber.Ctx) error {
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
	logOperation(c, logService, "附件管理", "移动分组", fmt.Sprintf("移动附件 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "移动成功").ToJSON(c)
}
