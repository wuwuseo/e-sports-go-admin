// Package apps connects source feature sets to go-admin persistence and security.
package apps

//go:generate go run github.com/wuwuseo/cmf/app/cmd/appgen -dir . -out zz_generated.go -pkg apps
//go:generate go run github.com/wuwuseo/cmf/app/cmd/entgen -apps-dir . -out ../ent/schema/zz_apps_gen.go -pkg schema

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/gofiber/fiber/v3"
	cmfapp "github.com/wuwuseo/cmf/app"
	"github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/jwt"

	"go-admin/internal/apps/esports"
	"go-admin/internal/ent"
	"go-admin/internal/ent/casbinrule"
	"go-admin/internal/ent/cronjob"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/ent/systempermission"
	"go-admin/internal/global"
	"go-admin/internal/middleware"
	"go-admin/internal/service"
)

var reservedCodes = []string{"admin", "public", "user", "plugin", "attachments", "auth"}

// retiredAppPermissions 记录功能集已合并/下线的权限码。种子权限只做创建与更新、
// 不删除历史行，不显式清理会在超管菜单树里留下孤儿菜单。
var retiredAppPermissions = map[string][]string{
	"esports": {
		"esports:pages:home_menu", "esports:products:detail", "esports:orders:seat_roles",
		"esports:spec_groups:list", "esports:spec_groups:create", "esports:spec_groups:update",
		"esports:skus:list", "esports:skus:create", "esports:skus:update",
		"esports:skus:suspension_request", "esports:skus:suspension_list",
		"esports:skus:suspension_detail", "esports:skus:suspension_decision",
		"esports:skus:suspension_cancel", "esports:skus:suspension_disposal",
		"esports:skus:add", "esports:skus:edit",
	},
}

type appPolicyMigration struct{ from, to, method, kind string }

var appPolicyMigrations = map[string][]appPolicyMigration{
	"esports": {
		{"/api/v1/esports/admin/skus", "/api/v1/esports/admin/products", "GET", "api"},
		{"/api/v1/esports/admin/skus", "/api/v1/esports/admin/products", "POST", "api"},
		{"/api/v1/esports/admin/skus/{id}", "/api/v1/esports/admin/products/{id}", "PUT", "api"},
		{"/api/v1/esports/admin/skus/{id}/suspensions", "/api/v1/esports/admin/products/{id}/suspensions", "POST", "api"},
		{"/api/v1/esports/admin/sku-suspensions", "/api/v1/esports/admin/product-suspensions", "GET", "api"},
		{"/api/v1/esports/admin/sku-suspensions/{id}", "/api/v1/esports/admin/product-suspensions/{id}", "GET", "api"},
		{"/api/v1/esports/admin/sku-suspensions/{id}/decision", "/api/v1/esports/admin/product-suspensions/{id}/decision", "POST", "api"},
		{"/api/v1/esports/admin/sku-suspensions/{id}/cancel", "/api/v1/esports/admin/product-suspensions/{id}/cancel", "POST", "api"},
		{"/api/v1/esports/admin/sku-suspensions/{id}/orders/{orderId}/disposal", "/api/v1/esports/admin/product-suspensions/{id}/orders/{orderId}/disposal", "POST", "api"},
		{"esports:skus:add", "esports:products:add", "button", "button"},
		{"esports:skus:edit", "esports:products:edit", "button", "button"},
	},
}

// appMenuPathMigrations 记录菜单路径变更后的角色授权迁移。角色授权以菜单路径为
// Casbin object，路径改了但角色规则没跟着走，非超管角色会丢掉该菜单。
var appMenuPathMigrations = map[string][][2]string{
	"esports": {{"/esports/pages", "/esports/pages/home"}},
}

func options() cmfapp.Options {
	return cmfapp.Options{BasePath: "/api/v1", ReservedCodes: reservedCodes}
}

// Validate checks the registry before Ent begins its automatic migration.
func Validate() error { return cmfapp.Validate(options()) }

