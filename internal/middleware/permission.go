package middleware

import (
	"strings"

	"go-admin/internal/global"
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

		userID, ok := userIDVal.(string)
		if !ok || userID == "" {
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
		method := c.Method()
		if isSelfPermissionEndpoint(c.Path(), method) {
			return c.Next()
		}

		// 获取Casbin执行器
		enforcer, err := global.EnforcerManager.GetEnforcer(domain)
		if err != nil {
			resp := utils.NewResponse()
			return resp.Fail(fiber.StatusInternalServerError, "获取权限执行器失败").ToJSON(c)
		}

		// 直接从 Casbin 读取用户在当前域下的角色（角色分配时由服务层写入 Casbin）
		roleNames := enforcer.GetRolesForUserInDomain(userID, domain)
		// 防御纵深：过滤禁用/已删除角色，即使 Casbin g 未同步也不放行其权限（fail-closed）
		activeRoles, rolesDiffer, rolesResolved := global.FilterAssignedActiveRoleNames(c.Context(), userID, roleNames)
		if !rolesResolved {
			resp := utils.NewResponse()
			return resp.Fail(fiber.StatusInternalServerError, "读取角色分配失败").ToJSON(c)
		}
		if rolesDiffer {
			// g 是数据库角色绑定的派生索引；不一致时自动重建。
			if _, syncErr := enforcer.DeleteRolesForUserInDomain(userID, domain); syncErr != nil {
				resp := utils.NewResponse()
				return resp.Fail(fiber.StatusInternalServerError, "同步角色权限失败").ToJSON(c)
			}
			for _, roleName := range activeRoles {
				if _, syncErr := enforcer.AddRoleForUserInDomain(userID, roleName, domain); syncErr != nil {
					resp := utils.NewResponse()
					return resp.Fail(fiber.StatusInternalServerError, "同步角色权限失败").ToJSON(c)
				}
			}
		}
		if hasRoleName(activeRoles, "admin") {
			c.Locals("isSuperAdmin", true)
		}

		// 检查权限。先用实际请求路径匹配，再用 Fiber 路由模板匹配
		// （例如 /roles/2 -> /roles/{id}），兼容系统权限中的模板路径。
		ok = false
		objects := permissionObjectsForRequest(c)
		for _, object := range objects {
			ok, err = enforcer.Enforce(userID, domain, object, method)
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

func hasRoleName(roleNames []string, target string) bool {
	for _, roleName := range roleNames {
		if roleName == target {
			return true
		}
	}
	return false
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

func permissionObjectsForRequest(c fiber.Ctx) []string {
	path := c.Path()
	method := c.Method()
	objects := make([]string, 0, 3)
	seen := make(map[string]struct{})

	objects = appendUniquePermissionObject(objects, seen, path)
	if route := c.Route(); route != nil {
		if route.Method == method && fiberRoutePathMatchesRequest(route.Path, path) {
			objects = appendUniquePermissionObject(objects, seen, normalizePermissionRoutePath(route.Path))
		}
	}
	if app := c.App(); app != nil {
		for _, route := range app.GetRoutes(true) {
			if route.Method != method {
				continue
			}
			if !fiberRoutePathMatchesRequest(route.Path, path) {
				continue
			}
			objects = appendUniquePermissionObject(objects, seen, normalizePermissionRoutePath(route.Path))
		}
	}
	return objects
}

func appendUniquePermissionObject(objects []string, seen map[string]struct{}, object string) []string {
	if object == "" {
		return objects
	}
	if _, ok := seen[object]; ok {
		return objects
	}
	seen[object] = struct{}{}
	return append(objects, object)
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

func fiberRoutePathMatchesRequest(routePath, requestPath string) bool {
	routeParts := splitPermissionPath(routePath)
	requestParts := splitPermissionPath(requestPath)

	for i, routePart := range routeParts {
		if isFiberWildcardSegment(routePart) {
			return true
		}
		if i >= len(requestParts) {
			return false
		}
		if !fiberRouteSegmentMatchesRequest(routePart, requestParts[i]) {
			return false
		}
	}
	return len(routeParts) == len(requestParts)
}

func splitPermissionPath(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

func fiberRouteSegmentMatchesRequest(routeSegment, requestSegment string) bool {
	if routeSegment == requestSegment {
		return true
	}
	if strings.HasPrefix(routeSegment, ":") && len(routeSegment) > 1 {
		return requestSegment != ""
	}
	return false
}

func isFiberWildcardSegment(segment string) bool {
	return segment == "*" || strings.HasPrefix(segment, "*")
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
