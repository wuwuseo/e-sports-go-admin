package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
)

// -------------------------------------------------------------------
// stubPermissionService 用于 permission handler 的轻量桩
// -------------------------------------------------------------------
type stubPermissionService struct {
	createFn            func(ctx context.Context, operatorUID string, req *service.CreatePermissionRequest, operators ...*service.EffectiveDataScope) error
	updateFn            func(ctx context.Context, operatorUID string, req *service.UpdatePermissionRequest, operators ...*service.EffectiveDataScope) error
	deleteFn            func(ctx context.Context, operatorUID string, roleName, domain, object, action string, operators ...*service.EffectiveDataScope) error
	getFn               func(ctx context.Context, roleName, domain, object, action string, operators ...*service.EffectiveDataScope) (*service.PermissionResponse, error)
	listFn              func(ctx context.Context, operatorUID string, req *service.ListPermissionsRequest, operators ...*service.EffectiveDataScope) (*service.ListPermissionsResponse, error)
	listAllFn           func(ctx context.Context, operatorUID string, keyword string, permType service.PermissionType, domain string, roleName string, operators ...*service.EffectiveDataScope) ([]*service.PermissionResponse, error)
	assignFn            func(ctx context.Context, operatorUID string, roleName, domain string, permissions []service.PermissionRule, operators ...*service.EffectiveDataScope) error
	removeFn            func(ctx context.Context, operatorUID string, roleName, domain string, permissions []service.PermissionRule, operators ...*service.EffectiveDataScope) error
	getRolePermsFn      func(ctx context.Context, roleName, domain string, operators ...*service.EffectiveDataScope) ([]*service.PermissionResponse, error)
	getRolePermsByIDFn  func(ctx context.Context, roleID int, domain string, operators ...*service.EffectiveDataScope) ([]*service.PermissionResponse, error)
	saveRolePermsFn     func(ctx context.Context, operatorUID string, roleID int, req *service.SaveRolePermissionsRequest, operators ...*service.EffectiveDataScope) error
	getUserMenuTreeFn   func(ctx context.Context, userID string, domain string) ([]*service.SystemPermissionTreeNode, error)
	getUserButtonFn     func(ctx context.Context, userID string, domain string) ([]*service.PermissionResponse, error)
	getUserAPIFn        func(ctx context.Context, userID string, domain string) ([]*service.PermissionResponse, error)
	getUserPermTreeFn   func(ctx context.Context, userID string, domain string) ([]*service.SystemPermissionTreeNode, error)
	getUserMenuFn       func(ctx context.Context, userID string, domain string) ([]*service.PermissionResponse, error)
	checkPermFn         func(ctx context.Context, userID string, domain, object, action string) (bool, error)
	checkUserHasRoleFn  func(ctx context.Context, userUID string, domain string, roleName string) (bool, error)
}

