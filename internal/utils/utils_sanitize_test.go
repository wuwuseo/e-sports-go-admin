package utils

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func TestIsSensitiveKey_DefaultKeys(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"password", true},
		{"Password", true},
		{"PASSWORD", true},
		{"old_password", true},
		{"new_password", true},
		{"secret", true},
		{"client_secret", true},
		{"token", true},
		{"access_token", true},
		{"refresh_token", true},
		{"api_key", true},
		{"apikey", true},
		{"private_key", true},
		{"salt", true},
		{"authorization", true},
		{"username", false},
		{"email", false},
		{"id", false},
		{"name", false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := isSensitiveKey(tt.key, nil)
			if got != tt.want {
				t.Errorf("isSensitiveKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestIsSensitiveKey_ExtraKeys(t *testing.T) {
	extra := []string{"custom_secret", "apiToken"}

	if !isSensitiveKey("custom_secret", extra) {
		t.Fatal("custom_secret should be sensitive with extra keys")
	}
	if !isSensitiveKey("APITOKEN", extra) {
		t.Fatal("APITOKEN should be sensitive (case insensitive) with extra keys")
	}
	if isSensitiveKey("normal_field", extra) {
		t.Fatal("normal_field should not be sensitive")
	}
}

func TestRedactSensitiveFields_JSONFormat(t *testing.T) {
	input := `{"username":"admin","password":"secret123","email":"test@example.com"}`
	output := redactSensitiveFields(input)

	if strings.Contains(output, "secret123") {
		t.Fatalf("password should be redacted, got: %s", output)
	}
	if !strings.Contains(output, `"password":"***REDACTED***"`) {
		t.Fatalf("password should be replaced with ***REDACTED***, got: %s", output)
	}
	if !strings.Contains(output, `"username":"admin"`) {
		t.Fatalf("username should be preserved, got: %s", output)
	}
}

func TestRedactSensitiveFields_FormFormat(t *testing.T) {
	input := `username=admin&password=secret123&email=test@example.com`
	output := redactSensitiveFields(input)

	if strings.Contains(output, "secret123") {
		t.Fatalf("password should be redacted, got: %s", output)
	}
	if !strings.Contains(output, "password=***REDACTED***") {
		t.Fatalf("password should be replaced with ***REDACTED***, got: %s", output)
	}
	if !strings.Contains(output, "username=admin") {
		t.Fatalf("username should be preserved, got: %s", output)
	}
}

func TestRedactSensitiveFields_CaseInsensitive(t *testing.T) {
	input := `{"Password":"secret123","SECRET":"mysecret"}`
	output := redactSensitiveFields(input)

	if strings.Contains(output, "secret123") {
		t.Fatalf("Password should be redacted (case insensitive), got: %s", output)
	}
	if strings.Contains(output, "mysecret") {
		t.Fatalf("SECRET should be redacted (case insensitive), got: %s", output)
	}
}

func TestRedactSensitiveFields_EmptyBody(t *testing.T) {
	output := redactSensitiveFields("")
	if output != "" {
		t.Fatalf("empty input should return empty, got: %q", output)
	}
}

func TestRedactSensitiveFields_ExtraKeys(t *testing.T) {
	input := `{"username":"admin","custom_field":"sensitive_value"}`
	output := redactSensitiveFields(input, "custom_field")

	if strings.Contains(output, "sensitive_value") {
		t.Fatalf("custom_field should be redacted, got: %s", output)
	}
	if !strings.Contains(output, `"custom_field":"***REDACTED***"`) {
		t.Fatalf("custom_field should be replaced, got: %s", output)
	}
}

func TestRedactSensitiveFields_MultipleSensitiveFields(t *testing.T) {
	input := `{"password":"pwd123","token":"tok456","api_key":"key789","name":"test"}`
	output := redactSensitiveFields(input)

	if strings.Contains(output, "pwd123") {
		t.Fatalf("password should be redacted, got: %s", output)
	}
	if strings.Contains(output, "tok456") {
		t.Fatalf("token should be redacted, got: %s", output)
	}
	if strings.Contains(output, "key789") {
		t.Fatalf("api_key should be redacted, got: %s", output)
	}
	if !strings.Contains(output, `"name":"test"`) {
		t.Fatalf("name should be preserved, got: %s", output)
	}
}

func TestRedactSensitiveFields_FormAtStart(t *testing.T) {
	input := `password=secret123&username=admin`
	output := redactSensitiveFields(input)

	if strings.Contains(output, "secret123") {
		t.Fatalf("password at start should be redacted, got: %s", output)
	}
}

func readTestResp(t *testing.T, resp *http.Response) string {
	t.Helper()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	return string(data)
}

func TestSafeGetBody_JSONRequest(t *testing.T) {
	app := fiber.New()
	app.Post("/test", func(c fiber.Ctx) error {
		body := SafeGetBody(c)
		return c.SendString(body)
	})

	req := httptest.NewRequest("POST", "/test", strings.NewReader(`{"password":"secret123","username":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body := readTestResp(t, resp)
	if strings.Contains(body, "secret123") {
		t.Fatalf("SafeGetBody should redact password, got: %s", body)
	}
	if !strings.Contains(body, "admin") {
		t.Fatalf("SafeGetBody should preserve username, got: %s", body)
	}
}

func TestSafeGetBody_FormRequest(t *testing.T) {
	app := fiber.New()
	app.Post("/test", func(c fiber.Ctx) error {
		body := SafeGetBody(c)
		return c.SendString(body)
	})

	req := httptest.NewRequest("POST", "/test", strings.NewReader("password=secret123&username=admin"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body := readTestResp(t, resp)
	if strings.Contains(body, "secret123") {
		t.Fatalf("SafeGetBody should redact password from form, got: %s", body)
	}
	if !strings.Contains(body, "admin") {
		t.Fatalf("SafeGetBody should preserve username from form, got: %s", body)
	}
}

func TestSafeGetBody_EmptyBody(t *testing.T) {
	app := fiber.New()
	app.Post("/test", func(c fiber.Ctx) error {
		body := SafeGetBody(c)
		return c.SendString(body)
	})

	req := httptest.NewRequest("POST", "/test", nil)
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body := readTestResp(t, resp)
	if body != "" {
		t.Fatalf("empty body should return empty string, got: %q", body)
	}
}

func TestSafeGetBody_ExtraRedactKeys(t *testing.T) {
	app := fiber.New()
	app.Post("/test", func(c fiber.Ctx) error {
		body := SafeGetBody(c, "custom_secret")
		return c.SendString(body)
	})

	req := httptest.NewRequest("POST", "/test", strings.NewReader(`{"custom_secret":"sensitive","name":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body := readTestResp(t, resp)
	if strings.Contains(body, "sensitive") {
		t.Fatalf("SafeGetBody should redact custom_secret with extra keys, got: %s", body)
	}
	if !strings.Contains(body, "test") {
		t.Fatalf("SafeGetBody should preserve name, got: %s", body)
	}
}

func TestSafeGetBody_TruncatesLongBody(t *testing.T) {
	app := fiber.New()
	app.Post("/test", func(c fiber.Ctx) error {
		body := SafeGetBody(c)
		return c.SendString(body)
	})

	longValue := strings.Repeat("a", 3000)
	reqBody := `{"data":"` + longValue + `"}`
	req := httptest.NewRequest("POST", "/test", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body := readTestResp(t, resp)
	if len(body) > 2052 {
		t.Fatalf("body should be truncated to ~2048 chars, got length: %d", len(body))
	}
	if !strings.HasSuffix(body, "...") {
		t.Fatalf("truncated body should end with '...', got: %s", body[len(body)-10:])
	}
}

func TestSafeGetBody_MultipartForm(t *testing.T) {
	app := fiber.New()
	app.Post("/test", func(c fiber.Ctx) error {
		body := SafeGetBody(c)
		return c.SendString(body)
	})

	body := "--boundary\r\n" +
		"Content-Disposition: form-data; name=\"username\"\r\n\r\n" +
		"admin\r\n" +
		"--boundary\r\n" +
		"Content-Disposition: form-data; name=\"password\"\r\n\r\n" +
		"secret123\r\n" +
		"--boundary--\r\n"

	req := httptest.NewRequest("POST", "/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=boundary")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	result := readTestResp(t, resp)
	if strings.Contains(result, "secret123") {
		t.Fatalf("multipart form password should be redacted, got: %s", result)
	}
	if !strings.Contains(result, "admin") {
		t.Fatalf("multipart form username should be preserved, got: %s", result)
	}
	if !strings.Contains(result, "***REDACTED***") {
		t.Fatalf("multipart form should contain redacted marker, got: %s", result)
	}
}
