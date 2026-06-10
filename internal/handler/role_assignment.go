package handler

import (
	"strconv"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/validate"
)

// GetUserRoles 获取用户角色列表
// @Summary 获取用户角色列表
// @Description 获取指定用户的所有角色
// @Tags 角色分配
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "用户ID"
// @Success 200 {object} utils.Response{data=service.UserRolesResponse} "获取成功"
// @Failure 400 {object} utils.Response "参数错误"
// @Failure 401 {object} utils.Response "未授权"
// @Failure 403 {object} utils.Response "权限不足"
// @Failure 404 {object} utils.Response "用户不存在"
// @Router /api/v1/admin/auth/users/{id}/roles [get]
func GetUserRoles(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	userID, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return response.Fail(global.ResponseErrorCode, "无效的用户ID").ToJSON(c)
	}

	svc := c.Locals("service.RoleAssignmentService").(service.RoleAssignmentServiceInterface)
	result, err := svc.GetUserRoles(c.Context(), userID)
	if err != nil {
		switch err {
		case service.ErrUserNotFound:
			return response.Fail(global.ResponseUserNotFound, global.ResponseMsg[global.ResponseUserNotFound]).ToJSON(c)
		default:
			return response.Fail(global.ResponseInternalServerError, "获取用户角色失败").ToJSON(c)
		}
	}

	return response.Success(result).ToJSON(c)
}

// AssignUserRoles 分配用户角色
// @Summary 分配用户角色
// @Description 为指定用户分配角色，包括主角色
// @Tags 角色分配
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "用户ID"
// @Param request body service.AssignRolesRequest true "分配角色请求"
// @Success 200 {object} utils.Response "分配成功"
// @Failure 400 {object} utils.Response "参数错误/无效的角色ID/主角色无效"
// @Failure 401 {object} utils.Response "未授权"
// @Failure 403 {object} utils.Response "权限不足"
// @Failure 404 {object} utils.Response "用户不存在"
// @Router /api/v1/admin/auth/users/{id}/roles [put]
func AssignUserRoles(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	userID, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return response.Fail(global.ResponseErrorCode, "无效的用户ID").ToJSON(c)
	}

	req, err := validate.ParseAndValidate[service.AssignRolesRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return response.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return response.Fail(global.ResponseErrorCode, "无效的请求参数").ToJSON(c)
	}

	operatorUID := c.Locals("uid").(string)

	svc := c.Locals("service.RoleAssignmentService").(service.RoleAssignmentServiceInterface)
	err = svc.AssignRoles(c.Context(), userID, req, operatorUID)
	if err != nil {
		switch err {
		case service.ErrUserNotFound:
			return response.Fail(global.ResponseUserNotFound, global.ResponseMsg[global.ResponseUserNotFound]).ToJSON(c)
		case service.ErrInvalidRoleIDs:
			return response.Fail(global.ResponseInvalidRoleIDs, global.ResponseMsg[global.ResponseInvalidRoleIDs]).ToJSON(c)
		case service.ErrPrimaryRoleInvalid:
			return response.Fail(global.ResponsePrimaryRoleInvalid, global.ResponseMsg[global.ResponsePrimaryRoleInvalid]).ToJSON(c)
		case service.ErrSuperAdminRoleAssignmentForbidden:
			return response.Fail(global.ResponseForbidden, err.Error()).ToJSON(c)
		default:
			return response.Fail(global.ResponseInternalServerError, "分配角色失败").ToJSON(c)
		}
	}

	return response.Success(nil, global.ResponseSuccessActionCode).ToJSON(c)
}

// GetAdminRoles 获取管理员角色列表
// @Summary 获取管理员角色列表
// @Description 获取指定管理员的所有角色
// @Tags 角色分配
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "管理员ID"
// @Success 200 {object} utils.Response{data=service.UserRolesResponse} "获取成功"
// @Failure 400 {object} utils.Response "参数错误"
// @Failure 401 {object} utils.Response "未授权"
// @Failure 403 {object} utils.Response "权限不足"
// @Failure 404 {object} utils.Response "管理员不存在"
// @Router /api/v1/admin/auth/admins/{id}/roles [get]
func GetAdminRoles(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	adminID, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return response.Fail(global.ResponseErrorCode, "无效的管理员ID").ToJSON(c)
	}

	svc := c.Locals("service.RoleAssignmentService").(service.RoleAssignmentServiceInterface)
	dataPermSvc := c.Locals("service.DataPermissionService").(*service.DataPermissionService)
	op, err := dataPermSvc.NewOperatorContext(c.Context(), getOperatorUID(c))
	if err != nil {
		return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
	}

	result, err := svc.GetAdminRoles(c.Context(), op, adminID)
	if err != nil {
		switch err {
		case service.ErrUserNotFound:
			return response.Fail(global.ResponseUserNotFound, global.ResponseMsg[global.ResponseUserNotFound]).ToJSON(c)
		default:
			return mapAdminScopeError(c, response, err)
		}
	}

	return response.Success(result).ToJSON(c)
}

// AssignAdminRoles 分配管理员角色
// @Summary 分配管理员角色
// @Description 为指定管理员分配角色，包括主角色
// @Tags 角色分配
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "管理员ID"
// @Param request body service.AssignRolesRequest true "分配角色请求"
// @Success 200 {object} utils.Response "分配成功"
// @Failure 400 {object} utils.Response "参数错误/无效的角色ID/主角色无效"
// @Failure 401 {object} utils.Response "未授权"
// @Failure 403 {object} utils.Response "权限不足"
// @Failure 404 {object} utils.Response "管理员不存在"
// @Router /api/v1/admin/auth/admins/{id}/roles [put]
func AssignAdminRoles(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	adminID, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return response.Fail(global.ResponseErrorCode, "无效的管理员ID").ToJSON(c)
	}

	req, err := validate.ParseAndValidate[service.AssignRolesRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return response.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return response.Fail(global.ResponseErrorCode, "无效的请求参数").ToJSON(c)
	}

	operatorUID := getOperatorUID(c)

	svc := c.Locals("service.RoleAssignmentService").(service.RoleAssignmentServiceInterface)
	dataPermSvc := c.Locals("service.DataPermissionService").(*service.DataPermissionService)
	op, err := dataPermSvc.NewOperatorContext(c.Context(), operatorUID)
	if err != nil {
		return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
	}

	err = svc.AssignAdminRoles(c.Context(), op, adminID, req, operatorUID)
	if err != nil {
		switch err {
		case service.ErrUserNotFound:
			return response.Fail(global.ResponseUserNotFound, global.ResponseMsg[global.ResponseUserNotFound]).ToJSON(c)
		case service.ErrInvalidRoleIDs:
			return response.Fail(global.ResponseInvalidRoleIDs, global.ResponseMsg[global.ResponseInvalidRoleIDs]).ToJSON(c)
		case service.ErrPrimaryRoleInvalid:
			return response.Fail(global.ResponsePrimaryRoleInvalid, global.ResponseMsg[global.ResponsePrimaryRoleInvalid]).ToJSON(c)
		case service.ErrSuperAdminRoleAssignmentForbidden:
			return response.Fail(global.ResponseForbidden, err.Error()).ToJSON(c)
		default:
			return mapAdminScopeError(c, response, err)
		}
	}

	return response.Success(nil, global.ResponseSuccessActionCode).ToJSON(c)
}
