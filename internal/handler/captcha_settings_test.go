package handler

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appcaptcha "go-admin/internal/captcha"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	_ "github.com/mattn/go-sqlite3"
	cmfcaptcha "github.com/wuwuseo/cmf/captcha"
	"github.com/wuwuseo/cmf/captcha/provider"
	"github.com/wuwuseo/cmf/secureconfig"
)

func newCaptchaSettingsHandlerApp(t *testing.T) *fiber.App {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:"+t.Name()+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })
	repository := appcaptcha.NewSystemConfigRepository(client)
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
	settings := appcaptcha.NewSettingsService(
		appcaptcha.NewDynamicSources(client, manager),
		manager,
		registry,
		[]string{"user_login", "admin_login"},
	)
	app := fiber.New()
	group := app.Group("/captcha", func(c fiber.Ctx) error {
		c.Locals("utils.Response", utils.NewResponse())
		c.Locals("captcha.SettingsService", settings)
		return c.Next()
	})
	group.Get("/settings", GetCaptchaSettings)
	group.Put("/scenes/:scene", UpdateCaptchaScene)
	group.Put("/providers/:provider", UpdateCaptchaProvider)
	group.Post("/providers/:provider/test", ValidateCaptchaProvider)
	return app
}

func TestCaptchaSettingsHandlersMaskProviderSecret(t *testing.T) {
	app := newCaptchaSettingsHandlerApp(t)
	body := `{"config":{"captcha_id":"public-id","timeout_ms":1500},"secret":"private-key"}`
	request := httptest.NewRequest(
		http.MethodPut,
		"/captcha/providers/geetest_gt4",
		bytes.NewBufferString(body),
	)
	request.Header.Set("Content-Type", "application/json")
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("app.Test(update) error = %v", err)
	}
	updateBody, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d body = %s", response.StatusCode, updateBody)
	}
	if strings.Contains(string(updateBody), "private-key") {
		t.Fatalf("update response leaked secret: %s", updateBody)
	}

	response, err = app.Test(httptest.NewRequest(http.MethodGet, "/captcha/settings", nil))
	if err != nil {
		t.Fatalf("app.Test(get) error = %v", err)
	}
	getBody, _ := io.ReadAll(response.Body)
	if strings.Contains(string(getBody), "private-key") {
		t.Fatalf("settings response leaked secret: %s", getBody)
	}
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Providers map[string]appcaptcha.ProviderSettingsResponse `json:"providers"`
		} `json:"data"`
	}
	if err := json.Unmarshal(getBody, &envelope); err != nil {
		t.Fatalf("decode settings response: %v body=%s", err, getBody)
	}
	item := envelope.Data.Providers["geetest_gt4"]
	if envelope.Code != 1 || !item.SecretConfigured || item.KeyVersion != "v1" {
		t.Fatalf("settings response = %+v body=%s", envelope, getBody)
	}
}

func TestValidateCaptchaProviderRejectsIncompleteConfig(t *testing.T) {
	app := newCaptchaSettingsHandlerApp(t)
	request := httptest.NewRequest(
		http.MethodPost,
		"/captcha/providers/geetest_gt4/test",
		bytes.NewBufferString(`{"config":{"captcha_id":"public-id","timeout_ms":1500}}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	var envelope struct {
		Code int `json:"code"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Code == 1 {
		t.Fatal("incomplete provider config was accepted")
	}
}
