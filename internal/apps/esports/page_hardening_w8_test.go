package esports

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// 「清零残留」批次（审核报告 §8.2-16 / §B.3-2 / §8.1-8）回归测试。
// 本文件全部辅助物以 w8 前缀命名，避免与并行批次 / 既有测试的辅助物撞名。

// 单元测试不下发真实 DNS：默认所有域名解析到公网地址；
// 需要构造 DNS rebinding 场景的用例再用 withW8Resolver 覆盖。
func init() {
	pageResolveHostIPs = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
}

// w8PublicIP 是默认公网解析结果（example.com 的固定测试地址）。
const w8PublicIP = "93.184.216.34"

// withW8Resolver 把 DNS 解析替换成固定结果（不发起真实解析）。
func withW8Resolver(t *testing.T, ips ...string) {
	t.Helper()
	parsed := make([]net.IP, 0, len(ips))
	for _, raw := range ips {
		parsed = append(parsed, net.ParseIP(raw))
	}
	original := pageResolveHostIPs
	pageResolveHostIPs = func(context.Context, string) ([]net.IP, error) { return parsed, nil }
	t.Cleanup(func() { pageResolveHostIPs = original })
}

// w8ProbeCounter 统计图片探测调用次数与地址（断言「未发起请求」/「同一 URL 只探测一次」）。
type w8ProbeCounter struct {
	mu      sync.Mutex
	urls    []string
	inFling int
	maxFly  int
	mode    func(rawURL string) error
	delay   time.Duration
}

func (c *w8ProbeCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.urls)
}

func (c *w8ProbeCounter) maxInFlight() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxFly
}

func (c *w8ProbeCounter) calledWith(rawURL string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, seen := range c.urls {
		if seen == rawURL {
			n++
		}
	}
	return n
}

