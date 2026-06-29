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

// TestSanitizeHTML_BlocksEventHandlers 验证所有 on* 事件处理器被剥离
func TestSanitizeHTML_BlocksEventHandlers(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"onclick", `<p onclick="alert(1)">text</p>`},
		{"onload", `<img src="x.jpg" onload="alert(1)">`},
		{"onerror", `<img src=x onerror="alert(1)">`},
		{"onmouseover", `<div onmouseover="alert(1)">hover</div>`},
		{"onfocus", `<input onfocus="alert(1)">`},
		{"onblur", `<input onblur="alert(1)">`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := sanitizeHTML(c.input)
			if strings.Contains(strings.ToLower(out), "on"+c.name) {
				t.Errorf("事件处理器未被剥离，输出: %s", out)
			}
		})
	}
}

// TestSanitizeHTML_BlocksDangerousProtocols 验证危险 URL 协议被拒绝
func TestSanitizeHTML_BlocksDangerousProtocols(t *testing.T) {
	cases := []struct {
		name  string
		input string
		check string
	}{
		{"javascript协议", `<a href="javascript:alert(1)">x</a>`, "javascript:"},
		{"vbscript协议", `<a href="vbscript:msgbox(1)">x</a>`, "vbscript:"},
		{"data协议HTML", `<a href="data:text/html,<script>alert(1)</script>">x</a>`, "data:"},
		{"data协议base64", `<img src="data:image/svg+xml;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==">`, "data:"},
		{"协议大小写混合", `<a href="JaVaScRiPt:alert(1)">x</a>`, "javascript"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := sanitizeHTML(c.input)
			if strings.Contains(strings.ToLower(out), strings.ToLower(c.check)) {
				t.Errorf("危险协议未被拒绝，输出: %s", out)
			}
		})
	}
}

