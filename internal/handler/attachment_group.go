package handler

import (
	"fmt"
	"strconv"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v2"
	"github.com/wuwuseo/cmf/validate"
)

// CreateAttachmentGroup 创建附件分组
// @Summary 创建附件分组
// @Description 创建新的附件分组
// @Tags 附件分组管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body service.CreateGroupRequest true "分组信息"
// @Success 200 {object} utils.Response{data=service.GroupResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 409 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/attachment-groups [post]
func CreateAttachmentGroup(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.AttachmentGroupService").(*service.AttachmentGroupService)

	req, err := validate.ParseAndValidate[service.CreateGroupRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	group, err := svc.Create(c.Context(), req)
	if err != nil {
		if err == service.ErrAttachmentGroupExists {
			return resp.Fail(global.ErrCodeAttachmentGroupExists, "分组名称已存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "创建分组失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "attachment", "创建", "创建附件分组: "+req.Name)

	return resp.Success(group, global.ResponseSuccessCode, "分组创建成功").ToJSON(c)
}

// ListAttachmentGroups 获取附件分组列表
// @Summary 获取附件分组列表
// @Description 获取附件分组列表，支持分页和关键字搜索
// @Tags 附件分组管理
// @Produce json
// @Security BearerAuth
// @Param page query int false "页码，默认1"
// @Param page_size query int false "每页数量，默认10"
// @Param keyword query string false "关键字"
// @Success 200 {object} utils.Response{data=service.ListGroupsResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/attachment-groups [get]
func ListAttachmentGroups(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.AttachmentGroupService").(*service.AttachmentGroupService)

	req, err := validate.ParseQueryAndValidate[service.ListGroupsRequest](c)
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

	groups, err := svc.List(c.Context(), req)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取分组列表失败").ToJSON(c)
	}

	return resp.Success(groups, global.ResponseSuccessCode, "获取分组列表成功").ToJSON(c)
}

// GetAttachmentGroup 获取附件分组详情
// @Summary 获取附件分组详情
// @Description 根据ID获取附件分组详细信息
// @Tags 附件分组管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "分组ID"
// @Success 200 {object} utils.Response{data=service.GroupResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/attachment-groups/{id} [get]
func GetAttachmentGroup(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.AttachmentGroupService").(*service.AttachmentGroupService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的分组ID").ToJSON(c)
	}

	group, err := svc.Get(c.Context(), id)
	if err != nil {
		if err == service.ErrAttachmentGroupNotFound {
			return resp.Fail(global.ErrCodeAttachmentGroupNotFound, "分组不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "获取分组失败").ToJSON(c)
	}

	return resp.Success(group, global.ResponseSuccessCode, "获取分组成功").ToJSON(c)
}

// UpdateAttachmentGroup 更新附件分组
// @Summary 更新附件分组
// @Description 根据ID更新附件分组信息
// @Tags 附件分组管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "分组ID"
// @Param body body service.UpdateGroupRequest true "分组信息"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 409 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/attachment-groups/{id} [put]
func UpdateAttachmentGroup(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.AttachmentGroupService").(*service.AttachmentGroupService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的分组ID").ToJSON(c)
	}

	req, err := validate.ParseAndValidate[service.UpdateGroupRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	if err := svc.Update(c.Context(), id, req); err != nil {
		switch err {
		case service.ErrAttachmentGroupNotFound:
			return resp.Fail(global.ErrCodeAttachmentGroupNotFound, "分组不存在").ToJSON(c)
		case service.ErrAttachmentGroupExists:
			return resp.Fail(global.ErrCodeAttachmentGroupExists, "分组名称已存在").ToJSON(c)
		default:
			return resp.Fail(global.ResponseInternalServerError, "更新分组失败").ToJSON(c)
		}
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "attachment", "更新", fmt.Sprintf("更新附件分组 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "分组更新成功").ToJSON(c)
}

// DeleteAttachmentGroup 删除附件分组
// @Summary 删除附件分组
// @Description 根据ID删除附件分组，分组下有附件时拒绝删除
// @Tags 附件分组管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "分组ID"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 409 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/attachment-groups/{id} [delete]
func DeleteAttachmentGroup(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.AttachmentGroupService").(*service.AttachmentGroupService)

	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的分组ID").ToJSON(c)
	}

	if err := svc.Delete(c.Context(), id); err != nil {
		switch err {
		case service.ErrAttachmentGroupNotFound:
			return resp.Fail(global.ErrCodeAttachmentGroupNotFound, "分组不存在").ToJSON(c)
		case service.ErrAttachmentGroupNotEmpty:
			return resp.Fail(global.ErrCodeAttachmentGroupNotEmpty, "分组下存在附件，无法删除").ToJSON(c)
		default:
			return resp.Fail(global.ResponseInternalServerError, "删除分组失败").ToJSON(c)
		}
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "attachment", "删除", fmt.Sprintf("删除附件分组 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "分组删除成功").ToJSON(c)
}
