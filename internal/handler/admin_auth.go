package handler

import (
	"time"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/crypto"
	"github.com/wuwuseo/cmf/jwt"
	"github.com/wuwuseo/cmf/validate"
)

// AdminLogin 管理员登录
// @Summary 管理员登录
// @Description 管理员账号密码登录，支持RSA加密登录
// @Tags 管理员认证
// @Accept json
// @Produce json
// @Param request body service.AdminLoginRequest true "登录请求"
// @Success 200 {object} utils.Response{data=service.TokenResponse} "登录成功"
// @Failure 400 {object} utils.Response "请求参数错误"
// @Failure 401 {object} utils.Response "密码错误"
// @Failure 403 {object} utils.Response "账号已禁用"
// @Failure 404 {object} utils.Response "管理员不存在"
// @Router /api/v1/admin/public/login [post]
func AdminLogin(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	req, err := validate.ParseAndValidate[service.AdminLoginRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return response.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return response.Fail(global.ResponseErrorCode, "Invalid request body").ToJSON(c)
	}
	if req.EncryptedUsername != "" && req.EncryptedPassword != "" {

		if req.Timestamp == 0 {
			return response.Fail(global.ResponseErrorCode, "Missing timestamp").ToJSON(c)
		}
		if !crypto.ValidateTimestamp(req.Timestamp, 5*time.Minute) {
			return response.Fail(global.ResponseErrorCode, "Timestamp expired").ToJSON(c)
		}

		if req.Nonce == "" {
			return response.Fail(global.ResponseErrorCode, "Missing nonce").ToJSON(c)
		}
		nonceStore := crypto.GetNonceStore()
		if !nonceStore.Add(req.Nonce) {
			return response.Fail(global.ResponseErrorCode, "Nonce already used").ToJSON(c)
		}

		if req.KeyID == "" {
			return response.Fail(global.ResponseErrorCode, "Missing key_id").ToJSON(c)
		}

		keyManager := crypto.GetKeyManager()
		decryptedUsername, err := keyManager.DecryptData(req.KeyID, req.EncryptedUsername)
		if err != nil {
			return response.Fail(global.ResponseErrorCode, "Failed to decrypt username: "+err.Error()).ToJSON(c)
		}

		decryptedPassword, err := keyManager.DecryptData(req.KeyID, req.EncryptedPassword)
		if err != nil {
			return response.Fail(global.ResponseErrorCode, "Failed to decrypt password: "+err.Error()).ToJSON(c)
		}
		req.Email = decryptedUsername
		req.Password = decryptedPassword
	}

	adminService := c.Locals("service.AdminService").(service.AdminServiceInterface)
	token, err := adminService.Login(c.Context(), req)
	if err != nil {
		switch err {
		case service.ErrAdminNotFound:
			return response.Fail(global.ResponseNotFound, "Admin not found").ToJSON(c)
		case service.ErrInvalidAdminPassword:
			return response.Fail(global.ResponseUnauthorized, "Invalid password").ToJSON(c)
		case service.ErrInvalidAdminStatus:
			return response.Fail(global.ResponseForbidden, "Admin is not active").ToJSON(c)
		default:
			return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
		}
	}

	return response.Success(token).ToJSON(c)
}

// AdminLogout 管理员退出登录
// @Summary 管理员退出登录
// @Description 管理员退出登录，清除Token
// @Tags 管理员认证
// @Accept json
// @Produce json
// @Security BearerAuth
// @Success 200 {object} utils.Response "退出成功"
// @Failure 401 {object} utils.Response "未登录"
// @Router /api/v1/admin/auth/logout [post]
func AdminLogout(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	claims := jwt.GetJWTUserData(c)
	if claims == nil {
		return response.Fail(global.ResponseUnauthorized, "未登录").ToJSON(c)
	}

	uid, ok := claims["id"].(string)
	if !ok || uid == "" {
		return response.Fail(global.ResponseUnauthorized, "无效的管理员信息").ToJSON(c)
	}

	adminService := c.Locals("service.AdminService").(service.AdminServiceInterface)
	if err := adminService.Logout(c.Context(), uid); err != nil {
		return response.Fail(global.ResponseInternalServerError, "退出登录失败").ToJSON(c)
	}

	return response.Success(nil, global.ResponseSuccessActionCode, "退出成功").ToJSON(c)
}

