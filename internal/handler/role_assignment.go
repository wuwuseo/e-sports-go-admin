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
		default:
			return response.Fail(global.ResponseInternalServerError, "分配角色失败").ToJSON(c)
		}
	}

	return response.Success(nil, global.ResponseSuccessActionCode).ToJSON(c)
}
