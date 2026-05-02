package internal

import (
	"go-admin/internal/global"
	"go-admin/internal/handler"
	"go-admin/internal/middleware"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v2"
	"github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/jwt"
	"github.com/wuwuseo/cmf/log"
)

func RegisterRoute(app *fiber.App, config *config.Config) {
	// 添加请求日志中间件
	if appInstance != nil && appInstance.Logger != nil {
		app.Use(log.RequestLoggerMiddleware(appInstance.Logger))
	}

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

	// 初始化带缓存的服务
	var permissionService service.PermissionServiceInterface
	var roleService service.RoleServiceInterface
	if appInstance != nil && appInstance.Cache != nil {
		permissionService = service.NewPermissionService(appInstance.Cache)
		roleService = service.NewRoleService(appInstance.Cache)
	} else {
		permissionService = service.NewPermissionService(nil)
		roleService = service.NewRoleService(nil)
	}

	// 初始化操作日志服务
	opLogService := service.NewOperationLogService()
	// 公开接口
	adminPublic.Post("/login", handler.Login)
	adminPublic.Post("/refresh-token", handler.RefreshToken)
	adminPublic.Get("/public-key", handler.GetPublicKey) // 获取RSA公钥用于加密登录

	// 仅认证、不需要额外权限的用户个人信息路由（必须在 adminAuth 之前声明，避免被 PermissionMiddleware 拦截）
	adminAuthUser := admin.Group("/auth", jwt.NewJWTMiddleware(config.App.Secret), func(c *fiber.Ctx) error {
		c.Locals("service.UserService", service.NewUserService())
		c.Locals("utils.Response", utils.NewResponse())
		return c.Next()
	})
	adminAuthUser.Get("/user", handler.GetUser)
	adminAuthUser.Put("/user", handler.UpdateUser)
	adminAuthUser.Put("/password", handler.ChangePassword)

	// 需要认证+权限校验的接口
	adminAuth := admin.Group("/auth", jwt.NewJWTMiddleware(config.App.Secret), middleware.DomainMiddleware(), func(c *fiber.Ctx) error {
		c.Locals("service.UserService", service.NewUserService())
		c.Locals("service.RoleService", roleService)
		c.Locals("service.PermissionService", permissionService)
		c.Locals("service.OperationLogService", opLogService)
		return c.Next()
	}, middleware.PermissionMiddleware())

	// 角色管理路由
	roles := adminAuth.Group("/roles")
	roles.Post("", handler.CreateRole) // 创建角色

	// 角色权限管理路由（必须在 /:id 之前注册，避免被参数化路由捕获）
	roles.Get("/permissions", handler.GetRolePermissions)                // 获取角色权限
	roles.Post("/permissions/assign", handler.AssignPermissionsToRole)   // 为角色分配权限
	roles.Post("/permissions/remove", handler.RemovePermissionsFromRole) // 从角色移除权限

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

	// 用户权限路由
	userPermissions := adminAuth.Group("/user")
	userPermissions.Get("/menu-permissions", handler.GetUserMenuPermissions)     // 获取用户菜单权限
	userPermissions.Get("/button-permissions", handler.GetUserButtonPermissions) // 获取用户按钮权限
	userPermissions.Get("/api-permissions", handler.GetUserAPIPermissions)       // 获取用户API权限
	userPermissions.Get("/permissions", handler.GetUserPermissions)              // 获取用户所有权限（菜单和按钮）

	// 定时任务管理路由
	cronJobService := service.NewCronJobService(global.CronManager)
	cronJobs := adminAuth.Group("/cron-jobs", func(c *fiber.Ctx) error {
		c.Locals("service.CronJobService", cronJobService)
		return c.Next()
	})
	cronJobs.Post("", handler.CreateCronJob)                          // 创建定时任务
	cronJobs.Get("", handler.ListCronJobs)                            // 获取定时任务列表
	cronJobs.Get("/:id", handler.GetCronJob)                          // 获取定时任务详情
	cronJobs.Put("/:id", handler.UpdateCronJob)                       // 更新定时任务
	cronJobs.Delete("/:id", handler.DeleteCronJob)                    // 删除定时任务
	cronJobs.Post("/:id/enable", handler.EnableCronJob)               // 启用定时任务
	cronJobs.Post("/:id/disable", handler.DisableCronJob)             // 禁用定时任务
	cronJobs.Post("/:id/run", handler.RunCronJob)                     // 立即执行
	cronJobs.Get("/:id/logs", handler.GetCronJobLogs)                 // 获取日志列表
	cronJobs.Get("/:id/logs/tail", handler.TailCronJobLog)            // 读取日志尾部
	cronJobs.Get("/:id/logs/:filename", handler.GetCronJobLogContent) // 获取日志内容

	// 附件分组管理路由
	attachmentGroupService := service.NewAttachmentGroupService()
	var attachmentService service.AttachmentServiceInterface
	if appInstance != nil && appInstance.Filesystem != nil {
		attachmentService = service.NewAttachmentService(appInstance.Filesystem)
	} else {
		attachmentService = service.NewAttachmentService(nil)
	}

	groups := adminAuth.Group("/attachment-groups", func(c *fiber.Ctx) error {
		c.Locals("service.AttachmentGroupService", attachmentGroupService)
		return c.Next()
	})
	groups.Post("/", handler.CreateAttachmentGroup)
	groups.Get("/", handler.ListAttachmentGroups)
	groups.Get("/:id", handler.GetAttachmentGroup)
	groups.Put("/:id", handler.UpdateAttachmentGroup)
	groups.Delete("/:id", handler.DeleteAttachmentGroup)

	// 附件管理路由
	attachments := adminAuth.Group("/attachments", func(c *fiber.Ctx) error {
		c.Locals("service.AttachmentService", attachmentService)
		return c.Next()
	})
	attachments.Post("/upload", handler.UploadAttachment)
	attachments.Delete("/batch", handler.BatchDeleteAttachments)
	attachments.Get("/", handler.ListAttachments)
	attachments.Get("/:id", handler.GetAttachment)
	attachments.Get("/:id/preview", handler.PreviewAttachment)
	attachments.Delete("/:id", handler.DeleteAttachment)
	attachments.Put("/:id/move", handler.MoveAttachment)

	// 操作日志管理路由
	opLogs := adminAuth.Group("/operation-logs")
	opLogs.Get("/", handler.ListOperationLogs)
	opLogs.Get("/:id", handler.GetOperationLog)

	// 数据字典路由
	dictDataService := service.NewDictDataService()
	dictData := adminAuth.Group("/dict-data", func(c *fiber.Ctx) error {
		c.Locals("service.DictDataService", dictDataService)
		return c.Next()
	})
	dictData.Get("/type/:type", handler.GetDictDataByType)
	dictData.Post("", handler.CreateDictData)
	dictData.Get("", handler.ListDictData)
	dictData.Get("/:id", handler.GetDictData)
	dictData.Put("/:id", handler.UpdateDictData)
	dictData.Delete("/:id", handler.DeleteDictData)

	// 动态配置路由
	var configService *service.ConfigService
	if appInstance != nil && appInstance.Cache != nil {
		configService = service.NewConfigService(appInstance.Cache)
	} else {
		configService = service.NewConfigService(nil)
	}
	configs := adminAuth.Group("/configs", func(c *fiber.Ctx) error {
		c.Locals("service.ConfigService", configService)
		return c.Next()
	})
	configs.Get("/groups", handler.GetConfigGroups)
	configs.Get("/by-key/:key", handler.GetConfigByKey)
	configs.Get("/group/:group", handler.GetConfigsByGroup)
	configs.Post("", handler.CreateConfig)
	configs.Get("", handler.ListConfigs)
	configs.Get("/:id", handler.GetConfig)
	configs.Put("/:id", handler.UpdateConfig)
	configs.Delete("/:id", handler.DeleteConfig)
}