// GetAdmin 获取当前管理员信息
// @Summary 获取当前管理员信息
// @Description 获取当前登录管理员的个人信息
// @Tags 管理员管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Success 200 {object} utils.Response{data=map[string]interface{}} "获取成功"
// @Failure 401 {object} utils.Response "未授权"
// @Failure 404 {object} utils.Response "管理员不存在"
// @Router /api/v1/admin/auth/user [get]
func GetAdmin(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)
	claims := jwt.GetJWTUserData(c)
	uid := claims["id"].(string)

	adminService := c.Locals("service.AdminService").(service.AdminServiceInterface)
	admin, err := adminService.GetAdminByUID(c.Context(), uid)
	if err != nil {
		if err == service.ErrAdminNotFound {
			return response.Fail(global.ResponseNotFound, "Admin not found").ToJSON(c)
		}
		return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
	}

	adminData := fiber.Map{
		"id":             uid,
		"email":          admin.Email,
		"username":       admin.Username,
		"nickname":       admin.Nickname,
		"name":           admin.Name,
		"phone":          admin.Phone,
		"avatar":         admin.Avatar,
		"department":     admin.Department,
		"position":       admin.Position,
		"status":         admin.Status,
		"is_super_admin": admin.IsSuperAdmin,
		"roles":          admin.Roles,
	}

	return response.Success(adminData).ToJSON(c)
}

// UpdateAdmin 更新管理员信息
// @Summary 更新管理员信息
// @Description 更新当前登录管理员的个人信息
// @Tags 管理员管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body map[string]interface{} true "更新字段"
// @Success 200 {object} utils.Response "更新成功"
// @Failure 400 {object} utils.Response "请求参数错误"
// @Failure 401 {object} utils.Response "未授权"
// @Router /api/v1/admin/auth/user [put]
func UpdateAdmin(c fiber.Ctx) error {
	claims := jwt.GetJWTUserData(c)
	uid := claims["id"].(string)
	response := c.Locals("utils.Response").(*utils.Response)

	var updates map[string]interface{}
	if err := c.Bind().Body(&updates); err != nil {
		return response.Fail(global.ResponseErrorCode, "Invalid request body").ToJSON(c)
	}

	adminService := c.Locals("service.AdminService").(service.AdminServiceInterface)
	if err := adminService.UpdateAdmin(c.Context(), uid, updates); err != nil {
		return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
	}

	return response.Success(nil).ToJSON(c)
}

// ChangeAdminPasswordRequest 管理员修改密码请求
type ChangeAdminPasswordRequest struct {
	OldPassword string `json:"old_password" validate:"required"`
	NewPassword string `json:"new_password" validate:"required,min=6"`
}

// ChangeAdminPassword 管理员修改密码
// @Summary 管理员修改密码
// @Description 当前管理员修改自己的登录密码
// @Tags 管理员管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body ChangeAdminPasswordRequest true "修改密码请求"
// @Success 200 {object} utils.Response "修改成功"
// @Failure 400 {object} utils.Response "请求参数错误"
// @Failure 401 {object} utils.Response "原密码错误"
// @Failure 404 {object} utils.Response "管理员不存在"
// @Router /api/v1/admin/auth/password [put]
func ChangeAdminPassword(c fiber.Ctx) error {
	claims := jwt.GetJWTUserData(c)
	uid := claims["id"].(string)
	response := c.Locals("utils.Response").(*utils.Response)

	req, err := validate.ParseAndValidate[ChangeAdminPasswordRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return response.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return response.Fail(global.ResponseErrorCode, "Invalid request body").ToJSON(c)
	}

	adminService := c.Locals("service.AdminService").(service.AdminServiceInterface)
	if err := adminService.ChangePassword(c.Context(), uid, req.OldPassword, req.NewPassword); err != nil {
		switch err {
		case service.ErrAdminNotFound:
			return response.Fail(global.ResponseNotFound, "Admin not found").ToJSON(c)
		case service.ErrInvalidAdminPassword:
			return response.Fail(global.ResponseUnauthorized, "Invalid old password").ToJSON(c)
		default:
			return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
		}
	}

	return response.Success(nil).ToJSON(c)
}

// AdminRefreshToken 管理员刷新令牌
// @Summary 管理员刷新令牌
// @Description 使用Refresh Token刷新Access Token
// @Tags 管理员认证
// @Accept json
// @Produce json
// @Param request body service.RefreshTokenRequest true "刷新令牌请求"
// @Success 200 {object} utils.Response{data=service.TokenResponse} "刷新成功"
// @Failure 400 {object} utils.Response "请求参数错误"
// @Failure 401 {object} utils.Response "Refresh Token无效或已过期"
// @Router /api/v1/admin/public/refresh [post]
func AdminRefreshToken(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	req, err := validate.ParseAndValidate[service.RefreshTokenRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return response.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return response.Fail(global.ResponseErrorCode, "Invalid request body").ToJSON(c)
	}

	adminService := c.Locals("service.AdminService").(service.AdminServiceInterface)

	token, err := adminService.RefreshToken(c.Context(), req.RefreshToken)
	if err != nil {
		return response.Fail(global.ResponseUnauthorized, "Invalid refresh token").ToJSON(c)
	}

	return response.Success(token).ToJSON(c)
}
