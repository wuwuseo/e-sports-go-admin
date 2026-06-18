package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func TestSystemPermissionDomainUsesAuthenticatedDomain(t *testing.T) {
	app := fiber.New()
	var got string
	var ok bool

	app.Get("/system-permissions", func(c fiber.Ctx) error {
		c.Locals("domain", "default")
		got, ok = systemPermissionDomain(c)
		return c.SendStatus(fiber.StatusNoContent)
	})

	resp, err := app.Test(httptest.NewRequest(
		fiber.MethodGet,
		"/system-permissions?domain=other-tenant",
		nil,
	))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusNoContent)
	}
	if !ok {
		t.Fatal("expected authenticated domain to be available")
	}
	if got != "default" {
		t.Fatalf("domain = %q, want authenticated domain %q", got, "default")
	}
}
