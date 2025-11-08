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
	bootstrap.Run()
}
