package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"go-admin/internal/global"
	"go-admin/internal/service"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	cmfqueue "github.com/wuwuseo/cmf/queue"
)

func TestQueueAdmissionErrorsHaveBusinessCodes(t *testing.T) {
	for _, tt := range []struct {
		err  error
		code int
	}{
		{cmfqueue.ErrQueueFull, global.ErrCodeQueueFull},
		{cmfqueue.ErrPayloadTooLarge, global.ErrCodeQueuePayloadTooLarge},
		{service.ErrQueueInvalidQueueName, global.ErrCodeQueueInvalidName},
		{cmfqueue.ErrUnsupportedCapability, global.ErrCodeQueueUnsupported},
		{cmfqueue.ErrMessageNotRetained, global.ErrCodeQueueMessageNotRetained},
	} {
		app := fiber.New()
		app.Get("/error", func(c fiber.Ctx) error { return respondQueueError(utils.NewResponse(), c, tt.err) })
		resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/error", nil))
		if err != nil {
			t.Fatal(err)
		}
		var body utils.Response
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil || body.Code != tt.code {
			t.Fatalf("error=%v code=%d want=%d decode=%v", tt.err, body.Code, tt.code, err)
		}
	}
}

// queueJobProdErrorHandler 复刻生产 cmf/bootstrap 的 ErrorHandler 关键行为：
// 对 *fiber.Error 类型走 errors.As 后读取 e.Code。
// 若 handler 返回 typed-nil *fiber.Error，此处将解引用 nil 而 panic。
func queueJobProdErrorHandler(c fiber.Ctx, err error) error {
	var e *fiber.Error
	if errors.As(err, &e) {
		_ = e.Code
	}
	return c.SendStatus(fiber.StatusInternalServerError)
}

// TestRespondQueueErrorSendsJSONAndReturnsNilError 锁定缺陷行为：
// respondQueueError 必须发送 JSON 响应并返回 ToJSON 的结果（nil）。
// 修复前它返回 typed-nil *fiber.Error--接口非 nil，Fiber 会将其送入
// ErrorHandler 并在 e.Code 处 panic（进程崩溃）。
func TestRespondQueueErrorSendsJSONAndReturnsNilError(t *testing.T) {
	app := fiber.New()
	var captured error
	app.Get("/queue-error", func(c fiber.Ctx) error {
		c.Locals("utils.Response", utils.NewResponse())
		captured = respondQueueError(c.Locals("utils.Response").(*utils.Response), c, service.ErrQueueUnavailable)
		return captured
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/queue-error", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if captured != nil {
		t.Fatalf("respondQueueError 应返回 nil（响应已发送），实际 %#v；typed-nil 会让生产 ErrorHandler panic", captured)
	}
	var payload utils.Response
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("错误路径应返回 JSON 响应: %v, body=%s", err, body)
	}
	if payload.Code != global.ErrCodeQueueUnavailable {
		t.Fatalf("错误码期望 %d，实际 %d（body=%s）", global.ErrCodeQueueUnavailable, payload.Code, body)
	}
}

// TestQueueStatsUnavailableReturnsJSONNotPanic 端到端回归：
// 队列未初始化时 QueueStats 的错误路径必须返回业务 JSON（4105），
// 而非触发 ErrorHandler panic / 500。
func TestQueueStatsUnavailableReturnsJSONNotPanic(t *testing.T) {
	saved := global.QueueRuntime.Swap(nil)
	t.Cleanup(func() { global.QueueRuntime.Store(saved) })

	app := fiber.New(fiber.Config{ErrorHandler: queueJobProdErrorHandler})
	app.Get("/api/v1/admin/auth/queue-jobs/stats", func(c fiber.Ctx) error {
		c.Locals("utils.Response", utils.NewResponse())
		c.Locals("service.QueueJobService", service.NewQueueJobService())
		return QueueStats(c)
	})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/admin/auth/queue-jobs/stats", nil))
	if err != nil {
		t.Fatalf("app.Test 失败（修复前 typed-nil 会让 ErrorHandler panic）: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("错误路径应返回 200 + 业务错误码，实际 HTTP %d, body=%s", resp.StatusCode, body)
	}
	var payload utils.Response
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("响应不是合法 JSON: %v, body=%s", err, body)
	}
	if payload.Code != global.ErrCodeQueueUnavailable {
		t.Fatalf("错误码期望 %d，实际 %d（body=%s）", global.ErrCodeQueueUnavailable, payload.Code, body)
	}
}
