package middleware

import (
	"context"
	"strings"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/casbin/casbin/v2"
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

		// 获取当前域（由DomainMiddleware从JWT中获取）
		domain, ok := c.Locals("domain").(string)
		if !ok || domain == "" {
			resp := utils.NewResponse()
			return resp.Fail(fiber.StatusUnauthorized, "缺少域信息").ToJSON(c)
		}

		permissionSvc := c.Locals("service.PermissionService").(service.PermissionServiceInterface)

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

		roleNames, err := syncCasbinRoleLinks(c.Context(), permissionSvc, enforcer, userID, domain)
		if err != nil {
			resp := utils.NewResponse()
			return resp.Fail(fiber.StatusInternalServerError, "同步权限角色失败").ToJSON(c)
		}
		if hasRoleName(roleNames, "admin") {
			c.Locals("isSuperAdmin", true)
		}

		// 检查权限。先用实际请求路径匹配，再用 Fiber 路由模板匹配
		// （例如 /roles/2 -> /roles/{id}），兼容系统权限中的模板路径。
		ok = false
		for _, object := range permissionObjectsForRequest(c) {
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

func syncCasbinRoleLinks(ctx context.Context, permissionSvc service.PermissionServiceInterface, enforcer *casbin.Enforcer, userID, domain string) ([]string, error) {
	roleNames, err := permissionSvc.GetUserRoleNames(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(roleNames) == 0 {
		return roleNames, nil
	}

	existingRoleNames := enforcer.GetRolesForUserInDomain(userID, domain)

	for _, roleName := range missingRoleNames(roleNames, existingRoleNames) {
		if _, err := enforcer.AddRoleForUserInDomain(userID, roleName, domain); err != nil {
			return nil, err
		}
	}
	return roleNames, nil
}

func missingRoleNames(desired, existing []string) []string {
	existingSet := make(map[string]struct{}, len(existing))
	for _, roleName := range existing {
		existingSet[roleName] = struct{}{}
	}

	missing := make([]string, 0)
	for _, roleName := range desired {
		if _, ok := existingSet[roleName]; !ok {
			missing = append(missing, roleName)
		}
	}
	return missing
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
