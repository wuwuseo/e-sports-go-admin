package apps

import (
	"testing"

	"github.com/gofiber/fiber/v3"
	_ "github.com/mattn/go-sqlite3"
	cmfapp "github.com/wuwuseo/cmf/app"
	"github.com/wuwuseo/cmf/config"

	"go-admin/internal/ent/dictdata"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/ent/systempermission"
	"go-admin/internal/global"
)

func TestProvisionCMSIdempotent(t *testing.T) {
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
	}
	menu := client.SystemPermission.Query().Where(systempermission.CodeEQ("cms:menu")).OnlyX(t.Context())
	for _, code := range []string{"cms:contents:list", "cms:contents:add", "cms:categories:list"} {
		p := client.SystemPermission.Query().Where(systempermission.CodeEQ(code)).OnlyX(t.Context())
		if p.ParentID != menu.ID || p.Domain != "default" {
			t.Fatalf("permission %s = %+v, menu id %d", code, p, menu.ID)
		}
	}
	if count := client.SystemPermission.Query().Where(systempermission.CodeHasPrefix("cms:")).CountX(t.Context()); count != 14 {
		t.Fatalf("cms permission count = %d", count)
	}
	category := client.CmsCategory.Create().SetName("News").SaveX(t.Context())
	model := client.CmsContent.Create().SetTitle("Example").SetCategoryID(category.ID).SaveX(t.Context())
	loaded := client.CmsContent.GetX(t.Context(), model.ID)
	if loaded.CategoryID != category.ID {
		t.Fatal("generated CMS model did not persist category")
	}
}

func TestProvisionFeatureSetDictSeeds(t *testing.T) {
	cmfapp.Register(cmfapp.App{
		Code: "dictdemo", Name: "Dict Demo", Enabled: true,
		Dicts: []cmfapp.DictItem{
			{Type: "dictdemo_status", Label: "启用", Value: "1", Sort: 1, IsDefault: true, CSSClass: "success", Remark: "默认启用"},
			{Type: "dictdemo_status", Label: "禁用", Value: "0", Sort: 2},
		},
	})
	client := enttest.Open(t, "sqlite3", "file:source_apps_dict?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer client.Close()
	cfg := &config.Config{}
	cfg.Casbin.DomainsDefault = "default"
	for run := 0; run < 2; run++ {
		if err := Provision(t.Context(), cfg, client); err != nil {
			t.Fatal(err)
		}
	}
	items := client.DictData.Query().
		Where(dictdata.DictTypeEQ("dictdemo_status")).
		Order(dictdata.BySort()).
		AllX(t.Context())
	if len(items) != 2 {
		t.Fatalf("dict item count = %d", len(items))
	}
	if items[0].Label != "启用" || !items[0].IsDefault || items[0].CSSClass != "success" || items[0].Remark != "默认启用" || items[0].Status != 1 {
		t.Fatalf("first dict item = %+v", items[0])
	}
	client.DictData.UpdateOne(items[0]).SetLabel("用户改过的标签").SaveX(t.Context())
	if err := Provision(t.Context(), cfg, client); err != nil {
		t.Fatal(err)
	}
	if got := client.DictData.GetX(t.Context(), items[0].ID); got.Label != "用户改过的标签" || got.Sort != 1 {
		t.Fatalf("existing dict item was overwritten: %+v", got)
	}
	if count := client.DictData.Query().Where(dictdata.DictTypeEQ("dictdemo_status")).CountX(t.Context()); count != 2 {
		t.Fatalf("dict item count after reseed = %d", count)
	}
}

func TestDisabledFeatureSetSkipsRuntimeState(t *testing.T) {
	cmfapp.Register(cmfapp.App{
		Code: "offdemo", Enabled: false,
		Config: []cmfapp.ConfigItem{{Key: "flag", Name: "Flag", Type: "string"}},
		Dicts:  []cmfapp.DictItem{{Type: "offdemo_status", Label: "启用", Value: "1"}},
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
	if client.DictData.Query().Where(dictdata.DictTypeEQ("offdemo_status")).ExistX(t.Context()) {
		t.Fatal("disabled feature dict was inserted")
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
