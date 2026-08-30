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
// stubDepartmentService 用于 department handler 的轻量桩
// -------------------------------------------------------------------
type stubDepartmentService struct {
	createFn     func(ctx context.Context, op *service.EffectiveDataScope, req *service.CreateDepartmentRequest) (*service.DepartmentResponse, error)
	updateFn     func(ctx context.Context, op *service.EffectiveDataScope, id int, req *service.UpdateDepartmentRequest) error
	deleteFn     func(ctx context.Context, op *service.EffectiveDataScope, id int) error
	getFn        func(ctx context.Context, op *service.EffectiveDataScope, id int) (*service.DepartmentResponse, error)
	treeFn       func(ctx context.Context, req *service.ListDepartmentsRequest) ([]*service.DepartmentTreeNode, error)
	treeInScopeFn func(ctx context.Context, op *service.EffectiveDataScope, req *service.ListDepartmentsRequest) ([]*service.DepartmentTreeNode, error)
	childIDsFn   func(ctx context.Context, id int, includeSelf bool) ([]int, error)
}

func (s *stubDepartmentService) Create(ctx context.Context, op *service.EffectiveDataScope, req *service.CreateDepartmentRequest) (*service.DepartmentResponse, error) {
	if s.createFn != nil {
		return s.createFn(ctx, op, req)
	}
	return &service.DepartmentResponse{}, nil
}
func (s *stubDepartmentService) Update(ctx context.Context, op *service.EffectiveDataScope, id int, req *service.UpdateDepartmentRequest) error {
	if s.updateFn != nil {
		return s.updateFn(ctx, op, id, req)
	}
	return nil
}
func (s *stubDepartmentService) Delete(ctx context.Context, op *service.EffectiveDataScope, id int) error {
	if s.deleteFn != nil {
		return s.deleteFn(ctx, op, id)
	}
	return nil
}
func (s *stubDepartmentService) Get(ctx context.Context, op *service.EffectiveDataScope, id int) (*service.DepartmentResponse, error) {
	if s.getFn != nil {
		return s.getFn(ctx, op, id)
	}
	return nil, nil
}
func (s *stubDepartmentService) Tree(ctx context.Context, req *service.ListDepartmentsRequest) ([]*service.DepartmentTreeNode, error) {
	if s.treeFn != nil {
		return s.treeFn(ctx, req)
	}
	return nil, nil
}
func (s *stubDepartmentService) TreeInScope(ctx context.Context, op *service.EffectiveDataScope, req *service.ListDepartmentsRequest) ([]*service.DepartmentTreeNode, error) {
	if s.treeInScopeFn != nil {
		return s.treeInScopeFn(ctx, op, req)
	}
	return nil, nil
}
func (s *stubDepartmentService) ChildIDs(ctx context.Context, id int, includeSelf bool) ([]int, error) {
	if s.childIDsFn != nil {
		return s.childIDsFn(ctx, id, includeSelf)
	}
	return nil, nil
}

var _ service.DepartmentServiceInterface = (*stubDepartmentService)(nil)

// newDepartmentApp 构造最小可用的 department handler 测试容器
func newDepartmentApp(deptSvc service.DepartmentServiceInterface, dataPermSvc service.DataPermissionOperatorProvider, authLocals func(c fiber.Ctx)) *fiber.App {
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("utils.Response", utils.NewResponse())
		c.Locals("service.DepartmentService", deptSvc)
		c.Locals("service.DataPermissionService", dataPermSvc)
		c.Locals("service.OperationLogService", &noopLogSvc{})
		if authLocals != nil {
			authLocals(c)
		}
		return c.Next()
	})
	app.Post("/api/v1/admin/auth/departments", CreateDepartment)
	app.Get("/api/v1/admin/auth/departments/tree", ListDepartmentTree)
	app.Get("/api/v1/admin/auth/departments/:id", GetDepartment)
	app.Put("/api/v1/admin/auth/departments/:id", UpdateDepartment)
	app.Delete("/api/v1/admin/auth/departments/:id", DeleteDepartment)
	return app
}

// -------------------------------------------------------------------
// CreateDepartment 安全路径与错误映射
// -------------------------------------------------------------------

