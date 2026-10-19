package esports

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// P0-17 / P1 轮播图 / P1-20 素材限制的回归测试。
// 本文件全部辅助物以 ssrf 前缀命名，避免与并行桶的同名测试辅助物撞名。

// ssrfProbeRecorder 记录图片探测被调用了几次、探测了哪些地址。
// 「白名单外必须一次也不探测」是 P0-17 的核心断言，不能只看返回值。
type ssrfProbeRecorder struct {
	calls atomic.Int64
	urls  chan string
}

func (r *ssrfProbeRecorder) count() int64 {
	return r.calls.Load()
}

// withSSRFProbeRecorder 把探测替换成计数器（不发起任何真实网络请求），测试结束恢复原探测函数。
func withSSRFProbeRecorder(t *testing.T) *ssrfProbeRecorder {
	t.Helper()
	recorder := &ssrfProbeRecorder{urls: make(chan string, 32)}
	original := pageProbeImage
	pageProbeImage = func(_ context.Context, rawURL string) error {
		recorder.calls.Add(1)
		select {
		case recorder.urls <- rawURL:
		default:
		}
		return nil
	}
	t.Cleanup(func() { pageProbeImage = original })
	return recorder
}

// ssrfCarouselComponents 构造「轮播图 + 必需客服入口」的组件集，field 为图片字段名（image / images）。
func ssrfCarouselComponents(field string, value interface{}) []pageComponent {
	visible := true
	return []pageComponent{
		{Type: "search", Visible: &visible},
		{Type: "carousel", Visible: &visible, Props: map[string]interface{}{
			"items": []interface{}{map[string]interface{}{field: value}},
		}},
		{Type: "support_entry", Visible: &visible},
	}
}

// ssrfImageConfig 构造含单个外部图片的发布校验配置。
func ssrfImageConfig(field string, value interface{}) pageConfig {
	return pageConfig{Components: ssrfCarouselComponents(field, value)}
}

// 白名单外的 https 图片：发布前资源校验被拒，且**未发起任何网络请求**（SSRF 回归）。
func TestPageProbeRequiresAllowedDomainFirst(t *testing.T) {
	db := openPageDB(t, "esports_page_ssrf_whitelist")
	recorder := withSSRFProbeRecorder(t)
	cfg := ssrfImageConfig("image", "https://cdn.example.com/a.png")

	// ① 未配置白名单：fail-closed，明确指出配置键，且不探测。
	err := validatePageResources(t.Context(), db, cfg)
	if !errors.Is(err, errInvalidInput) {
		t.Fatalf("未配置白名单应以参数错误拒绝，实际：%v", err)
	}
	if !strings.Contains(err.Error(), pageAllowedDomainsKey) {
		t.Fatalf("未配置白名单应有中文提示指明配置键 %s，实际：%v", pageAllowedDomainsKey, err)
	}
	if n := recorder.count(); n != 0 {
		t.Fatalf("白名单缺失时不得发起探测，实际 %d 次", n)
	}

	// ② 白名单只含 allowed.example.com：其它域名被拒，且不探测。
	setPageAllowedDomains(t, db, "allowed.example.com")
	err = validatePageResources(t.Context(), db, cfg)
	if !errors.Is(err, errInvalidInput) {
		t.Fatalf("白名单外域名应以参数错误拒绝，实际：%v", err)
	}
	if !strings.Contains(err.Error(), "不在已审核域名白名单内") {
		t.Fatalf("白名单外域名应说明原因，实际：%v", err)
	}
	if n := recorder.count(); n != 0 {
		t.Fatalf("白名单外域名不得发起探测，实际 %d 次", n)
	}

	// ③ 命中白名单后才发起一次探测。
	setPageAllowedDomains(t, db, "cdn.example.com")
	if err = validatePageResources(t.Context(), db, cfg); err != nil {
		t.Fatalf("白名单内图片应通过校验，实际：%v", err)
	}
	if n := recorder.count(); n != 1 {
		t.Fatalf("白名单内图片应探测一次，实际 %d 次", n)
	}
}

