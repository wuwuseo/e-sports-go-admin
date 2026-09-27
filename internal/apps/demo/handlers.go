package demo

import (
	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

func ping(c fiber.Ctx) error { return c.JSON(fiber.Map{"ok": true}) }

func me(c fiber.Ctx) error { return c.JSON(fiber.Map{"uid": c.Locals("uid")}) }

func listItems(c fiber.Ctx) error {
	items, err := global.ORM.DemoItem.Query().All(c.Context())
	if err != nil {
		return err
	}
	return c.JSON(items)
}
