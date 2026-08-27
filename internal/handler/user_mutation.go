package handler

import (
	"fmt"
	"strconv"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/validate"
)

// UpdateManagedUser updates a user from the administrator user-management page.
// @Summary 更新用户
// @Tags 用户管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "用户ID"
// @Param request body service.UpdateManagedUserRequest true "更新用户请求"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Router /api/v1/admin/auth/users/{id} [put]
func UpdateManagedUser(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return response.Fail(global.ResponseErrorCode, "无效的用户ID").ToJSON(c)
	}
	req, err := validate.ParseAndValidate[service.UpdateManagedUserRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return response.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return response.Fail(global.ResponseErrorCode, "无效的请求参数").ToJSON(c)
	}

	userService := c.Locals("service.UserService").(service.UserServiceInterface)
	if err := userService.UpdateManagedUser(c.Context(), id, req); err != nil {
		switch err {
		case service.ErrUserNotFound:
			return response.Fail(global.ResponseNotFound, "用户不存在").ToJSON(c)
		case service.ErrEmailExists:
			return response.Fail(global.ResponseErrorCode, "该邮箱已被注册").ToJSON(c)
		case service.ErrUsernameExists:
			return response.Fail(global.ResponseErrorCode, "该用户名已存在").ToJSON(c)
		default:
			return response.Fail(global.ResponseErrorCode, err.Error()).ToJSON(c)
		}
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "user", "更新用户", fmt.Sprintf("更新用户: %d", id))
	return response.Success(nil, global.ResponseSuccessActionCode, "更新成功").ToJSON(c)
}

// DeleteManagedUser deletes a user from the administrator user-management page.
// @Summary 删除用户
// @Tags 用户管理
// @Produce json
// @Security BearerAuth
// @Param id path int true "用户ID"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Router /api/v1/admin/auth/users/{id} [delete]
func DeleteManagedUser(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return response.Fail(global.ResponseErrorCode, "无效的用户ID").ToJSON(c)
	}

	userService := c.Locals("service.UserService").(service.UserServiceInterface)
	operatorUID := c.Locals("uid").(string)
	if err := userService.DeleteManagedUser(c.Context(), id, operatorUID); err != nil {
		if err == service.ErrUserNotFound {
			return response.Fail(global.ResponseNotFound, "用户不存在").ToJSON(c)
		}
		if err == service.ErrCannotDeleteSelf {
			return response.Fail(global.ResponseCannotDeleteSelf, global.ResponseMsg[global.ResponseCannotDeleteSelf]).ToJSON(c)
		}
		return response.Fail(global.ResponseInternalServerError, "删除用户失败").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "user", "删除用户", fmt.Sprintf("删除用户: %d", id))
	return response.Success(nil, global.ResponseSuccessActionCode, "删除成功").ToJSON(c)
}
