package captcha

import (
	"encoding/base64"
	"testing"

	"github.com/wuwuseo/cmf/secureconfig"
)

func TestReencryptProviderConfigsSupportsDryRunAndRotation(t *testing.T) {
	client := openCaptchaTestClient(t)
	repository := NewSystemConfigRepository(client)
	v1, err := secureconfig.NewKeyring("v1", map[string]string{
		"v1": base64.StdEncoding.EncodeToString(make([]byte, 32)),
	})
	if err != nil {
		t.Fatalf("NewKeyring(v1) error = %v", err)
	}
	oldManager := secureconfig.NewManager(repository, secureconfig.NewAESGCMCipher(v1))
	if err := oldManager.SetJSON(
		t.Context(),
		"captcha.provider.geetest_gt4",
		map[string]any{"captcha_id": "id", "captcha_key": "secret"},
	); err != nil {
		t.Fatalf("SetJSON() error = %v", err)
	}

	keyV2 := make([]byte, 32)
	for i := range keyV2 {
		keyV2[i] = 2
	}
	lookup := func(name string) string {
		switch name {
		case "CMF_CONFIG_SECRET_ACTIVE_VERSION":
			return "v2"
		case "CMF_CONFIG_SECRET_KEY_V1":
			return base64.StdEncoding.EncodeToString(make([]byte, 32))
		case "CMF_CONFIG_SECRET_KEY_V2":
			return base64.StdEncoding.EncodeToString(keyV2)
		default:
			return ""
		}
	}
	report, err := ReencryptProviderConfigs(t.Context(), client, lookup, true)
	if err != nil {
		t.Fatalf("ReencryptProviderConfigs(dry-run) error = %v", err)
	}
	if report.WouldReencrypt != 1 || report.Reencrypted != 0 {
		t.Fatalf("dry-run report = %+v", report)
	}
	report, err = ReencryptProviderConfigs(t.Context(), client, lookup, false)
	if err != nil {
		t.Fatalf("ReencryptProviderConfigs() error = %v", err)
	}
	if report.Reencrypted != 1 {
		t.Fatalf("rotation report = %+v", report)
	}
	metadata, err := secureconfig.NewManager(
		repository,
		secureconfig.NewAESGCMCipher(mustKeyringFromLookup(t, lookup)),
	).Inspect(t.Context(), "captcha.provider.geetest_gt4")
	if err != nil || metadata.KeyVersion != "v2" {
		t.Fatalf("rotated metadata = %+v, %v", metadata, err)
	}
}

func mustKeyringFromLookup(t *testing.T, lookup func(string) string) *secureconfig.Keyring {
	t.Helper()
	keyring, err := secureconfig.NewKeyringFromLookup(lookup)
	if err != nil {
		t.Fatalf("NewKeyringFromLookup() error = %v", err)
	}
	return keyring
}