// Provision inserts missing settings and updates declared permissions.
func Provision(ctx context.Context, cfg *config.Config, client *ent.Client) error {
	if err := Validate(); err != nil {
		return err
	}
	if cfg == nil || client == nil {
		return fmt.Errorf("feature set provisioning requires config and Ent client")
	}
	domain := cfg.Casbin.DomainsDefault
	if domain == "" {
		domain = "default"
	}
	for _, a := range cmfapp.Registered() {
		if !a.Enabled {
			continue
		}
		for _, item := range a.Config {
			key := "app." + a.Code + "." + item.Key
			if key == "app.esports.page_preview_secret" {
				if err := seedPagePreviewSecret(ctx, client, key, item); err != nil {
					return fmt.Errorf("app %s: seed config %s: %w", a.Code, key, err)
				}
				continue
			}
			exists, err := client.SystemConfig.Query().Where(systemconfig.KeyEQ(key)).Exist(ctx)
			if err != nil {
				return fmt.Errorf("app %s: query config %s: %w", a.Code, key, err)
			}
			if exists {
				continue
			}
			if _, err := client.SystemConfig.Create().SetKey(key).SetName(item.Name).
				SetValue(item.Value).SetType(item.Type).SetGroup(item.Group).
				SetRemark(item.Remark).SetStatus(1).Save(ctx); err != nil {
				return fmt.Errorf("app %s: create config %s: %w", a.Code, key, err)
			}
		}
		seeds, err := permissionSeeds(a, domain)
		if err != nil {
			return err
		}
		if err := service.NewSystemPermissionServiceWithClient(client).InitSeedPermissions(ctx, seeds); err != nil {
			return fmt.Errorf("app %s: provision permissions: %w", a.Code, err)
		}
		if err := retireAppPermissions(ctx, client, a.Code, domain); err != nil {
			return err
		}
		if err := seedCronJobs(ctx, client, a.Code); err != nil {
			return err
		}
		if err := seedFeatureBaseline(ctx, client, a.Code); err != nil {
			return err
		}
	}
	return nil
}

// seedPagePreviewSecret gives each installation its own persistent signing key.
// Older installations may already have an empty seed row; fill only that value.
func seedPagePreviewSecret(ctx context.Context, client *ent.Client, key string, item cmfapp.ConfigItem) error {
	row, err := client.SystemConfig.Query().Where(systemconfig.KeyEQ(key)).Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return fmt.Errorf("query config: %w", err)
	}
	if err == nil && strings.TrimSpace(row.Value) != "" {
		return nil
	}
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Errorf("generate random signing key: %w", err)
	}
	secret := hex.EncodeToString(bytes)
	if row != nil {
		// Preserve a value set by an administrator after the read.
		if _, err := client.SystemConfig.Update().
			Where(systemconfig.KeyEQ(key), systemconfig.ValueEQ(row.Value)).
			SetValue(secret).Save(ctx); err != nil {
			return fmt.Errorf("update empty config: %w", err)
		}
		return nil
	}
	if _, err := client.SystemConfig.Create().SetKey(key).SetName(item.Name).
		SetValue(secret).SetType(item.Type).SetGroup(item.Group).
		SetRemark(item.Remark).SetStatus(1).Save(ctx); err != nil {
		return fmt.Errorf("create config: %w", err)
	}
	return nil
}

