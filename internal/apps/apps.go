// Package apps connects source feature sets to go-admin persistence and security.
package apps

//go:generate go run github.com/wuwuseo/cmf/app/cmd/appgen -dir . -out zz_generated.go -pkg apps
//go:generate go run github.com/wuwuseo/cmf/app/cmd/entgen -apps-dir . -out ../ent/schema/zz_apps_gen.go -pkg schema

import (
	"context"
	"fmt"
	"sort"

	"github.com/gofiber/fiber/v3"
	cmfapp "github.com/wuwuseo/cmf/app"
	"github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/jwt"

	"go-admin/internal/apps/apphook"
	"go-admin/internal/ent"
	"go-admin/internal/ent/casbinrule"
	"go-admin/internal/ent/cronjob"
	"go-admin/internal/ent/dictdata"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/ent/systempermission"
	"go-admin/internal/global"
	"go-admin/internal/middleware"
	"go-admin/internal/service"
)

var reservedCodes = []string{"admin", "public", "user", "plugin", "attachments", "auth"}

func options() cmfapp.Options {
	return cmfapp.Options{BasePath: "/api/v1", ReservedCodes: reservedCodes}
}

// Validate checks the registry before Ent begins its automatic migration.
func Validate() error { return cmfapp.Validate(options()) }

// provisionDictSeeds 幂等种入功能集声明的字典项（随功能集启停：仅 Enabled 的功能集
// 会执行到此处；dict_type + value 查重）。
func provisionDictSeeds(ctx context.Context, client *ent.Client, a cmfapp.App) error {
	ext := apphook.Lookup(a.Code)
	if ext == nil {
		return nil
	}
	for _, item := range ext.DictSeeds() {
		subject := item.Value
		if item.DictType == "sys_config_group" {
			subject = "config group " + subject
		}
		exists, err := client.DictData.Query().
			Where(dictdata.DictTypeEQ(item.DictType), dictdata.ValueEQ(item.Value)).
			Exist(ctx)
		if err != nil {
			return fmt.Errorf("app %s: query dict %s: %w", a.Code, subject, err)
		}
		if exists {
			continue
		}
		if _, err := client.DictData.Create().
			SetLabel(item.Label).SetValue(item.Value).SetDictType(item.DictType).
			SetSort(item.Sort).SetStatus(1).SetIsDefault(false).Save(ctx); err != nil {
			return fmt.Errorf("app %s: create dict %s: %w", a.Code, subject, err)
		}
	}
	return nil
}

