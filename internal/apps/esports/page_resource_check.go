package esports

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsproduct"
	"go-admin/internal/ent/esportsrechargecampaign"
	"go-admin/internal/ent/esportssku"
)

// 发布前资源有效性校验（§16.4 增量补充）：在既有域名白名单校验之后执行，
// 不改动、不回退既有校验链（validatePageConfigWithAllowedDomains）。
//
// 本轮（清零残留 §8.2-16）在此文件内做两件事：
//  1. DNS rebinding 防护：白名单域名解析出的 IP 逐条必须为公网地址，否则直接拒绝且**不发请求**；
//     真正拨号时用的是**同一份被校验过的解析结果**（自建 Transport 的 DialContext）。
//  2. 图片探测串行 → 有并发上限的并行 + 同一 URL 只探测一次 + 整体超时。

// pageGenericTextLimit 是组件属性中任意字符串字段的通用长度上限（字符）；
// title/subtitle/name 已有更严的专用上限（page.go），此处兜底其余文本字段（如公告 text）。
const pageGenericTextLimit = 500

const (
	// pageImageProbeTimeout 是单张图片 URL 可访问性探测的超时。
	pageImageProbeTimeout = 5 * time.Second
	// pageProbeConcurrency 是发布期图片探测的并发上限。
	// 取值理由：发布是低频管理操作，4 并发既能显著缩短多图页面的发布耗时，
	// 又不会对单个图床 / CDN 形成突发压力（原实现逐张串行，N 张图最坏耗时 5s×N）。
	pageProbeConcurrency = 4
)

// pageProbeTotalTimeout 是发布期图片探测的整体超时上限：
// 单张 5s × 4 并发 × 15s 足以覆盖 12 张图；再多也不允许把发布请求无限拖住。
// 以变量暴露仅为让单元测试能缩短到毫秒级，生产路径不修改。
var pageProbeTotalTimeout = 15 * time.Second

// pageResolveHostIPs 解析域名得到 IP 列表。
// 以包级变量暴露，便于测试注入解析结果（单元测试不下发真实 DNS），
// 生产路径默认走标准库解析器。
var pageResolveHostIPs = func(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		if addr.IP != nil {
			ips = append(ips, addr.IP)
		}
	}
	return ips, nil
}

// pageDialIP 实际建立 TCP 连接。以包级变量暴露，测试可断言「校验不通过时一次拨号都没发生」。
var pageDialIP = func(ctx context.Context, network, address string) (net.Conn, error) {
	return (&net.Dialer{Timeout: pageImageProbeTimeout}).DialContext(ctx, network, address)
}

// pageProbeImage 探测外部图片 URL 可访问性（§16.4）：默认 HEAD→GET，失败视为不可访问。
// 以包级变量暴露，便于测试中替换为桩函数，避免单元测试发起真实网络请求。
// 默认实现使用 pageProbeHTTPClient：其拨号器会用「被校验过的解析结果」连目标 IP。
var pageProbeImage = func(ctx context.Context, rawURL string) error {
	return probePageImageURLWithClient(ctx, pageProbeHTTPClient(), rawURL)
}

// 指向商品 / 活动的组件属性键（§16.4）：自由 props 中引用这些键时，发布前校验其有效性。
var (
	pageSKUFields      = map[string]bool{"sku_id": true, "sku_ids": true}
	pageProductFields  = map[string]bool{"product_id": true, "product_ids": true}
	pageCampaignFields = map[string]bool{"campaign_id": true, "campaign_ids": true, "activity_id": true, "activity_ids": true}
)

// validatePageResources 发布前校验资源有效性（§16.4 增量）：图片 URL 可访问、跳转链接合法性、
// 商品/活动有效性、文本长度超限。additive：应在域名白名单校验通过后调用。
//
// P0-17：外部图片探测必须先过 `app.esports.page_allowed_domains` 域名白名单，
// 不在白名单内的地址**不发起任何网络请求**，直接判定不可用（避免发布动作被当作 SSRF 探测器）。
// 白名单缺失时按 fail-closed 处理：视为空集合，拒绝全部外部图片探测并提示配置键。
//
// §8.2-16：白名单域名还要过「解析后 IP 必须是公网地址」的二次校验（见 pageCheckExternalImage），
// 且全部组件的同步校验通过后才并行发起探测（同一 URL 只探测一次，结果统一汇总）。
func validatePageResources(ctx context.Context, db *ent.Client, cfg pageConfig) error {
	allowedDomains := pageAllowedDomains(ctx, db)
	probes := newPageProbeCollector()
	for i, comp := range cfg.Components {
		if err := validatePageResourceProps(ctx, db, allowedDomains, probes, i, comp.Props); err != nil {
			return err
		}
	}
	// 探测阶段放在同步校验之后：任何结构 / 白名单 / DNS 问题都能在发起请求前快速失败。
	return probes.run(ctx)
}

