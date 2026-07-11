package captcha

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go-admin/internal/ent"

	cmfcaptcha "github.com/wuwuseo/cmf/captcha"
	"github.com/wuwuseo/cmf/secureconfig"
)

const (
	defaultChallengeTTL = 120
	defaultProofTTL     = 90
)

type ProviderSettingsRequest struct {
	Config      json.RawMessage `json:"config" swaggertype:"object"`
	Secret      string          `json:"secret"`
	ClearSecret bool            `json:"clear_secret"`
}

type ProviderSettingsResponse struct {
	Provider         string                         `json:"provider"`
	Config           json.RawMessage                `json:"config" swaggertype:"object"`
	SecretConfigured bool                           `json:"secret_configured"`
	KeyVersion       string                         `json:"key_version,omitempty"`
	Descriptor       *cmfcaptcha.ProviderDescriptor `json:"descriptor,omitempty" swaggertype:"object"`
}

type SettingsResponse struct {
	Scenes    map[string]SceneSettings            `json:"scenes"`
	Providers map[string]ProviderSettingsResponse `json:"providers"`
}

type SettingsService struct {
	sources  *DynamicSources
	secrets  *secureconfig.Manager
	registry *cmfcaptcha.Registry
	scenes   map[string]struct{}
}

func NewSettingsService(
	sources *DynamicSources,
	secrets *secureconfig.Manager,
	registry *cmfcaptcha.Registry,
	scenes []string,
) *SettingsService {
	knownScenes := make(map[string]struct{}, len(scenes))
	for _, scene := range scenes {
		scene = strings.TrimSpace(scene)
		if scene != "" {
			knownScenes[scene] = struct{}{}
		}
	}
	return &SettingsService{
		sources:  sources,
		secrets:  secrets,
		registry: registry,
		scenes:   knownScenes,
	}
}

func (s *SettingsService) UpdateScene(
	ctx context.Context,
	scene string,
	settings SceneSettings,
) (SceneSettings, error) {
	if _, ok := s.scenes[scene]; !ok {
		return SceneSettings{}, fmt.Errorf("%w: unknown scene %q", cmfcaptcha.ErrSceneInvalid, scene)
	}
	if settings.Enabled {
		provider, ok := s.registry.Get(settings.Provider)
		if !ok {
			return SceneSettings{}, fmt.Errorf("%w: unknown provider %q", cmfcaptcha.ErrProviderMisconfigured, settings.Provider)
		}
		config, err := s.sources.Config(ctx, settings.Provider)
		if err != nil || provider.ValidateConfig(config) != nil {
			return SceneSettings{}, cmfcaptcha.ErrProviderMisconfigured
		}
	}
	if settings.ChallengeTTL == 0 {
		settings.ChallengeTTL = defaultChallengeTTL
	}
	if settings.ProofTTL == 0 {
		settings.ProofTTL = defaultProofTTL
	}
	if err := s.sources.SaveScene(ctx, scene, settings); err != nil {
		return SceneSettings{}, err
	}
	return settings, nil
}

func (s *SettingsService) UpdateProvider(
	ctx context.Context,
	name string,
	request ProviderSettingsRequest,
) (ProviderSettingsResponse, error) {
	if s.secrets == nil {
		return ProviderSettingsResponse{}, fmt.Errorf("captcha environment master key is not configured")
	}
	item, merged, publicConfig, secretField, err := s.prepareProviderConfig(ctx, name, request)
	if err != nil {
		return ProviderSettingsResponse{}, err
	}
	validationConfig := merged
	if _, configured := validationConfig[secretField]; !configured {
		validationConfig = cloneConfig(validationConfig)
		validationConfig[secretField] = "0000000000000000"
	}
	raw, err := json.Marshal(validationConfig)
	if err != nil {
		return ProviderSettingsResponse{}, fmt.Errorf("marshal provider config: %w", err)
	}
	if err := item.ValidateConfig(raw); err != nil {
		return ProviderSettingsResponse{}, err
	}
	raw, err = json.Marshal(merged)
	if err != nil {
		return ProviderSettingsResponse{}, fmt.Errorf("marshal provider config: %w", err)
	}
	if err := s.secrets.SetJSON(ctx, providerKeyPrefix+name, json.RawMessage(raw)); err != nil {
		return ProviderSettingsResponse{}, err
	}
	metadata, err := s.secrets.Inspect(ctx, providerKeyPrefix+name)
	if err != nil {
		return ProviderSettingsResponse{}, err
	}
	sanitized, err := json.Marshal(publicConfig)
	if err != nil {
		return ProviderSettingsResponse{}, fmt.Errorf("marshal public provider config: %w", err)
	}
	_, secretConfigured := merged[secretField]
	return ProviderSettingsResponse{
		Provider:         name,
		Config:           sanitized,
		SecretConfigured: secretConfigured,
		KeyVersion:       metadata.KeyVersion,
		Descriptor:       providerDescriptor(item),
	}, nil
}