// Provision inserts missing settings and updates declared permissions.
// 功能集自身的种子数据与迁移（字典、特殊配置、退役权限、Casbin 迁移、定时任务、
// 业务基准数据）由各功能集通过 apphook 注册，此处按编码统一执行。
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
		ext := apphook.Lookup(a.Code)
		for _, item := range a.Config {
			key := "app." + a.Code + "." + item.Key
			if ext != nil {
				handled, err := ext.SeedConfig(ctx, client, key, item)
				if err != nil {
					return fmt.Errorf("app %s: seed config %s: %w", a.Code, key, err)
				}
				if handled {
					continue
				}
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
		for _, item := range a.Dicts {
			exists, err := client.DictData.Query().
				Where(dictdata.DictTypeEQ(item.Type), dictdata.ValueEQ(item.Value)).
				Exist(ctx)
			if err != nil {
				return fmt.Errorf("app %s: query dict %s/%s: %w", a.Code, item.Type, item.Value, err)
			}
			if exists {
				continue
			}
			create := client.DictData.Create().
				SetDictType(item.Type).
				SetLabel(item.Label).
				SetValue(item.Value).
				SetSort(item.Sort).
				SetStatus(1).
				SetIsDefault(item.IsDefault)
			if item.CSSClass != "" {
				create.SetCSSClass(item.CSSClass)
			}
			if item.Remark != "" {
				create.SetRemark(item.Remark)
			}
			if _, err := create.Save(ctx); err != nil {
				return fmt.Errorf("app %s: create dict %s/%s: %w", a.Code, item.Type, item.Value, err)
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

// retireAppPermissions 迁移功能集的 Casbin 规则与菜单授权，并清理已退役的权限行。
// 各步都必须幂等：Provision 每次启动都会执行。迁移与清理清单由功能集经 apphook 声明。
func retireAppPermissions(ctx context.Context, client *ent.Client, appCode, domain string) error {
	ext := apphook.Lookup(appCode)
	if ext == nil {
		return nil
	}
	for _, migration := range ext.PolicyMigrations() {
		oldRules, err := client.CasbinRule.Query().Where(
			casbinrule.PtypeEQ("p"), casbinrule.V1EQ(domain),
			casbinrule.V2EQ(migration.From), casbinrule.V3EQ(migration.Method),
			casbinrule.V4EQ(migration.Kind),
		).All(ctx)
		if err != nil {
			return err
		}
		for _, old := range oldRules {
			exists, err := client.CasbinRule.Query().Where(
				casbinrule.PtypeEQ(old.Ptype), casbinrule.V0EQ(old.V0), casbinrule.V1EQ(old.V1),
				casbinrule.V2EQ(migration.To), casbinrule.V3EQ(old.V3),
				casbinrule.V4EQ(old.V4), casbinrule.V5EQ(old.V5),
			).Exist(ctx)
			if err != nil {
				return err
			}
			if exists {
				if err := client.CasbinRule.DeleteOne(old).Exec(ctx); err != nil {
					return err
				}
			} else if _, err := client.CasbinRule.UpdateOne(old).SetV2(migration.To).Save(ctx); err != nil {
				return fmt.Errorf("app %s: migrate policy %s -> %s: %w", appCode, migration.From, migration.To, err)
			}
		}
	}
	for _, rule := range ext.RetiredPolicies() {
		query := client.CasbinRule.Delete().Where(
			casbinrule.PtypeEQ("p"), casbinrule.V1EQ(domain), casbinrule.V2EQ(rule.Path),
			casbinrule.V4EQ("api"),
		)
		if rule.Method != "" {
			query = query.Where(casbinrule.V3EQ(rule.Method))
		}
		if _, err := query.Exec(ctx); err != nil {
			return fmt.Errorf("app %s: retire policy %s: %w", appCode, rule.Path, err)
		}
	}
	for _, code := range ext.RetiredPermissions() {
		if _, err := client.SystemPermission.Delete().
			Where(systempermission.DomainEQ(domain), systempermission.CodeEQ(code)).
			Exec(ctx); err != nil {
			return fmt.Errorf("app %s: retire permission %s: %w", appCode, code, err)
		}
	}
	for _, migration := range ext.MenuPathMigrations() {
		if _, err := client.CasbinRule.Update().
			Where(
				casbinrule.PtypeEQ("p"),
				casbinrule.V1EQ(domain),
				casbinrule.V2EQ(migration.From),
				casbinrule.V4EQ("menu"),
			).
			SetV2(migration.To).
			Save(ctx); err != nil {
			return fmt.Errorf("app %s: migrate menu grant %s -> %s: %w", appCode, migration.From, migration.To, err)
		}
	}
	return nil
}

// seedFeatureBaseline 播种功能集声明的「新库必需」基准数据（幂等，已有数据一律不改动）。
// 与 seedCronJobs 的分工：前者保证任务会被调度，本函数保证**业务本身可用**。
// 基准清单由功能集经 apphook 声明（如 esports 的会员等级，审计 D14）。
func seedFeatureBaseline(ctx context.Context, client *ent.Client, code string) error {
	ext := apphook.Lookup(code)
	if ext == nil {
		return nil
	}
	if err := ext.SeedBaseline(ctx, client); err != nil {
		return fmt.Errorf("app %s: seed baseline: %w", code, err)
	}
	return nil
}

// seedCronJobs 播种功能集自带的定时任务 CronJob 行（幂等，按 name 判重）。
// 任务函数注册在功能集 Go 侧，但只有存在 enabled 的 CronJob 行才会被调度器加载。
// 已存在的行一律不改动（尊重运维在后台的启停与表达式调整）。
func seedCronJobs(ctx context.Context, client *ent.Client, code string) error {
	ext := apphook.Lookup(code)
	if ext == nil {
		return nil
	}
	for _, seed := range ext.CronSeeds() {
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
