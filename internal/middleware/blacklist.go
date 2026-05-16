package middleware

import (
	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/jwt"
)

// BlacklistCheck 黑名单校验中间件
// 在JWT认证通过后、PermissionMiddleware之前执行
func BlacklistCheck(blacklistSvc service.BlacklistServiceInterface) fiber.Handler {
	return func(c fiber.Ctx) error {
		claims := jwt.GetJWTUserData(c)
		if claims == nil {
			return c.Next()
		}
		uid, ok := claims["id"].(string)
		if !ok || uid == "" {
			return c.Next()
		}

		if blacklistSvc != nil {
			blacklisted, err := blacklistSvc.IsBlacklisted(c.Context(), uid)
			if err != nil {
				response := utils.NewResponse()
				return response.Fail(global.ResponseInternalServerError, "黑名单校验异常").ToJSON(c)
			}
			if blacklisted {
				response := utils.NewResponse()
				return response.Fail(global.ResponseForbidden, "您的账号已被限制访问，请联系管理员").ToJSON(c)
			}

			loggedOut, err := blacklistSvc.IsLoggedOut(c.Context(), uid)
			if err != nil {
				response := utils.NewResponse()
				return response.Fail(global.ResponseInternalServerError, "会话校验异常").ToJSON(c)
			}
			if loggedOut {
				response := utils.NewResponse()
				return response.Fail(global.ResponseUnauthorized, "登录已失效，请重新登录").ToJSON(c)
			}
		}

		return c.Next()
	}
}