func (s *stubPermissionService) CreatePermission(ctx context.Context, operatorUID string, req *service.CreatePermissionRequest, operators ...*service.EffectiveDataScope) error {
	if s.createFn != nil {
		return s.createFn(ctx, operatorUID, req, operators...)
	}
	return nil
}
func (s *stubPermissionService) UpdatePermission(ctx context.Context, operatorUID string, req *service.UpdatePermissionRequest, operators ...*service.EffectiveDataScope) error {
	if s.updateFn != nil {
		return s.updateFn(ctx, operatorUID, req, operators...)
	}
	return nil
}
func (s *stubPermissionService) DeletePermission(ctx context.Context, operatorUID string, roleName, domain, object, action string, operators ...*service.EffectiveDataScope) error {
	if s.deleteFn != nil {
		return s.deleteFn(ctx, operatorUID, roleName, domain, object, action, operators...)
	}
	return nil
}
func (s *stubPermissionService) GetPermission(ctx context.Context, roleName, domain, object, action string, operators ...*service.EffectiveDataScope) (*service.PermissionResponse, error) {
	if s.getFn != nil {
		return s.getFn(ctx, roleName, domain, object, action, operators...)
	}
	return nil, nil
}
func (s *stubPermissionService) ListPermissions(ctx context.Context, operatorUID string, req *service.ListPermissionsRequest, operators ...*service.EffectiveDataScope) (*service.ListPermissionsResponse, error) {
	if s.listFn != nil {
		return s.listFn(ctx, operatorUID, req, operators...)
	}
	return &service.ListPermissionsResponse{}, nil
}
func (s *stubPermissionService) ListAllPermissions(ctx context.Context, operatorUID string, keyword string, permType service.PermissionType, domain string, roleName string, operators ...*service.EffectiveDataScope) ([]*service.PermissionResponse, error) {
	if s.listAllFn != nil {
		return s.listAllFn(ctx, operatorUID, keyword, permType, domain, roleName, operators...)
	}
	return nil, nil
}
func (s *stubPermissionService) AssignPermissionsToRole(ctx context.Context, operatorUID string, roleName, domain string, permissions []service.PermissionRule, operators ...*service.EffectiveDataScope) error {
	if s.assignFn != nil {
		return s.assignFn(ctx, operatorUID, roleName, domain, permissions, operators...)
	}
	return nil
}
func (s *stubPermissionService) RemovePermissionsFromRole(ctx context.Context, operatorUID string, roleName, domain string, permissions []service.PermissionRule, operators ...*service.EffectiveDataScope) error {
	if s.removeFn != nil {
		return s.removeFn(ctx, operatorUID, roleName, domain, permissions, operators...)
	}
	return nil
}
func (s *stubPermissionService) GetRolePermissions(ctx context.Context, roleName, domain string, operators ...*service.EffectiveDataScope) ([]*service.PermissionResponse, error) {
	if s.getRolePermsFn != nil {
		return s.getRolePermsFn(ctx, roleName, domain, operators...)
	}
	return nil, nil
}
func (s *stubPermissionService) GetRolePermissionsByRoleID(ctx context.Context, roleID int, domain string, operators ...*service.EffectiveDataScope) ([]*service.PermissionResponse, error) {
	if s.getRolePermsByIDFn != nil {
		return s.getRolePermsByIDFn(ctx, roleID, domain, operators...)
	}
	return nil, nil
}
func (s *stubPermissionService) SaveRolePermissions(ctx context.Context, operatorUID string, roleID int, req *service.SaveRolePermissionsRequest, operators ...*service.EffectiveDataScope) error {
	if s.saveRolePermsFn != nil {
		return s.saveRolePermsFn(ctx, operatorUID, roleID, req, operators...)
	}
	return nil
}
func (s *stubPermissionService) GetUserMenuPermissions(ctx context.Context, userID string, domain string) ([]*service.PermissionResponse, error) {
	if s.getUserMenuFn != nil {
		return s.getUserMenuFn(ctx, userID, domain)
	}
	return nil, nil
}
func (s *stubPermissionService) GetUserMenuPermissionTree(ctx context.Context, userID string, domain string) ([]*service.SystemPermissionTreeNode, error) {
	if s.getUserMenuTreeFn != nil {
		return s.getUserMenuTreeFn(ctx, userID, domain)
	}
	return nil, nil
}
func (s *stubPermissionService) GetUserPermissionTree(ctx context.Context, userID string, domain string) ([]*service.SystemPermissionTreeNode, error) {
	if s.getUserPermTreeFn != nil {
		return s.getUserPermTreeFn(ctx, userID, domain)
	}
	return nil, nil
}
func (s *stubPermissionService) GetUserButtonPermissions(ctx context.Context, userID string, domain string) ([]*service.PermissionResponse, error) {
	if s.getUserButtonFn != nil {
		return s.getUserButtonFn(ctx, userID, domain)
	}
	return nil, nil
}
func (s *stubPermissionService) GetUserAPIPermissions(ctx context.Context, userID string, domain string) ([]*service.PermissionResponse, error) {
	if s.getUserAPIFn != nil {
		return s.getUserAPIFn(ctx, userID, domain)
	}
	return nil, nil
}
func (s *stubPermissionService) CheckPermission(ctx context.Context, userID string, domain, object, action string) (bool, error) {
	if s.checkPermFn != nil {
		return s.checkPermFn(ctx, userID, domain, object, action)
	}
	return false, nil
}
func (s *stubPermissionService) CheckUserHasRole(ctx context.Context, userUID string, domain string, roleName string) (bool, error) {
	if s.checkUserHasRoleFn != nil {
		return s.checkUserHasRoleFn(ctx, userUID, domain, roleName)
	}
	return false, nil
}
func (s *stubPermissionService) GetUserRoleNames(ctx context.Context, userUID string) ([]string, error) {
	return nil, nil
}

var _ service.PermissionServiceInterface = (*stubPermissionService)(nil)

