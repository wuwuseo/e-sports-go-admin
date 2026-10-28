package main

import (
	"embed"
	"io/fs"
	stdlog "log"
	"mime"
	"path"
	"strings"

	"go-admin/internal"
	"go-admin/internal/global"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/static"

	_ "go-admin/internal/tasks"            // 触发 init() 注册 Go tasks
	_ "go-admin/internal/tasks/logcleanup" // 注册过期日志清理任务

	_ "github.com/go-sql-driver/mysql"
	"github.com/wuwuseo/cmf/bootstrap"
	"github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/filesystem"
	"github.com/wuwuseo/cmf/log"
	"go.uber.org/zap"
)

// @title 管理系统 API
// @version 1.0
// @description 管理系统的API文档
// @termsOfService http://swagger.io/terms/

// @contact.name API Support
// @contact.url http://www.example.com/support
// @contact.email support@example.com

// @license.name Apache 2.0
// @license.url http://www.apache.org/licenses/LICENSE-2.0.html

// @host localhost:9501
// @BasePath /api/v1

// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description 使用Bearer {token}格式进行认证

//go:embed web/dist
var WebRoot embed.FS

func main() {
	// 通过 Wire 初始化应用依赖
	app, err := internal.InitializeApp()
	if err != nil {
		stdlog.Fatalf("Failed to initialize app: %v", err)
	}
	internal.SetApp(app)

	// 设置全局 logger
	log.SetDefault(app.Logger)
	log.Info("应用依赖初始化完成")

	bootstrap := bootstrap.NewBootstrap()
	global.Bootstrap = bootstrap
	bootstrap.RegisterInitFunc(internal.Init)
	bootstrap.RegisterMiddleware(func(app *fiber.App, config *config.Config) {
		// 静态文件服务（跳过 API 路由）
		// 使用 fs.Sub 从嵌入的文件系统中提取 web/dist 子目录，
		// 使 URL 路径 /assets/xxx 能正确映射到 web/dist/assets/xxx
		subFS, err := fs.Sub(WebRoot, "web/dist")
		if err != nil {
			stdlog.Fatalf("Failed to create sub filesystem: %v", err)
		}
		// 本地附件静态目录：从 filesystem.disks.local.options.root 推导，
		// 而不是写死 ./data/storage/public。默认磁盘切到 S3 后，这里仍然
		// 为历史本地附件提供 /uploads 访问；未配置 local 磁盘则跳过挂载。
		if uploadRoot, ok := filesystem.LocalPublicRoot(*config, ""); ok {
			log.Info("挂载本地附件静态目录", zap.String("root", uploadRoot))
			registerUploadStaticMiddleware(app, uploadRoot)
		} else {
			log.Info("未配置本地存储磁盘，跳过 /uploads 静态挂载")
		}
		staticHandler := static.New("", static.Config{
			FS:     subFS,
			Browse: false,
		})
		// 首页统一出口：index.html 必须每次协商，避免浏览器长期缓存旧首页后
		// 继续引用已被新构建替换掉的 hash 资源。
		sendIndex := func(c fiber.Ctx) error {
			indexFile, err := fs.ReadFile(subFS, "index.html")
			if err != nil {
				return err
			}
			c.Set(fiber.HeaderCacheControl, "no-cache, must-revalidate")
			c.Type("html")
			return c.Send(indexFile)
		}

		app.Use(func(c fiber.Ctx) error {
			p := c.Path()
			if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/uploads/") {
				return c.Next()
			}
			// 构建产物：文件名带内容哈希，可永久缓存；缺失时必须返回 404。
			// 若落到 SPA fallback，浏览器会把 index.html 当成 ES module 执行，
			// 报 MIME 类型错误，动态 import 永不 resolve，表现为页面卡死。
			if strings.HasPrefix(p, "/assets/") {
				name := strings.TrimPrefix(path.Clean(p), "/")
				data, err := fs.ReadFile(subFS, name)
				if err != nil {
					return c.SendStatus(fiber.StatusNotFound)
				}
				if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
					c.Set(fiber.HeaderContentType, ct)
				}
				c.Set(fiber.HeaderCacheControl, "public, max-age=31536000, immutable")
				return c.Send(data)
			}
			if p == "/" || p == "/index.html" {
				return sendIndex(c)
			}
			return staticHandler(c)
		})

		// SPA fallback：仅前端路由返回 index.html（/uploads 缺失文件仍应 404）
		app.Use(func(c fiber.Ctx) error {
			p := c.Path()
			if strings.HasPrefix(p, "/api/") ||
				strings.HasPrefix(p, "/uploads/") {
				return c.Next()
			}
			return sendIndex(c)
		})
	})
	bootstrap.RegisterRoute(internal.RegisterRoute)
	bootstrap.RegisterCleanupFunc(internal.Cleanup)

	defer func() {
		if err := internal.Cleanup(); err != nil {
			log.Error("Final cleanup error", zap.Error(err))
		}
	}()

	if err := bootstrap.Run(); err != nil {
		log.Fatal("Failed to run application", zap.Error(err))
	}
}

func registerUploadStaticMiddleware(app *fiber.App, root string) {
	// Fiber uses the route prefix to map /uploads/<file> to root/<file>.
	app.Use("/uploads", static.New(root, static.Config{Browse: false}))
}