// retireAppPermissions 清理功能集已退役的权限行，并把变更过路径的菜单授权平移到新路径。
// 两步都必须幂等：Provision 每次启动都会执行。
func retireAppPermissions(ctx context.Context, client *ent.Client, appCode, domain string) error {
	for _, migration := range appPolicyMigrations[appCode] {
		oldRules, err := client.CasbinRule.Query().Where(
			casbinrule.PtypeEQ("p"), casbinrule.V1EQ(domain),
			casbinrule.V2EQ(migration.from), casbinrule.V3EQ(migration.method),
			casbinrule.V4EQ(migration.kind),
		).All(ctx)
		if err != nil {
			return err
		}
		for _, old := range oldRules {
			exists, err := client.CasbinRule.Query().Where(
				casbinrule.PtypeEQ(old.Ptype), casbinrule.V0EQ(old.V0), casbinrule.V1EQ(old.V1),
				casbinrule.V2EQ(migration.to), casbinrule.V3EQ(old.V3),
				casbinrule.V4EQ(old.V4), casbinrule.V5EQ(old.V5),
			).Exist(ctx)
			if err != nil {
				return err
			}
			if exists {
				if err := client.CasbinRule.DeleteOne(old).Exec(ctx); err != nil {
					return err
				}
			} else if _, err := client.CasbinRule.UpdateOne(old).SetV2(migration.to).Save(ctx); err != nil {
				return fmt.Errorf("app %s: migrate policy %s -> %s: %w", appCode, migration.from, migration.to, err)
			}
		}
	}
	if appCode == "esports" {
		for _, path := range []string{
			"/api/v1/esports/admin/spec-groups", "/api/v1/esports/admin/spec-groups/{id}",
			"/api/v1/esports/admin/products/{id}", "/api/v1/esports/admin/orders/{id}/seat-roles",
		} {
			query := client.CasbinRule.Delete().Where(
				casbinrule.PtypeEQ("p"), casbinrule.V1EQ(domain), casbinrule.V2EQ(path),
				casbinrule.V4EQ("api"),
			)
			if path == "/api/v1/esports/admin/products/{id}" {
				query = query.Where(casbinrule.V3EQ("GET"))
			}
			if _, err := query.Exec(ctx); err != nil {
				return err
			}
		}
	}
	for _, code := range retiredAppPermissions[appCode] {
		if _, err := client.SystemPermission.Delete().
			Where(systempermission.DomainEQ(domain), systempermission.CodeEQ(code)).
			Exec(ctx); err != nil {
			return fmt.Errorf("app %s: retire permission %s: %w", appCode, code, err)
		}
	}
	for _, migration := range appMenuPathMigrations[appCode] {
		from, to := migration[0], migration[1]
		if _, err := client.CasbinRule.Update().
			Where(
				casbinrule.PtypeEQ("p"),
				casbinrule.V1EQ(domain),
				casbinrule.V2EQ(from),
				casbinrule.V4EQ("menu"),
			).
			SetV2(to).
			Save(ctx); err != nil {
			return fmt.Errorf("app %s: migrate menu grant %s -> %s: %w", appCode, from, to, err)
		}
	}
	return nil
}

// seedFeatureBaseline 为公司功能集播种「新库必需」的基准数据（幂等，已有数据一律不改动）。
// 与 seedCronJobs 的分工：前者保证任务会被调度，本函数保证**业务本身可用**。
// 以会员等级为例：全新库里等级表为空，客户将永远无法定级，折扣、优先派单、
// 专属客服等按等级生效的权益会**全部静默失效**（审计 D14），因此功能启用时必须播种五档默认等级。
func seedFeatureBaseline(ctx context.Context, client *ent.Client, code string) error {
	if code != "esports" {
		return nil
	}
	if _, err := esports.SeedMemberLevels(ctx, client); err != nil {
		return fmt.Errorf("app %s: seed member levels: %w", code, err)
	}
	if _, err := esports.SeedRechargeCampaign(ctx, client); err != nil {
		return fmt.Errorf("app %s: seed recharge campaign: %w", code, err)
	}
	return nil
}

