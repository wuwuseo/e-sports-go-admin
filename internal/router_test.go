package internal

import (
	"strings"
	"testing"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/global"

	"github.com/gofiber/fiber/v3"
	_ "github.com/mattn/go-sqlite3"
	"github.com/wuwuseo/cmf/config"
)

func TestRegisterRouteRegistersPluginProxyRoutesWithoutProxySegment(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:router_plugin_proxy?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)),
	)
	defer client.Close()
	global.ORM = client

	app := fiber.New()
	cfg := config.NewConfig()
	cfg.App.AdminSecret = "admin-test-secret"
	RegisterRoute(app, cfg)

	routes := app.GetRoutes(true)
	assertRouteRegistered(t, routes, "/api/v1/plugin/:plugin_id/*")
	assertRouteRegistered(t, routes, "/api/v1/plugin/:plugin_id/admin/auth")
	assertRouteRegistered(t, routes, "/api/v1/plugin/:plugin_id/admin/auth/*")
	assertRouteNotRegistered(t, routes, "/proxy")
}

func assertRouteRegistered(t *testing.T, routes []fiber.Route, wantPath string) {
	t.Helper()
	for _, route := range routes {
		if route.Path == wantPath {
			return
		}
	}
	t.Fatalf("route %s is not registered", wantPath)
}

func assertRouteNotRegistered(t *testing.T, routes []fiber.Route, forbidden string) {
	t.Helper()
	for _, route := range routes {
		if strings.Contains(route.Path, forbidden) {
			t.Fatalf("route %s contains forbidden segment %s", route.Path, forbidden)
		}
	}
}