// stubDataPermServiceAlwaysOK 总是返回有效 OperatorContext 的 DataPermissionService 桩
type stubDataPermServiceAlwaysOK struct{}

func (s *stubDataPermServiceAlwaysOK) NewOperatorContext(ctx context.Context, currentUID string, domains ...string) (*service.EffectiveDataScope, error) {
	return &service.EffectiveDataScope{Domain: "default"}, nil
}

// stubDataPermServiceRequireUID 当 UID 为空时返回 ErrPermissionOperatorRequired
type stubDataPermServiceRequireUID struct{}

func (s *stubDataPermServiceRequireUID) NewOperatorContext(ctx context.Context, currentUID string, domains ...string) (*service.EffectiveDataScope, error) {
	if currentUID == "" {
		return nil, service.ErrPermissionOperatorRequired
	}
	return &service.EffectiveDataScope{Domain: "default"}, nil
}

var _ service.DataPermissionOperatorProvider = (*stubDataPermServiceAlwaysOK)(nil)
var _ service.DataPermissionOperatorProvider = (*stubDataPermServiceRequireUID)(nil)

// newPermissionApp 构造最小可用的 permission handler 测试容器
func newPermissionApp(svc service.PermissionServiceInterface, dataPermSvc service.DataPermissionOperatorProvider, authLocals func(c fiber.Ctx)) *fiber.App {
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("utils.Response", utils.NewResponse())
		c.Locals("service.PermissionService", svc)
		c.Locals("service.DataPermissionService", dataPermSvc)
		c.Locals("service.OperationLogService", &noopLogSvc{})
		if authLocals != nil {
			authLocals(c)
		}
		return c.Next()
	})
	app.Post("/api/v1/admin/auth/permissions", CreatePermission)
	app.Put("/api/v1/admin/auth/permissions", UpdatePermission)
	app.Delete("/api/v1/admin/auth/permissions", DeletePermission)
	app.Get("/api/v1/admin/auth/permissions", ListPermissions)
	app.Get("/api/v1/admin/auth/permissions/all", ListAllPermissions)
	app.Post("/api/v1/admin/auth/roles/permissions/assign", AssignPermissionsToRole)
	app.Post("/api/v1/admin/auth/roles/permissions/remove", RemovePermissionsFromRole)
	app.Get("/api/v1/admin/auth/roles/permissions", GetRolePermissions)
	app.Get("/api/v1/admin/auth/roles/:id/permissions", GetRolePermissionsByRoleID)
	app.Post("/api/v1/admin/auth/roles/:id/permissions", SaveRolePermissions)
	app.Get("/api/v1/admin/auth/user/menu-permissions", GetUserMenuPermissions)
	app.Get("/api/v1/admin/auth/user/button-permissions", GetUserButtonPermissions)
	app.Get("/api/v1/admin/auth/user/api-permissions", GetUserAPIPermissions)
	app.Get("/api/v1/admin/auth/user/permissions", GetUserPermissions)
	return app
}

type noopLogSvc struct{}

func (n *noopLogSvc) Log(ctx context.Context, entry *service.LogEntry) error { return nil }
func (n *noopLogSvc) List(ctx context.Context, scope *service.EffectiveDataScope, req *service.ListOperationLogsRequest) (*service.ListOperationLogsResponse, error) {
	return nil, nil
}
func (n *noopLogSvc) Get(ctx context.Context, scope *service.EffectiveDataScope, id int) (*service.OperationLogResponse, error) {
	return nil, nil
}

var _ service.OperationLogServiceInterface = (*noopLogSvc)(nil)

// -------------------------------------------------------------------
// CreatePermission 安全路径测试
// -------------------------------------------------------------------

func TestCreatePermission_CrossDomainReturns403(t *testing.T) {
	svc := &stubPermissionService{}
	app := newPermissionApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "tenant-a")
		c.Locals("uid", "uid-1")
	})

	body := `{"role_name":"admin","domain":"tenant-b","type":"api","object":"/users","action":"GET"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/permissions", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var r utils.Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if r.Code != global.ResponseForbidden {
		t.Fatalf("code=%d, want %d (跨域应403)", r.Code, global.ResponseForbidden)
	}
}

func TestCreatePermission_MissingUIDReturns401(t *testing.T) {
	svc := &stubPermissionService{}
	app := newPermissionApp(svc, &stubDataPermServiceRequireUID{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		// 不设置 uid
	})

	body := `{"role_name":"admin","domain":"default","type":"api","object":"/users","action":"GET"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/permissions", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var r utils.Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if r.Code != global.ResponseUnauthorized {
		t.Fatalf("code=%d, want %d (缺uid应401)", r.Code, global.ResponseUnauthorized)
	}
}

