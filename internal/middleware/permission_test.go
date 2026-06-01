package middleware

import "testing"

func TestNormalizePermissionRoutePath_ConvertsFiberParamsToPolicyParams(t *testing.T) {
	got := normalizePermissionRoutePath("/api/v1/admin/auth/roles/:id/permissions")
	want := "/api/v1/admin/auth/roles/{id}/permissions"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
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
