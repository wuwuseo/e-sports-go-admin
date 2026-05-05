package main

import (
	"embed"
	"io/fs"
	stdlog "log"
	"strings"

	"go-admin/internal"
	"go-admin/internal/global"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/static"

	_ "go-admin/internal/tasks" // 触发 init() 注册 Go tasks

	_ "github.com/go-sql-driver/mysql"
	"github.com/wuwuseo/cmf/bootstrap"
	"github.com/wuwuseo/cmf/config"
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
		staticHandler := static.New("", static.Config{
			FS:     subFS,
			Browse: false,
		})
		app.Use(func(c fiber.Ctx) error {
			if strings.HasPrefix(c.Path(), "/api/") {
				return c.Next()
			}
			return staticHandler(c)
		})

		// SPA fallback：静态文件未命中时返回 index.html
		app.Use(func(c fiber.Ctx) error {
			if strings.HasPrefix(c.Path(), "/api/") {
				return c.Next()
			}
			indexFile, err := fs.ReadFile(subFS, "index.html")
			if err != nil {
				return err
			}
			c.Type("html")
			return c.Send(indexFile)
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
