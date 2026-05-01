package internal

import (
	"context"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/casbin/casbin/v2/persist"
	cmfCasbin "github.com/wuwuseo/cmf/casbin"
	"github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/orm"

	"go-admin/internal/casbin/adapter"
	"go-admin/internal/ent"
	"go-admin/internal/ent/migrate"
)

// NewEntClient 创建 Ent ORM 客户端（Wire Provider）
func NewEntClient(cfg *config.Config, dbManager *orm.DBManager) (*ent.Client, error) {
	driver := orm.GetDatabaseConfig([]string{cfg.Database.Default}, cfg).Driver
	db, err := orm.GetSqlDb(driver, orm.GetDatabaseSourceDns(cfg, cfg.Database.Default))
	if err != nil {
		return nil, err
	}
	drv := entsql.OpenDB(driver, db)
	client := ent.NewClient(ent.Driver(drv))

	// 自动迁移 schema（WithDropIndex/WithDropColumn 确保 schema 变更时自动清理旧索引和旧列）
	if err := client.Schema.Create(context.Background(), migrate.WithDropIndex(true), migrate.WithDropColumn(true), migrate.WithForeignKeys(false)); err != nil {
		return nil, err
	}

	return client, nil
}

// NewCasbinAdapter 创建 Casbin 适配器（Wire Provider）
func NewCasbinAdapter(client *ent.Client) (persist.Adapter, error) {
	return adapter.NewAdapterWithClient(client)
}

// NewEnforcerManager 创建 Casbin EnforcerManager（Wire Provider）
func NewEnforcerManager(adp persist.Adapter, cfg *config.Config) *cmfCasbin.EnforcerManager {
	return cmfCasbin.InitEnforcerManager(adp, cfg)
}
