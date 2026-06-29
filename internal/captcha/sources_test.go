package captcha

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	cmfcaptcha "github.com/wuwuseo/cmf/captcha"
	"github.com/wuwuseo/cmf/secureconfig"
)

func newSourcesTestPair(t *testing.T) (*DynamicSources, *SystemConfigRepository) {
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
	return NewDynamicSources(client, manager), repository
}

func TestDynamicSourcesPolicyRoundTrip(t *testing.T) {
	sources, _ := newSourcesTestPair(t)
	settings := SceneSettings{
		Enabled:      true,
		Provider:     "image_math",
		ChallengeTTL: 120,
		ProofTTL:     90,
	}
	if err := sources.SaveScene(t.Context(), "user_login", settings); err != nil {
		t.Fatalf("SaveScene() error = %v", err)
	}
	policy, err := sources.Policy(t.Context(), "user_login")
	if err != nil {
		t.Fatalf("Policy() error = %v", err)
	}
	if !policy.Enabled || policy.Provider != "image_math" {
		t.Fatalf("Policy() = %+v", policy)
	}
	if policy.ChallengeTTL.Seconds() != 120 || policy.ProofTTL.Seconds() != 90 {
		t.Fatalf("Policy() TTL = %+v", policy)
	}
}

func TestDynamicSourcesPolicyRejectsUnknownScene(t *testing.T) {
	sources, _ := newSourcesTestPair(t)
	_, err := sources.Policy(t.Context(), "nonexistent_scene")
	if err == nil {
		t.Fatal("Policy() succeeded for unknown scene")
	}
	if !errors.Is(err, cmfcaptcha.ErrSceneInvalid) {
		t.Fatalf("Policy() err = %v, want ErrSceneInvalid", err)
	}
}

func TestDynamicSourcesPolicyRejectsInvalidJSON(t *testing.T) {
	sources, repo := newSourcesTestPair(t)
	if err := repo.Put(t.Context(), sceneKeyPrefix+"broken_scene", "{not valid json"); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	_, err := sources.Policy(t.Context(), "broken_scene")
	if err == nil {
		t.Fatal("Policy() succeeded with invalid JSON")
	}
	if !errors.Is(err, cmfcaptcha.ErrProviderMisconfigured) {
		t.Fatalf("Policy() err = %v, want ErrProviderMisconfigured", err)
	}
}

func TestDynamicSourcesPolicyRejectsNegativeTTL(t *testing.T) {
	sources, _ := newSourcesTestPair(t)
	badSettings := SceneSettings{
		Enabled:      true,
		Provider:     "image_math",
		ChallengeTTL: -1,
		ProofTTL:     90,
	}
	if err := sources.SaveScene(t.Context(), "bad_ttl", badSettings); err == nil {
		t.Fatal("SaveScene() accepted negative TTL")
	}
}

func TestDynamicSourcesSaveSceneRejectsEmptyScene(t *testing.T) {
	sources, _ := newSourcesTestPair(t)
	err := sources.SaveScene(t.Context(), "", SceneSettings{
		Enabled:      true,
		Provider:     "image_math",
		ChallengeTTL: 120,
		ProofTTL:     90,
	})
	if err == nil {
		t.Fatal("SaveScene() accepted empty scene name")
	}
	if !errors.Is(err, cmfcaptcha.ErrSceneInvalid) {
		t.Fatalf("SaveScene() err = %v, want ErrSceneInvalid", err)
	}
}

func TestDynamicSourcesSaveSceneRejectsEnabledWithoutProvider(t *testing.T) {
	sources, _ := newSourcesTestPair(t)
	err := sources.SaveScene(t.Context(), "no_provider", SceneSettings{
		Enabled:      true,
		Provider:     "",
		ChallengeTTL: 120,
		ProofTTL:     90,
	})
	if err == nil {
		t.Fatal("SaveScene() accepted enabled scene without provider")
	}
	if !errors.Is(err, cmfcaptcha.ErrProviderMisconfigured) {
		t.Fatalf("SaveScene() err = %v, want ErrProviderMisconfigured", err)
	}
}

func TestDynamicSourcesConfigRequiresSecretsManager(t *testing.T) {
	client := openCaptchaTestClient(t)
	sources := NewDynamicSources(client, nil)
	_, err := sources.Config(t.Context(), "image_math")
	if err == nil {
		t.Fatal("Config() succeeded without secrets manager")
	}
	if !errors.Is(err, cmfcaptcha.ErrProviderMisconfigured) {
		t.Fatalf("Config() err = %v, want ErrProviderMisconfigured", err)
	}
}

