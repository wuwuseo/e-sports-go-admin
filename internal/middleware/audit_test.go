package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/operationlog"
	"go-admin/internal/global"
	"go-admin/internal/service"

	"github.com/gofiber/fiber/v3"
	jwtlib "github.com/golang-jwt/jwt/v5"
	_ "github.com/mattn/go-sqlite3"
	cmfjwt "github.com/wuwuseo/cmf/jwt"
)

const auditTestSecret = "audit-test-secret-key"

// makeAuditTestToken 构造带操作者身份的 JWT，模拟真实认证链路
func makeAuditTestToken(t *testing.T, uid, username string) string {
	t.Helper()
	token, err := jwtlib.NewWithClaims(jwtlib.SigningMethodHS256, jwtlib.MapClaims{
		"id":      uid,
		"subject": username,
		"domain":  "default",
		"exp":     time.Now().Add(time.Hour).Unix(),
	}).SignedString([]byte(auditTestSecret))
	if err != nil {
		t.Fatalf("CreateToken 失败: %v", err)
	}
	return token
}

func TestAudit_RecordsGroupDenial(t *testing.T) {
	for _, path := range []string{"/api/v1/admin/auth/admins/5/activate", "/API/v1/admin/auth/admins/5/activate/"} {
		t.Run(path, func(t *testing.T) {
			client, svc, cleanup := setupAuditTest(t)
			defer cleanup()
			app := fiber.New()
			auth := app.Group("/api/v1/admin/auth", cmfjwt.NewJWTMiddleware(auditTestSecret), Audit(AdminAuditRules(), svc), func(c fiber.Ctx) error {
				return c.SendStatus(http.StatusForbidden)
			})
			auth.Put("/admins/:id/activate", func(c fiber.Ctx) error {
				t.Error("denied request reached handler")
				return nil
			})
			req := httptest.NewRequest(http.MethodPut, path, nil)
			req.Header.Set("Authorization", "Bearer "+makeAuditTestToken(t, "32", "operator"))
			resp, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			if err := service.ShutdownDefaultOperationLogService(t.Context()); err != nil {
				t.Fatal(err)
			}
			row := client.OperationLog.Query().OnlyX(t.Context())
			if row.StatusCode != http.StatusForbidden || row.Action != "启用" || row.UserID != 32 {
				t.Fatalf("incorrect denied request log: %+v", row)
			}
		})
	}
}

func TestAudit_RecordsReturnedErrorStatus(t *testing.T) {
	client, svc, cleanup := setupAuditTest(t)
	defer cleanup()
	app := fiber.New()
	app.Use(cmfjwt.NewJWTMiddleware(auditTestSecret))
	app.Use(Audit(AdminAuditRules(), svc))
	app.Post("/api/v1/admin/auth/admins", func(c fiber.Ctx) error { return fiber.ErrBadRequest })
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/admins", nil)
	req.Header.Set("Authorization", "Bearer "+makeAuditTestToken(t, "32", "operator"))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := service.ShutdownDefaultOperationLogService(t.Context()); err != nil {
		t.Fatal(err)
	}
	row := client.OperationLog.Query().OnlyX(t.Context())
	if row.StatusCode != resp.StatusCode || row.StatusCode != http.StatusBadRequest {
		t.Fatalf("logged status=%d, response=%d", row.StatusCode, resp.StatusCode)
	}
}

func setupAuditTest(t *testing.T) (*ent.Client, service.OperationLogServiceInterface, func()) {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:audit?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	prevORM := global.ORM
	global.ORM = client

	// 同步写（Workers=0）便于断言；NewOperationLogServiceWithOptions 会注册默认实例
	svc := service.NewOperationLogServiceWithOptions(service.OperationLogOptions{
		QueueSize:       10,
		Workers:         1,
		MaxRetries:      1,
		WriteTimeout:    2 * time.Second,
		EnqueueTimeout:  100 * time.Millisecond,
		MonitorInterval: 0,
	})

	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = svc.Shutdown(ctx)
		global.ORM = prevORM
		client.Close()
	}
	return client, svc, cleanup
}

