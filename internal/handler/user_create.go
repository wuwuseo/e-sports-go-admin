package handler

import (
	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/validate"
)

// CreateUser 管理员创建新用户
// @Summary 创建新用户
// @Description 管理员创建新用户账号
// @Tags 用户管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body service.CreateUserRequest true "创建用户请求"
// @Success 200 {object} utils.Response{data=service.UserResponse} "创建成功"
// @Failure 400 {object} utils.Response "参数错误/邮箱已存在/用户名已存在"
// @Failure 401 {object} utils.Response "未授权"
// @Failure 403 {object} utils.Response "权限不足"
// @Router /api/v1/admin/auth/users [post]
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