func TestDynamicSourcesConfigRejectsUnconfiguredProvider(t *testing.T) {
	sources, _ := newSourcesTestPair(t)
	_, err := sources.Config(t.Context(), "never_configured_provider")
	if err == nil {
		t.Fatal("Config() succeeded for unconfigured provider")
	}
	if !errors.Is(err, cmfcaptcha.ErrProviderMisconfigured) {
		t.Fatalf("Config() err = %v, want ErrProviderMisconfigured", err)
	}
}

func TestDynamicSourcesConfigRoundTrip(t *testing.T) {
	_, repo := newSourcesTestPair(t)
	client := openCaptchaTestClient(t)
	configData := map[string]any{
		"captcha_id":  "test-id",
		"captcha_key": "test-secret",
	}

	keyring, _ := secureconfig.NewKeyring("v1", map[string]string{
		"v1": base64.StdEncoding.EncodeToString(make([]byte, 32)),
	})
	manager := secureconfig.NewManager(repo, secureconfig.NewAESGCMCipher(keyring))
	if err := manager.SetJSON(t.Context(), providerKeyPrefix+"geetest_gt4", configData); err != nil {
		t.Fatalf("SetJSON() error = %v", err)
	}
	sourcesWithSecrets := NewDynamicSources(client, manager)
	result, err := sourcesWithSecrets.Config(t.Context(), "geetest_gt4")
	if err != nil {
		t.Fatalf("Config() error = %v", err)
	}
	if len(result) == 0 {
		t.Fatal("Config() returned empty config")
	}
	var decoded map[string]any
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatalf("Config() result not valid JSON: %v", err)
	}
	if decoded["captcha_id"] != "test-id" {
		t.Fatalf("Config() captcha_id = %v", decoded["captcha_id"])
	}
}

func TestProviderSecretFieldMapping(t *testing.T) {
	if providerSecretField("geetest_gt4") != "captcha_key" {
		t.Fatal("geetest_gt4 secret field should be captcha_key")
	}
	if providerSecretField("image_math") != "secret" {
		t.Fatal("default secret field should be secret")
	}
	if providerSecretField("") != "secret" {
		t.Fatal("empty name secret field should be secret")
	}
}

func TestDecodeConfigObject(t *testing.T) {
	tests := []struct {
		name    string
		input   json.RawMessage
		wantErr bool
		check   func(map[string]any) bool
	}{
		{
			name:    "empty raw returns empty map",
			input:   json.RawMessage{},
			wantErr: false,
			check:   func(m map[string]any) bool { return len(m) == 0 },
		},
		{
			name:    "null raw returns error (nil map)",
			input:   json.RawMessage(`null`),
			wantErr: true,
		},
		{
			name:    "valid object",
			input:   json.RawMessage(`{"key":"val"}`),
			wantErr: false,
			check:   func(m map[string]any) bool { return m["key"] == "val" },
		},
		{
			name:    "invalid JSON",
			input:   json.RawMessage(`{broken`),
			wantErr: true,
		},
		{
			name:    "array instead of object",
			input:   json.RawMessage(`[1,2,3]`),
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := decodeConfigObject(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("decodeConfigObject() err = %v, wantErr = %v", err, tt.wantErr)
			}
			if !tt.wantErr && tt.check != nil && !tt.check(result) {
				t.Fatalf("decodeConfigObject() = %+v", result)
			}
		})
	}
}

func TestCloneConfig(t *testing.T) {
	original := map[string]any{"a": 1, "b": "two"}
	cloned := cloneConfig(original)
	if len(cloned) != 2 || cloned["a"] != 1 || cloned["b"] != "two" {
		t.Fatalf("cloneConfig() = %+v", cloned)
	}
	cloned["a"] = 99
	if original["a"] != 1 {
		t.Fatal("cloneConfig() did not create independent copy")
	}
}

func TestCaptchaResponseCoversAllErrorCodes(t *testing.T) {
	codes := []struct {
		code cmfcaptcha.ErrorCode
		msg  string
	}{
		{cmfcaptcha.CodeRequired, "需要完成验证码"},
		{cmfcaptcha.CodeSceneInvalid, "验证码场景无效"},
		{cmfcaptcha.CodeUnavailable, "验证码服务暂不可用"},
		{cmfcaptcha.CodeMisconfigured, "验证码服务配置错误"},
		{cmfcaptcha.CodeInvalid, "验证码无效或已过期"},
	}
	for _, c := range codes {
		_, msg := captchaResponse(c.code)
		if !strings.Contains(msg, c.msg[:2]) {
			t.Fatalf("captchaResponse(%v) = %q, want %q", c.code, msg, c.msg)
		}
	}
}
