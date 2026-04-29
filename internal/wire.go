//go:build wireinject
// +build wireinject

package internal

import (
	"context"

	"github.com/google/wire"
	"github.com/wuwuseo/cmf/cache"
	"github.com/wuwuseo/cmf/config"
	"github.com/wuwuseo/cmf/filesystem"
	"github.com/wuwuseo/cmf/log"
	"github.com/wuwuseo/cmf/orm"
	"github.com/wuwuseo/cmf/validate"
)

func provideContext() context.Context {
	return context.Background()
}

// InitializeApp 通过 Wire 初始化应用依赖
func InitializeApp() (*App, error) {
	wire.Build(
		provideContext,
		config.NewConfig,
		log.ProviderSet,
		orm.ProviderSet,
		validate.ProviderSet,
		cache.ProviderSet,
		filesystem.ProviderSet,
		NewEntClient,
		NewCasbinAdapter,
		NewEnforcerManager,
	)
	return nil, nil
}
