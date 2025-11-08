package internal

import (
	"context"

	entsql "entgo.io/ent/dialect/sql"
	fiberlog "github.com/gofiber/fiber/v2/log"
	cmfCasbin "github.com/wuwuseo/cmf/casbin"
	"github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/orm"

	"go-admin/internal/casbin/adapter"
	"go-admin/internal/ent"
	"go-admin/internal/global"
)

func Init(config *config.Config) error {
	// 初始化数据库
	err := initDatabase(config)
	if err != nil {
		return err
	}

	// 初始化 Casbin（在数据库初始化之后）
	err = initCasbin(config)
	if err != nil {
		return err
	}

	return nil
}

func initDatabase(config *config.Config) error {
	driver := orm.GetDatabaseConfig([]string{config.Database.Default}, config).Driver
	fiberlog.Infof("using database driver: %s", driver)
	db, err := orm.GetSqlDb(driver, orm.GetDatabaseSourceDns(config, config.Database.Default))
	if err != nil {
		return err
	}
	drv := entsql.OpenDB(driver, db)
	client := ent.NewClient(ent.Driver(drv))
	if schemaErr := client.Schema.Create(context.Background()); schemaErr != nil {
		fiberlog.Fatalf("failed creating schema resources: %v", schemaErr)
	}
	global.ORM = client
	return nil
}

// InitCasbin 初始化 Casbin 权限执行器管理器
func initCasbin(config *config.Config) error {
	fiberlog.Info("initializing Casbin Enforcer Manager...")

	// 创建 Ent 适配器用于 Casbin 策略存储
	adapter, err := adapter.NewAdapterWithClient(global.ORM)
	if err != nil {
		fiberlog.Errorf("failed to create Casbin Ent adapter: %v", err)
		return err
	}

	// 使用全局 ORM 初始化 Casbin 执行器管理器
	global.EnforcerManager = cmfCasbin.InitEnforcerManager(adapter, config)

	fiberlog.Info("Casbin Enforcer Manager initialized successfully")
	return nil
}
