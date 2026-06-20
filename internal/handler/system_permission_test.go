package handler

import (
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

// -------------------------------------------------------------------
// systemPermissionDomain 边界：locals 中无 domain / domain 为空字符串
//
// 这是 75f9e0b "filter system permissions by current roles" 提交后
// 新增的鉴权防御点：缺失/空域必须返回 (_, false)，调用方据此返回 401。
// 防止在未配置 domain 的中间件链路下被误判为合法域从而注入跨域权限。
// -------------------------------------------------------------------

func TestSystemPermissionDomain_MissingLocalReturnsFalse(t *testing.T) {
	app := fiber.New()
	var got string
	var ok bool

	app.Get("/system-permissions", func(c fiber.Ctx) error {
		got, ok = systemPermissionDomain(c)
		return c.SendStatus(fiber.StatusNoContent)
	})

	resp, err := app.Test(httptest.NewRequest(
		fiber.MethodGet, "/system-permissions", nil,
	))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusNoContent)
	}
	if ok {
		t.Fatal("locals 中没有 domain 时应返回 ok=false")
	}
	if got != "" {
		t.Fatalf("locals 中没有 domain 时应返回空串，got %q", got)
	}
}

func TestSystemPermissionDomain_EmptyStringReturnsFalse(t *testing.T) {
	app := fiber.New()
	var got string
	var ok bool

	app.Get("/system-permissions", func(c fiber.Ctx) error {
		c.Locals("domain", "")
		got, ok = systemPermissionDomain(c)
		return c.SendStatus(fiber.StatusNoContent)
	})

	resp, err := app.Test(httptest.NewRequest(
		fiber.MethodGet, "/system-permissions?domain=attempt-inject", nil,
	))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusNoContent)
	}
	if ok {
		t.Fatal("locals 中 domain 为空字符串时应返回 ok=false，避免跨域注入")
	}
	if got != "" {
		t.Fatalf("空 domain 时应返回空串，got %q", got)
	}
}

func TestSystemPermissionDomain_NonStringLocalReturnsFalse(t *testing.T) {
	app := fiber.New()
	var got string
	var ok bool

	app.Get("/system-permissions", func(c fiber.Ctx) error {
		// 模拟中间件链路上把 domain 错误地放成 int / nil 等其它类型
		c.Locals("domain", 42)
		got, ok = systemPermissionDomain(c)
		return c.SendStatus(fiber.StatusNoContent)
	})

	resp, err := app.Test(httptest.NewRequest(
		fiber.MethodGet, "/system-permissions", nil,
	))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if ok {
		t.Fatal("locals 中 domain 类型不是 string 时应返回 ok=false")
	}
	if got != "" {
		t.Fatalf("locals 中 domain 类型不是 string 时应返回空串，got %q", got)
	}
}

// -------------------------------------------------------------------
// ListSystemPermissions / GetSystemPermissionTree 鉴权与错误路径测试
//
// 75f9e0b 提交让这两个接口从"任意传入 domain"切换到"必须从已认证 locals
// 中取 domain"，并把 ErrPermissionOperatorRequired 映射为 401。下列测试
// 锁定该安全门禁的每一条分支，防止未来重构漏掉任一鉴权点。
// -------------------------------------------------------------------

// stubSystemPermissionService 用于 List/GetTree 错误路径的轻量桩。
// 只实现与目标测试相关的 2 个方法，其余 panic 以便发现未预期的调用。
type stubSystemPermissionService struct {
	listFn        func(ctx context.Context, operatorUID string, req *service.ListSystemPermissionsRequest) (*service.ListSystemPermissionsResponse, error)
	getTreeFn     func(ctx context.Context, operatorUID string, domain string) ([]*service.SystemPermissionTreeNode, error)
	calledList    bool
	calledGetTree bool
	gotUID        string
	gotDomain     string
}

func (s *stubSystemPermissionService) Create(ctx context.Context, req *service.CreateSystemPermissionRequest) (*service.SystemPermissionResponse, error) {
	panic("Create 不应被调用")
}