// 发布链路同样拦白名单外图片（发布动作不能被当成 SSRF 探测器）。
func TestPagePublishRejectsUnwhitelistedImage(t *testing.T) {
	db := openPageDB(t, "esports_page_ssrf_publish")
	recorder := withSSRFProbeRecorder(t)
	channel := pageChannelCustomerHome
	now := time.Now().UTC()

	// 保存草稿放行待审核域名（与外部跳转链接同一口径），发布前才强制白名单。
	if _, err := savePageDraft(t.Context(), db, channel, 1, pageDraftInput{
		Components: ssrfCarouselComponents("image", "https://evil.example.com/a.png"), ExpectedVersion: 0,
	}); err != nil {
		t.Fatalf("保存草稿不应拦待审核域名：%v", err)
	}
	setPageAllowedDomains(t, db, "allowed.example.com")
	if _, err := publishPage(t.Context(), db, channel, 1, pagePublishInput{Mode: "immediate"}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("白名单外图片发布应被拒，实际：%v", err)
	}
	if n := recorder.count(); n != 0 {
		t.Fatalf("发布被拒时不得探测，实际 %d 次", n)
	}

	// 白名单内图片：发布成功。
	setPageAllowedDomains(t, db, "cdn.example.com")
	if _, err := savePageDraft(t.Context(), db, channel, 1, pageDraftInput{
		Components: ssrfCarouselComponents("image", "https://cdn.example.com/a.png"), ExpectedVersion: 1,
	}); err != nil {
		t.Fatalf("更新草稿失败：%v", err)
	}
	if _, err := publishPage(t.Context(), db, channel, 1, pagePublishInput{Mode: "immediate"}, now); err != nil {
		t.Fatalf("白名单内图片应可发布，实际：%v", err)
	}
	if n := recorder.count(); n != 1 {
		t.Fatalf("发布校验应探测一次，实际 %d 次", n)
	}
}

// 轮播图 images 数组：每个元素都过 https + 白名单 + 探测（不能只认单数 image）。
func TestPageCarouselImagesArrayValidated(t *testing.T) {
	db := openPageDB(t, "esports_page_ssrf_images")
	recorder := withSSRFProbeRecorder(t)
	channel := pageChannelCustomerHome
	now := time.Now().UTC()

	// 数组元素为 http：保存草稿即被拒（此前数组元素完全不校验）。
	if _, err := savePageDraft(t.Context(), db, channel, 1, pageDraftInput{
		Components: ssrfCarouselComponents("images", []interface{}{"http://cdn.example.com/a.png"}),
	}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("images 数组中的 http 地址保存时应被拒，实际：%v", err)
	}
	// 数组元素带凭证 / 指向内网：同样被拒。
	for _, raw := range []string{"https://user:pass@cdn.example.com/a.png", "https://127.0.0.1/a.png"} {
		if _, err := savePageDraft(t.Context(), db, channel, 1, pageDraftInput{
			Components: ssrfCarouselComponents("images", []interface{}{raw}),
		}); !errors.Is(err, errInvalidInput) {
			t.Fatalf("images 数组中的非法地址 %q 保存时应被拒，实际：%v", raw, err)
		}
	}
	if n := recorder.count(); n != 0 {
		t.Fatalf("保存阶段不应发起探测，实际 %d 次", n)
	}

	// 合法 https 但白名单外：发布被拒且不探测。
	if _, err := savePageDraft(t.Context(), db, channel, 1, pageDraftInput{
		Components: ssrfCarouselComponents("images", []interface{}{"https://cdn.example.com/a.png", "/static/b.png"}),
	}); err != nil {
		t.Fatalf("images 数组合法地址应可保存：%v", err)
	}
	setPageAllowedDomains(t, db, "allowed.example.com")
	if _, err := publishPage(t.Context(), db, channel, 1, pagePublishInput{Mode: "immediate"}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("images 数组中白名单外地址发布应被拒，实际：%v", err)
	}
	if n := recorder.count(); n != 0 {
		t.Fatalf("images 数组白名单外不得探测，实际 %d 次", n)
	}

	// 白名单命中：数组里每个外部地址各探测一次（站内相对路径不探测）。
	setPageAllowedDomains(t, db, "cdn.example.com")
	if _, err := publishPage(t.Context(), db, channel, 1, pagePublishInput{Mode: "immediate"}, now); err != nil {
		t.Fatalf("images 数组白名单内地址应可发布，实际：%v", err)
	}
	if n := recorder.count(); n != 1 {
		t.Fatalf("images 数组应只探测外部地址一次，实际 %d 次", n)
	}
}