// withW8ProbeCounter 用计数器（不发起真实网络请求）替换图片探测，测试结束恢复。
func withW8ProbeCounter(t *testing.T, mode func(rawURL string) error, delay time.Duration) *w8ProbeCounter {
	t.Helper()
	counter := &w8ProbeCounter{mode: mode, delay: delay}
	original := pageProbeImage
	pageProbeImage = func(ctx context.Context, rawURL string) error {
		counter.mu.Lock()
		counter.urls = append(counter.urls, rawURL)
		counter.inFling++
		if counter.inFling > counter.maxFly {
			counter.maxFly = counter.inFling
		}
		counter.mu.Unlock()
		defer func() {
			counter.mu.Lock()
			counter.inFling--
			counter.mu.Unlock()
		}()
		if counter.delay > 0 {
			select {
			case <-time.After(counter.delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if counter.mode != nil {
			return counter.mode(rawURL)
		}
		return nil
	}
	t.Cleanup(func() { pageProbeImage = original })
	return counter
}

// w8ImageConfig 构造含若干外部图片的发布校验配置（carousel 的 items[].image）。
func w8ImageConfig(urls ...string) pageConfig {
	visible := true
	items := make([]interface{}, 0, len(urls))
	for _, raw := range urls {
		items = append(items, map[string]interface{}{"image": raw})
	}
	return pageConfig{Components: []pageComponent{
		{Type: "search", Visible: &visible},
		{Type: "carousel", Visible: &visible, Props: map[string]interface{}{"items": items}},
		{Type: "support_entry", Visible: &visible},
	}}
}

// ── §8.2-16 / §B.3-2：DNS rebinding 防护 ──

// 白名单域名解析到环回 / 私网 / 链路本地 / 唯一本地 / 未指定地址时：拒绝且**不发请求**。
func TestPageProbeRejectsNonPublicResolvedIP(t *testing.T) {
	cases := []struct {
		name string
		ip   string
	}{
		{"环回 127.0.0.1", "127.0.0.1"},
		{"私网 10/8", "10.0.0.1"},
		{"私网 172.16/12", "172.16.5.5"},
		{"私网 192.168/16", "192.168.1.9"},
		{"链路本地 169.254/16", "169.254.169.254"},
		{"环回 ::1", "::1"},
		{"IPv6 唯一本地 fc00::/7", "fc00::1"},
		{"IPv6 链路本地 fe80::/10", "fe80::1"},
		{"未指定 0.0.0.0", "0.0.0.0"},
		{"未指定 ::", "::"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openPageDB(t, "esports_page_w8_dns_"+strings.ReplaceAll(tc.ip, ":", "_"))
			setPageAllowedDomains(t, db, "cdn.example.com")
			counter := withW8ProbeCounter(t, nil, 0)
			withW8Resolver(t, tc.ip)

			err := validatePageResources(t.Context(), db, w8ImageConfig("https://cdn.example.com/a.png"))
			if !errors.Is(err, errInvalidInput) {
				t.Fatalf("%s 解析结果应被拒，实际：%v", tc.name, err)
			}
			if !strings.Contains(err.Error(), "解析到本机或内网地址") {
				t.Fatalf("%s 应有中文提示说明原因，实际：%v", tc.name, err)
			}
			if n := counter.count(); n != 0 {
				t.Fatalf("%s 不得发起任何探测请求，实际 %d 次", tc.name, n)
			}
		})
	}
}

// 一次解析返回多个 IP 时，只要有一条是内网地址就整体拒绝（不做「取第一条」放行）。
func TestPageProbeRejectsMixedResolution(t *testing.T) {
	db := openPageDB(t, "esports_page_w8_dns_mixed")
	setPageAllowedDomains(t, db, "cdn.example.com")
	counter := withW8ProbeCounter(t, nil, 0)
	withW8Resolver(t, w8PublicIP, "10.1.2.3")

	err := validatePageResources(t.Context(), db, w8ImageConfig("https://cdn.example.com/a.png"))
	if !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "10.1.2.3") {
		t.Fatalf("混合解析结果（含私网 IP）应被拒并指明地址，实际：%v", err)
	}
	if n := counter.count(); n != 0 {
		t.Fatalf("混合解析结果不得发起探测，实际 %d 次", n)
	}

	// 反向：全部为公网地址时正常探测一次。
	withW8Resolver(t, w8PublicIP, "93.184.216.35")
	if err = validatePageResources(t.Context(), db, w8ImageConfig("https://cdn.example.com/a.png")); err != nil {
		t.Fatalf("全部公网解析结果应通过，实际：%v", err)
	}
	if n := counter.count(); n != 1 {
		t.Fatalf("公网解析结果应探测一次，实际 %d 次", n)
	}
}

// 解析失败同样拒绝且不发请求（fail-closed）。
func TestPageProbeRejectsResolutionFailure(t *testing.T) {
	db := openPageDB(t, "esports_page_w8_dns_fail")
	setPageAllowedDomains(t, db, "cdn.example.com")
	counter := withW8ProbeCounter(t, nil, 0)
	original := pageResolveHostIPs
	pageResolveHostIPs = func(context.Context, string) ([]net.IP, error) {
		return nil, errors.New("no such host")
	}
	t.Cleanup(func() { pageResolveHostIPs = original })

	err := validatePageResources(t.Context(), db, w8ImageConfig("https://cdn.example.com/a.png"))
	if !errors.Is(err, errInvalidInput) || !strings.Contains(err.Error(), "解析失败") {
		t.Fatalf("解析失败应 fail-closed，实际：%v", err)
	}
	if n := counter.count(); n != 0 {
		t.Fatalf("解析失败不得发起探测，实际 %d 次", n)
	}
}

// 拨号器必须用「被校验过的同一份解析结果」建连：内网解析结果一次拨号都不发生，
// 公网解析结果拨到的正是校验过的 IP（杜绝校验一次、请求时重新解析的 rebinding 窗口）。
func TestPageProbeDialUsesValidatedIPs(t *testing.T) {
	var dialed []string
	originalDial := pageDialIP
	pageDialIP = func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		return nil, errors.New("dial stub")
	}
	t.Cleanup(func() { pageDialIP = originalDial })

	// 内网解析结果：拒绝，且未拨号。
	withW8Resolver(t, "10.0.0.7")
	_, err := pageProbeDialContext(t.Context(), "tcp", "cdn.example.com:443")
	if err == nil || !strings.Contains(err.Error(), "10.0.0.7") {
		t.Fatalf("内网解析结果应拒绝拨号并指明地址，实际：%v", err)
	}
	if len(dialed) != 0 {
		t.Fatalf("内网解析结果不得拨号，实际拨号 %v", dialed)
	}

	// 公网解析结果：拨号目标就是被校验过的 IP（不是重新解析的地址）。
	withW8Resolver(t, w8PublicIP)
	_, err = pageProbeDialContext(t.Context(), "tcp", "cdn.example.com:443")
	if err == nil {
		t.Fatal("拨号桩返回错误，调用方应把错误透出")
	}
	if len(dialed) != 1 || dialed[0] != net.JoinHostPort(w8PublicIP, "443") {
		t.Fatalf("拨号目标应为被校验过的 IP，实际 %v", dialed)
	}
}

