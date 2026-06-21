package middleware

import (
	"strconv"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
)

// rateLimitEntry 存储每个标识的限流状态
type rateLimitEntry struct {
	mu        sync.Mutex
	count     int
	windowEnd time.Time
}

// RateLimiterConfig 限流器配置
type RateLimiterConfig struct {
	Window   time.Duration            // 时间窗口
	MaxCount int                      // 窗口内最大请求数
	KeyFunc  func(c fiber.Ctx) string // 自定义键函数，默认为客户端 IP
}

// RateLimiter 固定窗口限流器
type RateLimiter struct {
	entries sync.Map
	config  RateLimiterConfig
}

// NewRateLimiter 创建新的限流器
func NewRateLimiter(config RateLimiterConfig) *RateLimiter {
	if config.KeyFunc == nil {
		config.KeyFunc = func(c fiber.Ctx) string {
			return c.IP()
		}
	}
	rl := &RateLimiter{config: config}
	go rl.cleanup()
	return rl
}

// Handler 返回 Fiber 中间件
func (rl *RateLimiter) Handler() fiber.Handler {
	return func(c fiber.Ctx) error {
		key := rl.config.KeyFunc(c)
		now := time.Now()

		val, _ := rl.entries.LoadOrStore(key, &rateLimitEntry{})
		entry := val.(*rateLimitEntry)

		entry.mu.Lock()
		defer entry.mu.Unlock()

		if now.After(entry.windowEnd) {
			entry.count = 0
			entry.windowEnd = now.Add(rl.config.Window)
		}

		entry.count++
		if entry.count > rl.config.MaxCount {
			remaining := int(entry.windowEnd.Sub(now).Seconds())
			if remaining < 1 {
				remaining = 1
			}
			c.Set("Retry-After", strconv.Itoa(remaining))
			c.Set("X-RateLimit-Limit", strconv.Itoa(rl.config.MaxCount))
			c.Set("X-RateLimit-Remaining", "0")
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"code":    0,
				"message": "请求过于频繁，请稍后再试",
			})
		}

		c.Set("X-RateLimit-Limit", strconv.Itoa(rl.config.MaxCount))
		c.Set("X-RateLimit-Remaining", strconv.Itoa(rl.config.MaxCount-entry.count))

		return c.Next()
	}
}

// cleanup 定期清理过期的限流条目
func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(rl.config.Window * 2)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		rl.entries.Range(func(key, value any) bool {
			entry := value.(*rateLimitEntry)
			entry.mu.Lock()
			expired := now.After(entry.windowEnd)
			entry.mu.Unlock()
			if expired {
				rl.entries.Delete(key)
			}
			return true
		})
	}
}
