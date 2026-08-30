package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
)

// -------------------------------------------------------------------
// stubRoleService 用于 role handler 的轻量桩
// -------------------------------------------------------------------
type stubRoleService struct {
	createFn  func(ctx context.Context, op *service.EffectiveDataScope, req *service.CreateRoleRequest) (*service.RoleResponse, error)
	getFn     func(ctx context.Context, id int, operators ...*service.EffectiveDataScope) (*service.RoleResponse, error)
	listFn    func(ctx context.Context, req *service.ListRolesRequest, operators ...*service.EffectiveDataScope) (*service.ListRolesResponse, error)
	listAllFn func(ctx context.Context, req *service.ListAllRolesRequest, operators ...*service.EffectiveDataScope) ([]*service.RoleResponse, error)
	updateFn  func(ctx context.Context, op *service.EffectiveDataScope, id int, req *service.UpdateRoleRequest) error
	deleteFn  func(ctx context.Context, op *service.EffectiveDataScope, id int) error
	enableFn  func(ctx context.Context, op *service.EffectiveDataScope, id int) error
	disableFn func(ctx context.Context, op *service.EffectiveDataScope, id int) error
}

func (s *stubRoleService) Create(ctx context.Context, op *service.EffectiveDataScope, req *service.CreateRoleRequest) (*service.RoleResponse, error) {
	if s.createFn != nil {
		return s.createFn(ctx, op, req)
	}
	return &service.RoleResponse{}, nil
}
func (s *stubRoleService) Get(ctx context.Context, id int, operators ...*service.EffectiveDataScope) (*service.RoleResponse, error) {
	if s.getFn != nil {
		return s.getFn(ctx, id, operators...)
	}
	return nil, nil
}
func (s *stubRoleService) List(ctx context.Context, req *service.ListRolesRequest, operators ...*service.EffectiveDataScope) (*service.ListRolesResponse, error) {
	if s.listFn != nil {
		return s.listFn(ctx, req, operators...)
	}
	return &service.ListRolesResponse{}, nil
}
func (s *stubRoleService) ListAll(ctx context.Context, req *service.ListAllRolesRequest, operators ...*service.EffectiveDataScope) ([]*service.RoleResponse, error) {
	if s.listAllFn != nil {
		return s.listAllFn(ctx, req, operators...)
	}
	return nil, nil
}
func (s *stubRoleService) Update(ctx context.Context, op *service.EffectiveDataScope, id int, req *service.UpdateRoleRequest) error {
	if s.updateFn != nil {
		return s.updateFn(ctx, op, id, req)
	}
	return nil
}
func (s *stubRoleService) Delete(ctx context.Context, op *service.EffectiveDataScope, id int) error {
	if s.deleteFn != nil {
		return s.deleteFn(ctx, op, id)
	}
	return nil
}
func (s *stubRoleService) Enable(ctx context.Context, op *service.EffectiveDataScope, id int) error {
	if s.enableFn != nil {
		return s.enableFn(ctx, op, id)
	}
	return nil
}
func (s *stubRoleService) Disable(ctx context.Context, op *service.EffectiveDataScope, id int) error {
	if s.disableFn != nil {
		return s.disableFn(ctx, op, id)
	}
	return nil
}

var _ service.RoleServiceInterface = (*stubRoleService)(nil)

// newRoleApp 构造最小可用的 role handler 测试容器
func newRoleApp(roleSvc service.RoleServiceInterface, dataPermSvc service.DataPermissionOperatorProvider, authLocals func(c fiber.Ctx)) *fiber.App {
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("utils.Response", utils.NewResponse())
		c.Locals("service.RoleService", roleSvc)
		c.Locals("service.DataPermissionService", dataPermSvc)
		c.Locals("service.OperationLogService", &noopLogSvc{})
		if authLocals != nil {
			authLocals(c)
		}
		return c.Next()
	})
	app.Post("/api/v1/admin/auth/roles", CreateRole)
	app.Get("/api/v1/admin/auth/roles/:id", GetRole)
	app.Get("/api/v1/admin/auth/roles", ListRoles)
	app.Get("/api/v1/admin/auth/roles/all", ListAllRoles)
	app.Put("/api/v1/admin/auth/roles/:id", UpdateRole)
	app.Delete("/api/v1/admin/auth/roles/:id", DeleteRole)
	app.Put("/api/v1/admin/auth/roles/:id/enable", EnableRole)
	app.Put("/api/v1/admin/auth/roles/:id/disable", DisableRole)
	return app
}

// -------------------------------------------------------------------
// CreateRole 安全路径与错误映射
// -------------------------------------------------------------------

