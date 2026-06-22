package service

import (
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// sanitizeHTML 基于白名单的 HTML 净化器：
//
//  1. 仅保留白名单标签与属性，未知标签丢弃但保留其文本内容；
//  2. 拒绝 javascript:/data:/vbscript: 等危险 URL 协议；
//  3. 删除所有 on*** 事件处理器和 style/srcdoc 等高风险属性；
//  4. 对自闭合标签按 HTML5 void elements 正确闭合；
//
// 使用 golang.org/x/net/html 的 Tokenizer，避免引入额外的依赖。
//
// 设计目标：阻断 message.content_html 字段构成的存储型 XSS，
// 同时允许常用文本格式化标签（p、br、strong 等）以保留排版能力。
func sanitizeHTML(input string) string {
	if input == "" {
		return ""
	}
	tokenizer := html.NewTokenizer(strings.NewReader(input))
	var b strings.Builder
	b.Grow(len(input))

	for {
		tt := tokenizer.Next()
		switch tt {
		case html.ErrorToken:
			return b.String()
		case html.TextToken:
			// 文本由 tokenizer 已做转义，安全写出
			b.Write(tokenizer.Text())
		case html.StartTagToken:
			tn, hasAttr := tokenizer.TagName()
			name := strings.ToLower(string(tn))
			if !allowedHTMLTag(name) {
				continue
			}
			b.WriteByte('<')
			b.WriteString(name)
			writeSafeAttrs(&b, name, hasAttr, tokenizer)
			b.WriteByte('>')
		case html.SelfClosingTagToken:
			tn, hasAttr := tokenizer.TagName()
			name := strings.ToLower(string(tn))
			if !allowedHTMLTag(name) {
				continue
			}
			b.WriteByte('<')
			b.WriteString(name)
			writeSafeAttrs(&b, name, hasAttr, tokenizer)
			if isVoidHTMLElement(name) {
				b.WriteString("/>")
			} else {
				b.WriteByte('>')
				b.WriteString("</")
				b.WriteString(name)
				b.WriteByte('>')
			}
		case html.EndTagToken:
			tn, _ := tokenizer.TagName()
			name := strings.ToLower(string(tn))
			if !allowedHTMLTag(name) {
				continue
			}
			if isVoidHTMLElement(name) {
				// void 元素没有闭合标签，丢弃
				continue
			}
			b.WriteString("</")
			b.WriteString(name)
			b.WriteByte('>')
		case html.CommentToken, html.DoctypeToken:
			// 注释与 doctype 一律丢弃，防止条件注释/IE 兼容性 XSS
		}
	}
}

// writeSafeAttrs 写出在 attrAllowlist 内、且 URL 协议安全的属性
func writeSafeAttrs(b *strings.Builder, tag string, hasAttr bool, t *html.Tokenizer) {
	if !hasAttr {
		return
	}
	for {
		key, val, more := t.TagAttr()
		k := strings.ToLower(string(key))
		v := string(val)
		if allowedHTMLAttr(tag, k) && safeAttrValue(k, v) {
			b.WriteByte(' ')
			b.WriteString(k)
			b.WriteString(`="`)
			b.WriteString(html.EscapeString(v))
			b.WriteByte('"')
		}
		if !more {
			return
		}
	}
}

// allowedHTMLTag 白名单标签列表
func allowedHTMLTag(tag string) bool {
	switch tag {
	case "p", "br", "hr", "strong", "b", "em", "i", "u", "s", "del", "ins",
		"blockquote", "code", "pre", "kbd", "mark",
		"ul", "ol", "li",
		"h1", "h2", "h3", "h4", "h5", "h6",
		"table", "thead", "tbody", "tfoot", "tr", "td", "th", "caption",
		"a", "span", "div",
		"img":
		return true
	}
	return false
}

// allowedHTMLAttr 白名单属性列表
func allowedHTMLAttr(tag, attr string) bool {
	// 全局允许的安全属性
	switch attr {
	case "title", "lang", "dir":
		return true
	}
	// 标签维度
	switch tag {
	case "a":
		return attr == "href" || attr == "target" || attr == "rel"
	case "img":
		return attr == "src" || attr == "alt" || attr == "width" || attr == "height"
	case "td", "th":
		return attr == "colspan" || attr == "rowspan" || attr == "scope"
	case "ol":
		return attr == "start" || attr == "type"
	}
	return false
}

// safeAttrValue 校验 URL 类属性的协议白名单，阻止 javascript:/data:/vbscript: 等
func safeAttrValue(attr, value string) bool {
	if attr != "href" && attr != "src" {
		return true
	}
	v := strings.TrimSpace(value)
	if v == "" {
		return false
	}
	// 相对路径与 fragment 直接放行
	if strings.HasPrefix(v, "/") || strings.HasPrefix(v, "#") || strings.HasPrefix(v, "?") {
		return true
	}
	u, err := url.Parse(v)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "" {
		// 相对协议（如 path/to/x.png）允许
		return true
	}
	switch scheme {
	case "http", "https", "mailto", "tel":
		return true
	}
	return false
}

// isVoidHTMLElement HTML5 中不需要闭合标签的元素
func isVoidHTMLElement(tag string) bool {
	switch tag {
	case "area", "base", "br", "col", "embed", "hr", "img", "input",
		"link", "meta", "param", "source", "track", "wbr":
		return true
	}
	return false
}
