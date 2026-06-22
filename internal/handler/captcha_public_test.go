package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appcaptcha "go-admin/internal/captcha"
	"go-admin/internal/utils"

	"github.com/gofiber/fiber/v3"
	cmfcaptcha "github.com/wuwuseo/cmf/captcha"
)

type publicCaptchaServiceStub struct{}

func (publicCaptchaServiceStub) CreateChallenge(
	context.Context,
	string,
	cmfcaptcha.ClientMeta,
) (cmfcaptcha.ChallengeResult, error) {
	return cmfcaptcha.ChallengeResult{Required: false}, nil
}

func (publicCaptchaServiceStub) VerifyChallenge(
	context.Context,
	string,
	json.RawMessage,
	cmfcaptcha.ClientMeta,
) (cmfcaptcha.ProofResult, error) {
	return cmfcaptcha.ProofResult{Token: "proof", ExpiresIn: 90}, nil
}

func (publicCaptchaServiceStub) ConsumeProof(context.Context, string, string) error {
	return nil
}

func TestPublicCaptchaHandlersDelegateToInjectedAdapter(t *testing.T) {
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("utils.Response", utils.NewResponse())
		c.Locals("captcha.FiberAdapter", appcaptcha.NewFiberAdapter(publicCaptchaServiceStub{}))
		return c.Next()
	})
	app.Post("/challenges", CreateCaptchaChallenge)
	app.Post("/verifications", VerifyCaptchaChallenge)

	for _, test := range []struct {
		path string
		body string
	}{
		{path: "/challenges", body: `{"scene":"user_login"}`},
		{path: "/verifications", body: `{"challenge_id":"challenge","response":{"answer":"ok"}}`},
	} {
		request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
		request.Header.Set("Content-Type", "application/json")
		response, err := app.Test(request)
		if err != nil {
			t.Fatalf("POST %s: %v", test.path, err)
		}
		var envelope struct {
			Code int `json:"code"`
		}
		if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode %s: %v", test.path, err)
		}
		if envelope.Code != 1 {
			t.Fatalf("POST %s code = %d", test.path, envelope.Code)
		}
	}
}
