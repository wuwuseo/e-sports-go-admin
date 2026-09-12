package utils

import "testing"

func TestParseUserAgent(t *testing.T) {
	tests := []struct {
		name    string
		ua      string
		browser string
		os      string
	}{
		{
			name:    "Edge on Windows",
			ua:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 Edg/120.0.2210.61",
			browser: "Edge 120",
			os:      "Windows",
		},
		{
			name:    "Chrome on Windows",
			ua:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
			browser: "Chrome 126",
			os:      "Windows",
		},
		{
			name:    "Chrome on macOS",
			ua:      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36",
			browser: "Chrome 125",
			os:      "macOS",
		},
		{
			name:    "Firefox on Windows",
			ua:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:127.0) Gecko/20100101 Firefox/127.0",
			browser: "Firefox 127",
			os:      "Windows",
		},
		{
			name:    "Safari on macOS",
			ua:      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Safari/605.1.15",
			browser: "Safari 17",
			os:      "macOS",
		},
		{
			name:    "Safari on iPhone",
			ua:      "Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Mobile/15E148 Safari/604.1",
			browser: "Safari 17",
			os:      "iOS",
		},
		{
			name:    "Chrome on Android",
			ua:      "Mozilla/5.0 (Linux; Android 13; Pixel 7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Mobile Safari/537.36",
			browser: "Chrome 124",
			os:      "Android",
		},
		{
			name:    "curl",
			ua:      "curl/8.4.0",
			browser: "curl",
			os:      "Unknown",
		},
		{
			name:    "empty",
			ua:      "",
			browser: "Unknown",
			os:      "Unknown",
		},
		{
			name:    "garbage",
			ua:      "not a user agent",
			browser: "Unknown",
			os:      "Unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			browser, osName := ParseUserAgent(tt.ua)
			if browser != tt.browser {
				t.Errorf("browser = %q, want %q", browser, tt.browser)
			}
			if osName != tt.os {
				t.Errorf("os = %q, want %q", osName, tt.os)
			}
		})
	}
}
