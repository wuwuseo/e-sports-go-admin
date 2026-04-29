package internal

import (
	"context"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	cmfCasbin "github.com/wuwuseo/cmf/casbin"
	"github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/orm"
	"go.uber.org/zap"

	"go-admin/internal/casbin/adapter"
	"go-admin/internal/cron"
	"go-admin/internal/ent"
	"go-admin/internal/global"
)

var appInstance *App

// SetApp 设置 Wire 注入的应用实例
func SetApp(app *App) {
	appInstance = app
}

// GetApp 获取当前应用实例
func GetApp() *App {
	return appInstance
}

func Init(config *config.Config) error {
	// 如果 appInstance 未设置，创建一个最小实例（兼容旧模式）
	if appInstance == nil {
		appInstance = &App{
			Config: config,
		}
	}

	// 初始化数据库：优先使用 Wire 注入的 EntClient
	if appInstance.EntClient != nil {
		global.ORM = appInstance.EntClient
		appInstance.Logger.Info("using Wire-injected EntClient")
	} else {
		if err := initDatabase(config); err != nil {
			return err
		}
	}

	// 初始化 Casbin：优先使用 Wire 注入的 EnforcerManager
	if appInstance.EnforcerManager != nil {
		global.EnforcerManager = appInstance.EnforcerManager
		appInstance.Logger.Info("using Wire-injected EnforcerManager")
	} else {
		if err := initCasbin(config); err != nil {
			return err
		}
	}

	// 初始化定时任务管理器
	cronManager := cron.NewCronManager(global.ORM, "./data/logs/cron", 7*24*time.Hour)
	if err := cronManager.Start(); err != nil {
		return err
	}
	global.CronManager = cronManager

	return nil
}

// initDatabase 手动初始化数据库（兼容旧模式，当 Wire 未注入时使用）
func initDatabase(config *config.Config) error {
	driver := orm.GetDatabaseConfig([]string{config.Database.Default}, config).Driver
	appInstance.Logger.Info("using database driver", zap.String("driver", driver))
	db, err := orm.GetSqlDb(driver, orm.GetDatabaseSourceDns(config, config.Database.Default))
	if err != nil {
		return err
	}
	drv := entsql.OpenDB(driver, db)
	client := ent.NewClient(ent.Driver(drv))
	if schemaErr := client.Schema.Create(context.Background()); schemaErr != nil {
		appInstance.Logger.Fatal("failed creating schema resources", zap.Error(schemaErr))
	}
	global.ORM = client
	return nil
}

// initCasbin 手动初始化 Casbin（兼容旧模式，当 Wire 未注入时使用）
func initCasbin(config *config.Config) error {
	appInstance.Logger.Info("initializing Casbin Enforcer Manager...")

	adp, err := adapter.NewAdapterWithClient(global.ORM)
	if err != nil {
		appInstance.Logger.Error("failed to create Casbin Ent adapter", zap.Error(err))
		return err
	}

	global.EnforcerManager = cmfCasbin.InitEnforcerManager(adp, config)

	appInstance.Logger.Info("Casbin Enforcer Manager initialized successfully")
	return nil
}
