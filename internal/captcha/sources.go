package captcha

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go-admin/internal/ent"

	cmfcaptcha "github.com/wuwuseo/cmf/captcha"
	"github.com/wuwuseo/cmf/secureconfig"
)

const (
	sceneKeyPrefix    = "captcha.scene."
	providerKeyPrefix = "captcha.provider."
)

type SceneSettings struct {
	Enabled      bool   `json:"enabled"`
	Provider     string `json:"provider"`
	ChallengeTTL int    `json:"challenge_ttl"`
	ProofTTL     int    `json:"proof_ttl"`
}

type DynamicSources struct {
	repository *SystemConfigRepository
	secrets    *secureconfig.Manager
}

var (
	_ cmfcaptcha.PolicySource         = (*DynamicSources)(nil)
	_ cmfcaptcha.ProviderConfigSource = (*DynamicSources)(nil)
)

func NewDynamicSources(client *ent.Client, secrets *secureconfig.Manager) *DynamicSources {
	return &DynamicSources{
		repository: NewSystemConfigRepository(client),
		secrets:    secrets,
	}
}

func (s *DynamicSources) Policy(ctx context.Context, scene string) (cmfcaptcha.ScenePolicy, error) {
	raw, err := s.repository.Get(ctx, sceneKeyPrefix+scene)
	if err != nil {
		return cmfcaptcha.ScenePolicy{}, cmfcaptcha.ErrSceneInvalid
	}
	var settings SceneSettings
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return cmfcaptcha.ScenePolicy{}, fmt.Errorf("%w: invalid scene settings", cmfcaptcha.ErrProviderMisconfigured)
	}
	if settings.ChallengeTTL < 0 || settings.ProofTTL < 0 {
		return cmfcaptcha.ScenePolicy{}, cmfcaptcha.ErrProviderMisconfigured
	}
	return cmfcaptcha.ScenePolicy{
		Enabled:      settings.Enabled,
		Provider:     settings.Provider,
		ChallengeTTL: time.Duration(settings.ChallengeTTL) * time.Second,
		ProofTTL:     time.Duration(settings.ProofTTL) * time.Second,
	}, nil
}

func (s *DynamicSources) Config(ctx context.Context, provider string) (json.RawMessage, error) {
	if s.secrets == nil {
		return nil, cmfcaptcha.ErrProviderMisconfigured
	}
	var config json.RawMessage
	if err := s.secrets.GetJSON(ctx, providerKeyPrefix+provider, &config); err != nil {
		return nil, cmfcaptcha.ErrProviderMisconfigured
	}
	return config, nil
}

func (s *DynamicSources) SaveScene(ctx context.Context, scene string, settings SceneSettings) error {
	if scene == "" || settings.ChallengeTTL < 0 || settings.ProofTTL < 0 {
		return cmfcaptcha.ErrSceneInvalid
	}
	if settings.Enabled && settings.Provider == "" {
		return cmfcaptcha.ErrProviderMisconfigured
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	return s.repository.Put(ctx, sceneKeyPrefix+scene, string(raw))
}
