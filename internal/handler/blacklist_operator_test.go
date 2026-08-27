package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	jwtlib "github.com/golang-jwt/jwt/v5"
	cmfjwt "github.com/wuwuseo/cmf/jwt"
)

func TestGetOperatorNameReadsJWTClaims(t *testing.T) {
	const secret = "blacklist-operator-test-secret"
	app := fiber.New()
	app.Use(cmfjwt.NewJWTMiddleware(secret))
	app.Get("/", func(c fiber.Ctx) error {
		return c.SendString(getOperatorName(c))
	})

	token, err := cmfjwt.CreateToken(jwtlib.MapClaims{
		"id":      "admin-uid",
		"subject": "admin",
		"domain":  "default",
		"exp":     time.Now().Add(time.Hour).Unix(),
	}, secret)
	if err != nil {
		t.Fatalf("CreateToken() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := app.Test(request)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(body) != "admin" {
		t.Fatalf("operator name = %q, want admin", body)
	}
}
