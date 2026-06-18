package handler

import (
	"errors"
	"strconv"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/validate"
)

// CreateSystemPermission 创建权限定义
// @Summary 创建权限定义
// @Description 创建新的权限定义
// @Tags 权限定义管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body service.CreateSystemPermissionRequest true "创建权限请求"
// @Success 200 {object} utils.Response{data=service.SystemPermissionResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/system-permissions [post]
func CreateSystemPermission(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.SystemPermissionService").(service.SystemPermissionServiceInterface)

	req, err := validate.ParseAndValidate[service.CreateSystemPermissionRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "无效的请求参数").ToJSON(c)
	}

	perm, err := svc.Create(c.Context(), req)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, err.Error()).ToJSON(c)
	}

	return resp.Success(perm, global.ResponseSuccessCode, "创建权限成功").ToJSON(c)
}

// UpdateSystemPermission 更新权限定义
// @Summary 更新权限定义
// @Description 更新权限定义信息
// @Tags 权限定义管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "权限ID"
// @Param request body service.UpdateSystemPermissionRequest true "更新权限请求"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/system-permissions/{id} [put]
func UpdateSystemPermission(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.SystemPermissionService").(service.SystemPermissionServiceInterface)

	idStr := c.Params("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的权限ID").ToJSON(c)
	}

	req, err := validate.ParseAndValidate[service.UpdateSystemPermissionRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "无效的请求参数").ToJSON(c)
	}

	if err := svc.Update(c.Context(), id, req); err != nil {
		if errors.Is(err, service.ErrPermissionParentCycle) {
			return resp.Fail(global.ResponsePermissionParentCycle, global.ResponseMsg[global.ResponsePermissionParentCycle]).ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, err.Error()).ToJSON(c)
	}

	return resp.Success(nil, global.ResponseSuccessCode, "更新权限成功").ToJSON(c)
}

// DeleteSystemPermission 删除权限定义
// @Summary 删除权限定义
// @Description 根据ID删除权限定义
// @Tags 权限定义管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "权限ID"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/system-permissions/{id} [delete]
func DeleteSystemPermission(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.SystemPermissionService").(service.SystemPermissionServiceInterface)

	idStr := c.Params("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的权限ID").ToJSON(c)
	}

	if err := svc.Delete(c.Context(), id); err != nil {
		return resp.Fail(global.ResponseInternalServerError, err.Error()).ToJSON(c)
	}

	return resp.Success(nil, global.ResponseSuccessCode, "删除权限成功").ToJSON(c)
}

// GetSystemPermission 获取权限定义详情
// @Summary 获取权限定义详情
// @Description 根据ID获取权限定义详细信息
// @Tags 权限定义管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "权限ID"
// @Success 200 {object} utils.Response{data=service.SystemPermissionResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/system-permissions/{id} [get]
func GetSystemPermission(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.SystemPermissionService").(service.SystemPermissionServiceInterface)

	idStr := c.Params("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的权限ID").ToJSON(c)
	}

	perm, err := svc.GetByID(c.Context(), id)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, err.Error()).ToJSON(c)
	}

	return resp.Success(perm, global.ResponseSuccessCode, "获取权限成功").ToJSON(c)
}

// ListSystemPermissions 获取权限定义列表
// @Summary 获取权限定义列表
// @Description 获取权限定义列表，支持分页和筛选
// @Tags 权限定义管理
// @Produce json
// @Security BearerAuth
// @Param page query int false "页码，默认1"
// @Param page_size query int false "每页数量，默认10"
// @Param domain query string false "域"
// @Param type query string false "权限类型"
// @Param keyword query string false "关键字"
// @Param status query int false "状态"
// @Success 200 {object} utils.Response{data=service.ListSystemPermissionsResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/system-permissions [get]
func ListSystemPermissions(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.SystemPermissionService").(service.SystemPermissionServiceInterface)

	req, err := validate.ParseQueryAndValidate[service.ListSystemPermissionsRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "无效的请求参数").ToJSON(c)
	}

	domain, ok := systemPermissionDomain(c)
	if !ok {
		return resp.Fail(global.ResponseUnauthorized, global.ResponseMsg[global.ResponseUnauthorized]).ToJSON(c)
	}
	req.Domain = domain

	uid, ok := c.Locals("uid").(string)
	if !ok || uid == "" {
		return resp.Fail(global.ResponseUnauthorized, global.ResponseMsg[global.ResponseUnauthorized]).ToJSON(c)
	}
	result, err := svc.List(c.Context(), uid, req)
	if err != nil {
		if err == service.ErrPermissionOperatorRequired {
			return resp.Fail(global.ResponseUnauthorized, global.ResponseMsg[global.ResponseUnauthorized]).ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "获取权限列表失败").ToJSON(c)
	}

	return resp.Success(result, global.ResponseSuccessCode, "获取权限列表成功").ToJSON(c)
}

// GetSystemPermissionTree 获取权限树
// @Summary 获取权限树
// @Description 获取树形结构的权限列表
// @Tags 权限定义管理
// @Produce json
// @Security BearerAuth
// @Param domain query string false "域，默认default"
// @Success 200 {object} utils.Response{data=[]service.SystemPermissionTreeNode}
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/system-permissions/tree [get]
func GetSystemPermissionTree(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	svc := c.Locals("service.SystemPermissionService").(service.SystemPermissionServiceInterface)

	domain, ok := systemPermissionDomain(c)
	if !ok {
		return resp.Fail(global.ResponseUnauthorized, global.ResponseMsg[global.ResponseUnauthorized]).ToJSON(c)
	}

	uid, ok := c.Locals("uid").(string)
	if !ok || uid == "" {
		return resp.Fail(global.ResponseUnauthorized, global.ResponseMsg[global.ResponseUnauthorized]).ToJSON(c)
	}

	tree, err := svc.GetTree(c.Context(), uid, domain)
	if err != nil {
		if err == service.ErrPermissionOperatorRequired {
			return resp.Fail(global.ResponseUnauthorized, global.ResponseMsg[global.ResponseUnauthorized]).ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "获取权限树失败").ToJSON(c)
	}

	return resp.Success(tree, global.ResponseSuccessCode, "获取权限树成功").ToJSON(c)
}

func systemPermissionDomain(c fiber.Ctx) (string, bool) {
	domain, ok := c.Locals("domain").(string)
	return domain, ok && domain != ""
}
