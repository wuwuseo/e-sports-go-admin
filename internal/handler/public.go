package handler

import (
	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/wuwuseo/cmf/jwt"

	"github.com/gofiber/fiber/v2"
)

// Login 用户登录
// @Summary 用户登录
// @Description 用户登录并获取访问令牌
// @Tags 用户管理
// @Accept json
// @Produce json
// @Param body body service.LoginRequest true "登录信息"
// @Success 200 {object} Response{data=service.TokenResponse}
// @Router /api/v1/admin/login [post]
func Login(c *fiber.Ctx) error {
	var req service.LoginRequest
	response := c.Locals("utils.Response").(*utils.Response)
	if err := c.BodyParser(&req); err != nil {
		return response.Fail(global.ResponseErrorCode, "Invalid request body").ToJSON(c)
	}

	userService := c.Locals("service.UserService").(service.UserServiceInterface)
	token, err := userService.Login(c.Context(), &req)
	if err != nil {
		switch err {
		case service.ErrUserNotFound:
			return response.Fail(global.ResponseNotFound, "User not found").ToJSON(c)
		case service.ErrInvalidPassword:
			return response.Fail(global.ResponseUnauthorized, "Invalid password").ToJSON(c)
		case service.ErrInvalidUserStatus:
			return response.Fail(global.ResponseForbidden, "User is not active").ToJSON(c)
		default:
			return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
		}
	}

	return response.Success(token).ToJSON(c)
}

// GetUser 获取当前用户信息
// @Summary 获取用户信息
// @Description 获取当前登录用户的详细信息
// @Tags 用户管理
// @Produce json
// @Security BearerAuth
// @Success 200 {object} Response
// @Router /api/v1/admin/auth/user [get]
func GetUser(c *fiber.Ctx) error {
	userService := c.Locals("service.UserService").(service.UserServiceInterface)

	response := c.Locals("utils.Response").(*utils.Response)
	claims := jwt.GetJWTUserData(c)
	userID := claims["id"].(string)
	user, err := userService.GetUserByID(c.Context(), userID)
	if err != nil {
		if err == service.ErrUserNotFound {
			return response.Fail(404, "User not found").ToJSON(c)
		}
		return response.Fail(500, "Internal server error").ToJSON(c)
	}

	// 过滤敏感信息
	userData := fiber.Map{
		"id":       user.ID,
		"email":    user.Email,
		"username": user.Username,
		"nickname": user.Nickname,
		"phone":    user.Phone,
		"avatar":   user.Avatar,
		"status":   user.Status,
	}

	return response.Success(userData).ToJSON(c)
}

// UpdateUser 更新用户信息
// @Summary 更新用户信息
// @Description 更新当前登录用户的信息
// @Tags 用户管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body map[string]interface{} true "用户信息"
// @Success 200 {object} Response
// @Router /api/v1/admin/auth/user [put]
func UpdateUser(c *fiber.Ctx) error {
	claims := jwt.GetJWTUserData(c)
	userID := claims["id"].(string)
	response := c.Locals("utils.Response").(*utils.Response)

	var updates map[string]any
	if err := c.BodyParser(&updates); err != nil {
		return response.Fail(global.ResponseErrorCode, "Invalid request body").ToJSON(c)
	}

	userService := c.Locals("service.UserService").(service.UserServiceInterface)
	if err := userService.UpdateUser(c.Context(), userID, updates); err != nil {
		return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
	}

	return response.Success(nil).ToJSON(c)
}

// ChangePassword 修改密码
// @Summary 修改密码
// @Description 修改当前登录用户的密码
// @Tags 用户管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body map[string]string true "密码信息"
// @Success 200 {object} Response
// @Router /api/v1/admin/auth/password [put]
func ChangePassword(c *fiber.Ctx) error {
	claims := jwt.GetJWTUserData(c)

	userID := claims["id"].(string)
	response := c.Locals("utils.Response").(*utils.Response)
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := c.BodyParser(&req); err != nil {
		return response.Fail(global.ResponseErrorCode, "Invalid request body").ToJSON(c)
	}

	userService := c.Locals("userService").(service.UserServiceInterface)
	if err := userService.ChangePassword(c.Context(), userID, req.OldPassword, req.NewPassword); err != nil {
		switch err {
		case service.ErrUserNotFound:
			return response.Fail(global.ResponseNotFound, "User not found").ToJSON(c)
		case service.ErrInvalidPassword:
			return response.Fail(global.ResponseUnauthorized, "Invalid old password").ToJSON(c)
		default:
			return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
		}
	}

	return response.Success(nil).ToJSON(c)
}