func TestCreateRole_RoleNameExistsReturns409(t *testing.T) {
	svc := &stubRoleService{
		createFn: func(ctx context.Context, op *service.EffectiveDataScope, req *service.CreateRoleRequest) (*service.RoleResponse, error) {
			return nil, service.ErrRoleNameExists
		},
	}
	app := newRoleApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	body := `{"name":"admin","display_name":"管理员","data_scope":"all"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/roles", bytes.NewReader([]byte(body)))
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
	if r.Code != global.ResponseConflict {
		t.Fatalf("code=%d, want %d (角色名已存在应409)", r.Code, global.ResponseConflict)
	}
}

func TestCreateRole_RoleScopeEscalationReturns403(t *testing.T) {
	svc := &stubRoleService{
		createFn: func(ctx context.Context, op *service.EffectiveDataScope, req *service.CreateRoleRequest) (*service.RoleResponse, error) {
			return nil, service.ErrRoleScopeEscalation
		},
	}
	app := newRoleApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	body := `{"name":"admin","display_name":"管理员","data_scope":"all"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/roles", bytes.NewReader([]byte(body)))
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
	if r.Code != global.ResponseRoleScopeEscalation {
		t.Fatalf("code=%d, want %d (角色范围越级应403)", r.Code, global.ResponseRoleScopeEscalation)
	}
}

func TestCreateRole_DepartmentNotFoundReturns404(t *testing.T) {
	svc := &stubRoleService{
		createFn: func(ctx context.Context, op *service.EffectiveDataScope, req *service.CreateRoleRequest) (*service.RoleResponse, error) {
			return nil, service.ErrDepartmentNotFound
		},
	}
	app := newRoleApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	body := `{"name":"admin","display_name":"管理员","data_scope":"department"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/roles", bytes.NewReader([]byte(body)))
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
	if r.Code != global.ResponseDepartmentNotFound {
		t.Fatalf("code=%d, want %d (部门不存在应404)", r.Code, global.ResponseDepartmentNotFound)
	}
}

func TestCreateRole_Success(t *testing.T) {
	called := false
	svc := &stubRoleService{
		createFn: func(ctx context.Context, op *service.EffectiveDataScope, req *service.CreateRoleRequest) (*service.RoleResponse, error) {
			called = true
			return &service.RoleResponse{ID: 1, Name: req.Name}, nil
		},
	}
	app := newRoleApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	body := `{"name":"editor","display_name":"编辑","data_scope":"all"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/roles", bytes.NewReader([]byte(body)))
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
		t.Fatal("Create 应被调用")
	}
}

// -------------------------------------------------------------------
// GetRole 安全路径与错误映射
// -------------------------------------------------------------------

func TestGetRole_InvalidIDReturns400(t *testing.T) {
	svc := &stubRoleService{}
	app := newRoleApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/roles/abc", nil)
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

func TestGetRole_NotFoundReturns404(t *testing.T) {
	svc := &stubRoleService{
		getFn: func(ctx context.Context, id int, operators ...*service.EffectiveDataScope) (*service.RoleResponse, error) {
			return nil, service.ErrRoleNotFound
		},
	}
	app := newRoleApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/roles/999", nil)
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
		t.Fatalf("code=%d, want %d (角色不存在应404)", r.Code, global.ResponseNotFound)
	}
}

func TestGetRole_RoleScopeEscalationReturns403(t *testing.T) {
	svc := &stubRoleService{
		getFn: func(ctx context.Context, id int, operators ...*service.EffectiveDataScope) (*service.RoleResponse, error) {
			return nil, service.ErrRoleScopeEscalation
		},
	}
	app := newRoleApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/roles/1", nil)
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
		t.Fatalf("code=%d, want %d (角色范围越级应403)", r.Code, global.ResponseForbidden)
	}
}

func TestGetRole_Success(t *testing.T) {
	svc := &stubRoleService{
		getFn: func(ctx context.Context, id int, operators ...*service.EffectiveDataScope) (*service.RoleResponse, error) {
			return &service.RoleResponse{ID: id, Name: "admin"}, nil
		},
	}
	app := newRoleApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/roles/1", nil)
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
}

// -------------------------------------------------------------------
// UpdateRole 安全路径与错误映射
// -------------------------------------------------------------------

