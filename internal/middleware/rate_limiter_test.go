package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
)

func TestRateLimiter_AllowsRequestsWithinLimit(t *testing.T) {
	rl := NewRateLimiter(RateLimiterConfig{
		Window:   time.Minute,
		MaxCount: 5,
	})
	app := fiber.New()
	app.Use(rl.Handler())
	app.Get("/test", func(c fiber.Ctx) error {
		return c.SendString("ok")
	})

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = "127.0.0.1:12345"
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200", i, resp.StatusCode)
		}
	}
}

func TestRateLimiter_RejectsRequestsOverLimit(t *testing.T) {
	rl := NewRateLimiter(RateLimiterConfig{
		Window:   time.Minute,
		MaxCount: 3,
	})
	app := fiber.New()
	app.Use(rl.Handler())
	app.Get("/test", func(c fiber.Ctx) error {
		return c.SendString("ok")
	})

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = "127.0.0.1:12345"
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200", i, resp.StatusCode)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("rate limited request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
}

func TestRateLimiter_RateLimitHeadersSet(t *testing.T) {
	rl := NewRateLimiter(RateLimiterConfig{
		Window:   time.Minute,
		MaxCount: 5,
	})
	app := fiber.New()
	app.Use(rl.Handler())
	app.Get("/test", func(c fiber.Ctx) error {
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	limit := resp.Header.Get("X-RateLimit-Limit")
	if limit != "5" {
		t.Fatalf("X-RateLimit-Limit = %q, want 5", limit)
	}
	remaining := resp.Header.Get("X-RateLimit-Remaining")
	if remaining != "4" {
		t.Fatalf("X-RateLimit-Remaining = %q, want 4", remaining)
	}
}

func TestRateLimiter_RetryAfterHeaderOnLimit(t *testing.T) {
	rl := NewRateLimiter(RateLimiterConfig{
		Window:   time.Minute,
		MaxCount: 1,
	})
	app := fiber.New()
	app.Use(rl.Handler())
	app.Get("/test", func(c fiber.Ctx) error {
		return c.SendString("ok")
	})

	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req1.RemoteAddr = "127.0.0.1:12345"
	resp1, err := app.Test(req1)
	if err != nil {
		t.Fatalf("first request failed: %v", err)
	}
	defer resp1.Body.Close()

	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req2.RemoteAddr = "127.0.0.1:12345"
	resp2, err := app.Test(req2)
	if err != nil {
		t.Fatalf("second request failed: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp2.StatusCode)
	}
	retryAfter := resp2.Header.Get("Retry-After")
	if retryAfter == "" {
		t.Fatal("Retry-After header should be set when rate limited")
	}
}

func TestRateLimiter_DifferentIPsIndependent(t *testing.T) {
	rl := NewRateLimiter(RateLimiterConfig{
		Window:   time.Minute,
		MaxCount: 1,
		KeyFunc: func(c fiber.Ctx) string {
			return c.Get("X-Test-IP")
		},
	})
	app := fiber.New()
	app.Use(rl.Handler())
	app.Get("/test", func(c fiber.Ctx) error {
		return c.SendString("ok")
	})

	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req1.Header.Set("X-Test-IP", "10.0.0.1")
	resp1, err := app.Test(req1)
	if err != nil {
		t.Fatalf("ip1 request failed: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("ip1 status = %d, want 200", resp1.StatusCode)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req2.Header.Set("X-Test-IP", "10.0.0.2")
	resp2, err := app.Test(req2)
	if err != nil {
		t.Fatalf("ip2 request failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("ip2 status = %d, want 200", resp2.StatusCode)
	}
}

func TestRateLimiter_CustomKeyFunc(t *testing.T) {
	rl := NewRateLimiter(RateLimiterConfig{
		Window:   time.Minute,
		MaxCount: 1,
		KeyFunc: func(c fiber.Ctx) string {
			return c.Get("X-API-Key")
		},
	})
	app := fiber.New()
	app.Use(rl.Handler())
	app.Get("/test", func(c fiber.Ctx) error {
		return c.SendString("ok")
	})

	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req1.Header.Set("X-API-Key", "key-a")
	resp1, err := app.Test(req1)
	if err != nil {
		t.Fatalf("key-a first request failed: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("key-a first status = %d, want 200", resp1.StatusCode)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req2.Header.Set("X-API-Key", "key-b")
	resp2, err := app.Test(req2)
	if err != nil {
		t.Fatalf("key-b request failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("key-b status = %d, want 200", resp2.StatusCode)
	}

	req3 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req3.Header.Set("X-API-Key", "key-a")
	resp3, err := app.Test(req3)
	if err != nil {
		t.Fatalf("key-a second request failed: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("key-a second status = %d, want 429", resp3.StatusCode)
	}
}

func TestRateLimiter_WindowReset(t *testing.T) {
	rl := NewRateLimiter(RateLimiterConfig{
		Window:   100 * time.Millisecond,
		MaxCount: 1,
	})
	app := fiber.New()
	app.Use(rl.Handler())
	app.Get("/test", func(c fiber.Ctx) error {
		return c.SendString("ok")
	})

	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req1.RemoteAddr = "127.0.0.1:12345"
	resp1, err := app.Test(req1)
	if err != nil {
		t.Fatalf("first request failed: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d, want 200", resp1.StatusCode)
	}

	time.Sleep(150 * time.Millisecond)

	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req2.RemoteAddr = "127.0.0.1:12345"
	resp2, err := app.Test(req2)
	if err != nil {
		t.Fatalf("second request failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("after window reset status = %d, want 200", resp2.StatusCode)
	}
}

func TestRateLimiter_ConcurrentRequests(t *testing.T) {
	rl := NewRateLimiter(RateLimiterConfig{
		Window:   time.Minute,
		MaxCount: 100,
	})
	app := fiber.New()
	app.Use(rl.Handler())
	app.Get("/test", func(c fiber.Ctx) error {
		return c.SendString("ok")
	})

	var wg sync.WaitGroup
	successCount := 0
	var mu sync.Mutex

	for i := 0; i < 150; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			req.RemoteAddr = "127.0.0.1:12345"
			resp, err := app.Test(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			mu.Lock()
			if resp.StatusCode == http.StatusOK {
				successCount++
			}
			mu.Unlock()
		}()
	}

	wg.Wait()

	if successCount > 100 {
		t.Fatalf("successful requests = %d, should not exceed 100", successCount)
	}
}

func TestRateLimiter_RemainingZeroWhenLimited(t *testing.T) {
	rl := NewRateLimiter(RateLimiterConfig{
		Window:   time.Minute,
		MaxCount: 2,
	})
	app := fiber.New()
	app.Use(rl.Handler())
	app.Get("/test", func(c fiber.Ctx) error {
		return c.SendString("ok")
	})

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.RemoteAddr = "127.0.0.1:12345"
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
		defer resp.Body.Close()
	}

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("rate limited request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	remaining := resp.Header.Get("X-RateLimit-Remaining")
	if remaining != "0" {
		t.Fatalf("X-RateLimit-Remaining = %q, want 0", remaining)
	}
}
