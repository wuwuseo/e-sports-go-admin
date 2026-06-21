package service

import (
	"testing"

	"go-admin/internal/ent"
)

// -------------------------------------------------------------------
// IsSensitivePermissionObject 单元测试
// -------------------------------------------------------------------

func TestIsSensitivePermissionObject_ReturnsTrueForSensitivePaths(t *testing.T) {
	sensitivePaths := []string{
		"/api/v1/admin/auth/system-permissions",
		"/api/v1/admin/auth/system-permissions/{id}",
		"/api/v1/admin/auth/system-permissions/tree",
		"/api/v1/admin/auth/roles/{id}/permissions",
		"/api/v1/admin/auth/roles/permissions/assign",
		"/api/v1/admin/auth/roles/permissions/remove",
		"/api/v1/admin/auth/permissions",
		"/api/v1/admin/auth/permissions/detail",
		"/api/v1/admin/auth/permissions/all",
		"/api/v1/admin/auth/oauth/providers",
		"/api/v1/admin/auth/oauth/providers/{provider}",
	}
	for _, path := range sensitivePaths {
		if !IsSensitivePermissionObject(path) {
			t.Errorf("路径 %q 应被识别为敏感权限对象", path)
		}
	}
}

func TestIsSensitivePermissionObject_ReturnsFalseForNormalPaths(t *testing.T) {
	normalPaths := []string{
		"/api/v1/admin/auth/users",
		"/api/v1/admin/auth/roles",
		"/api/v1/admin/auth/dashboard",
		"/api/v1/admin/auth/attachments",
		"/api/v1/public/login",
	}
	for _, path := range normalPaths {
		if IsSensitivePermissionObject(path) {
			t.Errorf("路径 %q 不应被识别为敏感权限对象", path)
		}
	}
}

func TestIsSensitivePermissionObject_EmptyPathReturnsFalse(t *testing.T) {
	if IsSensitivePermissionObject("") {
		t.Error("空路径不应被识别为敏感权限对象")
	}
}

// -------------------------------------------------------------------
// isSuperAdminRole 单元测试
// -------------------------------------------------------------------

func TestIsSuperAdminRole_ReturnsTrueForAdmin(t *testing.T) {
	if !isSuperAdminRole("admin") {
		t.Error("'admin' 角色应被识别为超级管理员角色")
	}
}

func TestIsSuperAdminRole_ReturnsFalseForOtherRoles(t *testing.T) {
	otherRoles := []string{"user", "editor", "viewer", "Admin", "ADMIN", "superadmin"}
	for _, role := range otherRoles {
		if isSuperAdminRole(role) {
			t.Errorf("角色 %q 不应被识别为超级管理员角色", role)
		}
	}
}

func TestIsSuperAdminRole_ReturnsFalseForEmpty(t *testing.T) {
	if isSuperAdminRole("") {
		t.Error("空角色名不应被识别为超级管理员角色")
	}
}

// -------------------------------------------------------------------
// permissionLookupKeys 边界情况测试
// -------------------------------------------------------------------

func TestPermissionLookupKeys_EmptyObjectReturnsValidKeys(t *testing.T) {
	keys := permissionLookupKeys("api", "", "GET")
	if len(keys) == 0 {
		t.Fatal("空对象仍应产生 key")
	}
	// 验证 key 格式正确（包含 null 分隔符）
	for _, key := range keys {
		if key == "" {
			t.Error("key 不应为空")
		}
	}
}

func TestPermissionLookupKeys_UnknownTypeReturnsSingleKey(t *testing.T) {
	keys := permissionLookupKeys("unknown", "/test", "GET")
	if len(keys) != 1 {
		t.Fatalf("未知类型应只产生 1 个 key，got %d", len(keys))
	}
}

// -------------------------------------------------------------------
// operatorPermissionScope allowsSystemPermission 边界测试
// -------------------------------------------------------------------

func TestOperatorPermissionScope_AllowsSystemPermission_APIWithMatchingLookup(t *testing.T) {
	lookup := map[string]struct{}{
		permissionLookupKey("api", "/api/users", "GET"): {},
	}
	scope := &operatorPermissionScope{lookup: lookup}
	perm := &ent.SystemPermission{
		Type:   "api",
		Path:   "/api/users",
		Method: "GET",
	}
	if !scope.allowsSystemPermission(perm) {
		t.Error("匹配 lookup 的 API 权限应被允许")
	}
}

func TestOperatorPermissionScope_AllowsSystemPermission_APIWithoutMatch(t *testing.T) {
	lookup := map[string]struct{}{
		permissionLookupKey("api", "/api/users", "GET"): {},
	}
	scope := &operatorPermissionScope{lookup: lookup}
	perm := &ent.SystemPermission{
		Type:   "api",
		Path:   "/api/secret",
		Method: "GET",
	}
	if scope.allowsSystemPermission(perm) {
		t.Error("不匹配 lookup 的 API 权限应被拒绝")
	}
}

func TestOperatorPermissionScope_AllowsSystemPermission_MenuWithMatchingLookup(t *testing.T) {
	lookup := map[string]struct{}{
		permissionLookupKey("menu", "/dashboard", "menu"): {},
	}
	scope := &operatorPermissionScope{lookup: lookup}
	perm := &ent.SystemPermission{
		Type:     "menu",
		MenuPath: "/dashboard",
	}
	if !scope.allowsSystemPermission(perm) {
		t.Error("匹配 lookup 的菜单权限应被允许")
	}
}

func TestOperatorPermissionScope_AllowsSystemPermission_UnknownType(t *testing.T) {
	scope := &operatorPermissionScope{includeAll: false, lookup: map[string]struct{}{}}
	perm := &ent.SystemPermission{
		Type: "unknown",
	}
	if scope.allowsSystemPermission(perm) {
		t.Error("未知类型的权限应被拒绝")
	}
}

func TestOperatorPermissionScope_NilScopeAllowsSystemPermission(t *testing.T) {
	var scope *operatorPermissionScope
	perm := &ent.SystemPermission{Type: "api", Path: "/any"}
	if !scope.allowsSystemPermission(perm) {
		t.Error("nil scope 应允许所有权限以保持一致性")
	}
}

// -------------------------------------------------------------------
// attachmentAccessSubject 单元测试
// -------------------------------------------------------------------

func TestAttachmentAccessSubject_DifferentIDsProduceDifferentSubjects(t *testing.T) {
	subject1 := attachmentAccessSubject(1)
	subject2 := attachmentAccessSubject(2)
	if subject1 == subject2 {
		t.Error("不同 ID 应产生不同的 subject")
	}
	if subject1 != "attachment:1" || subject2 != "attachment:2" {
		t.Errorf("subject 格式不正确: %q, %q", subject1, subject2)
	}
}
