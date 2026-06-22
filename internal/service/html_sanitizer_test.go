package service

import (
	"strings"
	"testing"
)

// TestSanitizeHTML_BlocksXSS 验证常见 XSS 载荷会被净化
func TestSanitizeHTML_BlocksXSS(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		mustNot   []string // 这些子串必须不出现在输出中
		mayRetain string   // 文本残留可接受
	}{
		{
			name:    "script 标签",
			input:   `hello<script>alert(1)</script>world`,
			mustNot: []string{"<script", "</script>"},
		},
		{
			name:    "img onerror 事件",
			input:   `<img src=x onerror="alert(1)">`,
			mustNot: []string{"onerror", "alert"},
		},
		{
			name:    "a javascript: 协议",
			input:   `<a href="javascript:alert(1)">x</a>`,
			mustNot: []string{"javascript:", "alert"},
		},
		{
			name:    "iframe 标签",
			input:   `<iframe src="evil"></iframe>`,
			mustNot: []string{"<iframe"},
		},
		{
			name:    "style 标签",
			input:   `<style>body{}</style>`,
			mustNot: []string{"<style"},
		},
		{
			name:    "svg onload",
			input:   `<svg onload="alert(1)">`,
			mustNot: []string{"<svg", "onload", "alert"},
		},
		{
			name:    "data: 协议",
			input:   `<a href="data:text/html,<script>alert(1)</script>">x</a>`,
			mustNot: []string{"data:", "<script"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := sanitizeHTML(c.input)
			for _, banned := range c.mustNot {
				if strings.Contains(strings.ToLower(out), strings.ToLower(banned)) {
					t.Errorf("净化结果不应包含 %q，实际输出: %s", banned, out)
				}
			}
		})
	}
}

// TestSanitizeHTML_KeepsSafeTags 验证常见安全标签被保留
func TestSanitizeHTML_KeepsSafeTags(t *testing.T) {
	in := `<p>hello <strong>world</strong> <a href="https://example.com" target="_blank" rel="noopener">link</a></p>`
	out := sanitizeHTML(in)
	for _, need := range []string{"<p>", "<strong>", "world", "</strong>", `href="https://example.com"`} {
		if !strings.Contains(out, need) {
			t.Errorf("净化结果应包含 %q，实际输出: %s", need, out)
		}
	}
}

// TestSanitizeHTML_EmptyInput 空输入直接返回空串
func TestSanitizeHTML_EmptyInput(t *testing.T) {
	if got := sanitizeHTML(""); got != "" {
		t.Errorf("空输入应返回空串，实际: %q", got)
	}
}
