package captcha

import (
	"context"
	"fmt"
	"strings"

	"go-admin/internal/ent"

	cmfcaptcha "github.com/wuwuseo/cmf/captcha"
	"github.com/wuwuseo/cmf/captcha/provider"
	"github.com/wuwuseo/cmf/config"
	cmfredis "github.com/wuwuseo/cmf/redis"
	"github.com/wuwuseo/cmf/secureconfig"
)

var DefaultScenes = []string{"user_login", "admin_login"}

type Runtime struct {
	Registry *cmfcaptcha.Registry
	Sources  *DynamicSources
	Secrets  *secureconfig.Manager
	Service  *cmfcaptcha.Service
	Settings *SettingsService
	Store    cmfcaptcha.Store
}

func NewRuntime(
	ctx context.Context,
	client *ent.Client,
	cfg *config.Config,
	lookup func(string) string,
	scenes ...string,
) (*Runtime, error) {
	if client == nil || cfg == nil {
		return nil, fmt.Errorf("captcha runtime requires database and application config")
	}
	if len(scenes) == 0 {
		scenes = append([]string(nil), DefaultScenes...)
	}

	registry := cmfcaptcha.NewRegistry()
	for _, item := range []cmfcaptcha.Provider{
		provider.NewAlphanumeric(),
		provider.NewMath(),
		provider.NewChineseInput(),
		provider.NewChineseClick(),
		provider.NewGeeTestGT4(nil),
	} {
		if err := registry.Register(item); err != nil {
			return nil, err
		}
	}

	store, err := newStore(ctx, cfg)
	if err != nil {
		return nil, err
	}
	var secrets *secureconfig.Manager
	if lookup != nil && strings.TrimSpace(lookup(secureconfig.ActiveVersionEnv)) != "" {
		secrets, err = NewSecretManager(client, lookup)
		if err != nil {
			return nil, fmt.Errorf("configure captcha master key: %w", err)
		}
	}
	sources := NewDynamicSources(client, secrets)
	return &Runtime{
		Registry: registry,
		Sources:  sources,
		Secrets:  secrets,
		Service:  cmfcaptcha.NewService(registry, sources, sources, store),
		Settings: NewSettingsService(sources, secrets, registry, scenes),
		Store:    store,
	}, nil
}

func newStore(ctx context.Context, cfg *config.Config) (cmfcaptcha.Store, error) {
	storeName := strings.ToLower(strings.TrimSpace(cfg.Captcha.Store))
	if storeName == "" {
		storeName = "memory"
	}
	switch storeName {
	case "memory":
		if cfg.App.Prefork {
			return nil, fmt.Errorf("captcha store must use redis when app.prefork is enabled")
		}
		return cmfcaptcha.NewMemoryStore(), nil
	case "redis":
		client, err := cmfredis.NewClientFromConfig(ctx, cfg, cfg.Captcha.RedisConnection)
		if err != nil {
			return nil, fmt.Errorf("initialize captcha redis store: %w", err)
		}
		return cmfcaptcha.NewRedisStore(client, cfg.Captcha.KeyPrefix), nil
	default:
		return nil, fmt.Errorf("unsupported captcha store %q", cfg.Captcha.Store)
	}
}