func TestUpdateRole_InvalidIDReturns400(t *testing.T) {
	svc := &stubRoleService{}
	app := newRoleApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	body := `{"display_name":"新名称"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/auth/roles/abc", bytes.NewReader([]byte(body)))
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

func TestUpdateRole_CannotModifySelfRolesReturns403(t *testing.T) {
	svc := &stubRoleService{
		updateFn: func(ctx context.Context, op *service.EffectiveDataScope, id int, req *service.UpdateRoleRequest) error {
			return service.ErrCannotModifySelfRoles
		},
	}
	app := newRoleApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	body := `{"display_name":"新名称"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/auth/roles/1", bytes.NewReader([]byte(body)))
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
	if r.Code != global.ResponseSelfRoleModifyForbidden {
		t.Fatalf("code=%d, want %d (不能修改自身角色应403)", r.Code, global.ResponseSelfRoleModifyForbidden)
	}
}

func TestUpdateRole_RoleScopeEscalationReturns403(t *testing.T) {
	svc := &stubRoleService{
		updateFn: func(ctx context.Context, op *service.EffectiveDataScope, id int, req *service.UpdateRoleRequest) error {
			return service.ErrRoleScopeEscalation
		},
	}
	app := newRoleApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	body := `{"display_name":"新名称"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/auth/roles/1", bytes.NewReader([]byte(body)))
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
	if r.Code != global.ResponseRoleScopeEscalation {
		t.Fatalf("code=%d, want %d (角色范围越级应403)", r.Code, global.ResponseRoleScopeEscalation)
	}
}

func TestUpdateRole_Success(t *testing.T) {
	called := false
	svc := &stubRoleService{
		updateFn: func(ctx context.Context, op *service.EffectiveDataScope, id int, req *service.UpdateRoleRequest) error {
			called = true
			return nil
		},
	}
	app := newRoleApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	body := `{"display_name":"新名称"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/auth/roles/1", bytes.NewReader([]byte(body)))
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
		t.Fatal("Update 应被调用")
	}
}

// -------------------------------------------------------------------
// DeleteRole 安全路径与错误映射
// -------------------------------------------------------------------

func TestDeleteRole_SystemRoleProtectedReturns403(t *testing.T) {
	svc := &stubRoleService{
		deleteFn: func(ctx context.Context, op *service.EffectiveDataScope, id int) error {
			return service.ErrSystemRoleProtected
		},
	}
	app := newRoleApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/auth/roles/1", nil)
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
		t.Fatalf("code=%d, want %d (系统角色保护应403)", r.Code, global.ResponseForbidden)
	}
}

func TestDeleteRole_RoleInUseReturns409(t *testing.T) {
	svc := &stubRoleService{
		deleteFn: func(ctx context.Context, op *service.EffectiveDataScope, id int) error {
			return service.ErrRoleInUse
		},
	}
	app := newRoleApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/auth/roles/1", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var r utils.Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if r.Code != global.ResponseConflict {
		t.Fatalf("code=%d, want %d (角色使用中应409)", r.Code, global.ResponseConflict)
	}
}

func TestDeleteRole_Success(t *testing.T) {
	called := false
	svc := &stubRoleService{
		deleteFn: func(ctx context.Context, op *service.EffectiveDataScope, id int) error {
			called = true
			return nil
		},
	}
	app := newRoleApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/auth/roles/1", nil)
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
		t.Fatal("Delete 应被调用")
	}
}

// -------------------------------------------------------------------
// EnableRole / DisableRole 基本路径
// -------------------------------------------------------------------

func TestEnableRole_Success(t *testing.T) {
	called := false
	svc := &stubRoleService{
		enableFn: func(ctx context.Context, op *service.EffectiveDataScope, id int) error {
			called = true
			return nil
		},
	}
	app := newRoleApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/auth/roles/1/enable", nil)
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
		t.Fatal("Enable 应被调用")
	}
}

func TestDisableRole_NotFoundReturns404(t *testing.T) {
	svc := &stubRoleService{
		disableFn: func(ctx context.Context, op *service.EffectiveDataScope, id int) error {
			return service.ErrRoleNotFound
		},
	}
	app := newRoleApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/auth/roles/999/disable", nil)
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
		t.Fatalf("code=%d, want %d (角色不存在应404)", r.Code, global.ResponseNotFound)
	}
}

// -------------------------------------------------------------------
// ListRoles / ListAllRoles 默认值与成功路径
// -------------------------------------------------------------------

func TestListRoles_DefaultPagination(t *testing.T) {
	var gotReq *service.ListRolesRequest
	svc := &stubRoleService{
		listFn: func(ctx context.Context, req *service.ListRolesRequest, operators ...*service.EffectiveDataScope) (*service.ListRolesResponse, error) {
			gotReq = req
			return &service.ListRolesResponse{}, nil
		},
	}
	app := newRoleApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/roles", nil)
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
	if gotReq == nil {
		t.Fatal("List 应被调用")
	}
	if gotReq.Page != 1 || gotReq.PageSize != 10 {
		t.Fatalf("defaults page=%d page_size=%d, want 1,10", gotReq.Page, gotReq.PageSize)
	}
}