func (s *stubSystemPermissionService) Update(ctx context.Context, id int, req *service.UpdateSystemPermissionRequest) error {
	panic("Update 不应被调用")
}

func (s *stubSystemPermissionService) Delete(ctx context.Context, id int) error {
	panic("Delete 不应被调用")
}

func (s *stubSystemPermissionService) GetByID(ctx context.Context, id int) (*service.SystemPermissionResponse, error) {
	panic("GetByID 不应被调用")
}

func (s *stubSystemPermissionService) List(ctx context.Context, operatorUID string, req *service.ListSystemPermissionsRequest) (*service.ListSystemPermissionsResponse, error) {
	s.calledList = true
	s.gotUID = operatorUID
	s.gotDomain = req.Domain
	if s.listFn != nil {
		return s.listFn(ctx, operatorUID, req)
	}
	return &service.ListSystemPermissionsResponse{}, nil
}

func (s *stubSystemPermissionService) GetTree(ctx context.Context, operatorUID string, domain string) ([]*service.SystemPermissionTreeNode, error) {
	s.calledGetTree = true
	s.gotUID = operatorUID
	s.gotDomain = domain
	if s.getTreeFn != nil {
		return s.getTreeFn(ctx, operatorUID, domain)
	}
	return nil, nil
}

func (s *stubSystemPermissionService) InitSeedPermissions(ctx context.Context, seeds []service.SeedPermission) error {
	panic("InitSeedPermissions 不应被调用")
}

// 验证实现满足接口，防止未来字段变更后该桩悄悄失效
var _ service.SystemPermissionServiceInterface = (*stubSystemPermissionService)(nil)

// newSystemPermissionAuthApp 构造一个最小可用的 handler 容器；
// authLocals 允许测试用例按需在中间件中注入 domain/uid 模拟不同鉴权状态。
func newSystemPermissionAuthApp(svc service.SystemPermissionServiceInterface, authLocals func(c fiber.Ctx)) *fiber.App {
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("utils.Response", utils.NewResponse())
		c.Locals("service.SystemPermissionService", svc)
		if authLocals != nil {
			authLocals(c)
		}
		return c.Next()
	})
	app.Get("/api/v1/admin/auth/system-permissions", ListSystemPermissions)
	app.Get("/api/v1/admin/auth/system-permissions/tree", GetSystemPermissionTree)
	return app
}

func decodeHandlerResponse(t *testing.T, resp *http.Response) utils.Response {
	t.Helper()
	defer resp.Body.Close()
	var body utils.Response
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}

func TestListSystemPermissions_MissingDomainReturns401(t *testing.T) {
	svc := &stubSystemPermissionService{}
	// 注意：authLocals 为 nil，locals 中无 domain 也不显式注入 uid
	app := newSystemPermissionAuthApp(svc, nil)

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/system-permissions", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	body := decodeHandlerResponse(t, resp)
	if body.Code != global.ResponseUnauthorized {
		t.Fatalf("code = %d, want %d (缺 domain 应 401)", body.Code, global.ResponseUnauthorized)
	}
	if svc.calledList {
		t.Fatal("缺 domain 时 svc.List 不应被调用")
	}
}