func TestCreatePermission_Success(t *testing.T) {
	called := false
	svc := &stubPermissionService{
		createFn: func(ctx context.Context, operatorUID string, req *service.CreatePermissionRequest, operators ...*service.EffectiveDataScope) error {
			called = true
			if req.Domain != "default" {
				t.Errorf("domain=%q, want default", req.Domain)
			}
			return nil
		},
	}
	app := newPermissionApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	body := `{"role_name":"admin","domain":"default","type":"api","object":"/users","action":"GET"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/permissions", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var r utils.Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if r.Code != global.ResponseSuccessCode {
		t.Fatalf("code=%d, want %d", r.Code, global.ResponseSuccessCode)
	}
	if !called {
		t.Fatal("CreatePermission 应被调用")
	}
}

// -------------------------------------------------------------------
// DeletePermission 安全路径测试
// -------------------------------------------------------------------

func TestDeletePermission_MissingParamsReturns400(t *testing.T) {
	svc := &stubPermissionService{}
	app := newPermissionApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/auth/permissions?role_name=admin&domain=default&object=/users", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var r utils.Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if r.Code != global.ResponseErrorCode {
		t.Fatalf("code=%d, want %d (参数缺失应400)", r.Code, global.ResponseErrorCode)
	}
}

func TestDeletePermission_CrossDomainReturns403(t *testing.T) {
	svc := &stubPermissionService{}
	app := newPermissionApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "tenant-a")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/auth/permissions?role_name=admin&domain=tenant-b&object=/users&action=GET", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var r utils.Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if r.Code != global.ResponseForbidden {
		t.Fatalf("code=%d, want %d (跨域应403)", r.Code, global.ResponseForbidden)
	}
}

func TestDeletePermission_RoleScopeEscalationReturns403(t *testing.T) {
	svc := &stubPermissionService{
		deleteFn: func(ctx context.Context, operatorUID string, roleName, domain, object, action string, operators ...*service.EffectiveDataScope) error {
			return service.ErrRoleScopeEscalation
		},
	}
	app := newPermissionApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/auth/permissions?role_name=admin&domain=default&object=/users&action=GET", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var r utils.Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if r.Code != global.ResponseRoleScopeEscalation {
		t.Fatalf("code=%d, want %d", r.Code, global.ResponseRoleScopeEscalation)
	}
}

func TestDeletePermission_Success(t *testing.T) {
	called := false
	svc := &stubPermissionService{
		deleteFn: func(ctx context.Context, operatorUID string, roleName, domain, object, action string, operators ...*service.EffectiveDataScope) error {
			called = true
			return nil
		},
	}
	app := newPermissionApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/auth/permissions?role_name=admin&domain=default&object=/users&action=GET", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var r utils.Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if r.Code != global.ResponseSuccessCode {
		t.Fatalf("code=%d, want %d", r.Code, global.ResponseSuccessCode)
	}
	if !called {
		t.Fatal("DeletePermission 应被调用")
	}
}

// -------------------------------------------------------------------
// ListPermissions / ListAllPermissions 安全路径测试
// -------------------------------------------------------------------

func TestListPermissions_CrossDomainInQueryReturns403(t *testing.T) {
	svc := &stubPermissionService{}
	app := newPermissionApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "tenant-a")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/permissions?domain=tenant-b", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var r utils.Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if r.Code != global.ResponseForbidden {
		t.Fatalf("code=%d, want %d (查询跨域应403)", r.Code, global.ResponseForbidden)
	}
}

func TestListAllPermissions_UsesDefaultDomainWhenEmpty(t *testing.T) {
	var gotDomain string
	svc := &stubPermissionService{
		listAllFn: func(ctx context.Context, operatorUID string, keyword string, permType service.PermissionType, domain string, roleName string, operators ...*service.EffectiveDataScope) ([]*service.PermissionResponse, error) {
			gotDomain = domain
			return nil, nil
		},
	}
	app := newPermissionApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/permissions/all", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var r utils.Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if r.Code != global.ResponseSuccessCode {
		t.Fatalf("code=%d, want %d", r.Code, global.ResponseSuccessCode)
	}
	if gotDomain != "default" {
		t.Fatalf("domain=%q, want default", gotDomain)
	}
}

// -------------------------------------------------------------------
// SaveRolePermissions 安全路径测试
// -------------------------------------------------------------------

func TestSaveRolePermissions_InvalidRoleIDReturns400(t *testing.T) {
	svc := &stubPermissionService{}
	app := newPermissionApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	body := `{"domain":"default","permissions":[{"object":"/users","action":"GET"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/roles/abc/permissions", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var r utils.Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if r.Code != global.ResponseErrorCode {
		t.Fatalf("code=%d, want %d (无效ID应400)", r.Code, global.ResponseErrorCode)
	}
}

func TestSaveRolePermissions_CrossDomainReturns403(t *testing.T) {
	svc := &stubPermissionService{}
	app := newPermissionApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "tenant-a")
		c.Locals("uid", "uid-1")
	})

	body := `{"domain":"tenant-b","permissions":[{"object":"/users","action":"GET"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/roles/1/permissions", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var r utils.Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if r.Code != global.ResponseForbidden {
		t.Fatalf("code=%d, want %d (跨域应403)", r.Code, global.ResponseForbidden)
	}
}

func TestSaveRolePermissions_SelfRoleMutationForbidden(t *testing.T) {
	svc := &stubPermissionService{
		saveRolePermsFn: func(ctx context.Context, operatorUID string, roleID int, req *service.SaveRolePermissionsRequest, operators ...*service.EffectiveDataScope) error {
			return service.ErrSelfRolePermissionMutationForbidden
		},
	}
	app := newPermissionApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	body := `{"domain":"default","permissions":[{"object":"/users","action":"GET"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/roles/1/permissions", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var r utils.Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if r.Code != global.ResponseSelfRolePermissionForbidden {
		t.Fatalf("code=%d, want %d (自身角色权限变更禁止)", r.Code, global.ResponseSelfRolePermissionForbidden)
	}
}

// -------------------------------------------------------------------
// GetUserPermissions 系列：缺少 domain 时应 401
// -------------------------------------------------------------------

func TestGetUserMenuPermissions_MissingDomainReturns401(t *testing.T) {
	svc := &stubPermissionService{}
	app := newPermissionApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("uid", "uid-1")
		// 不设置 domain
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/user/menu-permissions", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var r utils.Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if r.Code != global.ResponseUnauthorized {
		t.Fatalf("code=%d, want %d (缺domain应401)", r.Code, global.ResponseUnauthorized)
	}
}