// ── §8.2-16 后半：串行 → 有并发上限的并行 + 同一 URL 只探测一次 ──

// 多个 URL：并行探测、结果与串行一致（全部通过即通过）、重复 URL 只探测一次、并发不超过上限。
func TestPageResourcesProbeInParallelWithDedup(t *testing.T) {
	db := openPageDB(t, "esports_page_w8_parallel")
	setPageAllowedDomains(t, db, "cdn.example.com")
	counter := withW8ProbeCounter(t, nil, 40*time.Millisecond)
	withW8Resolver(t, w8PublicIP)

	urls := []string{
		"https://cdn.example.com/1.png",
		"https://cdn.example.com/2.png",
		"https://cdn.example.com/3.png",
		"https://cdn.example.com/4.png",
		"https://cdn.example.com/1.png", // 重复
		"https://cdn.example.com/2.png", // 重复
	}
	started := time.Now()
	if err := validatePageResources(t.Context(), db, w8ImageConfig(urls...)); err != nil {
		t.Fatalf("全部可访问的图片应通过，实际：%v", err)
	}
	elapsed := time.Since(started)

	if n := counter.count(); n != 4 {
		t.Fatalf("6 个地址（去重后 4 个）应只探测 4 次，实际 %d 次", n)
	}
	for _, raw := range urls {
		if n := counter.calledWith(raw); n != 1 {
			t.Fatalf("%s 应只探测一次，实际 %d 次", raw, n)
		}
	}
	// 串行需要 4×40ms=160ms；并行（上限 4）应明显更快，留足调度余量。
	if elapsed > 140*time.Millisecond {
		t.Fatalf("探测应并行执行，实际耗时 %s", elapsed)
	}
	if maxFly := counter.maxInFlight(); maxFly > pageProbeConcurrency {
		t.Fatalf("并发数不得超过 %d，实际 %d", pageProbeConcurrency, maxFly)
	}
}

// 部分失败：全部探测跑完后再统一返回错误，且按 URL 列出失败原因（不再只报第一个）。
func TestPageResourcesProbeAggregatesFailures(t *testing.T) {
	db := openPageDB(t, "esports_page_w8_aggregate")
	setPageAllowedDomains(t, db, "cdn.example.com")
	counter := withW8ProbeCounter(t, func(rawURL string) error {
		if strings.Contains(rawURL, "broken") {
			return errors.New("HTTP 404")
		}
		return nil
	}, 0)
	withW8Resolver(t, w8PublicIP)

	cfg := w8ImageConfig(
		"https://cdn.example.com/ok.png",
		"https://cdn.example.com/broken-1.png",
		"https://cdn.example.com/broken-2.png",
	)
	err := validatePageResources(t.Context(), db, cfg)
	if !errors.Is(err, errInvalidInput) {
		t.Fatalf("存在不可访问图片应以参数错误拒绝，实际：%v", err)
	}
	for _, raw := range []string{"broken-1.png", "broken-2.png"} {
		if !strings.Contains(err.Error(), raw) {
			t.Fatalf("错误信息应按 URL 列出失败原因（缺 %s）：%v", raw, err)
		}
	}
	if strings.Contains(err.Error(), "ok.png") {
		t.Fatalf("成功的 URL 不应出现在错误信息里：%v", err)
	}
	if n := counter.count(); n != 3 {
		t.Fatalf("三个地址都应被探测（并行跑完再汇总），实际 %d 次", n)
	}
}

