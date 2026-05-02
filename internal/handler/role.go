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

// CreateRole 创建角色
// @Summary 创建角色
// @Description 创建新角色
// @Tags 角色管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body service.CreateRoleRequest true "角色信息"
// @Success 200 {object} utils.Response{data=service.RoleResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 409 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/roles [post]
func CreateRole(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	roleService := c.Locals("service.RoleService").(*service.RoleService)

	req, err := validate.ParseAndValidate[service.CreateRoleRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	role, err := roleService.CreateRole(c.Context(), req)
	if err != nil {
		switch err {
		case service.ErrRoleNameExists:
			return resp.Fail(global.ResponseConflict, "角色名称已存在").ToJSON(c)
		case service.ErrInvalidRoleName:
			return resp.Fail(global.ResponseErrorCode, "角色名称无效").ToJSON(c)
		default:
			return resp.Fail(global.ResponseInternalServerError, "创建角色失败").ToJSON(c)
		}
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "role", "创建", "创建角色: "+req.Name)

	return resp.Success(role, global.ResponseSuccessCode, "角色创建成功").ToJSON(c)
}

// GetRole 获取角色详情
// @Summary 获取角色详情
// @Description 根据ID获取角色详细信息
// @Tags 角色管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "角色ID"
// @Success 200 {object} utils.Response{data=service.RoleResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/roles/{id} [get]
func GetRole(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	roleService := c.Locals("service.RoleService").(*service.RoleService)

	idStr := c.Params("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的角色ID").ToJSON(c)
	}

	role, err := roleService.GetRole(c.Context(), id)
	if err != nil {
		if err == service.ErrRoleNotFound {
			return resp.Fail(global.ResponseNotFound, "角色不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "获取角色失败").ToJSON(c)
	}

	return resp.Success(role, global.ResponseSuccessCode, "获取角色成功").ToJSON(c)
}

// ListRoles 获取角色列表
// @Summary 获取角色列表
// @Description 获取角色列表，支持分页和关键字筛选
// @Tags 角色管理
// @Produce json
// @Security BearerAuth
// @Param page query int false "页码，默认1"
// @Param page_size query int false "每页数量，默认10"
// @Param keyword query string false "关键字"
// @Param status query string false "状态"
// @Success 200 {object} utils.Response{data=service.ListRolesResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/roles [get]
func ListRoles(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	roleService := c.Locals("service.RoleService").(*service.RoleService)

	req, err := validate.ParseQueryAndValidate[service.ListRolesRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	// 设置默认值
	if req.Page == 0 {
		req.Page = 1
	}
	if req.PageSize == 0 {
		req.PageSize = 10
	}

	roles, err := roleService.ListRoles(c.Context(), req)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取角色列表失败").ToJSON(c)
	}

	return resp.Success(roles, global.ResponseSuccessCode, "获取角色列表成功").ToJSON(c)
}

// UpdateRole 更新角色
// @Summary 更新角色
// @Description 根据ID更新角色信息
// @Tags 角色管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "角色ID"
// @Param body body service.UpdateRoleRequest true "角色信息"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/roles/{id} [put]
func UpdateRole(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	roleService := c.Locals("service.RoleService").(*service.RoleService)

	idStr := c.Params("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的角色ID").ToJSON(c)
	}

	req, err := validate.ParseAndValidate[service.UpdateRoleRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return resp.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	if err := roleService.UpdateRole(c.Context(), id, req); err != nil {
		if err == service.ErrRoleNotFound {
			return resp.Fail(global.ResponseNotFound, "角色不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "更新角色失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "role", "更新", fmt.Sprintf("更新角色 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "角色更新成功").ToJSON(c)
}

// DeleteRole 删除角色
// @Summary 删除角色
// @Description 根据ID删除角色
// @Tags 角色管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "角色ID"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 409 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/roles/{id} [delete]
func DeleteRole(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	roleService := c.Locals("service.RoleService").(*service.RoleService)

	idStr := c.Params("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的角色ID").ToJSON(c)
	}

	if err := roleService.DeleteRole(c.Context(), id); err != nil {
		switch err {
		case service.ErrRoleNotFound:
			return resp.Fail(global.ResponseNotFound, "角色不存在").ToJSON(c)
		case service.ErrSystemRoleProtected:
			return resp.Fail(global.ResponseForbidden, "系统内置角色不可删除").ToJSON(c)
		case service.ErrRoleInUse:
			return resp.Fail(global.ResponseConflict, "角色正在使用中，无法删除").ToJSON(c)
		default:
			return resp.Fail(global.ResponseInternalServerError, "删除角色失败").ToJSON(c)
		}
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "role", "删除", fmt.Sprintf("删除角色 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "角色删除成功").ToJSON(c)
}

// EnableRole 启用角色
// @Summary 启用角色
// @Description 根据ID启用角色
// @Tags 角色管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "角色ID"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/roles/{id}/enable [put]
func EnableRole(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	roleService := c.Locals("service.RoleService").(*service.RoleService)

	idStr := c.Params("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的角色ID").ToJSON(c)
	}

	if err := roleService.EnableRole(c.Context(), id); err != nil {
		if err == service.ErrRoleNotFound {
			return resp.Fail(global.ResponseNotFound, "角色不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "启用角色失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "role", "启用", fmt.Sprintf("启用角色 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "角色启用成功").ToJSON(c)
}

// DisableRole 禁用角色
// @Summary 禁用角色
// @Description 根据ID禁用角色
// @Tags 角色管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "角色ID"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/roles/{id}/disable [put]
func DisableRole(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	roleService := c.Locals("service.RoleService").(*service.RoleService)

	idStr := c.Params("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的角色ID").ToJSON(c)
	}

	if err := roleService.DisableRole(c.Context(), id); err != nil {
		if err == service.ErrRoleNotFound {
			return resp.Fail(global.ResponseNotFound, "角色不存在").ToJSON(c)
		}
		return resp.Fail(global.ResponseInternalServerError, "禁用角色失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "role", "禁用", fmt.Sprintf("禁用角色 ID:%d", id))

	return resp.Success(nil, global.ResponseSuccessCode, "角色禁用成功").ToJSON(c)
}
