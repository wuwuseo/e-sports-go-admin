package apps

import (
	"testing"

	"github.com/gofiber/fiber/v3"
	_ "github.com/mattn/go-sqlite3"
	cmfapp "github.com/wuwuseo/cmf/app"
	"github.com/wuwuseo/cmf/config"

	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/ent/systempermission"
	"go-admin/internal/global"
)

func TestProvisionDemoIdempotent(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:source_apps_provision?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer client.Close()
	previous := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = previous })
	cfg := &config.Config{}
	cfg.Casbin.DomainsDefault = "default"
	for run := 0; run < 2; run++ {
		if err := Provision(t.Context(), cfg, client); err != nil {
			t.Fatal(err)
		}
		if run == 0 {
			item := client.SystemConfig.Query().Where(systemconfig.KeyEQ("app.demo.greeting")).OnlyX(t.Context())
			item.Update().SetValue("changed by user").SaveX(t.Context())
		}
	}
	item := client.SystemConfig.Query().Where(systemconfig.KeyEQ("app.demo.greeting")).OnlyX(t.Context())
	if item.Value != "changed by user" {
		t.Fatalf("config value overwritten: %s", item.Value)
	}
	count := client.SystemConfig.Query().Where(systemconfig.KeyEQ("app.demo.greeting")).CountX(t.Context())
	if count != 1 {
		t.Fatalf("config count = %d", count)
	}
	menu := client.SystemPermission.Query().Where(systempermission.CodeEQ("demo:menu")).OnlyX(t.Context())
	for _, code := range []string{"demo:items:list", "demo:items:add"} {
		p := client.SystemPermission.Query().Where(systempermission.CodeEQ(code)).OnlyX(t.Context())
		if p.ParentID != menu.ID || p.Domain != "default" {
			t.Fatalf("permission %s = %+v, menu id %d", code, p, menu.ID)
		}
	}
	if count := client.SystemPermission.Query().Where(systempermission.CodeHasPrefix("demo:")).CountX(t.Context()); count != 3 {
		t.Fatalf("demo permission count = %d", count)
	}
	category := client.DemoCategory.Create().SetName("Group").SaveX(t.Context())
	model := client.DemoItem.Create().SetTitle("Example").SetCategory(category).SaveX(t.Context())
	loaded := client.DemoItem.GetX(t.Context(), model.ID)
	if loaded.QueryCategory().OnlyX(t.Context()).ID != category.ID {
		t.Fatal("generated internal Ent relation did not persist")
	}
}

func TestDisabledFeatureSetSkipsRuntimeState(t *testing.T) {
	cmfapp.Register(cmfapp.App{
		Code: "offdemo", Enabled: false,
		Config: []cmfapp.ConfigItem{{Key: "flag", Name: "Flag", Type: "string"}},
		Routes: []cmfapp.Route{{Method: "GET", Path: "/ping", Auth: cmfapp.AuthPublic}},
	})
	client := enttest.Open(t, "sqlite3", "file:source_apps_disabled?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer client.Close()
	cfg := &config.Config{}
	if err := Provision(t.Context(), cfg, client); err != nil {
		t.Fatal(err)
	}
	if client.SystemConfig.Query().Where(systemconfig.KeyEQ("app.offdemo.flag")).ExistX(t.Context()) {
		t.Fatal("disabled feature config was inserted")
	}
	f := fiber.New()
	if err := Mount(f.Group("/api/v1"), cfg, nil); err != nil {
		t.Fatal(err)
	}
	for _, route := range f.GetRoutes(true) {
		if route.Path == "/api/v1/offdemo/ping" {
			t.Fatal("disabled feature route was mounted")
		}
	}
}
