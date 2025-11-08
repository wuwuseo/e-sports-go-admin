package main

import (
	"embed"
	"log"
	"net/http"
	"strings"

	"go-admin/internal"
	"go-admin/internal/global"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/filesystem"

	_ "github.com/go-sql-driver/mysql"
	"github.com/wuwuseo/cmf/bootstrap"
	"github.com/wuwuseo/cmf/config"
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
	bootstrap := bootstrap.NewBootstrap()
	global.Bootstrap = bootstrap
	bootstrap.RegisterInitFunc(internal.Init)
	bootstrap.RegisterMiddleware(func(app *fiber.App, config *config.Config) {
		app.Use("/", filesystem.New(filesystem.Config{
			Root:       http.FS(WebRoot),
			PathPrefix: "web/dist",
		}))

		// 添加一个通用的处理程序，用于处理 SPA 路由
		app.Use(func(c *fiber.Ctx) error {
			// 如果是API请求，则继续走后端路由
			path := c.Path()
			if strings.HasPrefix(path, "/api/") {
				return c.Next()
			}

			_, err := WebRoot.Open("web/dist" + path)

			// 如果文件不存在，则返回 index.html
			if err != nil {
				indexFile, err := WebRoot.ReadFile("web/dist/index.html")
				if err != nil {
					return err
				}
				c.Type("html")
				return c.Send(indexFile)
			}

			// 如果文件存在，则继续处理
			return c.Next()
		})
	})
	bootstrap.RegisterRoute(internal.RegisterRoute)
	bootstrap.RegisterCleanupFunc(internal.Cleanup)

	defer func() {
		if err := internal.Cleanup(); err != nil {
			log.Printf("Final cleanup error: %v", err)
		}
	}()

	if err := bootstrap.Run(); err != nil {
		log.Fatalf("Failed to run application: %v", err)
	}
}
