package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func TestNormalizePermissionRoutePath_ConvertsFiberParamsToPolicyParams(t *testing.T) {
	got := normalizePermissionRoutePath("/api/v1/admin/auth/roles/:id/permissions")
	want := "/api/v1/admin/auth/roles/{id}/permissions"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestFiberRoutePathMatchesRequest(t *testing.T) {
	tests := []struct {
		name        string
		routePath   string
		requestPath string
		want        bool
	}{
		{
			name:        "parameter segment matches one request segment",
			routePath:   "/api/v1/admin/auth/roles/:id",
			requestPath: "/api/v1/admin/auth/roles/4",
			want:        true,
		},
		{
			name:        "parameter segment does not match multiple request segments",
			routePath:   "/api/v1/admin/auth/roles/:id",
			requestPath: "/api/v1/admin/auth/roles/4/permissions",
			want:        false,
		},
		{
			name:        "wildcard matches remaining request path",
			routePath:   "/api/v1/plugin/:plugin_id/*",
			requestPath: "/api/v1/plugin/demo/admin/auth/dashboard",
			want:        true,
		},
		{
			name:        "static segment mismatch fails",
			routePath:   "/api/v1/admin/auth/departments/:id",
			requestPath: "/api/v1/admin/auth/roles/4",
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fiberRoutePathMatchesRequest(tt.routePath, tt.requestPath)
			if got != tt.want {
				t.Fatalf("fiberRoutePathMatchesRequest(%q, %q) = %v, want %v", tt.routePath, tt.requestPath, got, tt.want)
			}
		})
	}
}

func TestIsSelfPermissionEndpoint_AllowsCurrentUserPermissionReads(t *testing.T) {
	allowed := []string{
		"/api/v1/admin/auth/user/menu-permissions",
		"/api/v1/admin/auth/user/button-permissions",
		"/api/v1/admin/auth/user/api-permissions",
		"/api/v1/admin/auth/user/permissions",
	}
	for _, path := range allowed {
		if !isSelfPermissionEndpoint(path, "GET") {
			t.Fatalf("expected %s to bypass permission enforcement", path)
		}
	}

	if isSelfPermissionEndpoint("/api/v1/admin/auth/permissions", "GET") {
		t.Fatal("permission management endpoints must still require authorization")
	}
	if isSelfPermissionEndpoint("/api/v1/admin/auth/user/menu-permissions", "POST") {
		t.Fatal("only read requests should bypass permission enforcement")
	}
}

func TestPermissionObjectsForRequest_IncludesFinalRouteTemplateFromGroupedMiddleware(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		want   string
	}{
		{
			name:   "role update",
			method: http.MethodPut,
			path:   "/api/v1/admin/auth/roles/4",
			want:   "/api/v1/admin/auth/roles/{id}",
		},
		{
			name:   "role delete",
			method: http.MethodDelete,
			path:   "/api/v1/admin/auth/roles/4",
			want:   "/api/v1/admin/auth/roles/{id}",
		},
		{
			name:   "role permissions read",
			method: http.MethodGet,
			path:   "/api/v1/admin/auth/roles/4/permissions",
			want:   "/api/v1/admin/auth/roles/{id}/permissions",
		},
		{
			name:   "role permissions save",
			method: http.MethodPost,
			path:   "/api/v1/admin/auth/roles/4/permissions",
			want:   "/api/v1/admin/auth/roles/{id}/permissions",
		},
		{
			name:   "department update",
			method: http.MethodPut,
			path:   "/api/v1/admin/auth/departments/1",
			want:   "/api/v1/admin/auth/departments/{id}",
		},
		{
			name:   "department delete",
			method: http.MethodDelete,
			path:   "/api/v1/admin/auth/departments/1",
			want:   "/api/v1/admin/auth/departments/{id}",
		},
		{
			name:   "config group",
			method: http.MethodGet,
			path:   "/api/v1/admin/auth/configs/group/basic",
			want:   "/api/v1/admin/auth/configs/group/{group}",
		},
		{
			name:   "dict data type",
			method: http.MethodGet,
			path:   "/api/v1/admin/auth/dict-data/type/sys_oper_log_module",
			want:   "/api/v1/admin/auth/dict-data/type/{type}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collectPermissionObjectsForRoute(t, tt.method, tt.path)
			if !containsString(got, tt.path) {
				t.Fatalf("permission objects = %v, want actual path %q", got, tt.path)
			}
			if !containsString(got, tt.want) {
				t.Fatalf("permission objects = %v, want route template %q", got, tt.want)
			}
		})
	}
}

func collectPermissionObjectsForRoute(t *testing.T, method, path string) []string {
	t.Helper()

	app := fiber.New()
	var got []string

	adminAuth := app.Group("/api/v1/admin/auth", func(c fiber.Ctx) error {
		got = permissionObjectsForRequest(c)
		return c.Next()
	})

	roles := adminAuth.Group("/roles")
	roles.Get("/:id/permissions", noContentHandler)
	roles.Post("/:id/permissions", noContentHandler)
	roles.Put("/:id", noContentHandler)
	roles.Delete("/:id", noContentHandler)

	departments := adminAuth.Group("/departments")
	departments.Put("/:id", noContentHandler)
	departments.Delete("/:id", noContentHandler)

	configs := adminAuth.Group("/configs")
	configs.Get("/group/:group", noContentHandler)

	dictData := adminAuth.Group("/dict-data")
	dictData.Get("/type/:type", noContentHandler)

	resp, err := app.Test(httptest.NewRequest(method, path, nil))
	if err != nil {
		t.Fatalf("request %s %s failed: %v", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("request %s %s status = %d, want %d", method, path, resp.StatusCode, fiber.StatusNoContent)
	}
	if got == nil {
		t.Fatalf("permission objects were not collected for %s %s", method, path)
	}

	return got
}

func noContentHandler(c fiber.Ctx) error {
	return c.SendStatus(fiber.StatusNoContent)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
