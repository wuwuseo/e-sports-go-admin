package esports

import (
	"errors"
	"strings"
	"testing"
)

// 页面装修 props 白名单：未知键（html / script / 事件处理器 / 富文本注入键）
// 一律按参数错误拒绝，错误文案须含第几个组件与键名。
func TestFundingPagePropsWhitelistRejectsUnknownKeys(t *testing.T) {
	visible := true
	for _, key := range []string{"html", "script", "onclick", "dangerouslySetInnerHTML"} {
		cfg := pageConfig{Components: []pageComponent{
			{Type: "notice", Visible: &visible, Props: map[string]interface{}{
				key: "<script>alert(1)</script>",
			}},
			{Type: "support_entry", Visible: &visible},
		}}
		err := validatePageConfig(cfg)
		if !errors.Is(err, errInvalidInput) {
			t.Fatalf("组件属性未知键 %q 应被拒，实际：%v", key, err)
		}
		if !strings.Contains(err.Error(), "第 1 个组件") || !strings.Contains(err.Error(), key) {
			t.Fatalf("错误文案应含组件序号与键名 %q，实际：%v", key, err)
		}
	}

	// 嵌套 items 元素里的未知键同样拒绝（防止绕过顶层白名单）。
	nested := pageConfig{Components: []pageComponent{
		{Type: "quick_nav", Visible: &visible, Props: map[string]interface{}{
			"items": []interface{}{map[string]interface{}{
				"name": "我的订单", "target": "orders", "script": "alert(1)",
			}},
		}},
		{Type: "support_entry", Visible: &visible},
	}}
	if err := validatePageConfig(nested); !errors.Is(err, errInvalidInput) {
		t.Fatalf("嵌套 items 未知键应被拒，实际：%v", err)
	}
}

// 每类组件各一个合法配置必须通过，防止白名单误杀客户端真实消费的键。
func TestFundingPagePropsWhitelistAllowsEachComponentType(t *testing.T) {
	visible := true
	cases := []struct {
		name string
		comp pageComponent
	}{
		{"search", pageComponent{Type: "search", Visible: &visible, Props: map[string]interface{}{
			"placeholder": "搜索服务 / 陪玩",
		}}},
		{"carousel", pageComponent{Type: "carousel", Visible: &visible, Props: map[string]interface{}{
			"images":   []interface{}{"https://cdn.example.com/a.png", "/static/banner.png"},
			"interval": 3000,
		}}},
		{"carousel_items", pageComponent{Type: "carousel", Visible: &visible, Props: map[string]interface{}{
			"items": []interface{}{map[string]interface{}{
				"image": "/static/banner.png", "link": "https://cdn.example.com/x",
			}},
		}}},
		{"notice", pageComponent{Type: "notice", Visible: &visible, Props: map[string]interface{}{
			"text": "公告内容", "color": "#333333",
		}}},
		{"quick_nav", pageComponent{Type: "quick_nav", Visible: &visible, Props: map[string]interface{}{
			"items": []interface{}{map[string]interface{}{
				"name": "我的订单", "target": "orders", "icon": "/static/nav.png",
			}},
		}}},
		{"category", pageComponent{Type: "category", Visible: &visible, Props: map[string]interface{}{
			"title": "游戏 / 服务分类",
			"items": []interface{}{map[string]interface{}{"name": "王者荣耀", "target": "category"}},
		}}},
		{"product_list", pageComponent{Type: "product_list", Visible: &visible, Props: map[string]interface{}{
			"title": "在售服务", "limit": 10, "product_ids": []interface{}{1, 2}, "empty_text": "暂无可下单的服务",
		}}},
		{"player_recommend", pageComponent{Type: "player_recommend", Visible: &visible, Props: map[string]interface{}{
			"title": "陪玩推荐", "empty_text": "暂无在线可接单的陪玩",
		}}},
		{"activity", pageComponent{Type: "activity", Visible: &visible, Props: map[string]interface{}{
			"title": "活动专区", "campaign_id": 3, "empty_text": "暂无进行中的充值赠送活动",
		}}},
		{"member_entry", pageComponent{Type: "member_entry", Visible: &visible, Props: map[string]interface{}{
			"title": "会员与余额",
		}}},
		{"image", pageComponent{Type: "image", Visible: &visible, Props: map[string]interface{}{
			"image":   "https://cdn.example.com/promo.png",
			"link":    "/pages/order/create",
			"fit":     "aspectFill",
			"height":  180,
			"radius":  12,
			"spacing": 8,
		}}},
		{"support_entry", pageComponent{Type: "support_entry", Visible: &visible}},
	}
	for _, tc := range cases {
		cfg := pageConfig{Components: []pageComponent{
			tc.comp,
			{Type: "support_entry", Visible: &visible},
		}}
		if err := validatePageConfig(cfg); err != nil {
			t.Fatalf("组件 %s 的合法配置应通过，实际：%v", tc.name, err)
		}
	}
}

