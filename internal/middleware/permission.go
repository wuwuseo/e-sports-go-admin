package middleware

import (
	"strings"

	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/jwt"
)

// PermissionMiddleware API权限控制中间件
func PermissionMiddleware() fiber.Handler {
	return func(c fiber.Ctx) error {
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

		if isSelfPermissionEndpoint(c.Path(), c.Method()) {
			return c.Next()
		}

		// 获取当前域（由DomainMiddleware从JWT中获取）
		domain, ok := c.Locals("domain").(string)
		if !ok || domain == "" {
			resp := utils.NewResponse()
			return resp.Fail(fiber.StatusUnauthorized, "缺少域信息").ToJSON(c)
		}

		// ✨ 超级管理员旁路检查：admin角色直接放行
		permissionSvc := c.Locals("service.PermissionService").(service.PermissionServiceInterface)
		isAdmin, err := permissionSvc.CheckUserHasRole(c.Context(), userID, domain, "admin")
		if err == nil && isAdmin {
			c.Locals("isSuperAdmin", true)
			return c.Next()
		}

		// 获取请求路径和方法
		method := c.Method()

		// 检查权限。先用实际请求路径匹配，再用 Fiber 路由模板匹配
		// （例如 /roles/2 -> /roles/{id}），兼容系统权限中的模板路径。
		ok = false
		for _, object := range permissionObjectsForRequest(c) {
			ok, err = permissionSvc.CheckPermission(c.Context(), userID, domain, object, method)
			if err != nil {
				resp := utils.NewResponse()
				return resp.Fail(fiber.StatusInternalServerError, "权限检查失败").ToJSON(c)
			}
			if ok {
				break
			}
		}

		if !ok {
			resp := utils.NewResponse()
			return resp.Fail(fiber.StatusForbidden, "没有权限访问该资源").ToJSON(c)
		}

		return c.Next()
	}
}

func permissionObjectsForRequest(c fiber.Ctx) []string {
	path := c.Path()
	objects := []string{path}
	if route := c.Route(); route != nil {
		routePath := normalizePermissionRoutePath(route.Path)
		if routePath != "" && routePath != path {
			objects = append(objects, routePath)
		}
	}
	return objects
}

func normalizePermissionRoutePath(path string) string {
	if path == "" {
		return ""
	}
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if strings.HasPrefix(part, ":") && len(part) > 1 {
			parts[i] = "{" + strings.TrimPrefix(part, ":") + "}"
		}
	}
	return strings.Join(parts, "/")
}

func isSelfPermissionEndpoint(path, method string) bool {
	if method != fiber.MethodGet {
		return false
	}
	switch path {
	case "/api/v1/admin/auth/user/menu-permissions",
		"/api/v1/admin/auth/user/button-permissions",
		"/api/v1/admin/auth/user/api-permissions",
		"/api/v1/admin/auth/user/permissions":
		return true
	default:
		return false
	}
}

// DomainMiddleware 域中间件，用于设置当前域
func DomainMiddleware() fiber.Handler {
	return func(c fiber.Ctx) error {
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
