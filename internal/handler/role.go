package handler

import (
	"strconv"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v2"
)

// CreateRole 创建角色
func CreateRole(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	roleService := c.Locals("service.RoleService").(*service.RoleService)

	var req service.CreateRoleRequest
	if err := c.BodyParser(&req); err != nil {
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	role, err := roleService.CreateRole(c.Context(), &req)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, err.Error()).ToJSON(c)
	}

	return resp.Success(role, global.ResponseSuccessCode, "角色创建成功").ToJSON(c)
}

// GetRole 获取角色详情
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
		return resp.Fail(global.ResponseInternalServerError, err.Error()).ToJSON(c)
	}

	return resp.Success(role, global.ResponseSuccessCode, "获取角色成功").ToJSON(c)
}

// ListRoles 获取角色列表
func ListRoles(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	roleService := c.Locals("service.RoleService").(*service.RoleService)

	var req service.ListRolesRequest
	if err := c.QueryParser(&req); err != nil {
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	// 设置默认值
	if req.Page == 0 {
		req.Page = 1
	}
	if req.PageSize == 0 {
		req.PageSize = 10
	}

	roles, err := roleService.ListRoles(c.Context(), &req)
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, err.Error()).ToJSON(c)
	}

	return resp.Success(roles, global.ResponseSuccessCode, "获取角色列表成功").ToJSON(c)
}

// UpdateRole 更新角色
func UpdateRole(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	roleService := c.Locals("service.RoleService").(*service.RoleService)

	idStr := c.Params("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的角色ID").ToJSON(c)
	}

	var req service.UpdateRoleRequest
	if err := c.BodyParser(&req); err != nil {
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}

	if err := roleService.UpdateRole(c.Context(), id, &req); err != nil {
		return resp.Fail(global.ResponseInternalServerError, err.Error()).ToJSON(c)
	}

	return resp.Success(nil, global.ResponseSuccessCode, "角色更新成功").ToJSON(c)
}

// DeleteRole 删除角色
func DeleteRole(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	roleService := c.Locals("service.RoleService").(*service.RoleService)

	idStr := c.Params("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的角色ID").ToJSON(c)
	}

	if err := roleService.DeleteRole(c.Context(), id); err != nil {
		return resp.Fail(global.ResponseInternalServerError, err.Error()).ToJSON(c)
	}

	return resp.Success(nil, global.ResponseSuccessCode, "角色删除成功").ToJSON(c)
}

// EnableRole 启用角色
func EnableRole(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	roleService := c.Locals("service.RoleService").(*service.RoleService)

	idStr := c.Params("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的角色ID").ToJSON(c)
	}

	if err := roleService.EnableRole(c.Context(), id); err != nil {
		return resp.Fail(global.ResponseInternalServerError, err.Error()).ToJSON(c)
	}

	return resp.Success(nil, global.ResponseSuccessCode, "角色启用成功").ToJSON(c)
}

// DisableRole 禁用角色
func DisableRole(c *fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	roleService := c.Locals("service.RoleService").(*service.RoleService)

	idStr := c.Params("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, "无效的角色ID").ToJSON(c)
	}

	if err := roleService.DisableRole(c.Context(), id); err != nil {
		return resp.Fail(global.ResponseInternalServerError, err.Error()).ToJSON(c)
	}

	return resp.Success(nil, global.ResponseSuccessCode, "角色禁用成功").ToJSON(c)
}
