package utils

import (
	"strings"
)

const unknownUserAgentField = "Unknown"

// ParseUserAgent 从 User-Agent 中粗粒度解析浏览器与操作系统信息，
// 供登录日志等场景展示。解析失败时返回 "Unknown"，绝不返回空串。
func ParseUserAgent(userAgent string) (browser, osName string) {
	ua := strings.TrimSpace(userAgent)
	if ua == "" {
		return unknownUserAgentField, unknownUserAgentField
	}

	browser = normalizeUserAgentField(parseBrowserFromUserAgent(ua))
	osName = normalizeUserAgentField(parseOSFromUserAgent(ua))
	return browser, osName
}

func normalizeUserAgentField(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return unknownUserAgentField
	}
	return value
}

// parseBrowserFromUserAgent 按优先级识别浏览器。顺序很重要：
// Edge/Opera 等 Chromium 壳浏览器必须先于 Chrome 判断。
func parseBrowserFromUserAgent(ua string) string {
	switch {
	case strings.Contains(ua, "Edg/"):
		return "Edge " + userAgentVersionAfter(ua, "Edg/")
	case strings.Contains(ua, "OPR/"):
		return "Opera " + userAgentVersionAfter(ua, "OPR/")
	case strings.Contains(ua, "Opera"):
		return "Opera"
	case strings.Contains(ua, "Firefox/"):
		return "Firefox " + userAgentVersionAfter(ua, "Firefox/")
	case strings.Contains(ua, "MSIE"), strings.Contains(ua, "Trident/"):
		return "Internet Explorer"
	case strings.Contains(ua, "Chrome/"):
		return "Chrome " + userAgentVersionAfter(ua, "Chrome/")
	case strings.Contains(ua, "Version/") && strings.Contains(ua, "Safari"):
		return "Safari " + userAgentVersionAfter(ua, "Version/")
	case strings.Contains(ua, "Safari"):
		return "Safari"
	default:
		// 非浏览器客户端（curl、python-requests 等）取 "product/version" 的 product 段；
		// 形如 "not a user agent" 的垃圾串没有合法的 product/version 结构，视为未知
		slash := strings.Index(ua, "/")
		if slash <= 0 || strings.Contains(ua[:slash], " ") {
			return ""
		}
		token := ua[:slash]
		if strings.EqualFold(token, "Mozilla") {
			return ""
		}
		return token
	}
}

// parseOSFromUserAgent 识别操作系统。iPhone/iPad 的 UA 也会带 "Mac OS X"，
// Android 的 UA 也会带 "Linux"，所以必须先判移动端。
func parseOSFromUserAgent(ua string) string {
	switch {
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"), strings.Contains(ua, "iPod"):
		return "iOS"
	case strings.Contains(ua, "Android"):
		return "Android"
	case strings.Contains(ua, "Windows"):
		return "Windows"
	case strings.Contains(ua, "Macintosh"), strings.Contains(ua, "Mac OS"):
		return "macOS"
	case strings.Contains(ua, "Linux"), strings.Contains(ua, "X11"):
		return "Linux"
	default:
		return ""
	}
}

// userAgentVersionAfter 提取前缀后的主版本号（首个点分段）。
func userAgentVersionAfter(ua, prefix string) string {
	idx := strings.Index(ua, prefix)
	if idx < 0 {
		return ""
	}
	rest := ua[idx+len(prefix):]
	end := 0
	for end < len(rest) && (rest[end] >= '0' && rest[end] <= '9') {
		end++
	}
	if end == 0 {
		return ""
	}
	return rest[:end]
}
