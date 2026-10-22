package apps

import (
	"encoding/hex"
	"testing"

	"github.com/gofiber/fiber/v3"
	_ "github.com/mattn/go-sqlite3"
	cmfapp "github.com/wuwuseo/cmf/app"
	"github.com/wuwuseo/cmf/config"

	"go-admin/internal/apps/esports"
	"go-admin/internal/ent/casbinrule"
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

func TestSeedPagePreviewSecret(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:page_preview_secret_seed?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer client.Close()
	const key = "app.esports.page_preview_secret"
	item := cmfapp.ConfigItem{Key: "page_preview_secret", Name: "页面预览签名密钥", Type: "string", Group: "esports"}
	seed := func() string {
		t.Helper()
		if err := seedPagePreviewSecret(t.Context(), client, key, item); err != nil {
			t.Fatal(err)
		}
		return client.SystemConfig.Query().Where(systemconfig.KeyEQ(key)).OnlyX(t.Context()).Value
	}
	checkRandom := func(value string) {
		t.Helper()
		decoded, err := hex.DecodeString(value)
		if err != nil || len(decoded) != 32 {
			t.Fatalf("预览密钥应是 32 字节随机值的 hex 编码，长度=%d，解码错误=%v", len(decoded), err)
		}
	}
	first := seed()
	checkRandom(first)
	if got := seed(); got != first {
		t.Fatal("重复播种不应轮换已有密钥")
	}
	client.SystemConfig.Update().Where(systemconfig.KeyEQ(key)).SetValue("custom-secret").ExecX(t.Context())
	if got := seed(); got != "custom-secret" {
		t.Fatal("重复播种不应覆盖管理员配置的密钥")
	}
	client.SystemConfig.Update().Where(systemconfig.KeyEQ(key)).SetValue(" \t ").ExecX(t.Context())
	backfilled := seed()
	checkRandom(backfilled)
	if backfilled == first {
		t.Fatal("历史空值应生成新的随机密钥")
	}
	if count := client.SystemConfig.Query().Where(systemconfig.KeyEQ(key)).CountX(t.Context()); count != 1 {
		t.Fatalf("预览密钥配置行数=%d，期望 1", count)
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

// 功能集启用时必须为新库播种会员等级：等级表为空会让客户永远无法定级，
// 折扣、优先派单、储值卡赠送比例、专属客服等**按等级生效的权益全部静默失效**（审计 D14）。
// 该测试锁定「Provision → 五档等级存在」，而不是只测 esports 包内的播种函数本身。
func TestProvisionSeedsEsportsMemberLevels(t *testing.T) {
	enabled := false
	for _, app := range cmfapp.Registered() {
		if app.Code == "esports" {
			enabled = app.Enabled
		}
	}
	if !enabled {
		t.Skip("功能集未启用（需 ESPORTS_EXPERIMENTAL=1）：此时不应播种会员等级")
	}
	client := enttest.Open(t, "sqlite3", "file:esports_member_level_seed?mode=memory&cache=shared&_fk=1",
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

	levels := client.EsportsMemberLevelRule.Query().AllX(t.Context())
	got := make(map[string]bool, len(levels))
	for _, level := range levels {
		got[level.Code] = true
	}
	if len(levels) != 5 {
		t.Fatalf("应播种 5 档会员等级，实际 %d 档：%v", len(levels), got)
	}
	for _, code := range []string{"normal", "silver", "gold", "platinum", "diamond"} {
		if !got[code] {
			t.Fatalf("缺少会员等级 %q，实际：%v", code, got)
		}
	}
}

func TestProvisionSeedsEsportsRechargeCampaign(t *testing.T) {
	enabled := false
	for _, app := range cmfapp.Registered() {
		if app.Code == "esports" {
			enabled = app.Enabled
		}
	}
	if !enabled {
		t.Skip("功能集未启用（需 ESPORTS_EXPERIMENTAL=1）：此时不应播种充值活动")
	}
	client := enttest.Open(t, "sqlite3", "file:esports_campaign_seed_provision?mode=memory&cache=shared&_fk=1",
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
	if count := client.EsportsRechargeCampaign.Query().CountX(t.Context()); count != 1 {
		t.Fatalf("Provision 两次后应有一条充值活动，实际 %d", count)
	}
}

// 菜单合并后的存量同步：Provision 需要删除已退役的「客户首页」权限行，
// 并把角色授权从旧目录路径 /esports/pages 平移到新菜单路径 /esports/pages/home。
func TestProvisionRetiresMergedPageMenu(t *testing.T) {
	enabled := false
	for _, app := range cmfapp.Registered() {
		if app.Code == "esports" {
			enabled = app.Enabled
		}
	}
	if !enabled {
		t.Skip("功能集未启用（需 ESPORTS_EXPERIMENTAL=1）：此时不会同步功能集权限")
	}
	client := enttest.Open(t, "sqlite3", "file:esports_page_menu_sync?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer client.Close()
	previous := global.ORM
	global.ORM = client
	t.Cleanup(func() { global.ORM = previous })
	cfg := &config.Config{}
	cfg.Casbin.DomainsDefault = "default"
	if err := Provision(t.Context(), cfg, client); err != nil {
		t.Fatal(err)
	}

	operations := client.SystemPermission.Query().Where(systempermission.CodeEQ("esports:operations:menu")).OnlyX(t.Context())
	pageMenu := client.SystemPermission.Query().Where(systempermission.CodeEQ("esports:page:menu")).OnlyX(t.Context())
	if pageMenu.MenuPath != "/esports/pages/home" || pageMenu.Component != "esports/pages/index" || pageMenu.ParentID != operations.ID {
		t.Fatalf("页面装修菜单未同步为单条菜单：%+v", pageMenu)
	}

	// 模拟存量数据：旧子菜单行 + 指向旧目录路径的角色菜单授权。
	client.SystemPermission.Create().
		SetDomain("default").SetType("menu").SetName("客户首页").
		SetCode("esports:pages:home_menu").SetMenuPath("/esports/pages/home").
		SetParentID(pageMenu.ID).SetStatus(1).SaveX(t.Context())
	client.CasbinRule.Create().
		SetPtype("p").SetV0("cs_readonly").SetV1("default").
		SetV2("/esports/pages").SetV3("menu").SetV4("menu").SaveX(t.Context())

	if err := Provision(t.Context(), cfg, client); err != nil {
		t.Fatal(err)
	}
	if client.SystemPermission.Query().Where(systempermission.CodeEQ("esports:pages:home_menu")).ExistX(t.Context()) {
		t.Fatal("退役的「客户首页」权限行应被清理")
	}
	if client.CasbinRule.Query().Where(casbinrule.V2EQ("/esports/pages"), casbinrule.V4EQ("menu")).ExistX(t.Context()) {
		t.Fatal("旧目录路径的角色菜单授权应被迁移")
	}
	if got := client.CasbinRule.Query().Where(casbinrule.V2EQ("/esports/pages/home"), casbinrule.V4EQ("menu")).CountX(t.Context()); got != 1 {
		t.Fatalf("新菜单路径的角色授权应为 1 条，实际 %d", got)
	}
}

// 旧商品接口改名时保留角色授权；已授予新路径的角色不能插入重复规则。
func TestRetireSkuPermissionsMigratesRoleGrants(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:esports_product_grants?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
	defer client.Close()
	ctx := t.Context()
	for _, item := range []struct{ path, method, kind string }{
		{"/api/v1/esports/admin/skus", "GET", "api"},
		{"/api/v1/esports/admin/skus", "POST", "api"},
		{"/api/v1/esports/admin/products", "POST", "api"},
		{"esports:skus:add", "button", "button"},
	} {
		client.CasbinRule.Create().SetPtype("p").SetV0("product_manager").SetV1("default").
			SetV2(item.path).SetV3(item.method).SetV4(item.kind).SaveX(ctx)
	}
	if err := retireAppPermissions(ctx, client, "esports", "default"); err != nil {
		t.Fatal(err)
	}
	if err := retireAppPermissions(ctx, client, "esports", "default"); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ path, method, kind string }{
		{"/api/v1/esports/admin/products", "GET", "api"},
		{"/api/v1/esports/admin/products", "POST", "api"},
		{"esports:products:add", "button", "button"},
	} {
		n := client.CasbinRule.Query().Where(casbinrule.V0EQ("product_manager"), casbinrule.V2EQ(item.path),
			casbinrule.V3EQ(item.method), casbinrule.V4EQ(item.kind)).CountX(ctx)
		if n != 1 {
			t.Fatalf("商品授权 %s %s 应为 1 条，实际 %d", item.method, item.path, n)
		}
	}
	if client.CasbinRule.Query().Where(casbinrule.V2EQ("/api/v1/esports/admin/skus")).ExistX(ctx) {
		t.Fatal("旧商品接口授权仍存在")
	}
}