func TestCreateDepartment_DepartmentCycleReturns400(t *testing.T) {
	svc := &stubDepartmentService{
		createFn: func(ctx context.Context, op *service.EffectiveDataScope, req *service.CreateDepartmentRequest) (*service.DepartmentResponse, error) {
			return nil, service.ErrDepartmentCycle
		},
	}
	app := newDepartmentApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	body := `{"name":"研发部","parent_id":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/departments", bytes.NewReader([]byte(body)))
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
		t.Fatalf("code=%d, want %d (部门循环应400)", r.Code, global.ResponseErrorCode)
	}
}

func TestCreateDepartment_OutOfScopeReturns403(t *testing.T) {
	svc := &stubDepartmentService{
		createFn: func(ctx context.Context, op *service.EffectiveDataScope, req *service.CreateDepartmentRequest) (*service.DepartmentResponse, error) {
			return nil, service.ErrDepartmentOutOfScope
		},
	}
	app := newDepartmentApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	body := `{"name":"研发部","parent_id":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/departments", bytes.NewReader([]byte(body)))
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
	if r.Code != global.ResponseDepartmentOutOfScope {
		t.Fatalf("code=%d, want %d (超出范围应部门越权码)", r.Code, global.ResponseDepartmentOutOfScope)
	}
}

func TestCreateDepartment_InvalidParentReturns400(t *testing.T) {
	svc := &stubDepartmentService{
		createFn: func(ctx context.Context, op *service.EffectiveDataScope, req *service.CreateDepartmentRequest) (*service.DepartmentResponse, error) {
			return nil, service.ErrInvalidDepartmentParent
		},
	}
	app := newDepartmentApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	body := `{"name":"研发部","parent_id":999}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/departments", bytes.NewReader([]byte(body)))
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
		t.Fatalf("code=%d, want %d (无效父部门应400)", r.Code, global.ResponseErrorCode)
	}
}

func TestCreateDepartment_Success(t *testing.T) {
	called := false
	svc := &stubDepartmentService{
		createFn: func(ctx context.Context, op *service.EffectiveDataScope, req *service.CreateDepartmentRequest) (*service.DepartmentResponse, error) {
			called = true
			return &service.DepartmentResponse{ID: 1, Name: req.Name}, nil
		},
	}
	app := newDepartmentApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	body := `{"name":"研发部","parent_id":0}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/departments", bytes.NewReader([]byte(body)))
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
// ListDepartmentTree 成功路径
// -------------------------------------------------------------------

func TestListDepartmentTree_Success(t *testing.T) {
	called := false
	svc := &stubDepartmentService{
		treeInScopeFn: func(ctx context.Context, op *service.EffectiveDataScope, req *service.ListDepartmentsRequest) ([]*service.DepartmentTreeNode, error) {
			called = true
			return []*service.DepartmentTreeNode{{DepartmentResponse: &service.DepartmentResponse{ID: 1, Name: "总部"}}}, nil
		},
	}
	app := newDepartmentApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/departments/tree", nil)
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
		t.Fatal("TreeInScope 应被调用")
	}
}

// -------------------------------------------------------------------
// GetDepartment 安全路径与错误映射
// -------------------------------------------------------------------

func TestGetDepartment_InvalidIDReturns400(t *testing.T) {
	svc := &stubDepartmentService{}
	app := newDepartmentApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/departments/abc", nil)
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

func TestGetDepartment_NotFoundReturns404(t *testing.T) {
	svc := &stubDepartmentService{
		getFn: func(ctx context.Context, op *service.EffectiveDataScope, id int) (*service.DepartmentResponse, error) {
			return nil, service.ErrDepartmentNotFound
		},
	}
	app := newDepartmentApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/departments/999", nil)
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
		t.Fatalf("code=%d, want %d (部门不存在应404)", r.Code, global.ResponseNotFound)
	}
}

func TestGetDepartment_OutOfScopeReturns403(t *testing.T) {
	svc := &stubDepartmentService{
		getFn: func(ctx context.Context, op *service.EffectiveDataScope, id int) (*service.DepartmentResponse, error) {
			return nil, service.ErrDepartmentOutOfScope
		},
	}
	app := newDepartmentApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/departments/1", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var r utils.Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if r.Code != global.ResponseDepartmentOutOfScope {
		t.Fatalf("code=%d, want %d (超出范围应部门越权码)", r.Code, global.ResponseDepartmentOutOfScope)
	}
}

