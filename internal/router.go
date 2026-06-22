package internal

import (
	"context"
	"time"

	"go-admin/internal/global"
	"go-admin/internal/handler"
	"go-admin/internal/middleware"
	appoauth "go-admin/internal/oauth"
	adminplugin "go-admin/internal/plugin"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/jwt"
	"github.com/wuwuseo/cmf/log"
	cmfplugin "github.com/wuwuseo/cmf/plugin"
	"github.com/wuwuseo/cmf/sse"
)

func RegisterRoute(app *fiber.App, config *config.Config) {
	// 添加请求日志中间件
	if appInstance != nil && appInstance.Logger != nil {
		app.Use(log.RequestLoggerMiddleware(appInstance.Logger))
	}

	api := app.Group("/api")                         // API 路由组
	v1 := api.Group("/v1", func(c fiber.Ctx) error { // v1 版本路由中间件
		c.Set("Version", "1")
		c.Locals("utils.Response", utils.NewResponse())
		return c.Next()
	})

	admin := v1.Group("/admin")
	public := v1.Group("/public", func(c fiber.Ctx) error {
		c.Locals("service.UserService", service.NewUserService())
		return c.Next()
	})

	// 初始化带缓存的服务
	var permissionService service.PermissionServiceInterface
	departmentService := service.NewDepartmentService()
	var roleService service.RoleServiceInterface
	var systemPermissionService service.SystemPermissionServiceInterface
	if appInstance != nil && appInstance.Cache != nil {
		permissionService = service.NewPermissionService(appInstance.Cache)
		roleService = service.NewRoleServiceWithDepartmentService(appInstance.Cache, departmentService)
	} else {
		permissionService = service.NewPermissionService(nil)
		roleService = service.NewRoleServiceWithDepartmentService(nil, departmentService)
	}
	systemPermissionService = service.NewSystemPermissionService()
	dataPermissionService := service.NewDataPermissionService(departmentService)

	// 初始化操作日志服务
	opLogService := service.NewOperationLogService()

	// 初始化用户管理相关服务
	var userListService service.UserListServiceInterface
	var userActivationService service.UserActivationServiceInterface
	var blacklistService service.BlacklistServiceInterface
	var thirdPartyUserService service.ThirdPartyUserServiceInterface
	var roleAssignmentService service.RoleAssignmentServiceInterface
	var userService service.UserServiceInterface

	if appInstance != nil && appInstance.Cache != nil {
		blacklistService = service.NewBlacklistService(appInstance.Cache)
		roleAssignmentService = service.NewRoleAssignmentService(appInstance.Cache, departmentService)
		userService = service.NewUserServiceWithCache(appInstance.Cache)
	} else {
		blacklistService = service.NewBlacklistService(nil)
		roleAssignmentService = service.NewRoleAssignmentService(nil, departmentService)
		userService = service.NewUserService()
	}
	userListService = service.NewUserListService()
	userActivationService = service.NewUserActivationService()
	thirdPartyUserService = service.NewThirdPartyUserService()

	oauthCipher, _ := appoauth.NewSecretCipherFromEnv()
	oauthRegistry := appoauth.NewRegistry(appoauth.NewGitHubProvider())
	oauthConfigService := service.NewOAuthProviderConfigService(global.ORM, oauthCipher, oauthRegistry)
	oauthSessionService := service.NewOAuthSessionService(global.ORM, oauthCipher, time.Now)
	oauthAuthService := service.NewOAuthAuthService(global.ORM, oauthRegistry, oauthConfigService, oauthSessionService, time.Now)

	// 初始化站内消息服务（带 SSE Hub 推送能力）
	var hub *sse.Hub
	var ticketStore *sse.TicketStore
	if appInstance != nil {
		hub = appInstance.Hub
		ticketStore = appInstance.TicketStore
	}
	prefSvc := service.NewMessagePreferenceService()
	messageService := service.NewMessageServiceWithPreference(hub, prefSvc)
	broadcastService := service.NewMessageBroadcastService(messageService)

	// 初始化插件管理服务
	pluginRegistry := cmfplugin.NewMemoryExtensionRegistry()
	pluginRuntime := cmfplugin.NewProcessRuntime()
	pluginManager := adminplugin.NewManager(global.ORM, adminplugin.Options{
		PluginRoot:  "./data/plugins",
		PackageRoot: "./data/plugin-packages",
		TempRoot:    "./data/plugin-tmp",
		Registry:    pluginRegistry,
		Runtime:     pluginRuntime,
	})
	_ = pluginManager.RestoreEnabled(context.Background())
	pluginService := service.NewPluginService(pluginManager)
	// 登录限流器：每分钟最多 10 次请求，防止暴力破解
	loginLimiter := middleware.NewRateLimiter(middleware.RateLimiterConfig{
		Window:   1 * time.Minute,
		MaxCount: 10,
	})

	// 公开接口
	public.Post("/login", loginLimiter.Handler(), handler.Login)
	public.Post("/refresh-token", handler.RefreshToken)
	public.Get("/public-key", handler.GetPublicKey) // 获取RSA公钥用于加密登录
	publicOAuth := public.Group("/oauth", func(c fiber.Ctx) error {
		c.Locals("service.OAuthProviderConfigService", oauthConfigService)
		c.Locals("service.OAuthAuthService", oauthAuthService)
		return c.Next()
	})
	publicOAuth.Get("/providers", handler.ListOAuthProviders)
	publicOAuth.Get("/user/:provider/authorize", func(c fiber.Ctx) error {
		c.Locals("oauth.subject_type", "user")
		return handler.BeginOAuthLogin(c)
	})
	publicOAuth.Get("/admin/:provider/authorize", func(c fiber.Ctx) error {
		c.Locals("oauth.subject_type", "admin")
		return handler.BeginOAuthLogin(c)
	})
	publicOAuth.Get("/:provider/callback", handler.OAuthCallback)
	publicOAuth.Post("/exchange", handler.ExchangeOAuthLoginCode)

	userAuthBase := v1.Group("/user/auth", jwt.NewJWTMiddleware(config.App.Secret), middleware.DomainMiddleware(), func(c fiber.Ctx) error {
		c.Locals("service.OAuthAuthService", oauthAuthService)
		return c.Next()
	}, middleware.BlacklistCheck(blacklistService))
	userOAuth := userAuthBase.Group("/oauth")
	userOAuth.Get("/identities", handler.ListUserOAuthIdentities)
	userOAuth.Get("/:provider/authorize", handler.BeginOAuthBind("user"))
	userOAuth.Delete("/:provider", handler.UnbindUserOAuthIdentity)

	// 管理员公开接口（无需认证）
	adminPublicLogin := admin.Group("/public", func(c fiber.Ctx) error {
		c.Locals("service.AdminService", service.NewAdminServiceWithBlacklist(blacklistService))
		c.Locals("utils.Response", utils.NewResponse())
		return c.Next()
	})
	adminPublicLogin.Post("/login", loginLimiter.Handler(), handler.AdminLogin)
	adminPublicLogin.Post("/refresh", handler.AdminRefreshToken)

	pluginProxy := v1.Group("/plugin", func(c fiber.Ctx) error {
		c.Locals("service.PluginService", pluginService)
		return c.Next()
	})
	pluginProxyAuth := []any{jwt.NewJWTMiddleware(config.App.AdminSecret), middleware.DomainMiddleware(), func(c fiber.Ctx) error {
		c.Locals("service.PermissionService", permissionService)
		c.Locals("plugin.proxy_path_prefix", "/admin/auth")
		return c.Next()
	}, middleware.BlacklistCheck(blacklistService), middleware.PermissionMiddleware(), handler.ProxyPlugin}
	pluginProxy.All("/:plugin_id/admin/auth", pluginProxyAuth[0], pluginProxyAuth[1:]...)
	pluginProxy.All("/:plugin_id/admin/auth/*", pluginProxyAuth[0], pluginProxyAuth[1:]...)
	pluginProxy.All("/:plugin_id/*", jwt.NewJWTMiddleware(config.App.AdminSecret), middleware.DomainMiddleware(), middleware.BlacklistCheck(blacklistService), handler.ProxyPlugin)

	// 需要认证的接口
	adminAuthBase := admin.Group("/auth", jwt.NewJWTMiddleware(config.App.AdminSecret), middleware.DomainMiddleware(), func(c fiber.Ctx) error {
		c.Locals("service.SuperAdminService", service.NewSuperAdminService(dataPermissionService, departmentService))
		c.Locals("service.AdminService", service.NewAdminServiceWithBlacklist(blacklistService))
		c.Locals("service.UserService", userService)
		c.Locals("service.RoleService", roleService)
		c.Locals("service.PermissionService", permissionService)
		c.Locals("service.SystemPermissionService", systemPermissionService)
		c.Locals("service.OperationLogService", opLogService)
		c.Locals("service.UserListService", userListService)
		c.Locals("service.UserActivationService", userActivationService)
		c.Locals("service.BlacklistService", blacklistService)
		c.Locals("service.ThirdPartyUserService", thirdPartyUserService)
		c.Locals("service.RoleAssignmentService", roleAssignmentService)
		c.Locals("service.MessageService", messageService)
		c.Locals("service.MessagePreferenceService", prefSvc)
		c.Locals("service.MessageBroadcastService", broadcastService)
		c.Locals("service.PluginService", pluginService)
		c.Locals("service.DepartmentService", departmentService)
		c.Locals("service.DataPermissionService", dataPermissionService)
		c.Locals("service.OAuthProviderConfigService", oauthConfigService)
		c.Locals("service.OAuthAuthService", oauthAuthService)
		c.Locals("sse.TicketStore", ticketStore)
		return c.Next()
	}, middleware.BlacklistCheck(blacklistService))

	adminAuthBase.Get("/user", handler.GetAdmin)
	adminAuthBase.Put("/user", handler.UpdateAdmin)
	adminAuthBase.Put("/password", handler.ChangeAdminPassword)
	adminAuthBase.Post("/logout", handler.AdminLogout)
	adminOAuth := adminAuthBase.Group("/oauth")
	adminOAuth.Get("/identities", handler.ListAdminOAuthIdentities)
	adminOAuth.Get("/:provider/authorize", handler.BeginOAuthBind("admin"))
	adminOAuth.Delete("/:provider", handler.UnbindAdminOAuthIdentity)

	// 需要认证+权限校验的接口
	adminAuth := adminAuthBase.Group("", middleware.PermissionMiddleware())
	adminAuth.Get("/oauth/providers", handler.ListOAuthProviderConfigs)
	adminAuth.Get("/oauth/providers/:provider", handler.GetOAuthProviderConfig)
	adminAuth.Put("/oauth/providers/:provider", handler.UpsertOAuthProviderConfig)
	adminAuth.Delete("/oauth/providers/:provider", handler.DeleteOAuthProviderConfig)

	// 超级管理员管理路由（管理其他管理员账号）
	adminAuth.Post("/admins", handler.CreateAdmin)
	adminAuth.Get("/admins", handler.ListAdmins)
	adminAuth.Get("/admins/:id", handler.GetAdminDetail)
	adminAuth.Put("/admins/:id", handler.UpdateAdminByID)
	adminAuth.Get("/admins/:id/roles", handler.GetAdminRoles)
	adminAuth.Put("/admins/:id/roles", handler.AssignAdminRoles)
	adminAuth.Put("/admins/:id/activate", handler.ActivateAdmin)
	adminAuth.Put("/admins/:id/deactivate", handler.DeactivateAdmin)
	// 角色管理路由
	roles := adminAuth.Group("/roles")
	roles.Post("", handler.CreateRole) // 创建角色

	// 角色权限管理路由（必须在 /:id 之前注册，避免被参数化路由捕获）
	roles.Get("/permissions", handler.GetRolePermissions)                // 获取角色权限
	roles.Post("/permissions/assign", handler.AssignPermissionsToRole)   // 为角色分配权限
	roles.Post("/permissions/remove", handler.RemovePermissionsFromRole) // 从角色移除权限

	roles.Get("/all", handler.ListAllRoles)                           // 获取所有角色（不带分页）
	roles.Get("/:id/permissions", handler.GetRolePermissionsByRoleID) // 按ID获取角色权限
	roles.Post("/:id/permissions", handler.SaveRolePermissions)       // 覆盖式保存角色权限
	roles.Get("/:id", handler.GetRole)                                // 获取角色详情
	roles.Get("", handler.ListRoles)                                  // 获取角色列表
	roles.Put("/:id", handler.UpdateRole)                             // 更新角色
	roles.Delete("/:id", handler.DeleteRole)                          // 删除角色
	roles.Put("/:id/enable", handler.EnableRole)                      // 启用角色
	roles.Put("/:id/disable", handler.DisableRole)                    // 禁用角色

	// 权限管理路由
	permissions := adminAuth.Group("/permissions")
	permissions.Post("", handler.CreatePermission)      // 创建权限
	permissions.Put("", handler.UpdatePermission)       // 更新权限
	permissions.Delete("", handler.DeletePermission)    // 删除权限
	permissions.Get("/detail", handler.GetPermission)   // 获取权限详情
	permissions.Get("/all", handler.ListAllPermissions) // 获取所有权限（不带分页）
	permissions.Get("", handler.ListPermissions)        // 获取权限列表

	// 权限定义管理路由
	systemPermissions := adminAuth.Group("/system-permissions")
	systemPermissions.Post("", handler.CreateSystemPermission)       // 创建权限定义
	systemPermissions.Get("", handler.ListSystemPermissions)         // 获取权限定义列表
	systemPermissions.Get("/", handler.ListSystemPermissions)        // 获取权限定义列表（兼容尾斜杠）
	systemPermissions.Get("/tree", handler.GetSystemPermissionTree)  // 获取权限树
	systemPermissions.Get("/:id", handler.GetSystemPermission)       // 获取权限定义详情
	systemPermissions.Put("/:id", handler.UpdateSystemPermission)    // 更新权限定义
	systemPermissions.Delete("/:id", handler.DeleteSystemPermission) // 删除权限定义

	// 部门管理路由
	departments := adminAuth.Group("/departments")
	departments.Post("", handler.CreateDepartment)
	departments.Get("/tree", handler.ListDepartmentTree)
	departments.Get("/:id", handler.GetDepartment)
	departments.Put("/:id", handler.UpdateDepartment)
	departments.Delete("/:id", handler.DeleteDepartment)

	// 用户权限路由
	userPermissions := adminAuth.Group("/user")
	userPermissions.Get("/menu-permissions", handler.GetUserMenuPermissions)     // 获取用户菜单权限
	userPermissions.Get("/button-permissions", handler.GetUserButtonPermissions) // 获取用户按钮权限
	userPermissions.Get("/api-permissions", handler.GetUserAPIPermissions)       // 获取用户API权限
	userPermissions.Get("/permissions", handler.GetUserPermissions)              // 获取用户所有权限（菜单和按钮）

	// 定时任务管理路由
	cronJobService := service.NewCronJobService(global.CronManager)
	cronJobs := adminAuth.Group("/cron-jobs", func(c fiber.Ctx) error {
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

	// 插件管理路由
	// 注意：静态路径 /install 必须在参数化路由 /:plugin_id 之前注册
	plugins := adminAuth.Group("/plugins")
	plugins.Post("/install", handler.InstallPlugin)
	plugins.Get("", handler.ListPlugins)
	plugins.Get("/:plugin_id", handler.GetPlugin)
	plugins.Post("/:plugin_id/enable", handler.EnablePlugin)
	plugins.Post("/:plugin_id/disable", handler.DisablePlugin)
	plugins.Delete("/:plugin_id", handler.UninstallPlugin)

	// 附件分组管理路由
	attachmentGroupService := service.NewAttachmentGroupService()
	var attachmentService service.AttachmentServiceInterface
	if appInstance != nil && appInstance.Filesystem != nil {
		attachmentService = service.NewAttachmentService(appInstance.Filesystem)
	} else {
		attachmentService = service.NewAttachmentService(nil)
	}

	groups := adminAuth.Group("/attachment-groups", func(c fiber.Ctx) error {
		c.Locals("service.AttachmentGroupService", attachmentGroupService)
		return c.Next()
	})
	groups.Post("/", handler.CreateAttachmentGroup)
	groups.Get("/", handler.ListAttachmentGroups)
	groups.Get("/:id", handler.GetAttachmentGroup)
	groups.Put("/:id", handler.UpdateAttachmentGroup)
	groups.Delete("/:id", handler.DeleteAttachmentGroup)

	// 附件管理路由
	attachments := adminAuth.Group("/attachments", func(c fiber.Ctx) error {
		c.Locals("service.AttachmentService", attachmentService)
		c.Locals("attachment.access_secret", config.App.AdminSecret)
		c.Locals("attachment.content_base_path", "/api/v1/attachments")
		c.Locals("attachment.access_ttl", config.Attachment.AccessURLTTL)
		c.Locals("attachment.x_accel_enabled", config.Attachment.XAccelEnabled)
		return c.Next()
	})
	attachments.Post("/upload", handler.UploadAttachment)
	// 分片上传查询与取消路由（上传已统一到 /upload）
	attachments.Get("/chunk/status/:upload_id", handler.GetChunkUploadStatus)
	attachments.Delete("/chunk/:upload_id", handler.CancelChunkUpload)
	attachments.Delete("/batch", handler.BatchDeleteAttachments)
	attachments.Get("/", handler.ListAttachments)
	attachments.Get("/:id", handler.GetAttachment)
	attachments.Get("/:id/access-url", handler.GetAttachmentAccessURL)
	attachments.Get("/:id/preview", handler.PreviewAttachment)
	attachments.Delete("/:id", handler.DeleteAttachment)
	attachments.Put("/:id/move", handler.MoveAttachment)

	attachmentContent := v1.Group("/attachments", func(c fiber.Ctx) error {
		c.Locals("service.AttachmentService", attachmentService)
		c.Locals("attachment.access_secret", config.App.AdminSecret)
		c.Locals("attachment.access_ttl", config.Attachment.AccessURLTTL)
		c.Locals("attachment.x_accel_enabled", config.Attachment.XAccelEnabled)
		return c.Next()
	})
	attachmentContent.Get("/:id/content", handler.ServeAttachmentContent)

	// 操作日志管理路由
	opLogs := adminAuth.Group("/operation-logs")
	opLogs.Get("/", handler.ListOperationLogs)
	opLogs.Get("/:id", handler.GetOperationLog)

	// 数据字典路由
	dictDataService := service.NewDictDataService()
	dictData := adminAuth.Group("/dict-data", func(c fiber.Ctx) error {
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
	configs := adminAuth.Group("/configs", func(c fiber.Ctx) error {
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

	// 用户管理路由
	users := adminAuth.Group("/users")
	users.Post("", handler.CreateUser)
	users.Get("", handler.ListUsers)
	users.Post("/export", handler.ExportUsers)
	users.Get("/:id", handler.GetUserDetail)
	users.Put("/:id/activate", handler.ActivateUser)
	users.Put("/:id/deactivate", handler.DeactivateUser)
	users.Post("/:id/blacklist", handler.AddToBlacklist)
	users.Delete("/:id/blacklist", handler.RemoveFromBlacklist)
	users.Get("/:id/roles", handler.GetUserRoles)
	users.Put("/:id/roles", handler.AssignUserRoles)
	users.Get("/:id/third-party", handler.ListUserThirdParty)

	// 黑名单路由
	blacklists := adminAuth.Group("/blacklist")
	blacklists.Get("", handler.ListBlacklist)

	// 第三方用户管理路由
	thirdParty := adminAuth.Group("/third-party-users")
	thirdParty.Get("", handler.ListThirdPartyUsers)
	thirdParty.Delete("/:id", handler.UnbindThirdPartyUser)
	adminThirdParty := adminAuth.Group("/third-party-admins")
	adminThirdParty.Get("", handler.ListAdminThirdPartyIdentities)
	adminThirdParty.Delete("/:id", handler.DeleteAdminThirdPartyIdentity)

	// 站内消息路由（鉴权后访问）
	// 注意：所有静态路径（/preference、/unread-count 等）必须在 /:id 之前注册，
	// 避免被参数化路由捕获导致 strconv.Atoi 失败
	messages := adminAuth.Group("/messages")
	messages.Get("", handler.ListMessages)
	// 静态路径在前
	messages.Post("/read", handler.MarkRead)
	messages.Post("/read-all", handler.MarkAllRead)
	messages.Post("/unread", handler.MarkUnread)
	messages.Get("/unread-count", handler.UnreadCount)
	messages.Post("/delete-batch", handler.DeleteBatchMessages)
	messages.Post("/clear", handler.ClearMessages)
	messages.Get("/preference", handler.GetMessagePreference)
	messages.Put("/preference", handler.UpdateMessagePreference)
	messages.Post("/broadcast", handler.BroadcastMessage)
	messages.Post("/test-push", handler.TestPushMessage)
	// 参数化路由放最后
	messages.Get("/:id", handler.GetMessage)
	messages.Delete("/:id", handler.DeleteMessage)

	// SSE 凭证接口（鉴权后访问）
	adminAuth.Post("/sse/ticket", handler.IssueSSETicket)

	// SSE 连接接口（不走 BlacklistCheck/Permission，ticket 鉴权独立）
	sseGroup := admin.Group("/sse", func(c fiber.Ctx) error {
		c.Locals("utils.Response", utils.NewResponse())
		c.Locals("service.MessageService", messageService)
		c.Locals("sse.Hub", hub)
		c.Locals("sse.TicketStore", ticketStore)
		return c.Next()
	})
	sseGroup.Get("/connect", handler.SSEConnect)
}
