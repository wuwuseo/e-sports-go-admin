package handler

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
)

func TestGetOperatorUID(t *testing.T) {
	tests := []struct {
		name     string
		uidValue interface{}
		want     string
	}{
		{name: "string uid", uidValue: "admin-123", want: "admin-123"},
		{name: "empty string", uidValue: "", want: ""},
		{name: "nil", uidValue: nil, want: ""},
		{name: "int uid", uidValue: 123, want: ""},
		{name: "float uid", uidValue: 123.456, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := fiber.New()
			app.Get("/", func(c fiber.Ctx) error {
				if tt.uidValue != nil {
					c.Locals("uid", tt.uidValue)
				}
				got := getOperatorUID(c)
				if got != tt.want {
					t.Errorf("getOperatorUID() = %q, want %q", got, tt.want)
				}
				return nil
			})
			req := httptest.NewRequest("GET", "/", nil)
			app.Test(req)
		})
	}
}

func TestMapAdminScopeError(t *testing.T) {
	errMap := map[string]error{
		"ErrAdminOutOfScope":        service.ErrAdminOutOfScope,
		"ErrCannotToggleSelfStatus": service.ErrCannotToggleSelfStatus,
		"ErrCannotModifySelfRoles":  service.ErrCannotModifySelfRoles,
		"ErrDepartmentOutOfScope":   service.ErrDepartmentOutOfScope,
		"ErrRoleScopeEscalation":    service.ErrRoleScopeEscalation,
		"ErrDepartmentNotFound":     service.ErrDepartmentNotFound,
		"ErrAdminNotFound":          service.ErrAdminNotFound,
		"ErrAdminEmailExists":       service.ErrAdminEmailExists,
	}
	for name, err := range errMap {
		t.Run(name, func(t *testing.T) {
			app := fiber.New()
			app.Use(func(c fiber.Ctx) error {
				c.Locals("utils.Response", utils.NewResponse())
				return c.Next()
			})
			app.Get("/", func(c fiber.Ctx) error {
				response := c.Locals("utils.Response").(*utils.Response)
				return mapAdminScopeError(c, response, err)
			})
			req := httptest.NewRequest("GET", "/", nil)
			resp, _ := app.Test(req)
			if resp.StatusCode != 200 {
				t.Errorf("expected status 200, got %d", resp.StatusCode)
			}
		})
	}
	t.Run("unknown error", func(t *testing.T) {
		app := fiber.New()
		app.Use(func(c fiber.Ctx) error {
			c.Locals("utils.Response", utils.NewResponse())
			return c.Next()
		})
		app.Get("/", func(c fiber.Ctx) error {
			response := c.Locals("utils.Response").(*utils.Response)
			return mapAdminScopeError(c, response, errors.New("unknown"))
		})
		req := httptest.NewRequest("GET", "/", nil)
		resp, _ := app.Test(req)
		if resp.StatusCode != 200 {
			t.Errorf("expected status 200, got %d", resp.StatusCode)
		}
		var result utils.Response
		json.NewDecoder(resp.Body).Decode(&result)
		if result.Code != 500 {
			t.Errorf("expected code 500, got %d", result.Code)
		}
	})
}

func TestMapAdminCreateError(t *testing.T) {
	errMap := map[string]error{
		"ErrAdminEmailExists":                  service.ErrAdminEmailExists,
		"ErrAdminUsernameExists":               service.ErrAdminUsernameExists,
		"ErrSuperAdminRoleAssignmentForbidden": service.ErrSuperAdminRoleAssignmentForbidden,
		"ErrDepartmentOutOfScope":              service.ErrDepartmentOutOfScope,
		"ErrRoleScopeEscalation":               service.ErrRoleScopeEscalation,
		"ErrDepartmentNotFound":                service.ErrDepartmentNotFound,
		"ErrInvalidRoleIDs":                    service.ErrInvalidRoleIDs,
		"ErrPrimaryRoleInvalid":                service.ErrPrimaryRoleInvalid,
	}
	for name, err := range errMap {
		t.Run(name, func(t *testing.T) {
			app := fiber.New()
			app.Use(func(c fiber.Ctx) error {
				c.Locals("utils.Response", utils.NewResponse())
				return c.Next()
			})
			app.Get("/", func(c fiber.Ctx) error {
				response := c.Locals("utils.Response").(*utils.Response)
				return mapAdminCreateError(c, response, err)
			})
			req := httptest.NewRequest("GET", "/", nil)
			resp, _ := app.Test(req)
			if resp.StatusCode != 200 {
				t.Errorf("expected status 200, got %d", resp.StatusCode)
			}
		})
	}
	t.Run("unknown error", func(t *testing.T) {
		app := fiber.New()
		app.Use(func(c fiber.Ctx) error {
			c.Locals("utils.Response", utils.NewResponse())
			return c.Next()
		})
		app.Get("/", func(c fiber.Ctx) error {
			response := c.Locals("utils.Response").(*utils.Response)
			return mapAdminCreateError(c, response, errors.New("unknown"))
		})
		req := httptest.NewRequest("GET", "/", nil)
		resp, _ := app.Test(req)
		if resp.StatusCode != 200 {
			t.Errorf("expected status 200, got %d", resp.StatusCode)
		}
		var result utils.Response
		json.NewDecoder(resp.Body).Decode(&result)
		if result.Code != 500 {
			t.Errorf("expected code 500, got %d", result.Code)
		}
	})
}
