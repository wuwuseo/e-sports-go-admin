package handler

import (
	"testing"

	"go-admin/internal/service"
)

func TestIsOAuthSuperAdminAcceptsAdminRole(t *testing.T) {
	if !isOAuthSuperAdmin(&service.AdminResponse{Roles: []string{"admin"}}) {
		t.Fatal("admin role should be accepted as OAuth super administrator")
	}
}
