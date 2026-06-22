package handler

import (
	"encoding/json"

	"github.com/gofiber/fiber/v3"
	"github.com/wuwuseo/cmf/captcha/fiberadapter"
)

const captchaFiberAdapterLocal = "captcha.FiberAdapter"

type CaptchaChallengeRequest struct {
	Scene string `json:"scene" example:"user_login"`
}

type CaptchaChallengeResponse struct {
	Required    bool            `json:"required"`
	ChallengeID string          `json:"challenge_id,omitempty"`
	Provider    string          `json:"provider,omitempty"`
	ExpiresIn   int64           `json:"expires_in,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty" swaggertype:"object"`
}

type CaptchaVerificationRequest struct {
	ChallengeID string          `json:"challenge_id"`
	Response    json.RawMessage `json:"response" swaggertype:"object"`
}

type CaptchaVerificationResponse struct {
	CaptchaToken string `json:"captcha_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// CreateCaptchaChallenge creates a challenge for a fixed business scene.
// @Summary 创建验证码挑战
// @Tags 验证码
// @Accept json
// @Produce json
// @Param body body CaptchaChallengeRequest true "挑战请求"
// @Success 200 {object} utils.Response{data=CaptchaChallengeResponse}
// @Failure 400 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Failure 503 {object} utils.Response
// @Router /api/v1/public/captcha/challenges [post]
func CreateCaptchaChallenge(c fiber.Ctx) error {
	return captchaFiberAdapter(c).ChallengeHandler()(c)
}

// VerifyCaptchaChallenge verifies a challenge and issues a one-time proof token.
// @Summary 验证验证码挑战
// @Tags 验证码
// @Accept json
// @Produce json
// @Param body body CaptchaVerificationRequest true "验证请求"
// @Success 200 {object} utils.Response{data=CaptchaVerificationResponse}
// @Failure 400 {object} utils.Response
// @Failure 500 {object} utils.Response
// @Failure 503 {object} utils.Response
// @Router /api/v1/public/captcha/verifications [post]
func VerifyCaptchaChallenge(c fiber.Ctx) error {
	return captchaFiberAdapter(c).VerificationHandler()(c)
}

func captchaFiberAdapter(c fiber.Ctx) *fiberadapter.Adapter {
	return c.Locals(captchaFiberAdapterLocal).(*fiberadapter.Adapter)
}