func TestGetDepartment_Success(t *testing.T) {
	svc := &stubDepartmentService{
		getFn: func(ctx context.Context, op *service.EffectiveDataScope, id int) (*service.DepartmentResponse, error) {
			return &service.DepartmentResponse{ID: id, Name: "研发部"}, nil
		},
	}
	app := newDepartmentApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/departments/1", nil)
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
// UpdateDepartment 安全路径与错误映射
// -------------------------------------------------------------------

func TestUpdateDepartment_InvalidIDReturns400(t *testing.T) {
	svc := &stubDepartmentService{}
	app := newDepartmentApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	body := `{"name":"新名称"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/auth/departments/abc", bytes.NewReader([]byte(body)))
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

func TestUpdateDepartment_NotFoundReturns404(t *testing.T) {
	svc := &stubDepartmentService{
		updateFn: func(ctx context.Context, op *service.EffectiveDataScope, id int, req *service.UpdateDepartmentRequest) error {
			return service.ErrDepartmentNotFound
		},
	}
	app := newDepartmentApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	body := `{"name":"新名称"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/auth/departments/999", bytes.NewReader([]byte(body)))
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
		t.Fatalf("code=%d, want %d (部门不存在应404)", r.Code, global.ResponseNotFound)
	}
}

func TestUpdateDepartment_CycleReturns400(t *testing.T) {
	svc := &stubDepartmentService{
		updateFn: func(ctx context.Context, op *service.EffectiveDataScope, id int, req *service.UpdateDepartmentRequest) error {
			return service.ErrDepartmentCycle
		},
	}
	app := newDepartmentApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	body := `{"name":"新名称","parent_id":2}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/auth/departments/1", bytes.NewReader([]byte(body)))
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
		t.Fatalf("code=%d, want %d (部门循环应400)", r.Code, global.ResponseErrorCode)
	}
}

func TestUpdateDepartment_Success(t *testing.T) {
	called := false
	svc := &stubDepartmentService{
		updateFn: func(ctx context.Context, op *service.EffectiveDataScope, id int, req *service.UpdateDepartmentRequest) error {
			called = true
			return nil
		},
	}
	app := newDepartmentApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	body := `{"name":"新名称"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/auth/departments/1", bytes.NewReader([]byte(body)))
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
// DeleteDepartment 安全路径与错误映射
// -------------------------------------------------------------------

func TestDeleteDepartment_HasChildrenReturns409(t *testing.T) {
	svc := &stubDepartmentService{
		deleteFn: func(ctx context.Context, op *service.EffectiveDataScope, id int) error {
			return service.ErrDepartmentHasChildren
		},
	}
	app := newDepartmentApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/auth/departments/1", nil)
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
		t.Fatalf("code=%d, want %d (有子部门应409)", r.Code, global.ResponseConflict)
	}
}

func TestDeleteDepartment_OutOfScopeReturns403(t *testing.T) {
	svc := &stubDepartmentService{
		deleteFn: func(ctx context.Context, op *service.EffectiveDataScope, id int) error {
			return service.ErrDepartmentOutOfScope
		},
	}
	app := newDepartmentApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/auth/departments/1", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var r utils.Response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if r.Code != global.ResponseDepartmentOutOfScope {
		t.Fatalf("code=%d, want %d (超出范围应部门越权码)", r.Code, global.ResponseDepartmentOutOfScope)
	}
}

func TestDeleteDepartment_NotFoundReturns404(t *testing.T) {
	svc := &stubDepartmentService{
		deleteFn: func(ctx context.Context, op *service.EffectiveDataScope, id int) error {
			return service.ErrDepartmentNotFound
		},
	}
	app := newDepartmentApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/auth/departments/999", nil)
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
		t.Fatalf("code=%d, want %d (部门不存在应404)", r.Code, global.ResponseNotFound)
	}
}

func TestDeleteDepartment_Success(t *testing.T) {
	called := false
	svc := &stubDepartmentService{
		deleteFn: func(ctx context.Context, op *service.EffectiveDataScope, id int) error {
			called = true
			return nil
		},
	}
	app := newDepartmentApp(svc, &stubDataPermServiceAlwaysOK{}, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/auth/departments/1", nil)
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
