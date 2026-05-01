package utils

import (
	"go-admin/internal/global"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/gofiber/fiber/v2"
)

var (
	uidCounter  uint64
	uidMu       sync.Mutex
	counterFile = filepath.Clean("data/.uid_counter")
)

func init() {
	uidCounter = loadCounter()
}

// loadCounter 从持久化文件加载计数器值，文件不存在则从 100000 开始
func loadCounter() uint64 {
	data, err := os.ReadFile(counterFile)
	if err != nil {
		return 100000
	}
	val, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 100000
	}
	return val
}

// saveCounter 将当前计数器值持久化到文件
func saveCounter(val uint64) {
	os.MkdirAll(filepath.Dir(counterFile), 0755)
	os.WriteFile(counterFile, []byte(strconv.FormatUint(val, 10)), 0644)
}

// GetResponseMsg 获取响应消息
func GetResponseMsg(code int) string {
	return global.ResponseMsg[code]
}

// SafeGetBody 安全获取请求体内容，限制最大读取长度，避免大文件导致内存溢出
func SafeGetBody(c *fiber.Ctx) string {
	body := c.Body()
	if len(body) == 0 {
		return ""
	}
	// 仅保留前 2048 字节，防止存储超大请求体
	maxLen := 2048
	if len(body) > maxLen {
		body = body[:maxLen]
		return string(body) + "..."
	}
	return string(body)
}

// SaveUID 将当前 UID 计数器值持久化到文件
func SaveUID() {
	uidMu.Lock()
	defer uidMu.Unlock()
	saveCounter(uidCounter)
}

// GenerateUID 生成用户唯一标识
// 使用自增序列，从 100001 开始。
// 每 10 次调用自动持久化一次（崩溃安全），程序关闭时需调用 SaveUID() 确保完整持久化
func GenerateUID() string {
	uidMu.Lock()
	uidCounter++
	next := uidCounter
	// 每 10 次自动持久化，避免崩溃导致大量 ID 丢失
	if next%10 == 0 {
		saveCounter(next)
	}
	uidMu.Unlock()
	return strconv.FormatUint(next, 10)
}