// validatePageResourceProps 递归校验单个组件的自由属性。
func validatePageResourceProps(ctx context.Context, db *ent.Client, allowedDomains map[string]bool,
	probes *pageProbeCollector, compIndex int, props map[string]interface{}) error {
	for key, value := range props {
		if err := validatePageResourceValue(ctx, db, allowedDomains, probes, compIndex, key, value); err != nil {
			return err
		}
	}
	return nil
}

func validatePageResourceValue(ctx context.Context, db *ent.Client, allowedDomains map[string]bool,
	probes *pageProbeCollector, compIndex int, key string, value interface{}) error {
	switch v := value.(type) {
	case string:
		// 通用文本长度兜底（专用上限 title/subtitle/name 已在 validatePageValue 校验）。
		if n := len([]rune(v)); n > pageGenericTextLimit {
			return fmt.Errorf("%w：第 %d 个组件字段 %q 文本超长（%d/%d）", errInvalidInput, compIndex+1, key, n, pageGenericTextLimit)
		}
		if pageImageFields[key] {
			if err := pageCheckExternalImage(ctx, allowedDomains, probes, compIndex, key, v); err != nil {
				return err
			}
		}
		if pageLinkFields[key] {
			if err := validatePageLinkSafety(v, compIndex, key); err != nil {
				return err
			}
		}
		if id, ok := pageEntityFieldID(key, v); ok {
			return pageCheckEntity(ctx, db, key, id, compIndex)
		}
	case float64:
		if id, ok := pageEntityFieldID(key, v); ok {
			return pageCheckEntity(ctx, db, key, id, compIndex)
		}
	case int:
		if id, ok := pageEntityFieldID(key, v); ok {
			return pageCheckEntity(ctx, db, key, id, compIndex)
		}
	case map[string]interface{}:
		for k, sub := range v {
			if err := validatePageResourceValue(ctx, db, allowedDomains, probes, compIndex, k, sub); err != nil {
				return err
			}
		}
	case []interface{}:
		// 实体引用列表（sku_ids / product_ids / activity_ids 等）逐项校验。
		if pageSKUFields[key] || pageProductFields[key] || pageCampaignFields[key] {
			for _, item := range v {
				id, ok := pageEntityFieldID(key, item)
				if !ok {
					return fmt.Errorf("%w：第 %d 个组件字段 %q 含无效的 ID", errInvalidInput, compIndex+1, key)
				}
				if err := pageCheckEntity(ctx, db, key, id, compIndex); err != nil {
					return err
				}
			}
			return nil
		}
		// 图片数组（轮播图 images 等）：每个元素与单数字段走同一套校验（P1）。
		for _, item := range v {
			if err := validatePageResourceValue(ctx, db, allowedDomains, probes, compIndex, key, item); err != nil {
				return err
			}
		}
	}
	return nil
}

// pageEntityFieldID 从属性值中解析出实体 ID（数字或数字字符串）；非实体字段返回 ok=false。
func pageEntityFieldID(key string, value interface{}) (int, bool) {
	if !pageSKUFields[key] && !pageProductFields[key] && !pageCampaignFields[key] {
		return 0, false
	}
	switch v := value.(type) {
	case float64:
		if v < 0 || v != float64(int(v)) {
			return 0, false
		}
		return int(v), true
	case int:
		if v < 0 {
			return 0, false
		}
		return v, true
	case string:
		id, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || id < 0 {
			return 0, false
		}
		return id, true
	default:
		return 0, false
	}
}

// pageCheckEntity 校验商品 / 活动引用有效（§16.4）：须存在且处于可售卖 / 进行中状态。
func pageCheckEntity(ctx context.Context, db *ent.Client, key string, id, compIndex int) error {
	var err error
	switch key {
	case "sku_id", "sku_ids":
		err = pageCheckSKU(ctx, db, id)
	case "product_id", "product_ids":
		err = pageCheckProduct(ctx, db, id)
	case "campaign_id", "campaign_ids", "activity_id", "activity_ids":
		err = pageCheckCampaign(ctx, db, id)
	}
	if err == nil {
		return nil
	}
	if ent.IsNotFound(err) {
		return fmt.Errorf("%w：第 %d 个组件字段 %q 引用的资源 %d 不存在或未生效", errInvalidInput, compIndex+1, key, id)
	}
	return err
}

