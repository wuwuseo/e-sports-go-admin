package captcha

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	cmfcaptcha "github.com/wuwuseo/cmf/captcha"
	"github.com/wuwuseo/cmf/captcha/provider"
	"github.com/wuwuseo/cmf/secureconfig"
)

func newSettingsTestService(t *testing.T) (*SettingsService, *SystemConfigRepository) {
	t.Helper()
	client := openCaptchaTestClient(t)
	repository := NewSystemConfigRepository(client)
	keyring, err := secureconfig.NewKeyring("v1", map[string]string{
		"v1": base64.StdEncoding.EncodeToString(make([]byte, 32)),
	})
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	manager := secureconfig.NewManager(repository, secureconfig.NewAESGCMCipher(keyring))
	registry := cmfcaptcha.NewRegistry()
	for _, item := range []cmfcaptcha.Provider{
		provider.NewAlphanumeric(),
		provider.NewMath(),
		provider.NewChineseInput(),
		provider.NewChineseClick(),
		provider.NewGeeTestGT4(nil),
	} {
		if err := registry.Register(item); err != nil {
			t.Fatalf("Register(%s) error = %v", item.Name(), err)
		}
	}
	return NewSettingsService(
		NewDynamicSources(client, manager),
		manager,
		registry,
		[]string{"user_login", "admin_login"},
	), repository
}

func TestSettingsServiceUpdateProviderMasksSecretAndPreservesBlank(t *testing.T) {
	service, repository := newSettingsTestService(t)
	request := ProviderSettingsRequest{
		Config: json.RawMessage(`{"captcha_id":"public-id","timeout_ms":1500}`),
		Secret: "private-key",
	}
	response, err := service.UpdateProvider(t.Context(), "geetest_gt4", request)
	if err != nil {
		t.Fatalf("UpdateProvider() error = %v", err)
	}
	if !response.SecretConfigured || response.KeyVersion != "v1" {
		t.Fatalf("UpdateProvider() response = %+v", response)
	}
	if strings.Contains(string(response.Config), "private-key") {
		t.Fatal("UpdateProvider() response leaked secret")
	}
	stored, err := repository.Get(t.Context(), "captcha.provider.geetest_gt4")
	if err != nil {
		t.Fatalf("repository.Get() error = %v", err)
	}
	if strings.Contains(stored, "private-key") || !strings.HasPrefix(stored, "enc:v1:") {
		t.Fatalf("stored provider config = %q", stored)
	}

	response, err = service.UpdateProvider(t.Context(), "geetest_gt4", ProviderSettingsRequest{
		Config: json.RawMessage(`{"captcha_id":"new-public-id","timeout_ms":2000}`),
	})
	if err != nil {
		t.Fatalf("UpdateProvider(blank secret) error = %v", err)
	}
	if !response.SecretConfigured {
		t.Fatal("blank secret did not preserve existing secret")
	}
}

func TestSettingsServiceRejectsUnknownSceneAndProvider(t *testing.T) {
	service, _ := newSettingsTestService(t)
	if _, err := service.UpdateScene(t.Context(), "unknown", SceneSettings{}); err == nil {
		t.Fatal("UpdateScene(unknown) succeeded")
	}
	if _, err := service.UpdateProvider(t.Context(), "unknown", ProviderSettingsRequest{}); err == nil {
		t.Fatal("UpdateProvider(unknown) succeeded")
	}
}

func TestSettingsServiceSceneRoundTrip(t *testing.T) {
	service, _ := newSettingsTestService(t)
	if _, err := service.UpdateProvider(t.Context(), "image_math", ProviderSettingsRequest{
		Config: json.RawMessage(`{"operators":["+","-"],"max_operand":20}`),
		Secret: "0123456789abcdef",
	}); err != nil {
		t.Fatalf("UpdateProvider() error = %v", err)
	}
	response, err := service.UpdateScene(t.Context(), "admin_login", SceneSettings{
		Enabled:      true,
		Provider:     "image_math",
		ChallengeTTL: 120,
		ProofTTL:     90,
	})
	if err != nil {
		t.Fatalf("UpdateScene() error = %v", err)
	}
	if !response.Enabled || response.Provider != "image_math" {
		t.Fatalf("UpdateScene() = %+v", response)
	}
}

