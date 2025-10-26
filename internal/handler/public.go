package handler

import "github.com/gofiber/fiber/v2"

func Login(c *fiber.Ctx) error {
	return c.SendString("login	success")
}

func Register(c *fiber.Ctx) error {
	return c.SendString("register success")
}

func GetUser(c *fiber.Ctx) error {
	return c.SendString("get user success")
}
