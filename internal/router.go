package internal

import (
	"go-admin/internal/handler"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v2"
	"github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/jwt"
)

func RegisterRoute(app *fiber.App, config *config.Config) {
	api := app.Group("/api")                          // /api
	v1 := api.Group("/v1", func(c *fiber.Ctx) error { // middleware for /api/v1
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

	// 需要认证的接口
	adminAuth := admin.Group("/auth", jwt.NewJWTMiddleware(config.App.Secret))
	adminAuth.Get("/user", handler.GetUser)
	adminAuth.Put("/user", handler.UpdateUser)
	adminAuth.Put("/password", handler.ChangePassword)
}
