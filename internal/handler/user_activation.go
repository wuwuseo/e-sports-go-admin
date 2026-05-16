package handler

import (
	"strconv"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
)

// ActivateUser 激活用户
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
