package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go-admin/internal/ent/admin"
	"go-admin/internal/ent/role"
	"go-admin/internal/global"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
)

func TestQueueRequiresSuperAdminDespiteResidualPolicy(t *testing.T) {
	for _, roleName := range []string{"queue_editor", "admin"} {
		t.Run(roleName, func(t *testing.T) {
			client := permGapFixture(t)
			ctx := t.Context()
			r := client.Role.Create().SetName(roleName).SetDisplayName(roleName).SetDataScope(role.DataScopeSelf).SetStatus(role.StatusActive).SaveX(ctx)
			a := client.Admin.Create().SetEmail("queue@example.invalid").SetUID("queue-test").SetUsername("queue-test").SetPassword("test-only").SetStatus(admin.StatusActive).SaveX(ctx)
			client.AdminRole.Create().SetAdminID(a.ID).SetRoleID(r.ID).SaveX(ctx)
			enforcer, err := global.EnforcerManager.GetEnforcer("default")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := enforcer.AddPolicy(roleName, "default", "/api/v1/admin/auth/*", "*"); err != nil {
				t.Fatal(err)
			}
			app := permGapApp(t)
			group := app.Group("/api/v1/admin/auth/queue-jobs", RequireSuperAdmin())
			called := false
			group.Post("", func(c fiber.Ctx) error { called = true; return c.SendStatus(204) })
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/queue-jobs", nil)
			req.Header.Set("Authorization", "Bearer "+permGapToken(t, a.UID, "default"))
			resp, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if roleName == "admin" {
				if !called || resp.StatusCode != 204 {
					t.Fatalf("super admin was rejected: %d", resp.StatusCode)
				}
				return
			}
			var body utils.Response
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if called || body.Code != 403 {
				t.Fatalf("ordinary role passed high-risk gate: %+v", body)
			}
		})
	}
}
