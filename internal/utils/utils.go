package utils

import (
	"fmt"
	"go-admin/internal/global"
	"os"
	"path/filepath"
	"regexp"
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
	if err := os.MkdirAll(filepath.Dir(counterFile), 0755); err != nil {
		return
	}
	_ = os.WriteFile(counterFile, []byte(strconv.FormatUint(val, 10)), 0644)
}

// GetResponseMsg 获取响应消息
func GetResponseMsg(code int) string {
	return global.ResponseMsg[code]
}

// 密码强度策略常量
const (
	// MinPasswordLength 密码最小长度（提升至8位，符合等保 2.0 三级要求）
	MinPasswordLength = 8
	// MaxPasswordLength 密码最大长度（防止 DoS 攻击）
	MaxPasswordLength = 128
)

// passwordLowerRe 检查是否包含小写字母
var passwordLowerRe = regexp.MustCompile(`[a-z]`)

// passwordUpperRe 检查是否包含大写字母
var passwordUpperRe = regexp.MustCompile(`[A-Z]`)

// passwordDigitRe 检查是否包含数字
var passwordDigitRe = regexp.MustCompile(`[0-9]`)

// passwordSpecialRe 检查是否包含特殊字符
var passwordSpecialRe = regexp.MustCompile(`[!@#$%^&*()\-_=+\[\]{};:'",.<>/?\\|` + "`" + `~]`)

// ValidatePasswordStrength 校验密码强度
// 要求：长度 8-128 位，至少包含以下四类字符中的三类：
//   - 大写字母 (A-Z)
//   - 小写字母 (a-z)
//   - 数字 (0-9)
//   - 特殊字符 (!@#$%^&*() 等)
//
// 返回 nil 表示合规，否则返回中文错误说明
func ValidatePasswordStrength(password string) error {
	if len(password) < MinPasswordLength {
		return fmt.Errorf("密码长度不能少于 %d 位", MinPasswordLength)
	}
	if len(password) > MaxPasswordLength {
		return fmt.Errorf("密码长度不能超过 %d 位", MaxPasswordLength)
	}

	categories := 0
	if passwordLowerRe.MatchString(password) {
		categories++
	}
	if passwordUpperRe.MatchString(password) {
		categories++
	}
	if passwordDigitRe.MatchString(password) {
		categories++
	}
	if passwordSpecialRe.MatchString(password) {
		categories++
	}
	if categories < 3 {
		return fmt.Errorf("密码必须至少包含大写字母、小写字母、数字、特殊字符中的任意三类")
	}
	return nil
}

// defaultSensitiveKeys 默认需要脱敏的请求体字段名（小写匹配）
var defaultSensitiveKeys = []string{
	"password",
	"old_password",
	"new_password",
	"current_password",
	"secret",
	"client_secret",
	"api_secret",
	"secret_key",
	"token",
	"access_token",
	"refresh_token",
	"api_key",
	"apikey",
	"private_key",
	"password_salt",
	"salt",
	"authorization",
	"encrypted_password",
	"encrypted_email",
	"encrypted_username",
}

var (
	defaultSensitiveKeysStr = func() string {
		quoted := make([]string, len(defaultSensitiveKeys))
		for i, k := range defaultSensitiveKeys {
			quoted[i] = regexp.QuoteMeta(k)
		}
		return strings.Join(quoted, "|")
	}()
	defaultJSONRedactPattern = regexp.MustCompile(`(?i)"(` + defaultSensitiveKeysStr + `)"\s*:\s*"[^"]*"`)
	defaultFormRedactPattern = regexp.MustCompile(`(?i)(^|&)(` + defaultSensitiveKeysStr + `)=([^&]*)`)
)

// isSensitiveKey 判断字段名是否为敏感字段（支持额外键）
func isSensitiveKey(key string, extraKeys []string) bool {
	lower := strings.ToLower(key)
	for _, k := range defaultSensitiveKeys {
		if lower == k {
			return true
		}
	}
	for _, k := range extraKeys {
		if lower == strings.ToLower(k) {
			return true
		}
	}
	return false
}

// redactSensitiveFields 对请求体中的敏感字段值进行脱敏
// 支持 JSON 键值对和表单编码格式，extraKeys 用于追加额外需脱敏的字段（如配置项 value）
func redactSensitiveFields(body string, extraKeys ...string) string {
	if len(body) == 0 {
		return body
	}

	if len(extraKeys) == 0 {
		body = defaultJSONRedactPattern.ReplaceAllString(body, `"${1}":"***REDACTED***"`)
		body = defaultFormRedactPattern.ReplaceAllString(body, "${1}${2}=***REDACTED***")
		return body
	}

	keys := make([]string, 0, len(defaultSensitiveKeys)+len(extraKeys))
	keys = append(keys, defaultSensitiveKeys...)
	keys = append(keys, extraKeys...)
	quoted := make([]string, len(keys))
	for i, k := range keys {
		quoted[i] = regexp.QuoteMeta(strings.ToLower(k))
	}
	alternation := strings.Join(quoted, "|")

	jsonPattern := regexp.MustCompile(`(?i)"(` + alternation + `)"\s*:\s*"[^"]*"`)
	body = jsonPattern.ReplaceAllString(body, `"${1}":"***REDACTED***"`)

	formPattern := regexp.MustCompile(`(?i)(^|&)(` + alternation + `)=([^&]*)`)
	body = formPattern.ReplaceAllString(body, "${1}${2}=***REDACTED***")

	return body
}

// SafeGetBody 安全获取请求体内容，限制最大读取长度，避免大文件导致内存溢出
// 对敏感字段（密码、令牌、密钥等）自动脱敏，防止凭据泄露到操作日志
// extraRedactKeys 可传入额外需要脱敏的字段名（如配置项的 value 字段）
func SafeGetBody(c fiber.Ctx, extraRedactKeys ...string) string {
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
					if isSensitiveKey(key, extraRedactKeys) {
						v = "***REDACTED***"
					}
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
		redacted := redactSensitiveFields(string(rawBody), extraRedactKeys...)
		if len(redacted) > 2048 {
			redacted = redacted[:2048] + "..."
		}
		return redacted
	}

	// 非文件上传：脱敏后截断
	body := c.Body()
	if len(body) == 0 {
		return ""
	}
	redacted := redactSensitiveFields(string(body), extraRedactKeys...)
	if len(redacted) > 2048 {
		redacted = redacted[:2048] + "..."
	}
	return redacted
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
