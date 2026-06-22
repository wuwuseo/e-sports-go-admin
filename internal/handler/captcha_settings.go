package handler

import (
	appcaptcha "go-admin/internal/captcha"
	"go-admin/internal/global"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
)

const captchaSettingsServiceLocal = "captcha.SettingsService"

// GetCaptchaSettings returns all registered scene and provider settings.
// @Summary 获取验证码设置
// @Tags 验证码管理
// @Produce json
// @Security BearerAuth
// @Success 200 {object} utils.Response{data=captcha.SettingsResponse}
// @Failure 500 {object} utils.Response
// @Router /api/v1/admin/auth/captcha/settings [get]
func GetCaptchaSettings(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	settings, err := captchaSettingsService(c).GetSettings(c.Context())
	if err != nil {
		return resp.Fail(global.ResponseInternalServerError, "获取验证码设置失败").ToJSON(c)
	}
	return resp.Success(settings, global.ResponseSuccessCode, "获取验证码设置成功").ToJSON(c)
}

// UpdateCaptchaScene updates one fixed captcha scene policy.
// @Summary 更新验证码场景策略
// @Tags 验证码管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param scene path string true "场景"
// @Param body body captcha.SceneSettings true "场景策略"
// @Success 200 {object} utils.Response{data=captcha.SceneSettings}
// @Failure 400 {object} utils.Response
// @Router /api/v1/admin/auth/captcha/scenes/{scene} [put]
func UpdateCaptchaScene(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	var request appcaptcha.SceneSettings
	if err := c.Bind().Body(&request); err != nil {
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}
	settings, err := captchaSettingsService(c).UpdateScene(c.Context(), c.Params("scene"), request)
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, err.Error()).ToJSON(c)
	}
	return resp.Success(settings, global.ResponseSuccessCode, "验证码场景更新成功").ToJSON(c)
}

// UpdateCaptchaProvider updates encrypted provider settings.
// @Summary 更新验证码 Provider 配置
// @Tags 验证码管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param provider path string true "Provider"
// @Param body body captcha.ProviderSettingsRequest true "Provider 配置"
// @Success 200 {object} utils.Response{data=captcha.ProviderSettingsResponse}
// @Failure 400 {object} utils.Response
// @Router /api/v1/admin/auth/captcha/providers/{provider} [put]
func UpdateCaptchaProvider(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	var request appcaptcha.ProviderSettingsRequest
	if err := c.Bind().Body(&request); err != nil {
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}
	settings, err := captchaSettingsService(c).UpdateProvider(
		c.Context(),
		c.Params("provider"),
		request,
	)
	if err != nil {
		return resp.Fail(global.ResponseErrorCode, err.Error()).ToJSON(c)
	}
	return resp.Success(settings, global.ResponseSuccessCode, "验证码 Provider 更新成功").ToJSON(c)
}

// ValidateCaptchaProvider validates provider configuration without storing it.
// @Summary 校验验证码 Provider 配置
// @Tags 验证码管理
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param provider path string true "Provider"
// @Param body body captcha.ProviderSettingsRequest true "Provider 配置"
// @Success 200 {object} utils.Response
// @Failure 400 {object} utils.Response
// @Router /api/v1/admin/auth/captcha/providers/{provider}/test [post]
func ValidateCaptchaProvider(c fiber.Ctx) error {
	resp := c.Locals("utils.Response").(*utils.Response)
	var request appcaptcha.ProviderSettingsRequest
	if err := c.Bind().Body(&request); err != nil {
		return resp.Fail(global.ResponseErrorCode, "请求参数无效").ToJSON(c)
	}
	if err := captchaSettingsService(c).ValidateProviderConfig(
		c.Context(),
		c.Params("provider"),
		request,
	); err != nil {
		return resp.Fail(global.ResponseErrorCode, err.Error()).ToJSON(c)
	}
	return resp.Success(nil, global.ResponseSuccessCode, "验证码 Provider 配置有效").ToJSON(c)
}

func captchaSettingsService(c fiber.Ctx) *appcaptcha.SettingsService {
	return c.Locals(captchaSettingsServiceLocal).(*appcaptcha.SettingsService)
}
