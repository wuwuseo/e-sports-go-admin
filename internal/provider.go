package internal

import (
	"context"
	"fmt"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/casbin/casbin/v3/persist"
	cmfCasbin "github.com/wuwuseo/cmf/casbin"
	"github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/orm"

	"go-admin/internal/apps"
	"go-admin/internal/casbin/adapter"
	"go-admin/internal/ent"
	"go-admin/internal/ent/migrate"
)

// NewEntClient 创建 Ent ORM 客户端（Wire Provider）
// connName 可选指定数据库连接名（不传 = 默认连接），用于 Ent 接入 database.connections 中的其他连接
func NewEntClient(cfg *config.Config, dbManager *orm.DBManager, connName ...string) (*ent.Client, error) {
	if err := apps.Validate(); err != nil {
		return nil, fmt.Errorf("invalid source feature set: %w", err)
	}
	// 延迟初始化表前缀：配置已加载，此时可安全获取 table_prefix
	ent.InitTablePrefix(orm.GetTablePrefix())

	name := ""
	if len(connName) > 0 {
		name = connName[0]
	}

	// 复用 DBManager 管理的连接池（与 dbManager.DB(name) 返回同一实例，避免重复建连）
	db, err := dbManager.DB([]string{name}...)
	if err != nil {
		return nil, err
	}
	driver := orm.GetDatabaseConfig([]string{name}, cfg).Driver
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
