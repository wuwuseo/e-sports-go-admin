package handler

import (
	"fmt"
	"time"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/crypto"
	"github.com/wuwuseo/cmf/jwt"
	"github.com/wuwuseo/cmf/validate"
)

// GetPublicKeyResponse 获取公钥响应
type GetPublicKeyResponse struct {
	KeyID       string `json:"key_id"`       // 密钥ID
	PublicKey   string `json:"public_key"`   // 公钥内容
	Algorithm   string `json:"algorithm"`    // 加密算法
	CreatedAt   string `json:"created_at"`   // 密钥创建时间
	ExpiresAt   string `json:"expires_at"`   // 密钥过期时间
	RotateCycle string `json:"rotate_cycle"` // 密钥轮换周期
}

// GetPublicKey 获取RSA公钥
// @Summary 获取RSA公钥
// @Description 获取用于加密登录请求的RSA公钥
// @Tags 用户管理
// @Produce json
// @Success 200 {object} utils.Response{data=GetPublicKeyResponse}
// @Failure 500 {object} utils.Response
// @Router /api/v1/public/public-key [get]
func GetPublicKey(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	keyManager := crypto.GetKeyManager()
	keyID, publicKeyPEM, err := keyManager.GetPublicKeyPEM()
	if err != nil {
		return response.Fail(global.ResponseInternalServerError, "Failed to get public key").ToJSON(c)
	}

	// // 设置缓存控制头（缓存1小时，因为密钥24小时才轮换）
	// c.Set("Cache-Control", "public, max-age=3600, stale-while-revalidate=600")
	// c.Set("ETag", fmt.Sprintf("\"%s\"", keyID))

	return response.Success(GetPublicKeyResponse{
		KeyID:       keyID,
		PublicKey:   publicKeyPEM,
		Algorithm:   "RSA-2048",
		CreatedAt:   time.Now().Format(time.RFC3339),
		ExpiresAt:   time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		RotateCycle: "24h",
	}).ToJSON(c)
}

// ChangePasswordRequest 修改密码请求
type ChangePasswordRequest struct {
	OldPassword string `json:"old_password" validate:"required"`
	NewPassword string `json:"new_password" validate:"required,min=8"`
}

func decryptLoginCredentials(encryptedIdentity, encryptedPassword string, timestamp int64, nonce, keyID, identityField string) (string, string, error) {
	if timestamp == 0 {
		return "", "", fmt.Errorf("Missing timestamp")
	}
	if !crypto.ValidateTimestamp(timestamp, 5*time.Minute) {
		return "", "", fmt.Errorf("Timestamp expired")
	}
	if nonce == "" {
		return "", "", fmt.Errorf("Missing nonce")
	}
	if !crypto.GetNonceStore().Add(nonce) {
		return "", "", fmt.Errorf("Nonce already used")
	}
	if keyID == "" {
		return "", "", fmt.Errorf("Missing key_id")
	}
	identity, err := crypto.GetKeyManager().DecryptData(keyID, encryptedIdentity)
	if err != nil {
		return "", "", fmt.Errorf("Failed to decrypt %s: %w", identityField, err)
	}
	password, err := crypto.GetKeyManager().DecryptData(keyID, encryptedPassword)
	if err != nil {
		return "", "", fmt.Errorf("Failed to decrypt password: %w", err)
	}
	return identity, password, nil
}

// Login 用户登录
// @Summary 用户登录
// @Description 用户登录并获取访问令牌
// @Tags 用户管理
// @Accept json
// @Produce json
// @Param body body service.LoginRequest true "登录信息"
// @Success 200 {object} utils.Response{data=service.TokenResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 403 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/public/login [post]
func Login(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	req, err := validate.ParseAndValidate[service.LoginRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return response.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return response.Fail(global.ResponseErrorCode, "Invalid request body").ToJSON(c)
	}

	if req.EncryptedEmail != "" && req.EncryptedPassword != "" {
		decryptedEmail, decryptedPassword, err := decryptLoginCredentials(
			req.EncryptedEmail, req.EncryptedPassword, req.Timestamp, req.Nonce, req.KeyID, "email",
		)
		if err != nil {
			return response.Fail(global.ResponseErrorCode, err.Error()).ToJSON(c)
		}
		req.Email = decryptedEmail
		req.Password = decryptedPassword
	}

	userService := c.Locals("service.UserService").(service.UserServiceInterface)
	token, err := userService.Login(c.Context(), req)
	if err != nil {
		switch err {
		case service.ErrUserNotFound, service.ErrInvalidPassword, service.ErrInvalidUserStatus:
			return response.Fail(global.ResponseUnauthorized, "用户名或密码错误").ToJSON(c)
		default:
			return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
		}
	}

	return response.Success(token).ToJSON(c)
}