func TestListSystemPermissions_EmptyDomainReturns401(t *testing.T) {
	svc := &stubSystemPermissionService{}
	// 显式将 domain 设为空串 —— 模拟中间件 buggy 地把空字符串放进了 locals
	app := newSystemPermissionAuthApp(svc, func(c fiber.Ctx) {
		c.Locals("domain", "")
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/system-permissions", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	body := decodeHandlerResponse(t, resp)
	if body.Code != global.ResponseUnauthorized {
		t.Fatalf("code = %d, want %d (空 domain 应 401)", body.Code, global.ResponseUnauthorized)
	}
	if svc.calledList {
		t.Fatal("空 domain 时 svc.List 不应被调用")
	}
}

func TestListSystemPermissions_MissingUIDReturns401(t *testing.T) {
	svc := &stubSystemPermissionService{}
	app := newSystemPermissionAuthApp(svc, func(c fiber.Ctx) {
		// 只设置 domain，不设置 uid
		c.Locals("domain", "default")
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/system-permissions", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	body := decodeHandlerResponse(t, resp)
	if body.Code != global.ResponseUnauthorized {
		t.Fatalf("code = %d, want %d (uid 缺失应 401)", body.Code, global.ResponseUnauthorized)
	}
	if svc.calledList {
		t.Fatal("uid 缺失时 svc.List 不应被调用")
	}
}

func TestListSystemPermissions_EmptyUIDReturns401(t *testing.T) {
	svc := &stubSystemPermissionService{}
	app := newSystemPermissionAuthApp(svc, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "")
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/system-permissions", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	body := decodeHandlerResponse(t, resp)
	if body.Code != global.ResponseUnauthorized {
		t.Fatalf("code = %d, want %d (空 uid 应 401)", body.Code, global.ResponseUnauthorized)
	}
	if svc.calledList {
		t.Fatal("空 uid 时 svc.List 不应被调用")
	}
}

func TestListSystemPermissions_OperatorRequiredMapsTo401(t *testing.T) {
	svc := &stubSystemPermissionService{
		listFn: func(ctx context.Context, operatorUID string, req *service.ListSystemPermissionsRequest) (*service.ListSystemPermissionsResponse, error) {
			return nil, service.ErrPermissionOperatorRequired
		},
	}
	app := newSystemPermissionAuthApp(svc, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/system-permissions", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	body := decodeHandlerResponse(t, resp)
	if body.Code != global.ResponseUnauthorized {
		t.Fatalf("code = %d, want %d (ErrPermissionOperatorRequired 应映射为 401)", body.Code, global.ResponseUnauthorized)
	}
	if !svc.calledList {
		t.Fatal("domain/uid 完整时应进入 svc.List")
	}
}

func TestListSystemPermissions_GenericErrorMapsTo500(t *testing.T) {
	svc := &stubSystemPermissionService{
		listFn: func(ctx context.Context, operatorUID string, req *service.ListSystemPermissionsRequest) (*service.ListSystemPermissionsResponse, error) {
			return nil, errors.New("database down")
		},
	}
	app := newSystemPermissionAuthApp(svc, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/system-permissions", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	body := decodeHandlerResponse(t, resp)
	if body.Code != global.ResponseInternalServerError {
		t.Fatalf("code = %d, want %d (非 OperatorRequired 错误应映射为 500)", body.Code, global.ResponseInternalServerError)
	}
}

func TestListSystemPermissions_SuccessPassesAuthedDomain(t *testing.T) {
	svc := &stubSystemPermissionService{
		listFn: func(ctx context.Context, operatorUID string, req *service.ListSystemPermissionsRequest) (*service.ListSystemPermissionsResponse, error) {
			return &service.ListSystemPermissionsResponse{}, nil
		},
	}
	app := newSystemPermissionAuthApp(svc, func(c fiber.Ctx) {
		c.Locals("domain", "tenant-x")
		c.Locals("uid", "uid-operator")
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/system-permissions?domain=attempt-inject", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	body := decodeHandlerResponse(t, resp)
	if body.Code != global.ResponseSuccessCode {
		t.Fatalf("code = %d, want %d (成功路径应返回 1)", body.Code, global.ResponseSuccessCode)
	}
	if !svc.calledList {
		t.Fatal("合法请求应进入 svc.List")
	}
	if svc.gotDomain != "tenant-x" {
		t.Fatalf("domain 应被改写为已认证域 tenant-x，got %q", svc.gotDomain)
	}
	if svc.gotUID != "uid-operator" {
		t.Fatalf("uid 应为 uid-operator，got %q", svc.gotUID)
	}
}

func TestGetSystemPermissionTree_MissingDomainReturns401(t *testing.T) {
	svc := &stubSystemPermissionService{}
	app := newSystemPermissionAuthApp(svc, nil)

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/system-permissions/tree", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	body := decodeHandlerResponse(t, resp)
	if body.Code != global.ResponseUnauthorized {
		t.Fatalf("code = %d, want %d (GetTree 缺 domain 应 401)", body.Code, global.ResponseUnauthorized)
	}
	if svc.calledGetTree {
		t.Fatal("缺 domain 时 svc.GetTree 不应被调用")
	}
}

func TestGetSystemPermissionTree_EmptyDomainReturns401(t *testing.T) {
	svc := &stubSystemPermissionService{}
	app := newSystemPermissionAuthApp(svc, func(c fiber.Ctx) {
		c.Locals("domain", "")
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/system-permissions/tree", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	body := decodeHandlerResponse(t, resp)
	if body.Code != global.ResponseUnauthorized {
		t.Fatalf("code = %d, want %d (GetTree 空 domain 应 401)", body.Code, global.ResponseUnauthorized)
	}
	if svc.calledGetTree {
		t.Fatal("空 domain 时 svc.GetTree 不应被调用")
	}
}

func TestGetSystemPermissionTree_MissingUIDReturns401(t *testing.T) {
	svc := &stubSystemPermissionService{}
	app := newSystemPermissionAuthApp(svc, func(c fiber.Ctx) {
		c.Locals("domain", "default")
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/system-permissions/tree", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	body := decodeHandlerResponse(t, resp)
	if body.Code != global.ResponseUnauthorized {
		t.Fatalf("code = %d, want %d (GetTree 缺 uid 应 401)", body.Code, global.ResponseUnauthorized)
	}
	if svc.calledGetTree {
		t.Fatal("缺 uid 时 svc.GetTree 不应被调用")
	}
}

func TestGetSystemPermissionTree_OperatorRequiredMapsTo401(t *testing.T) {
	svc := &stubSystemPermissionService{
		getTreeFn: func(ctx context.Context, operatorUID string, domain string) ([]*service.SystemPermissionTreeNode, error) {
			return nil, service.ErrPermissionOperatorRequired
		},
	}
	app := newSystemPermissionAuthApp(svc, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/system-permissions/tree", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	body := decodeHandlerResponse(t, resp)
	if body.Code != global.ResponseUnauthorized {
		t.Fatalf("code = %d, want %d (GetTree 收到 ErrPermissionOperatorRequired 应 401)", body.Code, global.ResponseUnauthorized)
	}
}

func TestGetSystemPermissionTree_GenericErrorMapsTo500(t *testing.T) {
	svc := &stubSystemPermissionService{
		getTreeFn: func(ctx context.Context, operatorUID string, domain string) ([]*service.SystemPermissionTreeNode, error) {
			return nil, errors.New("db error")
		},
	}
	app := newSystemPermissionAuthApp(svc, func(c fiber.Ctx) {
		c.Locals("domain", "default")
		c.Locals("uid", "uid-1")
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/system-permissions/tree", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	body := decodeHandlerResponse(t, resp)
	if body.Code != global.ResponseInternalServerError {
		t.Fatalf("code = %d, want %d (GetTree 非 OperatorRequired 应 500)", body.Code, global.ResponseInternalServerError)
	}
}

func TestGetSystemPermissionTree_SuccessPassesAuthedDomain(t *testing.T) {
	svc := &stubSystemPermissionService{
		getTreeFn: func(ctx context.Context, operatorUID string, domain string) ([]*service.SystemPermissionTreeNode, error) {
			return nil, nil
		},
	}
	app := newSystemPermissionAuthApp(svc, func(c fiber.Ctx) {
		c.Locals("domain", "tenant-y")
		c.Locals("uid", "uid-operator-tree")
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/system-permissions/tree?domain=attempt-inject", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	body := decodeHandlerResponse(t, resp)
	if body.Code != global.ResponseSuccessCode {
		t.Fatalf("code = %d, want %d (GetTree 成功路径应 1)", body.Code, global.ResponseSuccessCode)
	}
	if svc.gotDomain != "tenant-y" {
		t.Fatalf("GetTree 应使用已认证域 tenant-y，got %q", svc.gotDomain)
	}
	if svc.gotUID != "uid-operator-tree" {
		t.Fatalf("GetTree uid 应为 uid-operator-tree，got %q", svc.gotUID)
	}
}
