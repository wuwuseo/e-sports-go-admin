package internal

import (
	"github.com/wuwuseo/go-admin/internal/handler"

	"github.com/gofiber/fiber/v2"
	"github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/jwt"
)

func RegisterRoute(app *fiber.App, config *config.Config) {
	api := app.Group("/api") // /api

	v1 := api.Group("/v1", func(c *fiber.Ctx) error { // middleware for /api/v1
		c.Set("Version", "1")
		return c.Next()
	})

	admin := v1.Group("/admin")
	admin.Post("/login", handler.Login)
	admin.Post("/register", handler.Register)

	adminAuth := admin.Group("/auth", jwt.NewJWTMiddleware(config.App.Secret))
	adminAuth.Get("/user", handler.GetUser)
}