// Logout 用户退出登录
func Logout(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	claims := jwt.GetJWTUserData(c)
	if claims == nil {
		return response.Fail(global.ResponseUnauthorized, "未登录").ToJSON(c)
	}

	uid, ok := claims["id"].(string)
	if !ok || uid == "" {
		return response.Fail(global.ResponseUnauthorized, "无效的用户信息").ToJSON(c)
	}

	userService := c.Locals("service.UserService").(service.UserServiceInterface)
	if err := userService.Logout(c.Context(), uid); err != nil {
		return response.Fail(global.ResponseInternalServerError, "退出登录失败").ToJSON(c)
	}

	return response.Success(nil, global.ResponseSuccessActionCode, "退出成功").ToJSON(c)
}

// GetUser 获取当前用户信息
// @Summary 获取当前用户信息
// @Description 获取当前登录用户的详细信息，仅需 JWT 认证，无需额外权限检查
// @Tags 用户个人信息
// @Produce json
// @Security BearerAuth
// @Success 200 {object} utils.Response{data=map[string]interface{}}
// @Failure 401 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/user [get]
func GetUser(c fiber.Ctx) error {
	userService := c.Locals("service.UserService").(service.UserServiceInterface)

	response := c.Locals("utils.Response").(*utils.Response)
	claims := jwt.GetJWTUserData(c)
	userID := claims["id"].(string)
	user, err := userService.GetUserByUID(c.Context(), userID)
	if err != nil {
		if err == service.ErrUserNotFound {
			return response.Fail(global.ResponseNotFound, "User not found").ToJSON(c)
		}
		return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
	}

	// 过滤敏感信息
	userData := fiber.Map{
		"id":       userID,
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
// @Summary 更新当前用户信息
// @Description 更新当前登录用户的信息，仅需 JWT 认证，无需额外权限检查
// @Tags 用户个人信息
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body map[string]interface{} true "用户信息"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/user [put]
func UpdateUser(c fiber.Ctx) error {
	claims := jwt.GetJWTUserData(c)
	userID := claims["id"].(string)
	response := c.Locals("utils.Response").(*utils.Response)

	var updates map[string]any
	if err := c.Bind().Body(&updates); err != nil {
		return response.Fail(global.ResponseErrorCode, "Invalid request body").ToJSON(c)
	}

	userService := c.Locals("service.UserService").(service.UserServiceInterface)
	if err := userService.UpdateUser(c.Context(), userID, updates); err != nil {
		return response.Fail(global.ResponseInternalServerError, "Internal server error").ToJSON(c)
	}

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "user", "更新信息", fmt.Sprintf("更新用户信息: %s", userID))

	return response.Success(nil).ToJSON(c)
}

// ChangePassword 修改密码
// @Summary 修改当前用户密码
// @Description 修改当前登录用户的密码，仅需 JWT 认证，无需额外权限检查
// @Tags 用户个人信息
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body ChangePasswordRequest true "密码信息"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 404 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/password [put]
func ChangePassword(c fiber.Ctx) error {
	claims := jwt.GetJWTUserData(c)

	userID := claims["id"].(string)
	response := c.Locals("utils.Response").(*utils.Response)
	req, err := validate.ParseAndValidate[ChangePasswordRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return response.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return response.Fail(global.ResponseErrorCode, "Invalid request body").ToJSON(c)
	}

	userService := c.Locals("service.UserService").(service.UserServiceInterface)
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

	logService := c.Locals("service.OperationLogService").(service.OperationLogServiceInterface)
	logOperation(c, logService, "user", "修改密码", fmt.Sprintf("用户修改密码: %s", userID))

	return response.Success(nil).ToJSON(c)
}

// RefreshToken 刷新访问令牌
// @Summary 刷新访问令牌
// @Description 使用刷新令牌获取新的访问令牌
// @Tags 用户管理
// @Accept json
// @Produce json
// @Param body body service.RefreshTokenRequest true "刷新令牌"
// @Success 200 {object} utils.Response{data=service.TokenResponse}
// @Failure 400 {object} utils.Response
// @Failure 401 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/refresh-token [post]
func RefreshToken(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	req, err := validate.ParseAndValidate[service.RefreshTokenRequest](c)
	if err != nil {
		if validate.IsValidationError(err) {
			return response.Fail(global.ResponseErrorCode, validate.FirstErrorMessage(err)).ToJSON(c)
		}
		return response.Fail(global.ResponseErrorCode, "Invalid request body").ToJSON(c)
	}

	// 获取用户服务
	userService := c.Locals("service.UserService").(service.UserServiceInterface)

	// 调用刷新令牌服务
	token, err := userService.RefreshToken(c.Context(), req.RefreshToken)
	if err != nil {
		return response.Fail(global.ResponseUnauthorized, "Invalid refresh token").ToJSON(c)
	}

	return response.Success(token).ToJSON(c)
}