func pageCheckSKU(ctx context.Context, db *ent.Client, id int) error {
	_, err := db.EsportsSku.Query().Where(esportssku.IDEQ(id), esportssku.StatusEQ(esportssku.StatusOnSale)).Only(ctx)
	return err
}

func pageCheckProduct(ctx context.Context, db *ent.Client, id int) error {
	_, err := db.EsportsProduct.Query().Where(esportsproduct.IDEQ(id), esportsproduct.StatusEQ(esportsproduct.StatusOnSale)).Only(ctx)
	return err
}

func pageCheckCampaign(ctx context.Context, db *ent.Client, id int) error {
	_, err := db.EsportsRechargeCampaign.Query().Where(
		esportsrechargecampaign.IDEQ(id), esportsrechargecampaign.StatusEQ(esportsrechargecampaign.StatusActive),
	).Only(ctx)
	return err
}

// validatePageLinkSafety 校验跳转链接合法性（§16.4 增量）：站内相对路径拒绝协议相对 / 路径穿越 /
// 控制字符；外部链接须为可解析的 https 且不含凭证信息。
func validatePageLinkSafety(raw string, compIndex int, field string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "/") {
		if strings.HasPrefix(raw, "//") || strings.Contains(raw, "..") || strings.ContainsAny(raw, "\r\n\t ") {
			return fmt.Errorf("%w：第 %d 个组件字段 %q 的站内跳转路径非法", errInvalidInput, compIndex+1, field)
		}
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%w：第 %d 个组件字段 %q 的跳转链接不合法", errInvalidInput, compIndex+1, field)
	}
	if u.User != nil {
		return fmt.Errorf("%w：第 %d 个组件字段 %q 的跳转链接不得包含凭证信息", errInvalidInput, compIndex+1, field)
	}
	return nil
}

// pageCheckExternalImage 校验发布前的外部图片地址（P0-17 + §8.2-16）。
// 顺序刻意固定：先做网络目标校验（协议 / 凭证 / 端口 / 内网字面量），再过域名白名单，
// 再做「解析后 IP 全为公网」的二次校验，最后才**登记**探测任务（全部组件同步校验通过后并行探测）——
// 任何一步不通过都不会产生网络请求。
func pageCheckExternalImage(ctx context.Context, allowedDomains map[string]bool, probes *pageProbeCollector,
	compIndex int, field, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "/") {
		return nil
	}
	// 图标短标识（如 icon-cs）由保存时的图标清单校验，不当作外部地址探测。
	if !strings.HasPrefix(raw, "https://") && !strings.HasPrefix(raw, "http://") {
		return nil
	}
	if !strings.HasPrefix(raw, "https://") {
		return fmt.Errorf("%w：第 %d 个组件字段 %q 的图片地址仅允许 https:// 或站内相对路径", errInvalidInput, compIndex+1, field)
	}
	if err := pageValidateHTTPSURLTarget(raw); err != nil {
		return fmt.Errorf("%w：第 %d 个组件字段 %q 的图片地址%s", errInvalidInput, compIndex+1, field, err)
	}
	host := pageURLHost(raw)
	if err := pageProbeAllowed(allowedDomains, raw); err != nil {
		return fmt.Errorf("%w：第 %d 个组件字段 %q 的图片不可用：%v", errInvalidInput, compIndex+1, field, err)
	}
	// §8.2-16：白名单域名解析出的每个 IP 都必须是公网地址（DNS rebinding 防护第一道）。
	// 这里先解析一次是为了「快速失败且不发请求」；拨号时还会用同一套校验再解析一次，
	// 真正建立连接所用的 IP 是拨号那一刻被校验过的结果（见 pageProbeDialContext）。
	if _, err := pageVerifyHostIPsPublic(ctx, host); err != nil {
		return fmt.Errorf("%w：第 %d 个组件字段 %q 的图片不可用：%v", errInvalidInput, compIndex+1, field, err)
	}
	probes.add(compIndex, field, raw)
	return nil
}

