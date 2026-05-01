package middleware

import (
	"go-admin/internal/global"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v2"
	"github.com/wuwuseo/cmf/jwt"
)

// PermissionMiddleware API权限控制中间件
func PermissionMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		// 从JWT中获取当前用户UID
		claims := jwt.GetJWTUserData(c)
		userIDVal, ok := claims["id"]
		if !ok {
			resp := utils.NewResponse()
			return resp.Fail(fiber.StatusUnauthorized, "未授权访问").ToJSON(c)
		}

		var userID string = userIDVal.(string)
		if userID == "" {
			resp := utils.NewResponse()
			return resp.Fail(fiber.StatusUnauthorized, "无效的用户ID类型").ToJSON(c)
		}

		// 将用户ID存入Locals，供后续处理器使用
		c.Locals("uid", userID)

		// 获取当前域（由DomainMiddleware从JWT中获取）
		domain, ok := c.Locals("domain").(string)
		if !ok || domain == "" {
			resp := utils.NewResponse()
			return resp.Fail(fiber.StatusUnauthorized, "缺少域信息").ToJSON(c)
		}

		// 获取请求路径和方法
		path := c.Path()
		method := c.Method()

		// 获取Casbin执行器
		enforcer, err := global.EnforcerManager.GetEnforcer(domain)
		if err != nil {
			resp := utils.NewResponse()
			return resp.Fail(fiber.StatusInternalServerError, "获取权限执行器失败").ToJSON(c)
		}

		// 检查权限
		ok, err = enforcer.Enforce(userID, domain, path, method)

		if err != nil {
			resp := utils.NewResponse()
			return resp.Fail(fiber.StatusInternalServerError, "权限检查失败").ToJSON(c)
		}

		if !ok {
			resp := utils.NewResponse()
			return resp.Fail(fiber.StatusForbidden, "没有权限访问该资源").ToJSON(c)
		}

		return c.Next()
	}
}

// DomainMiddleware 域中间件，用于设置当前域
func DomainMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		// 从JWT获取域信息
		claims := jwt.GetJWTUserData(c)
		domain, ok := claims["domain"].(string)
		if !ok || domain == "" {
			resp := utils.NewResponse()
			return resp.Fail(fiber.StatusUnauthorized, "JWT中缺少域信息").ToJSON(c)
		}
		c.Locals("domain", domain)
		return c.Next()
	}
}
