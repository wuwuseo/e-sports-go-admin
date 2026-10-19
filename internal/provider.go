package internal

import (
	"context"
	"database/sql"
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

	// 迁移前的数据归一化（必须早于 Schema.Create）：users.phone 带唯一索引（§6.1-01，列可空），
	// 唯一索引允许多个 NULL 但**不允许多个空串**。历史写入路径在「未填手机号」时写的是空串，
	// 存量库若有 ≥2 行空串，唯一索引会**建不上、服务直接启动失败**。这里先归一化为 NULL（幂等）。
	// 只影响空串行；真实手机号不受影响。真正的重复手机号仍需人工去重，那会由下一步迁移明确报错。
	if err := normalizeEmptyUserPhones(context.Background(), db); err != nil {
		return nil, err
	}

	// 自动迁移 schema（WithDropIndex/WithDropColumn 确保 schema 变更时自动清理旧索引和旧列）
	if err := client.Schema.Create(context.Background(), migrate.WithDropIndex(true), migrate.WithDropColumn(true), migrate.WithForeignKeys(false)); err != nil {
		return nil, err
	}

	return client, nil
}

// normalizeEmptyUserPhones 把 users.phone 的空串归一化为 NULL，为 phone 唯一索引（§6.1-01）清障。
//
// 为什么必须放在迁移之前：唯一索引是 Schema.Create 建的，一旦存量库存在多行「手机号为空串」的记录，
// 建索引会失败并使服务无法启动。归一化是幂等的（第二次已无可改行）。
//
// 表不存在（全新库在迁移前）等情形**不阻断启动**：此时没有需要清理的行，
// 而真正的 schema/约束问题会紧接着由 Schema.Create 报出来，不会因此被掩盖。
func normalizeEmptyUserPhones(ctx context.Context, db *sql.DB) error {
	table := "users"
	if prefix := orm.GetTablePrefix(); prefix != "" {
		table = prefix + table
	}
	if _, err := db.ExecContext(ctx,
		fmt.Sprintf("UPDATE %s SET phone = NULL WHERE phone = ''", table)); err != nil {
		return nil
	}
	return nil
}

// NewCasbinAdapter 创建 Casbin 适配器（Wire Provider）
func NewCasbinAdapter(client *ent.Client) (persist.Adapter, error) {
	return adapter.NewAdapterWithClient(client)
}

// NewEnforcerManager 创建 Casbin EnforcerManager（Wire Provider）
func NewEnforcerManager(adp persist.Adapter, cfg *config.Config) *cmfCasbin.EnforcerManager {
	return cmfCasbin.InitEnforcerManager(adp, cfg)
}