// seedCronJobs 为公司功能集自带的定时任务播种 CronJob 行（幂等，按 name 判重）。
// 任务函数注册在 Go 侧，但只有存在 enabled 的 CronJob 行才会被调度器加载，
// 缺少种子会导致订单超时释放、结算生成、保护期降级、账本核验、外盒/通知投递等永不执行。
// 已存在的行一律不改动（尊重运维在后台的启停与表达式调整）。
func seedCronJobs(ctx context.Context, client *ent.Client, code string) error {
	if code != "esports" {
		return nil
	}
	for _, seed := range esports.CronSeeds() {
		exists, err := client.CronJob.Query().Where(cronjob.NameEQ(seed.Name)).Exist(ctx)
		if err != nil {
			return fmt.Errorf("app %s: query cron job %s: %w", code, seed.Name, err)
		}
		if exists {
			continue
		}
		row, err := client.CronJob.Create().
			SetName(seed.Name).SetDescription(seed.Description).
			SetType(cronjob.TypeTask).SetExpression(seed.Expression).
			SetCommand(seed.Name).SetEnabled(true).Save(ctx)
		if err != nil {
			return fmt.Errorf("app %s: create cron job %s: %w", code, seed.Name, err)
		}
		// 调度器在 Provision 之前已启动，新行需注册后才能立即生效（重启前不必等待）。
		if global.CronManager != nil {
			if err := global.CronManager.AddJob(row); err != nil {
				return fmt.Errorf("app %s: register cron job %s: %w", code, seed.Name, err)
			}
		}
	}
	return nil
}

func permissionSeeds(a cmfapp.App, domain string) ([]service.SeedPermission, error) {
	byCode := make(map[string]cmfapp.Permission, len(a.Permissions))
	for _, p := range a.Permissions {
		byCode[p.Code] = p
	}
	codes := make([]string, 0, len(byCode))
	for code := range byCode {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	ordered := make([]cmfapp.Permission, 0, len(codes))
	visited := map[string]bool{}
	var visit func(string)
	visit = func(code string) {
		if visited[code] {
			return
		}
		p := byCode[code]
		if p.Parent != "" {
			visit(p.Parent)
		}
		visited[code] = true
		ordered = append(ordered, p)
	}
	for _, code := range codes {
		visit(code)
	}
	ids := make(map[string]int, len(ordered))
	for i, p := range ordered {
		ids[p.Code] = i + 1
	}
	seeds := make([]service.SeedPermission, 0, len(ordered))
	for _, p := range ordered {
		seeds = append(seeds, service.SeedPermission{
			ID: ids[p.Code], Domain: domain, Type: string(p.Type), Name: p.Name,
			Code: p.Code, Path: p.Path, Method: p.Method, MenuPath: p.MenuPath,
			Icon: p.Icon, Component: p.Component, ParentID: ids[p.Parent],
			SortOrder: p.SortOrder, IsCommon: p.IsCommon, Status: 1,
			Description: p.Description,
		})
	}
	return seeds, nil
}

// Mount registers all enabled feature routes under the caller's /api/v1 group.
func Mount(v1 fiber.Router, cfg *config.Config, blacklist service.BlacklistServiceInterface) error {
	if cfg == nil {
		return fmt.Errorf("feature set mounting requires config")
	}
	opts := options()
	jwtMiddleware := func(secret string) fiber.Handler {
		return jwt.NewJWTMiddlewareWithConfig(jwt.JWTConfig{
			Secret:       secret,
			ErrorHandler: func(c fiber.Ctx, _ error) error { return c.SendStatus(fiber.StatusUnauthorized) },
		})
	}
	opts.Middleware = func(auth cmfapp.Auth) []fiber.Handler {
		switch auth {
		case cmfapp.AuthUser:
			return []fiber.Handler{jwtMiddleware(cfg.App.Secret), middleware.DomainMiddleware(), middleware.UserIdentity(), middleware.BlacklistCheck(blacklist, "user")}
		case cmfapp.AuthAdmin:
			return []fiber.Handler{jwtMiddleware(cfg.App.AdminSecret), middleware.DomainMiddleware(), middleware.BlacklistCheck(blacklist, "admin"), middleware.PermissionMiddleware()}
		default:
			return nil
		}
	}
	return cmfapp.Mount(v1, opts)
}
