package captcha

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	cmfcaptcha "github.com/wuwuseo/cmf/captcha"
)

type adapterServiceStub struct {
	consumeScene string
	consumeToken string
	consumeErr   error
}

func (s *adapterServiceStub) CreateChallenge(
	context.Context,
	string,
	cmfcaptcha.ClientMeta,
) (cmfcaptcha.ChallengeResult, error) {
	return cmfcaptcha.ChallengeResult{Required: false}, nil
}

func (s *adapterServiceStub) VerifyChallenge(
	context.Context,
	string,
	json.RawMessage,
	cmfcaptcha.ClientMeta,
) (cmfcaptcha.ProofResult, error) {
	return cmfcaptcha.ProofResult{}, nil
}

func (s *adapterServiceStub) ConsumeProof(_ context.Context, scene, token string) error {
	s.consumeScene = scene
	s.consumeToken = token
	return s.consumeErr
}

func TestNewFiberAdapterUsesAdminEnvelopeAndCaptchaHeader(t *testing.T) {
	service := &adapterServiceStub{}
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("utils.Response", utils.NewResponse())
		return c.Next()
	})
	app.Post("/login", NewFiberAdapter(service).Require("user_login"), func(c fiber.Ctx) error {
		return c.JSON(map[string]bool{"ok": true})
	})
	request := httptest.NewRequest(http.MethodPost, "/login", nil)
	request.Header.Set("X-Captcha-Token", "proof-token")
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if service.consumeScene != "user_login" || service.consumeToken != "proof-token" {
		t.Fatalf("ConsumeProof() = %q, %q", service.consumeScene, service.consumeToken)
	}
}

func TestNewFiberAdapterMapsCaptchaErrorsToStableAdminCode(t *testing.T) {
	service := &adapterServiceStub{consumeErr: cmfcaptcha.ErrRequired}
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("utils.Response", utils.NewResponse())
		return c.Next()
	})
	app.Post("/login", NewFiberAdapter(service).Require("user_login"), func(c fiber.Ctx) error {
		return c.SendStatus(http.StatusNoContent)
	})
	response, err := app.Test(httptest.NewRequest(http.MethodPost, "/login", nil))
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	var envelope struct {
		Code int `json:"code"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Code != 4601 {
		t.Fatalf("captcha required code = %d", envelope.Code)
	}
}
