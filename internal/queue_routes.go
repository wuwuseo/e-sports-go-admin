package internal

import (
	"fmt"
	"net/http"
	"path"
	"strings"

	"go-admin/internal/handler"
	"go-admin/internal/middleware"
	"go-admin/internal/service"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/adaptor"
)

func mountQueueJobs(adminAuth fiber.Router) {
	svc := service.NewQueueJobService()
	jobs := adminAuth.Group("/queue-jobs", middleware.RequireSuperAdmin(), func(c fiber.Ctx) error {
		c.Locals("service.QueueJobService", svc)
		return c.Next()
	})
	jobs.Get("", handler.ListQueueTasks)
	jobs.Get("/stats", handler.QueueStats)
	jobs.Get("/handlers", handler.QueueHandlers)
	jobs.Get("/capabilities", handler.QueueCapabilities)
	jobs.Get("/overview", handler.QueueOverview)
	jobs.Post("", handler.EnqueueQueueTask)
	jobs.Post("/:queue/pause", handler.PauseQueue)
	jobs.Post("/:queue/resume", handler.ResumeQueue)
	jobs.Get("/:queue/messages/:sequence", handler.GetQueueRawMessage)
	jobs.Get("/:queue/:id", handler.GetQueueTask)
	jobs.Post("/:queue/:id/retry", handler.RetryQueueTask)
	jobs.Post("/:queue/:id/cancel", handler.CancelQueueTask)
	jobs.Delete("/:queue/:id", handler.DeleteQueueTask)
}

func mountQueueMonitor(adminAuth fiber.Router, monitorPath string, monitor http.Handler) error {
	const prefix = "/api/v1/admin/auth"
	if !strings.HasPrefix(monitorPath, prefix+"/") || path.Clean(monitorPath) != monitorPath || strings.ContainsAny(monitorPath, ":*?#\\") {
		return fmt.Errorf("queue.monitor_path 必须是 %s 下的完整静态路径", prefix)
	}
	relative := strings.TrimPrefix(monitorPath, prefix)
	handler := adaptor.HTTPHandler(monitor)
	monitorRoutes := adminAuth.Group(relative, middleware.RequireSuperAdmin())
	monitorRoutes.Get("", handler)
	monitorRoutes.Get("/*", handler)
	return nil
}
