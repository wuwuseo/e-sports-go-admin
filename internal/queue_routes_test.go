package internal

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
)

func TestQueueRoutesRejectNonSuperAdmin(t *testing.T) {
	app := fiber.New()
	mountQueueJobs(app.Group("/api/v1/admin/auth"))
	for _, route := range []struct{ method, path string }{
		{"GET", ""}, {"GET", "/stats"}, {"GET", "/handlers"}, {"POST", ""},
		{"GET", "/default/task"}, {"POST", "/default/task/retry"}, {"POST", "/default/task/cancel"}, {"DELETE", "/default/task"},
		{"GET", "/capabilities"}, {"GET", "/overview"},
		{"POST", "/default/pause"}, {"POST", "/default/resume"}, {"GET", "/default/messages/1"},
	} {
		resp, err := app.Test(httptest.NewRequest(route.method, "/api/v1/admin/auth/queue-jobs"+route.path, nil))
		if err != nil {
			t.Fatal(err)
		}
		var body utils.Response
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if body.Code != 403 {
			t.Errorf("%s %s passed super-admin gate: %+v", route.method, route.path, body)
		}
	}
}

func TestQueueMonitorRootAssetsAndAuthorization(t *testing.T) {
	const root = "/api/v1/admin/auth/custom-queue-monitor"
	for _, super := range []bool{false, true} {
		app := fiber.New()
		auth := app.Group("/api/v1/admin/auth", func(c fiber.Ctx) error { c.Locals("isSuperAdmin", super); return c.Next() })
		monitor := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "monitor:"+r.URL.Path) })
		if err := mountQueueMonitor(auth, root, monitor); err != nil {
			t.Fatal(err)
		}
		for _, suffix := range []string{"", "/", "/static/main.js", "/api/queues"} {
			resp, err := app.Test(httptest.NewRequest(http.MethodGet, root+suffix, nil))
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(string(body), "monitor:") != super {
				t.Errorf("super=%v suffix=%q body=%s", super, suffix, body)
			}
		}
		resp, err := app.Test(httptest.NewRequest(http.MethodPost, root+"/api/queues", nil))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.HasPrefix(string(body), "monitor:") {
			t.Fatal("monitor accepted mutation request")
		}
	}
}

func TestQueueMonitorRejectsUnsafeMountPaths(t *testing.T) {
	for _, path := range []string{"", "/public/monitor", "/api/v1/admin/auth", "/api/v1/admin/auth/../monitor", "/api/v1/admin/auth/monitor/*"} {
		app := fiber.New()
		if err := mountQueueMonitor(app.Group("/api/v1/admin/auth"), path, http.NotFoundHandler()); err == nil {
			t.Errorf("accepted unsafe path %q", path)
		}
	}
}
