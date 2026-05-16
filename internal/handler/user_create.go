package handler

import (
	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/validate"
)

// CreateUser 管理员创建新用户
func CreateUser(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	req, err := validate.ParseAndValidate[service.CreateUserRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return response.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return response.Fail(global.ResponseErrorCode, "无效的请求参数").ToJSON(c)
	}

	userService := c.Locals("service.UserService").(service.UserServiceInterface)
	result, err := userService.CreateUser(c.Context(), req)
	if err != nil {
		switch err {
		case service.ErrEmailExists:
			return response.Fail(global.ResponseErrorCode, "该邮箱已被注册").ToJSON(c)
		case service.ErrUsernameExists:
			return response.Fail(global.ResponseErrorCode, "该用户名已存在").ToJSON(c)
		default:
			return response.Fail(global.ResponseInternalServerError, "创建用户失败: "+err.Error()).ToJSON(c)
		}
	}

	return response.Success(result, global.ResponseSuccessActionCode, "创建成功").ToJSON(c)
}
