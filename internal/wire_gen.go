//go:build !wireinject
// +build !wireinject

package internal

import (
	"context"

	"github.com/wuwuseo/cmf/cache"
	"github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/filesystem"
	"github.com/wuwuseo/cmf/log"
	"github.com/wuwuseo/cmf/orm"
	"github.com/wuwuseo/cmf/validate"
)

// InitializeApp 手动实现的应用依赖初始化（等效于 Wire 生成代码）
func InitializeApp() (*App, error) {
	cfg := config.NewConfig()

	logger, err := log.NewLogger(cfg)
	if err != nil {
		return nil, err
	}

	dbManager, err := orm.NewDBManager(cfg)
	if err != nil {
		return nil, err
	}

	validator := validate.NewValidator()

	cacheManager := cache.NewCache(context.Background(), cfg)

	entClient, err := NewEntClient(cfg, dbManager)
	if err != nil {
		return nil, err
	}

	casbinAdapter, err := NewCasbinAdapter(entClient)
	if err != nil {
		return nil, err
	}

	enforcerManager := NewEnforcerManager(casbinAdapter, cfg)

	fs, err := filesystem.NewFilesystemFromConfig(cfg)
	if err != nil {
		return nil, err
	}

	return &App{
		Config:          cfg,
		Logger:          logger,
		DB:              dbManager,
		Validator:       validator,
		Cache:           cacheManager,
		EntClient:       entClient,
		EnforcerManager: enforcerManager,
		Filesystem:      fs,
	}, nil
}
