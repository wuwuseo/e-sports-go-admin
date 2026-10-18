package apps

import (
	"testing"

	"github.com/gofiber/fiber/v3"
	_ "github.com/mattn/go-sqlite3"
	cmfapp "github.com/wuwuseo/cmf/app"
	"github.com/wuwuseo/cmf/config"

	"go-admin/internal/apps/esports"
	"go-admin/internal/ent/cronjob"
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

// TestEsportsAppDeclarationsValidate 保证 esports 功能集的每条管理端路由
// 都能在权限声明中找到同方法、同规范化路径的 PermAPI；该检查不依赖
// ESPORTS_EXPERIMENTAL 环境变量，避免 CI 在开关关闭时漏掉声明回归。
func TestEsportsAppDeclarationsValidate(t *testing.T) {
	registered := cmfapp.Registered()
	enabled := make([]cmfapp.App, 0, len(registered))
	found := false
	for _, app := range registered {
		if app.Code == "esports" {
			app.Enabled = true
			found = true
		}
		enabled = append(enabled, app)
	}
	if !found {
		t.Fatal("esports feature set is not registered")
	}
	if err := cmfapp.ValidateApps(enabled, options()); err != nil {
		t.Fatalf("esports declarations invalid: %v", err)
	}
}

// 功能集启用时必须为新库播种定时任务：任务函数注册在 Go 侧，但没有 enabled 的 CronJob 行
// 就不会被调度器加载（订单超时释放、结算生成、保护期降级、账本核验、外盒/通知投递等永不执行）。
func TestProvisionSeedsEsportsCronJobs(t *testing.T) {
	enabled := false
	for _, app := range cmfapp.Registered() {
		if app.Code == "esports" {
			enabled = app.Enabled
		}
	}
	if !enabled {
		t.Skip("功能集未启用（需 ESPORTS_EXPERIMENTAL=1）：此时既不注册任务，也不应播种定时任务")
	}
	client := enttest.Open(t, "sqlite3", "file:esports_cron_seed?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer client.Close()
	previous := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = previous })
	cfg := &config.Config{}
	cfg.Casbin.DomainsDefault = "default"
	for run := 0; run < 2; run++ { // 两次 Provision 验证幂等（不重复建行）
		if err := Provision(t.Context(), cfg, client); err != nil {
			t.Fatal(err)
		}
	}
	seeds := esports.CronSeeds()
	if got := client.CronJob.Query().Where(cronjob.NameHasPrefix("esports_")).CountX(t.Context()); got != len(seeds) {
		t.Fatalf("应播种 %d 个定时任务，实际 %d", len(seeds), got)
	}
	for _, seed := range seeds {
		row := client.CronJob.Query().Where(cronjob.NameEQ(seed.Name)).OnlyX(t.Context())
		if !row.Enabled || row.Type != cronjob.TypeTask || row.Command != seed.Name ||
			row.Expression != seed.Expression || row.Description != seed.Description {
			t.Fatalf("定时任务播种不正确：%+v", row)
		}
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
