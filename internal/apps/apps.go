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

	"go-admin/internal/ent"
	"go-admin/internal/ent/systemconfig"
	"go-admin/internal/middleware"
	"go-admin/internal/service"
)

var reservedCodes = []string{"admin", "public", "user", "plugin", "attachments", "auth"}

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