// 整体超时：超过总时限即失败，并给中文提示。
func TestPageResourcesProbeTotalTimeout(t *testing.T) {
	db := openPageDB(t, "esports_page_w8_timeout")
	setPageAllowedDomains(t, db, "cdn.example.com")
	withW8Resolver(t, w8PublicIP)
	withW8ProbeCounter(t, nil, time.Hour) // 永不自行返回，只等 ctx 超时
	originalTimeout := pageProbeTotalTimeout
	pageProbeTotalTimeout = 60 * time.Millisecond
	t.Cleanup(func() { pageProbeTotalTimeout = originalTimeout })

	started := time.Now()
	err := validatePageResources(t.Context(), db, w8ImageConfig(
		"https://cdn.example.com/1.png",
		"https://cdn.example.com/2.png",
	))
	if !errors.Is(err, errInvalidInput) {
		t.Fatalf("整体超时应以参数错误失败，实际：%v", err)
	}
	if !strings.Contains(err.Error(), "超时") {
		t.Fatalf("超时应给中文提示，实际：%v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("总超时未生效，实际耗时 %s", elapsed)
	}
}

// ── §8.1-8 后半：底部导航允许增删条目与编辑 path / order ──

// w8TabbarItem 构造一个只填必填字段的导航项。
func w8TabbarItem(path, text string, order *int) pageTabbarItem {
	return pageTabbarItem{Path: path, Text: text, Order: order}
}

func w8IntPtr(v int) *int { return &v }

// 允许增删条目（1~5 项）、编辑 path 与 order，并按 order 升序返回。
func TestPageTabbarAllowsPathAndOrderEdits(t *testing.T) {
	// ① 增加到 5 项（上限），路径都命中已注册页面白名单。
	five := []pageTabbarItem{
		w8TabbarItem("/pages/index/index", "首页", nil),
		w8TabbarItem("/pages/order/list", "订单", nil),
		w8TabbarItem("/pages/mine/index", "我的", nil),
		w8TabbarItem("/pages/messages/index", "消息", nil),
		w8TabbarItem("/pages/wallet/index", "钱包", nil),
	}
	out, err := validatePageTabbar(five)
	if err != nil {
		t.Fatalf("5 项导航应被允许，实际：%v", err)
	}
	if len(out) != 5 {
		t.Fatalf("应保留 5 项，实际 %d 项", len(out))
	}

	// ② 改 path（仍命中白名单）与 order：结果按 order 升序。
	edited := []pageTabbarItem{
		w8TabbarItem("/pages/wallet/index", "钱包", w8IntPtr(3)),
		w8TabbarItem("/pages/index/index", "首页", w8IntPtr(1)),
		w8TabbarItem("/pages/order/list", "订单", w8IntPtr(2)),
	}
	out, err = validatePageTabbar(edited)
	if err != nil {
		t.Fatalf("编辑 path / order 应被允许，实际：%v", err)
	}
	gotPaths := []string{out[0].Path, out[1].Path, out[2].Path}
	wantPaths := []string{"/pages/index/index", "/pages/order/list", "/pages/wallet/index"}
	if strings.Join(gotPaths, ",") != strings.Join(wantPaths, ",") {
		t.Fatalf("应按 order 升序返回，实际 %v", gotPaths)
	}

	// ③ 删减到 1 项（下限）也被允许。
	one, err := validatePageTabbar([]pageTabbarItem{w8TabbarItem("/pages/order/list", "订单", nil)})
	if err != nil || len(one) != 1 {
		t.Fatalf("1 项导航应被允许，实际 %v / %v", one, err)
	}
	if one[0].Order == nil || *one[0].Order != 1 {
		t.Fatalf("未填 order 时应按位置补齐为 1，实际 %+v", one[0].Order)
	}

	// ④ 路径带 query / 末尾斜杠时按注册路径规范化。
	normalized, err := validatePageTabbar([]pageTabbarItem{w8TabbarItem("/pages/order/list?id=9", "订单", nil)})
	if err != nil || normalized[0].Path != "/pages/order/list" {
		t.Fatalf("带 query 的站内路径应规范化为注册路径，实际 %+v / %v", normalized, err)
	}
}

// 非法 path / 重复 path / 重复 order / 条目数越界 / 短标识图标一律拒绝（中文提示）。
func TestPageTabbarRejectsInvalidShape(t *testing.T) {
	cases := []struct {
		name  string
		items []pageTabbarItem
		want  string
	}{
		{
			name:  "未注册页面路径",
			items: []pageTabbarItem{w8TabbarItem("/pages/not-registered/index", "首页", nil)},
			want:  "不是已注册页面",
		},
		{
			name:  "缺少前导斜杠",
			items: []pageTabbarItem{w8TabbarItem("pages/index/index", "首页", nil)},
			want:  "站内相对路径",
		},
		{
			name:  "含协议",
			items: []pageTabbarItem{w8TabbarItem("https://evil.example.com/x", "首页", nil)},
			want:  "站内相对路径",
		},
		{
			name:  "含路径穿越",
			items: []pageTabbarItem{w8TabbarItem("/pages/../etc/passwd", "首页", nil)},
			want:  "非法",
		},
		{
			name:  "协议相对",
			items: []pageTabbarItem{w8TabbarItem("//evil.example.com/x", "首页", nil)},
			want:  "非法",
		},
		{
			name: "重复 path",
			items: []pageTabbarItem{
				w8TabbarItem("/pages/index/index", "首页", nil),
				w8TabbarItem("/pages/index/index", "首页2", nil),
			},
			want: "重复",
		},
		{
			name: "重复 order",
			items: []pageTabbarItem{
				w8TabbarItem("/pages/index/index", "首页", w8IntPtr(2)),
				w8TabbarItem("/pages/order/list", "订单", w8IntPtr(2)),
			},
			want: "order 2 重复",
		}, {
			name:  "order 越界",
			items: []pageTabbarItem{w8TabbarItem("/pages/index/index", "首页", w8IntPtr(9))},
			want:  "order 须在 1~5 之间",
		},
		{
			name:  "条目数为 0",
			items: nil,
			want:  "须为 1~5 项",
		},
		{
			name: "条目数为 6",
			items: []pageTabbarItem{
				w8TabbarItem("/pages/index/index", "首页", nil),
				w8TabbarItem("/pages/order/list", "订单", nil),
				w8TabbarItem("/pages/mine/index", "我的", nil),
				w8TabbarItem("/pages/messages/index", "消息", nil),
				w8TabbarItem("/pages/wallet/index", "钱包", nil),
				w8TabbarItem("/pages/login/index", "登录", nil),
			},
			want: "须为 1~5 项",
		},
		{
			name:  "图标短标识",
			items: []pageTabbarItem{{Path: "/pages/index/index", Text: "首页", Icon: "icon-home"}},
			want:  "不支持短标识",
		},
		{
			name:  "文案为空",
			items: []pageTabbarItem{w8TabbarItem("/pages/index/index", "   ", nil)},
			want:  "文案须为",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := validatePageTabbar(tc.items); !errors.Is(err, errInvalidInput) {
				t.Fatalf("%s 应以参数错误拒绝，实际：%v", tc.name, err)
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s 的中文提示应包含 %q，实际：%v", tc.name, tc.want, err)
			}
		})
	}
}

