package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func TestUploadStaticMiddleware(t *testing.T) {
	payload, err := os.ReadFile("web/dist/index.html")
	if err != nil {
		t.Fatal(err)
	}

	app := fiber.New()
	registerUploadStaticMiddleware(app, "web/dist")

	response, err := app.Test(httptest.NewRequest(http.MethodGet, "/uploads/index.html", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/html") || !bytes.Equal(data, payload) {
		t.Fatalf("upload response: status=%d type=%q body=%q", response.StatusCode, response.Header.Get("Content-Type"), data)
	}

	missing, err := app.Test(httptest.NewRequest(http.MethodGet, "/uploads/slides/missing.png", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing upload status = %d, want 404", missing.StatusCode)
	}
}