// 图片组件的填充方式 / 圆角 / 高度 / 间距在保存期即校验，避免客户端拿到不认的样式。
func TestFundingPageImagePropsValidated(t *testing.T) {
	visible := true
	build := func(props map[string]interface{}) pageConfig {
		return pageConfig{Components: []pageComponent{
			{Type: "image", Visible: &visible, Props: props},
			{Type: "support_entry", Visible: &visible},
		}}
	}
	cases := []map[string]interface{}{
		{"image": "https://cdn.example.com/a.png", "fit": "magic"},
		{"image": "https://cdn.example.com/a.png", "radius": 99},
		{"image": "https://cdn.example.com/a.png", "height": -1},
		{"image": "https://cdn.example.com/a.png", "spacing": pageSpacingMax + 1},
		{"image": "http://cdn.example.com/a.png"},
	}
	for _, props := range cases {
		if err := validatePageConfig(build(props)); !errors.Is(err, errInvalidInput) {
			t.Fatalf("图片属性 %v 应被拒，实际：%v", props, err)
		}
	}
}

// 站内跳转只允许 pages.json 已注册的页面路径：去掉 query / hash 后须命中白名单。
func TestFundingPageLinkRequiresRegisteredRoute(t *testing.T) {
	visible := true
	build := func(link string) pageConfig {
		return pageConfig{Components: []pageComponent{
			{Type: "search", Visible: &visible},
			{Type: "carousel", Visible: &visible, Props: map[string]interface{}{
				"items": []interface{}{map[string]interface{}{
					"image": "https://cdn.example.com/a.png", "link": link,
				}},
			}},
			{Type: "support_entry", Visible: &visible},
		}}
	}

	for _, link := range []string{
		"/arbitrary/not-registered",
		"/pages/index",
		"//evil.example.com/x",
		"/pages/../etc/passwd",
	} {
		if err := validatePageConfig(build(link)); !errors.Is(err, errInvalidInput) {
			t.Fatalf("未注册站内跳转 %q 应被拒，实际：%v", link, err)
		}
	}

	for _, link := range []string{
		"/pages/index/index",
		"/pages/index/index?tab=1",
		"/pages/wallet/index",
		"/pages/order/list#top",
		"/pages/order/detail?id=9",
		"/pages/aftersale/list",
	} {
		if err := validatePageConfig(build(link)); err != nil {
			t.Fatalf("已注册站内跳转 %q 应通过，实际：%v", link, err)
		}
	}
}

// 外部跳转域名白名单回归：未审核域名 / 空白名单 / 非 https 仍被拒，白名单域名仍通过。
func TestFundingPageLinkExternalDomainWhitelistRegression(t *testing.T) {
	visible := true
	build := func(link string) pageConfig {
		return pageConfig{Components: []pageComponent{
			{Type: "search", Visible: &visible},
			{Type: "carousel", Visible: &visible, Props: map[string]interface{}{
				"items": []interface{}{map[string]interface{}{
					"image": "https://cdn.example.com/a.png", "link": link,
				}},
			}},
			{Type: "support_entry", Visible: &visible},
		}}
	}
	allowed := map[string]bool{"allowed.example.com": true}

	if err := validatePageConfigWithAllowedDomains(build("https://unapproved.example.com/x"), allowed, true); !errors.Is(err, errInvalidInput) {
		t.Fatalf("未审核域名应被拒，实际：%v", err)
	}
	if err := validatePageConfigWithAllowedDomains(build("https://allowed.example.com/x"), allowed, true); err != nil {
		t.Fatalf("白名单域名应通过，实际：%v", err)
	}
	if err := validatePageConfigWithAllowedDomains(build("https://allowed.example.com/x"), nil, true); !errors.Is(err, errInvalidInput) {
		t.Fatalf("空白名单应拒所有外部跳转，实际：%v", err)
	}
	if err := validatePageConfig(build("http://allowed.example.com/x")); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非 https 外部跳转应被拒，实际：%v", err)
	}
}