// 落库链路：保存 1 项（删减）与 5 项（新增 + 自定义 order）都能往返，6 项被拒。
func TestPageTabbarSaveSupportsAddRemoveAndOrder(t *testing.T) {
	db := openPageDB(t, "esports_page_w8_tabbar_save")
	ctx := t.Context()
	channel := pageChannelCustomerHome
	if _, err := savePageDraft(ctx, db, channel, 1, pageDraftInput{
		Components: pageTestComponents(), ExpectedVersion: 0,
	}); err != nil {
		t.Fatalf("保存草稿失败：%v", err)
	}

	// 删减到 1 项。
	doc, err := savePageTabbar(ctx, db, channel, 1, 1, []pageTabbarItem{w8TabbarItem("/pages/order/list", "订单", nil)})
	if err != nil {
		t.Fatalf("删减到 1 项应被允许，实际：%v", err)
	}
	stored, err := pageTabbarFromConfig(doc.DraftConfig)
	if err != nil || len(stored) != 1 || stored[0].Path != "/pages/order/list" {
		t.Fatalf("1 项导航未正确落库：%+v / %v", stored, err)
	}

	// 新增到 5 项并自定义 order：读回按 order 升序。
	items := []pageTabbarItem{
		w8TabbarItem("/pages/wallet/index", "钱包", w8IntPtr(5)),
		w8TabbarItem("/pages/index/index", "首页", w8IntPtr(1)),
		w8TabbarItem("/pages/order/list", "订单", w8IntPtr(2)),
		w8TabbarItem("/pages/mine/index", "我的", w8IntPtr(3)),
		w8TabbarItem("/pages/messages/index", "消息", w8IntPtr(4)),
	}
	doc, err = savePageTabbar(ctx, db, channel, 1, doc.DraftVersion, items)
	if err != nil {
		t.Fatalf("新增到 5 项应被允许，实际：%v", err)
	}
	stored, err = pageTabbarFromConfig(doc.DraftConfig)
	if err != nil || len(stored) != 5 {
		t.Fatalf("5 项导航未正确落库：%+v / %v", stored, err)
	}
	if stored[0].Path != "/pages/index/index" || stored[4].Path != "/pages/wallet/index" {
		t.Fatalf("落库后应按 order 升序：%+v", stored)
	}

	// 6 项被拒，且不落库。
	tooMany := append(append([]pageTabbarItem{}, items...), w8TabbarItem("/pages/login/index", "登录", nil))
	if _, err = savePageTabbar(ctx, db, channel, 1, doc.DraftVersion, tooMany); !errors.Is(err, errInvalidInput) {
		t.Fatalf("6 项导航应以参数错误拒绝，实际：%v", err)
	}
}