// pageProbeAllowed 判断外部图片地址是否允许发起探测（P0-17）：主机必须命中「已审核 HTTPS 域名」白名单。
// 白名单来自既有的 `app.esports.page_allowed_domains`（与外部跳转链接同一份配置，不新增配置项）；
// 未配置时按 fail-closed 处理：视为空集合，一律不探测。
func pageProbeAllowed(allowedDomains map[string]bool, rawURL string) error {
	host := pageURLHost(rawURL)
	if host == "" {
		return fmt.Errorf("图片地址无法解析出域名")
	}
	if len(allowedDomains) == 0 {
		return fmt.Errorf("未配置已审核 HTTPS 域名白名单（%s），发布前不探测任何外部图片", pageAllowedDomainsKey)
	}
	if !allowedDomains[host] {
		return fmt.Errorf("域名 %s 不在已审核域名白名单内（%s），未发起探测", host, pageAllowedDomainsKey)
	}
	return nil
}

// pageValidateHTTPSURLTarget 校验外部 HTTPS 地址的网络目标（P0-17）：拒绝携带用户名 / 口令、
// 非 80 / 443 的显式端口、指向环回 / 私网 / 链路本地 / 未指定地址的字面量主机，
// 以及 localhost / *.localhost 这类必然指向本机的主机名。返回的中文片段由调用方拼接字段上下文。
//
// 这里只看字面量主机；域名解析后的 IP 校验在 pageVerifyHostIPsPublic / pageProbeDialContext
// 两处完成（§8.2-16 DNS rebinding 防护），白名单仍是第一道闸门。
func pageValidateHTTPSURLTarget(rawURL string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("无法解析")
	}
	if u.Scheme != "https" {
		return fmt.Errorf("仅允许 https://")
	}
	if u.User != nil {
		return fmt.Errorf("不得包含用户名或口令")
	}
	if port := u.Port(); port != "" && port != "443" && port != "80" {
		return fmt.Errorf("的端口 %s 不被允许（仅允许 80 / 443）", port)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return fmt.Errorf("缺少主机名")
	}
	if pageHostIsLocalOrPrivate(host) {
		return fmt.Errorf("指向本机或内网地址（%s），不允许作为装修资源", host)
	}
	return nil
}

// pageHostIsLocalOrPrivate 判断主机是否为 localhost / *.localhost 或字面量环回 / 私网 / 链路本地 IP。
func pageHostIsLocalOrPrivate(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return !pageIPIsPublicTarget(ip)
}

// pageIPIsPublicTarget 判断单个解析结果是否可作为探测目标（§8.2-16）：
// 拒绝环回（127/8、::1）、私网（10/8、172.16/12、192.168/16）、链路本地（169.254/16、fe80::/10）、
// IPv6 唯一本地（fc00::/7）、未指定（0.0.0.0、::）、组播以及 IPv4 映射的上述地址。
func pageIPIsPublicTarget(ip net.IP) bool {
	if ip == nil {
		return false
	}
	// IPv4-mapped IPv6（::ffff:127.0.0.1）按 IPv4 判定，避免绕过环回 / 私网检查。
	v4 := ip.To4()
	if v4 != nil {
		ip = v4
		if ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() ||
			ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
			return false
		}
		// 0.0.0.0/8「本网络」与 100.64/10 运营商级 NAT 同样不对外可达，按非公网处理。
		if ip[0] == 0 {
			return false
		}
		return true
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return false
	}
	// 显式兜底 fc00::/7（唯一本地）：不依赖标准库 IsPrivate 的版本差异。
	if len(ip) == net.IPv6len && ip[0]&0xfe == 0xfc {
		return false
	}
	return true
}

// pageVerifyHostIPsPublic 解析域名并逐条校验解析结果（§8.2-16）：
// 只要有一条命中环回 / 私网 / 链路本地 / 唯一本地 / 未指定地址，整体拒绝（不做「取第一条」放行）。
// 返回的 IP 列表即本次校验所依据的解析结果，供调用方在**同一份结果**上拨号。
func pageVerifyHostIPsPublic(ctx context.Context, host string) ([]net.IP, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, fmt.Errorf("图片地址缺少主机名")
	}
	ips, err := pageResolveHostIPs(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("域名 %s 解析失败（%v），未发起探测", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("域名 %s 未解析到任何地址，未发起探测", host)
	}
	for _, ip := range ips {
		if !pageIPIsPublicTarget(ip) {
			return nil, fmt.Errorf("域名 %s 解析到本机或内网地址 %s，不允许作为装修资源（DNS rebinding 防护）",
				host, ip.String())
		}
	}
	return ips, nil
}