// TestSanitizeHTML_AllowsSafeProtocols 验证安全协议被保留
func TestSanitizeHTML_AllowsSafeProtocols(t *testing.T) {
	cases := []struct {
		name  string
		input string
		check string
	}{
		{"http", `<a href="http://example.com">x</a>`, `href="http://example.com"`},
		{"https", `<a href="https://example.com">x</a>`, `href="https://example.com"`},
		{"mailto", `<a href="mailto:user@example.com">x</a>`, `href="mailto:user@example.com"`},
		{"tel", `<a href="tel:+1234567890">x</a>`, `href="tel:+1234567890"`},
		{"相对路径", `<a href="/path/to/page">x</a>`, `href="/path/to/page"`},
		{"锚点", `<a href="#section">x</a>`, `href="#section"`},
		{"查询参数", `<a href="?q=test">x</a>`, `href="?q=test"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := sanitizeHTML(c.input)
			if !strings.Contains(out, c.check) {
				t.Errorf("安全协议被错误移除，输出: %s", out)
			}
		})
	}
}

// TestSanitizeHTML_BlocksStyleAndSrcdoc 验证 style 和 srcdoc 属性被剥离
func TestSanitizeHTML_BlocksStyleAndSrcdoc(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"style属性", `<p style="color:expression(alert(1))">text</p>`},
		{"srcdoc属性", `<iframe srcdoc="<script>alert(1)</script>"></iframe>`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := sanitizeHTML(c.input)
			if strings.Contains(strings.ToLower(out), c.name) {
				t.Errorf("高风险属性未被剥离，输出: %s", out)
			}
		})
	}
}

// TestSanitizeHTML_BlocksCommentsAndDoctype 验证注释和 doctype 被丢弃
func TestSanitizeHTML_BlocksCommentsAndDoctype(t *testing.T) {
	cases := []struct {
		name  string
		input string
		check string
	}{
		{"HTML注释", `<p>hello<!-- comment --></p>`, "<!--"},
		{"条件注释", `<!--[if IE]><script>alert(1)</script><![endif]-->`, "<!--"},
		{"DOCTYPE", `<!DOCTYPE html><p>text</p>`, "<!DOCTYPE"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := sanitizeHTML(c.input)
			if strings.Contains(strings.ToLower(out), strings.ToLower(c.check)) {
				t.Errorf("%s 未被剥离，输出: %s", c.name, out)
			}
		})
	}
}

// TestSanitizeHTML_VoidElementsSelfClose 验证自闭合元素正确处理
func TestSanitizeHTML_VoidElementsSelfClose(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		mustHave string
		mustNot  string
	}{
		{"br自闭合", `<br/>`, "<br/>", ""},
		{"hr自闭合", `<hr>`, "<hr", "</hr>"},
		{"img自闭合", `<img src="x.jpg" alt="x">`, `<img src="x.jpg"`, "</img>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := sanitizeHTML(c.input)
			if !strings.Contains(out, c.mustHave) {
				t.Errorf("缺少 %s，输出: %s", c.mustHave, out)
			}
			if c.mustNot != "" && strings.Contains(out, c.mustNot) {
				t.Errorf("不应有 %s，输出: %s", c.mustNot, out)
			}
		})
	}
}

// TestSanitizeHTML_DropsUnknownTagsKeepsText 验证未知标签被丢弃但文本保留
func TestSanitizeHTML_DropsUnknownTagsKeepsText(t *testing.T) {
	out := sanitizeHTML(`<script>alert("xss")</script>hello<custom>world</custom>`)
	if !strings.Contains(out, "hello") || !strings.Contains(out, "world") {
		t.Errorf("文本内容丢失，输出: %s", out)
	}
	if strings.Contains(out, "<script") || strings.Contains(out, "<custom") {
		t.Errorf("未知标签未被丢弃，输出: %s", out)
	}
}

// TestSanitizeHTML_EscapesAttributeValues 验证属性值被正确转义防止注入
func TestSanitizeHTML_EscapesAttributeValues(t *testing.T) {
	out := sanitizeHTML(`<a href='test"onmouseover=alert(1)'>x</a>`)
	// 属性值中的双引号被转义为 &#34;，不会破坏属性边界
	if strings.Contains(out, `"onmouseover`) {
		t.Errorf("属性注入未被阻止，输出: %s", out)
	}
	// 验证属性值被正确转义
	if !strings.Contains(out, "&#34;") {
		t.Errorf("属性值未被转义，输出: %s", out)
	}
}

// TestSanitizeHTML_NestedTags 验证嵌套标签处理
func TestSanitizeHTML_NestedTags(t *testing.T) {
	out := sanitizeHTML(`<div><p>hello <strong>bold</strong> world</p></div>`)
	for _, need := range []string{"<div>", "<p>", "<strong>", "bold", "</strong>", "</p>", "</div>"} {
		if !strings.Contains(out, need) {
			t.Errorf("缺少 %s，输出: %s", need, out)
		}
	}
}

// TestSanitizeHTML_TableStructure 验证表格标签白名单
func TestSanitizeHTML_TableStructure(t *testing.T) {
	out := sanitizeHTML(`<table><thead><tr><th>col</th></tr></thead><tbody><tr><td>data</td></tr></tbody></table>`)
	for _, need := range []string{"<table>", "<thead>", "<tbody>", "<tr>", "<th>", "<td>"} {
		if !strings.Contains(out, need) {
			t.Errorf("缺少 %s，输出: %s", need, out)
		}
	}
}

// TestSanitizeHTML_AllowsGlobalAttributes 验证全局安全属性被保留
func TestSanitizeHTML_AllowsGlobalAttributes(t *testing.T) {
	out := sanitizeHTML(`<p title="tip" lang="en" dir="ltr">text</p>`)
	for _, need := range []string{`title="tip"`, `lang="en"`, `dir="ltr"`} {
		if !strings.Contains(out, need) {
			t.Errorf("缺少 %s，输出: %s", need, out)
		}
	}
}