func TestGetUserButtonPermissions_MissingDomainReturns401(t *testing.T) {
	svc := &stubPermissionService{}
	app := newPermissionApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/user/button-permissions", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var r utils.Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if r.Code != global.ResponseUnauthorized {
		t.Fatalf("code=%d, want %d (缺domain应401)", r.Code, global.ResponseUnauthorized)
	}
}

func TestGetUserAPIPermissions_MissingDomainReturns401(t *testing.T) {
	svc := &stubPermissionService{}
	app := newPermissionApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/user/api-permissions", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var r utils.Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if r.Code != global.ResponseUnauthorized {
		t.Fatalf("code=%d, want %d (缺domain应401)", r.Code, global.ResponseUnauthorized)
	}
}

func TestGetUserPermissions_MissingDomainReturns401(t *testing.T) {
	svc := &stubPermissionService{}
	app := newPermissionApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/user/permissions", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var r utils.Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if r.Code != global.ResponseUnauthorized {
		t.Fatalf("code=%d, want %d (缺domain应401)", r.Code, global.ResponseUnauthorized)
	}
}

// -------------------------------------------------------------------
// GetRolePermissionsByRoleID 安全路径
// -------------------------------------------------------------------

func TestGetRolePermissionsByRoleID_InvalidIDReturns400(t *testing.T) {
	svc := &stubPermissionService{}
	app := newPermissionApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/roles/abc/permissions", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var r utils.Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if r.Code != global.ResponseErrorCode {
		t.Fatalf("code=%d, want %d (无效ID应400)", r.Code, global.ResponseErrorCode)
	}
}

func TestGetRolePermissionsByRoleID_CrossDomainReturns403(t *testing.T) {
	svc := &stubPermissionService{}
	app := newPermissionApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "tenant-a")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/roles/1/permissions?domain=tenant-b", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var r utils.Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if r.Code != global.ResponseForbidden {
		t.Fatalf("code=%d, want %d (跨域应403)", r.Code, global.ResponseForbidden)
	}
}