// pageProbeDialContext 是图片探测客户端专用的拨号器（§8.2-16）：
// 先解析并逐条校验 IP，再用**校验通过的同一份解析结果**逐条拨号。
// 这样「校验时解析到公网、请求时又解析到内网」的时间窗被彻底关闭——连接目标就是被校验过的 IP。
func pageProbeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("图片探测目标地址无法解析：%v", err)
	}
	ips, err := pageVerifyHostIPsPublic(ctx, host)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, ip := range ips {
		conn, dialErr := pageDialIP(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	return nil, fmt.Errorf("连接 %s 失败：%v", host, lastErr)
}

// pageProbeHTTPClient 构造图片探测客户端：使用带 IP 二次校验的拨号器，
// 且**不跟随重定向**（3xx 直接视为可访问），避免 3xx 跳转把请求带到白名单外或内网地址。
func pageProbeHTTPClient() *http.Client {
	return &http.Client{
		Timeout: pageImageProbeTimeout,
		Transport: &http.Transport{
			DialContext:           pageProbeDialContext,
			TLSHandshakeTimeout:   pageImageProbeTimeout,
			ResponseHeaderTimeout: pageImageProbeTimeout,
			DisableKeepAlives:     true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// pageProbeTarget 是一次待执行的图片探测；同一 URL 只保留首次出现的位置用于错误提示。
type pageProbeTarget struct {
	URL       string
	CompIndex int
	Field     string
}

// pageProbeCollector 收集待探测的外部图片地址：同步校验阶段只登记，
// 全部组件校验通过后再并行探测（§8.2-16 后半：串行 → 有并发上限的并行）。
type pageProbeCollector struct {
	order   []string
	targets map[string]pageProbeTarget
}

func newPageProbeCollector() *pageProbeCollector {
	return &pageProbeCollector{targets: make(map[string]pageProbeTarget)}
}

// add 登记一个待探测地址；同一 URL 重复出现时复用首次登记的记录（只探测一次）。
func (c *pageProbeCollector) add(compIndex int, field, rawURL string) {
	if _, ok := c.targets[rawURL]; ok {
		return
	}
	c.targets[rawURL] = pageProbeTarget{URL: rawURL, CompIndex: compIndex, Field: field}
	c.order = append(c.order, rawURL)
}

// len 返回去重后的待探测地址数量。
func (c *pageProbeCollector) len() int { return len(c.order) }

// run 并行探测全部登记地址并汇总错误：
//   - 并发上限 pageProbeConcurrency；
//   - 每个地址只探测一次（add 已去重）；
//   - 整体超时 pageProbeTotalTimeout，超时即失败并给中文提示；
//   - 全部结束后按登记顺序逐条列出失败的 URL 与原因（不再「遇到第一个失败就返回」）。
func (c *pageProbeCollector) run(ctx context.Context) error {
	if len(c.order) == 0 {
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, pageProbeTotalTimeout)
	defer cancel()

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		failures = make(map[string]error, len(c.order))
	)
	sem := make(chan struct{}, pageProbeConcurrency)
	for _, rawURL := range c.order {
		wg.Add(1)
		go func(target string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := pageProbeImage(probeCtx, target); err != nil {
				mu.Lock()
				failures[target] = err
				mu.Unlock()
			}
		}(rawURL)
	}
	wg.Wait()

	if len(failures) == 0 {
		return nil
	}
	// 整体超时优先提示，避免把同一个「超时」原因按 URL 重复罗列。
	if errors.Is(probeCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		return fmt.Errorf("%w：发布前图片可访问性校验超时（%s 内未完成 %d 个地址），请检查图片地址或稍后重试",
			errInvalidInput, pageProbeTotalTimeout, len(c.order))
	}
	lines := make([]string, 0, len(failures))
	for _, rawURL := range c.order {
		probeErr, failed := failures[rawURL]
		if !failed {
			continue
		}
		target := c.targets[rawURL]
		lines = append(lines, fmt.Sprintf("第 %d 个组件字段 %q 的图片不可访问（%s）：%v",
			target.CompIndex+1, target.Field, rawURL, probeErr))
	}
	return fmt.Errorf("%w：以下图片未通过发布前可访问性校验：%s", errInvalidInput, strings.Join(lines, "；"))
}

// probePageImageURLWithClient 对外部图片 URL 先 HEAD 探测，失败回退 GET（部分 CDN 不支持 HEAD），
// 2xx/3xx 视为可访问。
func probePageImageURLWithClient(ctx context.Context, client *http.Client, rawURL string) error {
	if client == nil {
		client = pageProbeHTTPClient()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			return nil
		}
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err = client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
