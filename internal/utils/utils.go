package utils

import (
	"fmt"
	"go-admin/internal/global"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/gofiber/fiber/v3"
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
// 对于 multipart/form-data（文件上传），仅提取文本字段，过滤掉二进制文件内容
func SafeGetBody(c fiber.Ctx) string {
	contentType := strings.ToLower(string(c.Request().Header.ContentType()))

	// 文件上传请求：解析 multipart form，仅保留文本字段
	if strings.HasPrefix(contentType, "multipart/form-data") {
		// 先获取原始 body 作为降级后备（此调用会将 body 读入 fasthttp 缓冲区，
		// 后续 c.MultipartForm() 直接从缓冲区解析，两者互不干扰）
		rawBody := c.Body()

		form, err := c.MultipartForm()
		if err == nil && form != nil {
			var fields []string
			for key, values := range form.Value {
				for _, v := range values {
					fields = append(fields, fmt.Sprintf("%s=%s", key, v))
				}
			}
			// 标注文件字段（不包含二进制内容）
			for key, files := range form.File {
				for _, f := range files {
					fields = append(fields, fmt.Sprintf("%s=<file:%s,%dbytes>", key, f.Filename, f.Size))
				}
			}
			result := strings.Join(fields, ", ")
			if len(result) > 2048 {
				result = result[:2048] + "..."
			}
			return result
		}
		// 解析失败时回退到截断处理（使用已保存的 rawBody，避免从已消费流中读取）
		if len(rawBody) == 0 {
			return ""
		}
		if len(rawBody) > 2048 {
			return string(rawBody[:2048]) + "..."
		}
		return string(rawBody)
	}

	// 非文件上传：截断处理
	body := c.Body()
	if len(body) == 0 {
		return ""
	}
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

// NormalizePagination 标准化分页参数
// Page 默认值为 1，PageSize 默认值为 10，限制 PageSize 最大为 100
// PageSize = 0 表示不分页，返回全部数据
func NormalizePagination(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize == 0 {
		// 0 表示不分页，保持不变
	} else if pageSize < 1 {
		pageSize = 10
	} else if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

// BuildCacheKey 构建缓存键
func BuildCacheKey(prefix string, parts ...string) string {
	if len(parts) == 0 {
		return prefix
	}
	return prefix + ":" + strings.Join(parts, ":")
}