func (s *SettingsService) ValidateProviderConfig(
	ctx context.Context,
	name string,
	request ProviderSettingsRequest,
) error {
	if s.secrets == nil && request.Secret == "" {
		return fmt.Errorf("captcha environment master key is not configured")
	}
	item, merged, _, _, err := s.prepareProviderConfig(ctx, name, request)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		return fmt.Errorf("marshal provider config: %w", err)
	}
	return item.ValidateConfig(raw)
}

func (s *SettingsService) GetSettings(ctx context.Context) (SettingsResponse, error) {
	response := SettingsResponse{
		Scenes:    make(map[string]SceneSettings),
		Providers: make(map[string]ProviderSettingsResponse),
	}
	for _, name := range s.registry.Names() {
		item, _ := s.registry.Get(name)
		response.Providers[name] = ProviderSettingsResponse{
			Provider:   name,
			Config:     json.RawMessage(`{}`),
			Descriptor: providerDescriptor(item),
		}
	}
	sceneEntries, err := s.sources.repository.ListPrefix(ctx, sceneKeyPrefix)
	if err != nil {
		return SettingsResponse{}, err
	}
	for _, entry := range sceneEntries {
		var settings SceneSettings
		if err := json.Unmarshal([]byte(entry.Value), &settings); err != nil {
			return SettingsResponse{}, fmt.Errorf("decode scene %q: %w", entry.Key, err)
		}
		response.Scenes[strings.TrimPrefix(entry.Key, sceneKeyPrefix)] = settings
	}

	providerEntries, err := s.sources.repository.ListPrefix(ctx, providerKeyPrefix)
	if err != nil {
		return SettingsResponse{}, err
	}
	if len(providerEntries) > 0 && s.secrets == nil {
		return SettingsResponse{}, fmt.Errorf("captcha environment master key is not configured")
	}
	for _, entry := range providerEntries {
		name := strings.TrimPrefix(entry.Key, providerKeyPrefix)
		var config map[string]any
		if err := s.secrets.GetJSON(ctx, entry.Key, &config); err != nil {
			return SettingsResponse{}, err
		}
		secretField := providerSecretField(name)
		_, secretConfigured := config[secretField]
		delete(config, secretField)
		publicConfig, err := json.Marshal(config)
		if err != nil {
			return SettingsResponse{}, fmt.Errorf("marshal public provider config: %w", err)
		}
		metadata, err := s.secrets.Inspect(ctx, entry.Key)
		if err != nil {
			return SettingsResponse{}, err
		}
		response.Providers[name] = ProviderSettingsResponse{
			Provider:         name,
			Config:           publicConfig,
			SecretConfigured: secretConfigured,
			KeyVersion:       metadata.KeyVersion,
			Descriptor:       response.Providers[name].Descriptor,
		}
	}
	return response, nil
}

func providerDescriptor(item cmfcaptcha.Provider) *cmfcaptcha.ProviderDescriptor {
	described, ok := item.(cmfcaptcha.DescribedProvider)
	if !ok {
		return nil
	}
	descriptor := described.Descriptor()
	return &descriptor
}

func (s *SettingsService) prepareProviderConfig(
	ctx context.Context,
	name string,
	request ProviderSettingsRequest,
) (cmfcaptcha.Provider, map[string]any, map[string]any, string, error) {
	item, ok := s.registry.Get(name)
	if !ok {
		return nil, nil, nil, "", fmt.Errorf(
			"%w: unknown provider %q",
			cmfcaptcha.ErrProviderMisconfigured,
			name,
		)
	}
	publicConfig, err := decodeConfigObject(request.Config)
	if err != nil {
		return nil, nil, nil, "", err
	}
	secretField := providerSecretField(name)
	delete(publicConfig, secretField)
	merged := cloneConfig(publicConfig)
	existingSecret, err := s.existingSecret(ctx, name, secretField)
	if err != nil {
		return nil, nil, nil, "", err
	}
	if existingSecret != "" {
		merged[secretField] = existingSecret
	}
	if request.ClearSecret {
		delete(merged, secretField)
	} else if request.Secret != "" {
		merged[secretField] = request.Secret
	}
	return item, merged, publicConfig, secretField, nil
}

func (s *SettingsService) existingSecret(ctx context.Context, name, field string) (string, error) {
	if s.secrets == nil {
		return "", nil
	}
	var existing map[string]any
	err := s.secrets.GetJSON(ctx, providerKeyPrefix+name, &existing)
	if ent.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	value, _ := existing[field].(string)
	return value, nil
}

func decodeConfigObject(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return make(map[string]any), nil
	}
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil || config == nil {
		return nil, cmfcaptcha.ErrProviderMisconfigured
	}
	return config, nil
}

func providerSecretField(name string) string {
	if name == "geetest_gt4" {
		return "captcha_key"
	}
	return "secret"
}

func cloneConfig(config map[string]any) map[string]any {
	cloned := make(map[string]any, len(config))
	for key, value := range config {
		cloned[key] = value
	}
	return cloned
}
