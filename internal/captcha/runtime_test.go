package captcha

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wuwuseo/cmf/config"
)

func TestNewRuntimeUsesMemoryStoreWithoutMasterKey(t *testing.T) {
	client := openCaptchaTestClient(t)
	cfg := config.NewConfig()
	cfg.Captcha.Store = "memory"

	runtime, err := NewRuntime(t.Context(), client, cfg, func(string) string { return "" })
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	if runtime.Service == nil || runtime.Settings == nil || runtime.Registry == nil {
		t.Fatalf("NewRuntime() = %+v", runtime)
	}
	if runtime.Secrets != nil {
		t.Fatal("NewRuntime() configured secrets without a master key")
	}
	if _, err := runtime.Settings.UpdateProvider(t.Context(), "geetest_gt4", ProviderSettingsRequest{
		Config: json.RawMessage(`{"captcha_id":"public-id","timeout_ms":1500}`),
		Secret: "private-key",
	}); err == nil || !strings.Contains(err.Error(), "master key") {
		t.Fatalf("UpdateProvider() error = %v", err)
	}
}

func TestNewRuntimeRejectsMemoryStoreInPrefork(t *testing.T) {
	client := openCaptchaTestClient(t)
	cfg := config.NewConfig()
	cfg.Captcha.Store = "memory"
	cfg.App.Prefork = true

	_, err := NewRuntime(t.Context(), client, cfg, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "redis") {
		t.Fatalf("NewRuntime() error = %v", err)
	}
}

func TestNewRuntimeRejectsUnknownStore(t *testing.T) {
	client := openCaptchaTestClient(t)
	cfg := config.NewConfig()
	cfg.Captcha.Store = "unknown"

	if _, err := NewRuntime(t.Context(), client, cfg, func(string) string { return "" }); err == nil {
		t.Fatal("NewRuntime() accepted an unknown store")
	}
}