// http / 凭证 / 非 80·443 端口 / 本机与内网字面量地址一律拒绝，且不发起探测。
// 白名单里故意登记了这些主机：证明拒绝来自「网络目标校验」，不依赖白名单是否配置。
func TestPageProbeRejectsHTTPAndLocalTargets(t *testing.T) {
	db := openPageDB(t, "esports_page_ssrf_targets")
	recorder := withSSRFProbeRecorder(t)
	setPageAllowedDomains(t, db,
		"cdn.example.com,localhost,app.localhost,127.0.0.1,10.0.0.5,192.168.1.1,169.254.1.1,172.16.0.9,::1")

	cases := []struct {
		name string
		url  string
	}{
		{"http 明文", "http://cdn.example.com/a.png"},
		{"带用户名口令", "https://user:pass@cdn.example.com/a.png"},
		{"非 80/443 端口", "https://cdn.example.com:8443/a.png"},
		{"localhost", "https://localhost/a.png"},
		{"子域 localhost", "https://app.localhost/a.png"},
		{"环回 IPv4", "https://127.0.0.1/a.png"},
		{"私网 10/8", "https://10.0.0.5/a.png"},
		{"私网 192.168/16", "https://192.168.1.1/a.png"},
		{"私网 172.16/12", "https://172.16.0.9/a.png"},
		{"链路本地", "https://169.254.1.1/a.png"},
		{"环回 IPv6", "https://[::1]/a.png"},
	}
	for _, tc := range cases {
		err := validatePageResources(t.Context(), db, ssrfImageConfig("image", tc.url))
		if !errors.Is(err, errInvalidInput) {
			t.Fatalf("%s（%s）应以参数错误拒绝，实际：%v", tc.name, tc.url, err)
		}
	}
	if n := recorder.count(); n != 0 {
		t.Fatalf("非法网络目标不得发起任何探测，实际 %d 次", n)
	}
}

// P1-20：素材 mime / size_bytes 必填，超 10MiB 与非图片类型被拒。
func TestPageAssetRequiresMimeAndSize(t *testing.T) {
	db := openPageDB(t, "esports_page_asset_required")
	ctx := t.Context()

	base := pageAssetInput{Name: "图标", URL: "https://cdn.example.com/a.png", Mime: "image/png", SizeBytes: 1024}

	missingMime := base
	missingMime.Mime = ""
	if _, err := savePageAsset(ctx, db, 1, missingMime); !errors.Is(err, errInvalidInput) {
		t.Fatalf("缺少 mime 应被拒，实际：%v", err)
	} else if !strings.Contains(err.Error(), "mime") {
		t.Fatalf("缺少 mime 的中文提示应指明字段，实际：%v", err)
	}

	missingSize := base
	missingSize.SizeBytes = 0
	if _, err := savePageAsset(ctx, db, 1, missingSize); !errors.Is(err, errInvalidInput) {
		t.Fatalf("缺少 size_bytes 应被拒，实际：%v", err)
	} else if !strings.Contains(err.Error(), "size_bytes") {
		t.Fatalf("缺少 size_bytes 的中文提示应指明字段，实际：%v", err)
	}

	tooBig := base
	tooBig.SizeBytes = pageAssetMaxBytes + 1
	if _, err := savePageAsset(ctx, db, 1, tooBig); !errors.Is(err, errInvalidInput) {
		t.Fatalf("超过 10MiB 应被拒，实际：%v", err)
	}

	notImage := base
	notImage.Mime = "application/javascript"
	if _, err := savePageAsset(ctx, db, 1, notImage); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非图片 mime 应被拒，实际：%v", err)
	}

	// 上限内的合法素材仍可登记。
	asset, err := savePageAsset(ctx, db, 1, base)
	if err != nil {
		t.Fatalf("合法素材应可登记，实际：%v", err)
	}
	if asset.Mime != "image/png" || asset.SizeBytes != 1024 {
		t.Fatalf("素材落库字段异常：%+v", asset)
	}
	if n := db.EsportsPageAsset.Query().CountX(ctx); n != 1 {
		t.Fatalf("被拒素材不应落库，实际库中 %d 条", n)
	}
}
