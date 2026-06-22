package captcha

import (
	"go-admin/internal/global"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	cmfcaptcha "github.com/wuwuseo/cmf/captcha"
	"github.com/wuwuseo/cmf/captcha/fiberadapter"
)

func NewFiberAdapter(service fiberadapter.Service) *fiberadapter.Adapter {
	return fiberadapter.New(
		service,
		fiberadapter.WithSuccessHandler(func(c fiber.Ctx, data any) error {
			return c.Locals("utils.Response").(*utils.Response).
				Success(data, global.ResponseSuccessCode, "成功").
				ToJSON(c)
		}),
		fiberadapter.WithErrorHandler(func(
			c fiber.Ctx,
			code cmfcaptcha.ErrorCode,
			_ error,
		) error {
			responseCode, message := captchaResponse(code)
			return c.Locals("utils.Response").(*utils.Response).
				Fail(responseCode, message).
				ToJSON(c)
		}),
	)
}

func captchaResponse(code cmfcaptcha.ErrorCode) (int, string) {
	switch code {
	case cmfcaptcha.CodeRequired:
		return global.ResponseCaptchaRequired, "需要完成验证码"
	case cmfcaptcha.CodeSceneInvalid:
		return global.ResponseCaptchaSceneInvalid, "验证码场景无效"
	case cmfcaptcha.CodeUnavailable:
		return global.ResponseCaptchaProviderUnavailable, "验证码服务暂不可用"
	case cmfcaptcha.CodeMisconfigured:
		return global.ResponseCaptchaProviderMisconfigured, "验证码服务配置错误"
	default:
		return global.ResponseCaptchaInvalid, "验证码无效或已过期"
	}
}
