package internal

import (
	"go-admin/internal/handler"
	"go-admin/internal/middleware"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v2"
	"github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/jwt"
)

func RegisterRoute(app *fiber.App, config *config.Config) {
	api := app.Group("/api")                          // API 路由组
	v1 := api.Group("/v1", func(c *fiber.Ctx) error { // v1 版本路由中间件
		c.Set("Version", "1")
		c.Locals("utils.Response", utils.NewResponse())
		return c.Next()
	})

	admin := v1.Group("/admin")
	adminPublic := v1.Group("/public", func(c *fiber.Ctx) error {
		c.Locals("service.UserService", service.NewUserService())
		return c.Next()
	})
	// 公开接口
	adminPublic.Post("/login", handler.Login)
	adminPublic.Post("/refresh-token", handler.RefreshToken)

	// 需要认证的接口
	adminAuth := admin.Group("/auth", jwt.NewJWTMiddleware(config.App.Secret), middleware.DomainMiddleware(), func(c *fiber.Ctx) error {
		c.Locals("service.UserService", service.NewUserService())
		c.Locals("service.RoleService", service.NewRoleService())
		c.Locals("service.PermissionService", service.NewPermissionService())
		return c.Next()
	}, middleware.PermissionMiddleware())
	adminAuth.Get("/user", handler.GetUser)
	adminAuth.Put("/user", handler.UpdateUser)
	adminAuth.Put("/password", handler.ChangePassword)

	// 角色管理路由
	roles := adminAuth.Group("/roles")
	roles.Post("", handler.CreateRole)             // 创建角色
	roles.Get("/:id", handler.GetRole)             // 获取角色详情
	roles.Get("", handler.ListRoles)               // 获取角色列表
	roles.Put("/:id", handler.UpdateRole)          // 更新角色
	roles.Delete("/:id", handler.DeleteRole)       // 删除角色
	roles.Put("/:id/enable", handler.EnableRole)   // 启用角色
	roles.Put("/:id/disable", handler.DisableRole) // 禁用角色

	// 权限管理路由
	permissions := adminAuth.Group("/permissions")
	permissions.Post("", handler.CreatePermission)    // 创建权限
	permissions.Put("", handler.UpdatePermission)     // 更新权限
	permissions.Delete("", handler.DeletePermission)  // 删除权限
	permissions.Get("/detail", handler.GetPermission) // 获取权限详情
	permissions.Get("", handler.ListPermissions)      // 获取权限列表

	// 角色权限管理路由
	rolePermissions := adminAuth.Group("/roles/permissions")
	rolePermissions.Post("/assign", handler.AssignPermissionsToRole)   // 为角色分配权限
	rolePermissions.Post("/remove", handler.RemovePermissionsFromRole) // 从角色移除权限
	rolePermissions.Get("", handler.GetRolePermissions)                // 获取角色权限

	// 用户权限路由
	userPermissions := adminAuth.Group("/user")
	userPermissions.Get("/menu-permissions", handler.GetUserMenuPermissions)     // 获取用户菜单权限
	userPermissions.Get("/button-permissions", handler.GetUserButtonPermissions) // 获取用户按钮权限
	userPermissions.Get("/api-permissions", handler.GetUserAPIPermissions)       // 获取用户API权限
	userPermissions.Get("/permissions", handler.GetUserPermissions)              // 获取用户所有权限（菜单和按钮）
}
