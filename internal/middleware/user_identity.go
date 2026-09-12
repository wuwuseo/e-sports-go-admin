package middleware

import (
	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/jwt"

	"go-admin/internal/utils"
)

// UserIdentity 前台用户身份中间件：将用户JWT中的用户ID写入 Locals("uid")，
// 供上传等无需 Casbin 鉴权的前台接口使用（管理端由 PermissionMiddleware 注入）。
func UserIdentity() fiber.Handler {
	return func(c fiber.Ctx) error {
		claims := jwt.GetJWTUserData(c)
		if claims == nil {
			resp := utils.NewResponse()
			return resp.Fail(fiber.StatusUnauthorized, "未授权访问").ToJSON(c)
		}
		userID, ok := claims["id"].(string)
		if !ok || userID == "" {
			resp := utils.NewResponse()
			return resp.Fail(fiber.StatusUnauthorized, "无效的用户ID类型").ToJSON(c)
		}

		// 将用户ID存入Locals，供后续处理器使用
		c.Locals("uid", userID)
		return c.Next()
	}
}
