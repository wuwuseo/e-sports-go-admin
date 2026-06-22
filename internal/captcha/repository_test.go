package captcha

import (
	"context"
	"encoding/base64"
	"testing"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"

	_ "github.com/mattn/go-sqlite3"
	"github.com/wuwuseo/cmf/secureconfig"
)

func openCaptchaTestClient(t *testing.T) *ent.Client {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:"+t.Name()+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestSystemConfigRepositoryPutGetListAndCompareAndSwap(t *testing.T) {
	client := openCaptchaTestClient(t)
	repository := NewSystemConfigRepository(client)

	if err := repository.Put(t.Context(), "captcha.provider.geetest_gt4", "enc:v1:first"); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if got, err := repository.Get(t.Context(), "captcha.provider.geetest_gt4"); err != nil || got != "enc:v1:first" {
		t.Fatalf("Get() = %q, %v", got, err)
	}
	if err := repository.Put(t.Context(), "captcha.provider.geetest_gt4", "enc:v1:second"); err != nil {
		t.Fatalf("Put(update) error = %v", err)
	}
	entries, err := repository.ListPrefix(t.Context(), "captcha.provider.")
	if err != nil {
		t.Fatalf("ListPrefix() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Value != "enc:v1:second" {
		t.Fatalf("ListPrefix() = %+v", entries)
	}
	swapped, err := repository.CompareAndSwap(
		t.Context(),
		"captcha.provider.geetest_gt4",
		"enc:v1:first",
		"enc:v2:new",
	)
	if err != nil {
		t.Fatalf("CompareAndSwap(stale) error = %v", err)
	}
	if swapped {
		t.Fatal("CompareAndSwap(stale) = true, want false")
	}
	swapped, err = repository.CompareAndSwap(
		t.Context(),
		"captcha.provider.geetest_gt4",
		"enc:v1:second",
		"enc:v2:new",
	)
	if err != nil || !swapped {
		t.Fatalf("CompareAndSwap() = %v, %v", swapped, err)
	}
}

func TestDynamicSourcesReadSceneAndEncryptedProviderConfig(t *testing.T) {
	client := openCaptchaTestClient(t)
	repository := NewSystemConfigRepository(client)
	keyring, err := secureconfig.NewKeyring("v1", map[string]string{
		"v1": base64.StdEncoding.EncodeToString(make([]byte, 32)),
	})
	if err != nil {
		t.Fatalf("NewKeyring() error = %v", err)
	}
	manager := secureconfig.NewManager(repository, secureconfig.NewAESGCMCipher(keyring))
	sources := NewDynamicSources(client, manager)

	if err := sources.SaveScene(t.Context(), "admin_login", SceneSettings{
		Enabled:      true,
		Provider:     "image_math",
		ChallengeTTL: 120,
		ProofTTL:     90,
	}); err != nil {
		t.Fatalf("SaveScene() error = %v", err)
	}
	if err := manager.SetJSON(
		t.Context(),
		"captcha.provider.image_math",
		map[string]any{"secret": "0123456789abcdef"},
	); err != nil {
		t.Fatalf("SetJSON() error = %v", err)
	}

	policy, err := sources.Policy(context.Background(), "admin_login")
	if err != nil {
		t.Fatalf("Policy() error = %v", err)
	}
	if !policy.Enabled || policy.Provider != "image_math" {
		t.Fatalf("Policy() = %+v", policy)
	}
	config, err := sources.Config(context.Background(), "image_math")
	if err != nil {
		t.Fatalf("Config() error = %v", err)
	}
	if string(config) == "" || string(config) == "enc:v1:first" {
		t.Fatalf("Config() = %s", config)
	}
}
