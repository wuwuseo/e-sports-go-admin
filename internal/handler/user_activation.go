package handler

import (
	"strconv"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
)

// ActivateUser 激活用户
// @Summary 激活用户
// @Description 激活指定用户，使其可以正常登录
// @Tags 用户管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "用户ID"
// @Success 200 {object} utils.Response "激活成功"
// @Failure 400 {object} utils.Response "参数错误"
// @Failure 401 {object} utils.Response "未授权"
// @Failure 403 {object} utils.Response "权限不足"
// @Failure 404 {object} utils.Response "用户不存在"
// @Router /api/v1/admin/auth/users/{id}/activate [put]
func ActivateUser(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	userID, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return response.Fail(global.ResponseErrorCode, "无效的用户ID").ToJSON(c)
	}

	operatorUID := c.Locals("uid").(string)

	svc := c.Locals("service.UserActivationService").(service.UserActivationServiceInterface)
	err = svc.ActivateUser(c.Context(), userID, operatorUID)
	if err != nil {
		switch err {
		case service.ErrUserNotFound:
			return response.Fail(global.ResponseUserNotFound, global.ResponseMsg[global.ResponseUserNotFound]).ToJSON(c)
		case service.ErrUserAlreadyActive:
			return response.Fail(global.ResponseUserAlreadyActive, global.ResponseMsg[global.ResponseUserAlreadyActive]).ToJSON(c)
		case service.ErrUserBlacklisted:
			return response.Fail(global.ResponseUserBlacklisted, global.ResponseMsg[global.ResponseUserBlacklisted]).ToJSON(c)
		default:
			return response.Fail(global.ResponseInternalServerError, "激活用户失败").ToJSON(c)
		}
	}

	return response.Success(nil, global.ResponseSuccessActionCode).ToJSON(c)
}

// DeactivateUser 停用用户
// @Summary 停用用户
// @Description 停用指定用户，使其无法登录
// @Tags 用户管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path int true "用户ID"
// @Success 200 {object} utils.Response "停用成功"
// @Failure 400 {object} utils.Response "参数错误"
// @Failure 401 {object} utils.Response "未授权"
// @Failure 403 {object} utils.Response "权限不足"
// @Failure 404 {object} utils.Response "用户不存在"
// @Router /api/v1/admin/auth/users/{id}/deactivate [put]
func DeactivateUser(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	userID, err := strconv.Atoi(c.Params("id"))
	if err != nil {
		return response.Fail(global.ResponseErrorCode, "无效的用户ID").ToJSON(c)
	}

	operatorUID := c.Locals("uid").(string)

	svc := c.Locals("service.UserActivationService").(service.UserActivationServiceInterface)
	err = svc.DeactivateUser(c.Context(), userID, operatorUID)
	if err != nil {
		switch err {
		case service.ErrUserNotFound:
			return response.Fail(global.ResponseUserNotFound, global.ResponseMsg[global.ResponseUserNotFound]).ToJSON(c)
		case service.ErrUserAlreadyInactive:
			return response.Fail(global.ResponseUserAlreadyInactive, global.ResponseMsg[global.ResponseUserAlreadyInactive]).ToJSON(c)
		case service.ErrCannotDeactivateSelf:
			return response.Fail(global.ResponseCannotDeactivateSelf, global.ResponseMsg[global.ResponseCannotDeactivateSelf]).ToJSON(c)
		default:
			return response.Fail(global.ResponseInternalServerError, "停用用户失败").ToJSON(c)
		}
	}

	return response.Success(nil, global.ResponseSuccessActionCode).ToJSON(c)
}
