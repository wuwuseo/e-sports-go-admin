package handler

import (
	"errors"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/validate"
)

// SMSAuth 短信验证码登录通道（§6.1-01）。
//
// 两条路由均为 AuthPublic（平台级 `/api/v1/user/auth/*`，在 internal/router.go 的
// 鉴权分组**之前**注册），只返回完成该操作所需的最小信息：
// 发码只回 sent / expires_in，**不回验证码**；码登录失败一律同一句中文提示，
// 不暴露「手机号是否已注册」。

// smsCodeServiceFrom 取短信验证码服务；未接线时返回 nil（调用方按不可用处理）。
func smsCodeServiceFrom(c fiber.Ctx) *service.SMSCodeService {
	svc, ok := c.Locals("service.SMSCodeService").(*service.SMSCodeService)
	if !ok {
		return nil
	}
	return svc
}

// SendUserSMSCode 发送短信验证码
// @Summary 发送短信验证码
// @Description 向指定手机号发送登录验证码；返回是否已发送，不返回验证码本身。短信渠道未配置时明确失败。
// @Tags 用户认证
// @Accept json
// @Produce json
// @Param body body service.SendSMSCodeRequest true "手机号"
// @Success 200 {object} utils.Response{data=service.SendSMSCodeResponse}
// @Failure 200 {object} utils.Response "业务失败：短信渠道未配置 / 发送过于频繁 / 当日上限"
// @Router /api/v1/user/auth/sms-code [post]
func SendUserSMSCode(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	req, err := validate.ParseAndValidate[service.SendSMSCodeRequest](c)
	if err != nil {
		return response.Fail(global.ResponseErrorCode, "手机号格式不正确").ToJSON(c)
	}

	svc := smsCodeServiceFrom(c)
	if svc == nil {
		return response.Fail(global.ResponseInternalServerError, "短信验证码服务暂不可用，请使用密码登录").ToJSON(c)
	}

	result, err := svc.SendCode(c.Context(), req.Phone, c.IP())
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidPhone):
			return response.Fail(global.ResponseErrorCode, "手机号格式不正确").ToJSON(c)
		case errors.Is(err, service.ErrSMSChannelUnavailable):
			// 不假成功：渠道未配置就明确告知，让客户端回落密码登录。
			return response.Fail(global.ResponseInternalServerError, "短信渠道未配置，暂时无法发送验证码，请使用密码登录").ToJSON(c)
		case errors.Is(err, service.ErrSMSStoreUnavailable):
			return response.Fail(global.ResponseInternalServerError, "验证码服务暂不可用，请稍后重试或使用密码登录").ToJSON(c)
		case errors.Is(err, service.ErrSMSCodeTooFrequent):
			return response.Fail(global.ResponseErrorCode, "验证码发送过于频繁，请稍后再试").ToJSON(c)
		case errors.Is(err, service.ErrSMSCodeSendLimit):
			return response.Fail(global.ResponseErrorCode, "今日验证码发送次数已达上限，请明日再试或使用密码登录").ToJSON(c)
		default:
			return response.Fail(global.ResponseInternalServerError, "验证码发送失败，请稍后重试或使用密码登录").ToJSON(c)
		}
	}

	return response.Success(result).ToJSON(c)
}

// LoginUserBySMS 短信验证码登录
// @Summary 短信验证码登录
// @Description 用手机号 + 短信验证码登录并签发与密码登录同一套令牌；验证码一次性、短时效。
// @Tags 用户认证
// @Accept json
// @Produce json
// @Param body body service.SMSLoginRequest true "手机号与验证码"
// @Success 200 {object} utils.Response{data=service.TokenResponse}
// @Failure 200 {object} utils.Response "业务失败：手机号或验证码错误"
// @Router /api/v1/user/auth/login-by-sms [post]
func LoginUserBySMS(c fiber.Ctx) error {
	response := c.Locals("utils.Response").(*utils.Response)

	req, err := validate.ParseAndValidate[service.SMSLoginRequest](c)
	if err != nil {
		return response.Fail(global.ResponseUnauthorized, "手机号或验证码错误，请重新获取验证码").ToJSON(c)
	}

	svc := smsCodeServiceFrom(c)
	if svc == nil {
		return response.Fail(global.ResponseInternalServerError, "短信验证码服务暂不可用，请使用密码登录").ToJSON(c)
	}

	// 与密码登录同口径：注入来源元数据用于登录日志与 last_ip
	loginCtx := service.ContextWithLoginMeta(c.Context(), c.IP(), c.Get("User-Agent"))
	token, err := svc.LoginBySMS(loginCtx, req.Phone, req.Code)
	if err != nil {
		// 所有校验失败（含未注册手机号）同一句提示，杜绝手机号枚举。
		if errors.Is(err, service.ErrSMSCodeInvalid) {
			return response.Fail(global.ResponseUnauthorized, "手机号或验证码错误，请重新获取验证码").ToJSON(c)
		}
		if errors.Is(err, service.ErrSMSStoreUnavailable) {
			return response.Fail(global.ResponseInternalServerError, "验证码服务暂不可用，请稍后重试或使用密码登录").ToJSON(c)
		}
		return response.Fail(global.ResponseInternalServerError, "登录失败，请稍后重试或使用密码登录").ToJSON(c)
	}

	return response.Success(token).ToJSON(c)
}