// -------------------------------------------------------------------
// AssignPermissionsToRole / RemovePermissionsFromRole 安全路径
// -------------------------------------------------------------------

func TestAssignPermissionsToRole_CrossDomainReturns403(t *testing.T) {
	svc := &stubPermissionService{}
	app := newPermissionApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "tenant-a")
		c.Locals("uid", "uid-1")
	})

	body := `{"role_name":"admin","domain":"tenant-b","permissions":[{"object":"/users","action":"GET"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/roles/permissions/assign", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var r utils.Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if r.Code != global.ResponseForbidden {
		t.Fatalf("code=%d, want %d (跨域应403)", r.Code, global.ResponseForbidden)
	}
}

func TestRemovePermissionsFromRole_SuperAdminProtectedReturns403(t *testing.T) {
	svc := &stubPermissionService{
		removeFn: func(ctx context.Context, operatorUID string, roleName, domain string, permissions []service.PermissionRule, operators ...*service.EffectiveDataScope) error {
			return service.ErrSuperAdminProtected
		},
	}
	app := newPermissionApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	body := `{"role_name":"admin","domain":"default","permissions":[{"object":"/users","action":"GET"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/roles/permissions/remove", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var r utils.Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if r.Code != global.ResponseForbidden {
		t.Fatalf("code=%d, want %d (超级管理员保护应403)", r.Code, global.ResponseForbidden)
	}
}

// -------------------------------------------------------------------
// UpdatePermission 错误映射测试
// -------------------------------------------------------------------

func TestUpdatePermission_PermissionNotFoundReturns404(t *testing.T) {
	svc := &stubPermissionService{
		updateFn: func(ctx context.Context, operatorUID string, req *service.UpdatePermissionRequest, operators ...*service.EffectiveDataScope) error {
			return service.ErrPermissionNotFound
		},
	}
	app := newPermissionApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	body := `{"role_name":"admin","domain":"default","type":"api","object":"/users","action":"GET"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/auth/permissions", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var r utils.Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if r.Code != global.ResponseNotFound {
		t.Fatalf("code=%d, want %d (权限不存在应404)", r.Code, global.ResponseNotFound)
	}
}

func TestUpdatePermission_PermissionScopeEscalationReturns403(t *testing.T) {
	svc := &stubPermissionService{
		updateFn: func(ctx context.Context, operatorUID string, req *service.UpdatePermissionRequest, operators ...*service.EffectiveDataScope) error {
			return service.ErrPermissionScopeEscalation
		},
	}
	app := newPermissionApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	body := `{"role_name":"admin","domain":"default","type":"api","object":"/users","action":"GET"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/auth/permissions", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var r utils.Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if r.Code != global.ResponseForbidden {
		t.Fatalf("code=%d, want %d (权限范围越级应403)", r.Code, global.ResponseForbidden)
	}
}

// -------------------------------------------------------------------
// permissionDomainAllowed / permissionOperatorScope 直接测试
// -------------------------------------------------------------------

func TestPermissionDomainAllowed(t *testing.T) {
	app := fiber.New()
	var sameDomain, crossDomain, emptyDomain bool
	app.Get("/test", func(c fiber.Ctx) error {
		c.Locals("domain", "tenant-a")
		sameDomain = permissionDomainAllowed(c, "tenant-a")
		crossDomain = permissionDomainAllowed(c, "tenant-b")
		emptyDomain = permissionDomainAllowed(c, "")
		return c.SendStatus(fiber.StatusNoContent)
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/test", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	resp.Body.Close()

	if !sameDomain || crossDomain || emptyDomain {
		t.Fatalf("domain checks = same:%v cross:%v empty:%v", sameDomain, crossDomain, emptyDomain)
	}
}

func TestPermissionOperatorScope_MissingUIDReturnsError(t *testing.T) {
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("service.DataPermissionService", &stubDataPermServiceRequireUID{})
		return c.Next()
	})
	app.Get("/test", func(c fiber.Ctx) error {
		_, err := permissionOperatorScope(c)
		if !errors.Is(err, service.ErrPermissionOperatorRequired) {
			t.Fatalf("err=%v, want ErrPermissionOperatorRequired", err)
		}
		return c.SendStatus(fiber.StatusNoContent)
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/test", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	resp.Body.Close()
}