func waitForOperationLogCount(t *testing.T, client *ent.Client, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		n, err := client.OperationLog.Query().Count(context.Background())
		if err == nil && n == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	n, _ := client.OperationLog.Query().Count(context.Background())
	t.Fatalf("等待 %d 条操作日志超时，当前 %d 条", want, n)
}

func TestAudit_RecordsWhitelistedRoute(t *testing.T) {
	client, svc, cleanup := setupAuditTest(t)
	defer cleanup()

	app := fiber.New()
	app.Use(cmfjwt.NewJWTMiddleware(auditTestSecret))
	app.Use(Audit(AdminAuditRules(), svc))
	app.Post("/api/v1/admin/auth/admins", func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{"ok": true})
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/admins", nil)
	req.Header.Set("Authorization", "Bearer "+makeAuditTestToken(t, "31", "operator"))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	waitForOperationLogCount(t, client, 1)
	row, err := client.OperationLog.Query().Only(context.Background())
	if err != nil {
		t.Fatalf("查询操作日志失败: %v", err)
	}
	if row.Module != "管理员管理" || row.Action != "创建" {
		t.Fatalf("module/action = %s/%s, want 管理员管理/创建", row.Module, row.Action)
	}
	if row.StatusCode != http.StatusOK {
		t.Fatalf("status_code = %d, want 200", row.StatusCode)
	}
	if row.DurationMs < 0 {
		t.Fatalf("duration_ms = %d, should >= 0", row.DurationMs)
	}
	if row.Path != "/api/v1/admin/auth/admins" || row.Method != "POST" {
		t.Fatalf("path/method = %s/%s", row.Path, row.Method)
	}
	if row.UserID != 31 || row.Username != "operator" {
		t.Fatalf("操作者不符: user_id=%d username=%s", row.UserID, row.Username)
	}
}

func TestAudit_SkipsUnlistedRoute(t *testing.T) {
	client, svc, cleanup := setupAuditTest(t)
	defer cleanup()

	app := fiber.New()
	app.Use(Audit(AdminAuditRules(), svc))
	app.Get("/api/v1/admin/auth/attachments", func(c fiber.Ctx) error {
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/attachments", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	time.Sleep(100 * time.Millisecond)
	n, err := client.OperationLog.Query().Count(context.Background())
	if err != nil {
		t.Fatalf("查询操作日志失败: %v", err)
	}
	if n != 0 {
		t.Fatalf("白名单外的路由不应记录操作日志，got %d", n)
	}
}

func TestAudit_RecordsDeniedRequest(t *testing.T) {
	client, svc, cleanup := setupAuditTest(t)
	defer cleanup()

	app := fiber.New()
	app.Use(cmfjwt.NewJWTMiddleware(auditTestSecret))
	app.Use(Audit(AdminAuditRules(), svc))
	app.Put("/api/v1/admin/auth/admins/:id/activate", func(c fiber.Ctx) error {
		return c.SendStatus(http.StatusForbidden)
	})

	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/auth/admins/5/activate", nil)
	req.Header.Set("Authorization", "Bearer "+makeAuditTestToken(t, "32", "operator2"))
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	waitForOperationLogCount(t, client, 1)
	row, err := client.OperationLog.Query().
		Where(operationlog.StatusCodeEQ(http.StatusForbidden)).
		Only(context.Background())
	if err != nil {
		t.Fatalf("应记录 403 拒绝请求的审计日志: %v", err)
	}
	if row.Action != "启用" {
		t.Fatalf("action = %s, want 启用", row.Action)
	}
}

func TestAudit_NilServiceNoop(t *testing.T) {
	app := fiber.New()
	app.Use(Audit(AdminAuditRules(), nil))
	app.Post("/api/v1/admin/auth/admins", func(c fiber.Ctx) error {
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/auth/admins", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("nil 服务时中间件不应影响响应，got %d", resp.StatusCode)
	}
}