func TestSettingsServiceGetSettingsMasksProviderSecrets(t *testing.T) {
	service, _ := newSettingsTestService(t)
	if _, err := service.UpdateScene(t.Context(), "user_login", SceneSettings{
		Enabled:  false,
		Provider: "image_alphanumeric",
	}); err != nil {
		t.Fatalf("UpdateScene() error = %v", err)
	}
	if _, err := service.UpdateProvider(t.Context(), "image_alphanumeric", ProviderSettingsRequest{
		Config: json.RawMessage(`{"length":5,"width":180,"height":64}`),
		Secret: "0123456789abcdef",
	}); err != nil {
		t.Fatalf("UpdateProvider() error = %v", err)
	}

	settings, err := service.GetSettings(t.Context())
	if err != nil {
		t.Fatalf("GetSettings() error = %v", err)
	}
	if settings.Scenes["user_login"].ChallengeTTL != 120 {
		t.Fatalf("GetSettings() scene = %+v", settings.Scenes["user_login"])
	}
	item := settings.Providers["image_alphanumeric"]
	if !item.SecretConfigured || item.KeyVersion != "v1" {
		t.Fatalf("GetSettings() provider = %+v", item)
	}
	if len(settings.Providers) != 5 {
		t.Fatalf("GetSettings() provider count = %d", len(settings.Providers))
	}
	if settings.Providers["geetest_gt4"].SecretConfigured {
		t.Fatal("unconfigured provider reported a secret")
	}
	if strings.Contains(string(item.Config), "0123456789abcdef") {
		t.Fatal("GetSettings() leaked provider secret")
	}
	for name, providerSettings := range settings.Providers {
		if providerSettings.Descriptor == nil {
			t.Fatalf("provider %s has no descriptor", name)
		}
		if providerSettings.Descriptor.ChallengeType == "" ||
			providerSettings.Descriptor.DisplayName["zh-CN"] == "" ||
			providerSettings.Descriptor.DisplayName["en-US"] == "" {
			t.Fatalf("provider %s descriptor = %#v", name, providerSettings.Descriptor)
		}
	}
}

func TestSettingsServiceClearSecretRequiresExplicitFlag(t *testing.T) {
	service, _ := newSettingsTestService(t)
	if _, err := service.UpdateProvider(t.Context(), "geetest_gt4", ProviderSettingsRequest{
		Config: json.RawMessage(`{"captcha_id":"public-id","timeout_ms":1500}`),
		Secret: "private-key",
	}); err != nil {
		t.Fatalf("UpdateProvider() error = %v", err)
	}
	response, err := service.UpdateProvider(t.Context(), "geetest_gt4", ProviderSettingsRequest{
		Config:      json.RawMessage(`{"captcha_id":"public-id","timeout_ms":1500}`),
		ClearSecret: true,
	})
	if err != nil {
		t.Fatalf("UpdateProvider(clear secret) error = %v", err)
	}
	if response.SecretConfigured {
		t.Fatal("UpdateProvider(clear secret) retained secret")
	}
	if err := service.ValidateProviderConfig(t.Context(), "geetest_gt4", ProviderSettingsRequest{
		Config: json.RawMessage(`{"captcha_id":"public-id","timeout_ms":1500}`),
	}); err == nil {
		t.Fatal("ValidateProviderConfig() accepted cleared secret")
	}
}

func TestSettingsServiceRejectsEnablingUnconfiguredProvider(t *testing.T) {
	service, _ := newSettingsTestService(t)
	if _, err := service.UpdateScene(t.Context(), "user_login", SceneSettings{
		Enabled:  true,
		Provider: "image_math",
	}); err == nil {
		t.Fatal("UpdateScene() enabled an unconfigured provider")
	}
}

func TestSettingsServiceValidateProviderConfig(t *testing.T) {
	service, _ := newSettingsTestService(t)
	if err := service.ValidateProviderConfig(t.Context(), "geetest_gt4", ProviderSettingsRequest{
		Config: json.RawMessage(`{"captcha_id":"public-id","timeout_ms":1500}`),
		Secret: "private-key",
	}); err != nil {
		t.Fatalf("ValidateProviderConfig() error = %v", err)
	}
	if err := service.ValidateProviderConfig(t.Context(), "geetest_gt4", ProviderSettingsRequest{
		Config: json.RawMessage(`{"captcha_id":"public-id","timeout_ms":1500}`),
	}); err == nil {
		t.Fatal("ValidateProviderConfig() accepted missing secret")
	}
}
