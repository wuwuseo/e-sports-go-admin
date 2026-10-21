package esports

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportspagedocument"
	"go-admin/internal/ent/esportspageversion"
)

// 页面装修第 4 轮补齐（§16.2-05 / §16.2-06 / §16.3 / §16.4-02 / §16.4-06 / §16.5-04）：
//   - 有效期过滤（过期组件 / 轮播项不下发）；
//   - 底部导航文案与图标（路径 / 数量 / 顺序固定）；
//   - 设备预览码（换取的必须是草稿）；
//   - 按客户端能力下发兼容快照 + 版本缓存；
//   - 素材被引用位置反查与失效标记。

// ── §16.3-02 / §16.3-03 / §16.3-08 有效期过滤 ──

// filterExpiredPageConfig 剔除已过有效期的组件与嵌套项（§16.3-02 / 03 / 08）：
//   - 组件顶层带 valid_from / valid_to 且当前不在区间内 → 整块不下发；
//   - items 元素带 valid_from / valid_to 且过期 → 该条不下发；
//   - support_entry（客服与订单入口）永不因有效期被剔除，避免运营配错时间把入口关掉。
func filterExpiredPageConfig(cfg pageConfig, now time.Time) pageConfig {
	out := pageConfig{Tabbar: cfg.Tabbar, Components: make([]pageComponent, 0, len(cfg.Components))}
	for _, comp := range cfg.Components {
		if filtered, ok := filterExpiredPageComponent(comp, now); ok {
			out.Components = append(out.Components, filtered)
		}
	}
	return out
}

// filterExpiredPageComponent 对单个组件做有效期过滤；ok=false 表示整块不再下发。
// 布局按列保留 null 占位，某一列失效时其它列的位置不偏移。
func filterExpiredPageComponent(comp pageComponent, now time.Time) (pageComponent, bool) {
	if comp.Type != "support_entry" {
		active, ok := pagePropsWithinWindow(comp.Props, now)
		if ok && !active {
			return comp, false
		}
	}
	filteredProps := filterExpiredProps(comp.Props, now)
	comp.Props = filteredProps
	// 轮播 / 分类等以 items 承载内容的组件：全部条目过期时整块不下发（没有任何可渲染内容）。
	if items, ok := filteredProps["items"].([]interface{}); ok && len(items) == 0 {
		if _, hasImages := filteredProps["images"]; !hasImages {
			return comp, false
		}
	}
	if comp.Type != "layout" || len(comp.Children) == 0 {
		return comp, true
	}
	children := make([]*pageComponent, len(comp.Children))
	kept := 0
	for i, child := range comp.Children {
		if child == nil {
			continue
		}
		filtered, ok := filterExpiredPageComponent(*child, now)
		if !ok {
			continue
		}
		children[i] = &filtered
		kept++
	}
	comp.Children = children
	// 没有可渲染子组件的布局整行不下发。
	return comp, kept > 0
}

// normalizePageConfigForClient 是客户端读取装修配置前的统一归一化入口：
// 先剔除指向已下线功能的快捷入口，再剔除已过有效期的组件与嵌套项。
//
// 只读、不写库：已发布版本是不可变快照，不能回写历史数据，因此「已下线功能的入口」
// 只能在读取侧剔除，保证客户端不会拿到一个点了没反应的入口。
// 顺序有意如此——先剔除入口再过滤有效期，被剔空的 quick_nav 会由
// filterExpiredPageConfig 的「items 为空且无 images → 整块不下发」规则一并去掉。
func normalizePageConfigForClient(cfg pageConfig, now time.Time) pageConfig {
	return filterExpiredPageConfig(pruneRemovedPageNavTargets(cfg), now)
}

// pruneRemovedPageNavTargets 剔除 target 指向已下线功能（见 pageRemovedNavTargets）的 quick_nav 条目。
// 未命中时原样返回入参，避免对绝大多数页面产生额外分配。
func pruneRemovedPageNavTargets(cfg pageConfig) pageConfig {
	changed := false
	pruned := make([]pageComponent, len(cfg.Components))
	copy(pruned, cfg.Components)
	for index, comp := range cfg.Components {
		next, compChanged := pruneRemovedNavTargetsInComponent(comp)
		if compChanged {
			pruned[index] = next
			changed = true
		}
	}
	if !changed {
		return cfg
	}
	cfg.Components = pruned
	return cfg
}

// pruneRemovedNavTargetsInComponent 递归处理布局子组件里的 quick_nav，未命中时返回原值。
func pruneRemovedNavTargetsInComponent(comp pageComponent) (pageComponent, bool) {
	changed := false
	if comp.Type == "layout" && len(comp.Children) > 0 {
		children := comp.Children
		for index, child := range comp.Children {
			if child == nil {
				continue
			}
			next, childChanged := pruneRemovedNavTargetsInComponent(*child)
			if !childChanged {
				continue
			}
			if !changed {
				cloned := make([]*pageComponent, len(children))
				copy(cloned, children)
				children = cloned
				changed = true
			}
			children[index] = &next
		}
		comp.Children = children
	}
	if comp.Type != "quick_nav" || comp.Props == nil {
		return comp, changed
	}
	items, ok := comp.Props["items"].([]interface{})
	if !ok || len(items) == 0 {
		return comp, changed
	}
	kept := make([]interface{}, 0, len(items))
	removed := 0
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]interface{})
		if !ok {
			kept = append(kept, rawItem)
			continue
		}
		target, _ := item["target"].(string)
		if _, dead := pageRemovedNavTargets[strings.ToLower(strings.TrimSpace(target))]; dead {
			removed++
			continue
		}
		kept = append(kept, rawItem)
	}
	if removed == 0 {
		return comp, changed
	}
	clonedProps := make(map[string]interface{}, len(comp.Props))
	for key, value := range comp.Props {
		clonedProps[key] = value
	}
	clonedProps["items"] = kept
	comp.Props = clonedProps
	return comp, true
}

// pagePropsWithinWindow 判断属性集合自带的有效期是否覆盖 now；未配置有效期视为有效。
// 第二个返回值表示该属性集合是否配置过有效期（false = 未配置，调用方按有效处理）。
func pagePropsWithinWindow(props map[string]interface{}, now time.Time) (bool, bool) {
	from, hasFrom := pagePropWindowTime(props, "valid_from")
	to, hasTo := pagePropWindowTime(props, "valid_to")
	if !hasFrom && !hasTo {
		return true, false
	}
	if hasFrom && now.Before(from) {
		return false, true
	}
	if hasTo && now.After(to) {
		return false, true
	}
	return true, true
}

// pagePropWindowTime 从属性中解析有效期的某一端；未配置 / 非法（保存时已校验）返回 false。
func pagePropWindowTime(props map[string]interface{}, key string) (time.Time, bool) {
	raw, ok := props[key].(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return time.Time{}, false
	}
	at, err := parsePagePropTime(raw)
	if err != nil {
		return time.Time{}, false
	}
	return at, true
}

// filterExpiredProps 递归剔除嵌套项中已过有效期的条目（当前用于 items 数组元素）。
func filterExpiredProps(props map[string]interface{}, now time.Time) map[string]interface{} {
	if len(props) == 0 {
		return props
	}
	out := make(map[string]interface{}, len(props))
	for key, value := range props {
		items, ok := value.([]interface{})
		if !ok {
			out[key] = value
			continue
		}
		kept := make([]interface{}, 0, len(items))
		for _, item := range items {
			entry, ok := item.(map[string]interface{})
			if !ok {
				kept = append(kept, item)
				continue
			}
			if active, configured := pagePropsWithinWindow(entry, now); configured && !active {
				continue
			}
			kept = append(kept, filterExpiredProps(entry, now))
		}
		out[key] = kept
	}
	return out
}

// ── §16.3-12 底部导航文案 / 图标 ──

// pageTabbarItem 是底部导航项（§16.3-12，审核报告 §8.1-8 后放开）：
// 允许增删条目（1~5 项）、允许编辑 path 与 order；path 仍须命中已注册页面白名单。
// order 未填时按提交顺序补齐（见 pageValidateTabbar），落库后客户端按 order 升序展示。
type pageTabbarItem struct {
	Path    string `json:"path"`
	Text    string `json:"text"`
	Icon    string `json:"icon"`
	Order   *int   `json:"order,omitempty"`
	Visible *bool  `json:"visible,omitempty"`
}

// pageTabbarPaths 是底部导航的**默认**路径与顺序（§16.3-12，对应客户端 pages.json tabBar 三项）。
// 本轮放开编辑后它只作为系统默认值，不再是唯一合法形状（见 pageValidateTabbar）。
var pageTabbarPaths = []string{"/pages/index/index", "/pages/order/list", "/pages/mine/index"}

// pageTabbarDefaultTexts 是底部导航的默认文案（与客户端现状一致）。
var pageTabbarDefaultTexts = []string{"报单", "订单", "我的"}

// defaultPageTabbar 返回系统默认底部导航（路径 + 默认文案 + 顺序）。
func defaultPageTabbar() []pageTabbarItem {
	items := make([]pageTabbarItem, 0, len(pageTabbarPaths))
	for i, path := range pageTabbarPaths {
		order := i + 1
		items = append(items, pageTabbarItem{Path: path, Text: pageTabbarDefaultTexts[i], Order: &order})
	}
	return items
}

// validatePageTabbar 校验导航项（§16.3-12，审核报告 §8.1-8 后放开）：
// 允许增删条目、允许编辑 path 与 order，安全约束（站内相对路径 + 已注册页面白名单 +
// path/order 不重复 + 条目数 1~5）由 pageValidateTabbar 统一实施，本函数只做转发，
// 保持既有调用点（保存导航 / 读取配置回落）签名不变。
func validatePageTabbar(items []pageTabbarItem) ([]pageTabbarItem, error) {
	return pageValidateTabbar(items)
}

// pageTabbarFromConfig 从装修配置中读取导航项：未配置时回落系统默认（路径 + 默认文案）。
// 已存配置若与固定形状不符（历史脏数据），同样回落默认而不报错，保证客户端读取永远可用。
func pageTabbarFromConfig(raw map[string]interface{}) ([]pageTabbarItem, error) {
	value, ok := raw["tabbar"]
	if !ok || value == nil {
		return defaultPageTabbar(), nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return defaultPageTabbar(), nil
	}
	var items []pageTabbarItem
	if err = json.Unmarshal(data, &items); err != nil || len(items) == 0 {
		return defaultPageTabbar(), nil
	}
	validated, err := validatePageTabbar(items)
	if err != nil {
		return defaultPageTabbar(), nil
	}
	return validated, nil
}

// visiblePageTabbar 过滤隐藏项，供客户端下发使用。
func visiblePageTabbar(items []pageTabbarItem) []pageTabbarItem {
	out := make([]pageTabbarItem, 0, len(items))
	for _, item := range items {
		if item.Visible != nil && !*item.Visible {
			continue
		}
		out = append(out, item)
	}
	return out
}

// savePageTabbar 保存底部导航文案 / 图标（§16.3-12）：只改 text / icon / visible，
// 以草稿版本作乐观锁，并把导航写进草稿配置（随发布快照一并生效）。
func savePageTabbar(ctx context.Context, db *ent.Client, channel string, adminID, expectedVersion int,
	items []pageTabbarItem) (*ent.EsportsPageDocument, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" || len(channel) > 48 || adminID < 1 {
		return nil, errInvalidInput
	}
	validated, err := validatePageTabbar(items)
	if err != nil {
		return nil, err
	}
	doc, err := db.EsportsPageDocument.Query().Where(esportspagedocument.ChannelEQ(channel)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, fmt.Errorf("%w：请先保存首页草稿再配置底部导航", errInvalidInput)
	}
	if err != nil {
		return nil, err
	}
	if expectedVersion < 1 || expectedVersion != doc.DraftVersion {
		return nil, errConflict
	}
	cfg, err := decodePageConfig(doc.DraftConfig)
	if err != nil {
		return nil, err
	}
	cfg.Tabbar = validated
	changed, err := db.EsportsPageDocument.Update().Where(
		esportspagedocument.IDEQ(doc.ID), esportspagedocument.DraftVersionEQ(doc.DraftVersion),
	).SetDraftConfig(pageConfigToMap(cfg)).SetUpdatedBy(adminID).AddDraftVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsPageDocument.Get(ctx, doc.ID)
}

// ── §16.4-02 设备预览码 ──

const (
	// pagePreviewSecretKey 是预览码签名密钥；未配置时生成预览码明确失败（不伪成功）。
	pagePreviewSecretKey = "app.esports.page_preview_secret"
	// pagePreviewCodeTTL 是预览码有效期（够运营扫码真机查看草稿）。
	pagePreviewCodeTTL = 30 * time.Minute
	// pagePreviewCodeMaxLen 是预览码长度上限（校验入参，避免超长攻击）。
	pagePreviewCodeMaxLen = 512
)

// pagePreviewCodeView 是预览码下发视图：客户端扫码后按 path 换取草稿配置。
type pagePreviewCodeView struct {
	Channel      string `json:"channel"`
	Code         string `json:"code"`
	Path         string `json:"path"`
	DraftVersion int    `json:"draft_version"`
	ExpiresAt    string `json:"expires_at"`
}

// pagePreviewSecret 读取预览码签名密钥；未配置返回明确的未配置错误（§7 不假成功）。
func pagePreviewSecret(ctx context.Context, db *ent.Client) (string, error) {
	secret := strings.TrimSpace(assessmentConfigString(ctx, db, pagePreviewSecretKey, ""))
	if secret == "" {
		return "", fmt.Errorf("%w：页面预览签名密钥未配置（%s）", errNotConfigured, pagePreviewSecretKey)
	}
	return secret, nil
}

// signPagePreviewPayload 生成 base64url(payload).hex(hmac) 形式的预览码（不可伪造、自带有效期）。
func signPagePreviewPayload(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	signature := hex.EncodeToString(mac.Sum(nil))[:32]
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + signature
}

// issuePagePreviewCode 生成设备预览码（§16.4-02）：指向当前草稿版本，有效期 30 分钟。
func issuePagePreviewCode(ctx context.Context, db *ent.Client, channel string, now time.Time) (*pagePreviewCodeView, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" || len(channel) > 48 {
		return nil, errInvalidInput
	}
	secret, err := pagePreviewSecret(ctx, db)
	if err != nil {
		return nil, err
	}
	doc, err := db.EsportsPageDocument.Query().Where(esportspagedocument.ChannelEQ(channel)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, fmt.Errorf("%w：该渠道尚未装修，无法生成预览码", errInvalidInput)
	}
	if err != nil {
		return nil, err
	}
	expiresAt := now.UTC().Add(pagePreviewCodeTTL)
	payload := fmt.Sprintf("%s|%d|%d", channel, doc.DraftVersion, expiresAt.Unix())
	code := signPagePreviewPayload(secret, payload)
	return &pagePreviewCodeView{
		Channel:      channel,
		Code:         code,
		Path:         "/page/preview/" + code,
		DraftVersion: doc.DraftVersion,
		ExpiresAt:    expiresAt.Format(time.RFC3339),
	}, nil
}

// resolvePagePreviewCode 兑换预览码（§16.4-02）：验签 + 校验有效期后返回**草稿**配置与草稿版本号。
// 签名不符 / 过期 / 渠道不存在一律返回参数错误（不泄露草稿内容）。
func resolvePagePreviewCode(ctx context.Context, db *ent.Client, code string, now time.Time) (map[string]interface{}, int, error) {
	code = strings.TrimSpace(code)
	if code == "" || len(code) > pagePreviewCodeMaxLen {
		return nil, 0, errInvalidInput
	}
	secret, err := pagePreviewSecret(ctx, db)
	if err != nil {
		return nil, 0, err
	}
	dot := strings.LastIndex(code, ".")
	if dot <= 0 {
		return nil, 0, fmt.Errorf("%w：预览码无效", errInvalidInput)
	}
	raw, err := base64.RawURLEncoding.DecodeString(code[:dot])
	if err != nil {
		return nil, 0, fmt.Errorf("%w：预览码无效", errInvalidInput)
	}
	expected := signPagePreviewPayload(secret, string(raw))
	if !hmac.Equal([]byte(expected), []byte(code)) {
		return nil, 0, fmt.Errorf("%w：预览码无效或已被篡改", errInvalidInput)
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 3 {
		return nil, 0, fmt.Errorf("%w：预览码无效", errInvalidInput)
	}
	expiresUnix, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return nil, 0, fmt.Errorf("%w：预览码无效", errInvalidInput)
	}
	if now.UTC().Unix() > expiresUnix {
		return nil, 0, fmt.Errorf("%w：预览码已过期，请重新生成", errInvalidInput)
	}
	doc, err := db.EsportsPageDocument.Query().Where(esportspagedocument.ChannelEQ(parts[0])).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, 0, fmt.Errorf("%w：预览码对应的渠道不存在", errInvalidInput)
	}
	if err != nil {
		return nil, 0, err
	}
	return doc.DraftConfig, doc.DraftVersion, nil
}

// ── §16.4-06 能力协商与兼容快照 ──

// pageSnapshot 是下发快照：实际下发的配置 + 版本号 + 是否为当前生效版本 / 是否回落默认模板。
type pageSnapshot struct {
	Channel          string
	Config           map[string]interface{}
	VersionNo        int
	CurrentVersionNo int
	Compatible       bool
	Fallback         bool
}

// pageCapabilities 解析客户端能力声明（§16.4-06）：capab 为逗号分隔的「客户端支持的组件类型」清单。
// 未传（nil）表示客户端未声明能力 → 不做裁剪；显式传空串表示不支持任何组件 → 只能回默认模板。
func pageCapabilities(raw string, provided bool) map[string]bool {
	if !provided {
		return nil
	}
	caps := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		if token := strings.TrimSpace(part); token != "" {
			caps[token] = true
		}
	}
	return caps
}

// pageConfigSupportsCaps 判断配置是否可被该客户端渲染：所有「可见」组件的类型都必须在能力清单内。
func pageConfigSupportsCaps(cfg pageConfig, caps map[string]bool) bool {
	for _, comp := range cfg.Components {
		if !pageComponentSupportsCaps(comp, caps) {
			return false
		}
	}
	return true
}

// pageComponentSupportsCaps 递归检查组件与布局子组件：隐藏组件及其子组件不参与渲染，不做能力要求。
func pageComponentSupportsCaps(comp pageComponent, caps map[string]bool) bool {
	if comp.Visible != nil && !*comp.Visible {
		return true
	}
	if !caps[comp.Type] {
		return false
	}
	for _, child := range comp.Children {
		if child != nil && !pageComponentSupportsCaps(*child, caps) {
			return false
		}
	}
	return true
}

// pageSnapshotForClient 组装兼容快照（§16.4-06）：优先当前生效版本；不兼容时回退到最近一个
// 完全兼容的历史生效版本；都没有则回系统默认模板（fallback=true）。
func pageSnapshotForClient(ctx context.Context, db *ent.Client, channel string, caps map[string]bool, now time.Time) (*pageSnapshot, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" || len(channel) > 48 {
		return nil, errInvalidInput
	}
	doc, err := db.EsportsPageDocument.Query().Where(esportspagedocument.ChannelEQ(channel)).Only(ctx)
	if ent.IsNotFound(err) {
		return &pageSnapshot{Channel: channel, Config: pageConfigToMap(defaultPageConfig()), Fallback: true}, nil
	}
	if err != nil {
		return nil, err
	}
	if caps == nil {
		config, versionNo, err := publishedPageConfig(ctx, db, channel)
		if err != nil {
			return nil, err
		}
		return &pageSnapshot{
			Channel: channel, Config: config, VersionNo: versionNo,
			CurrentVersionNo: versionNo, Compatible: true,
		}, nil
	}
	if doc.PublishedVersionNo >= 1 {
		current, err := db.EsportsPageVersion.Query().Where(
			esportspageversion.DocumentIDEQ(doc.ID),
			esportspageversion.VersionNoEQ(doc.PublishedVersionNo),
		).Only(ctx)
		if err != nil && !ent.IsNotFound(err) {
			return nil, err
		}
		if err == nil {
			cfg, decErr := decodePageConfig(current.Config)
			if decErr != nil {
				return nil, decErr
			}
			if pageConfigSupportsCaps(cfg, caps) {
				return &pageSnapshot{
					Channel: channel, Config: current.Config, VersionNo: current.VersionNo,
					CurrentVersionNo: doc.PublishedVersionNo, Compatible: true,
				}, nil
			}
		}
	}
	// 回退：按版本号倒序找最近一个完全兼容的历史生效版本。
	versions, err := db.EsportsPageVersion.Query().Where(
		esportspageversion.DocumentIDEQ(doc.ID),
		esportspageversion.StatusIn(esportspageversion.StatusPublished, esportspageversion.StatusHistory),
	).Order(ent.Desc(esportspageversion.FieldVersionNo)).Limit(20).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, version := range versions {
		cfg, decErr := decodePageConfig(version.Config)
		if decErr != nil {
			continue
		}
		if pageConfigSupportsCaps(cfg, caps) {
			return &pageSnapshot{
				Channel: channel, Config: version.Config, VersionNo: version.VersionNo,
				CurrentVersionNo: doc.PublishedVersionNo,
			}, nil
		}
	}
	return &pageSnapshot{
		Channel: channel, Config: pageConfigToMap(defaultPageConfig()),
		CurrentVersionNo: doc.PublishedVersionNo, Fallback: true,
	}, nil
}

// ── §16.2-06 素材被引用位置反查 ──

// pageAssetUsage 是素材被引用的位置（渠道 / 版本 / 组件 / 属性路径）。
type pageAssetUsage struct {
	Channel        string `json:"channel"`
	VersionNo      int    `json:"version_no"`
	Scope          string `json:"scope"`
	ComponentIndex int    `json:"component_index"`
	ComponentType  string `json:"component_type"`
	PropPath       string `json:"prop_path"`
}

// pageAssetUsageView 反查某素材的引用位置，并标出「失效」（未被任何草稿或版本引用）。
func pageAssetUsageView(ctx context.Context, db *ent.Client, assetID int) (map[string]interface{}, error) {
	if assetID < 1 {
		return nil, errInvalidInput
	}
	asset, err := db.EsportsPageAsset.Get(ctx, assetID)
	if err != nil {
		return nil, err
	}
	usages, err := pageAssetUsages(ctx, db, asset.URL)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"asset_id": asset.ID,
		"name":     asset.Name,
		"url":      asset.URL,
		"stale":    len(usages) == 0,
		"usages":   usages,
	}, nil
}

// pageAssetUsages 反查某地址被哪些页面组件引用（草稿 + 全部历史版本，§16.2-06）。
func pageAssetUsages(ctx context.Context, db *ent.Client, assetURL string) ([]pageAssetUsage, error) {
	assetURL = strings.TrimSpace(assetURL)
	out := make([]pageAssetUsage, 0)
	if assetURL == "" {
		return out, nil
	}
	docs, err := db.EsportsPageDocument.Query().Order(ent.Asc(esportspagedocument.FieldChannel)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, doc := range docs {
		out = append(out, pageConfigAssetUsages(doc.Channel, 0, "draft", doc.DraftConfig, assetURL)...)
		versions, err := db.EsportsPageVersion.Query().Where(
			esportspageversion.DocumentIDEQ(doc.ID),
		).Order(ent.Asc(esportspageversion.FieldVersionNo)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, version := range versions {
			scope := version.Status.String()
			out = append(out, pageConfigAssetUsages(doc.Channel, version.VersionNo, scope, version.Config, assetURL)...)
		}
	}
	return out, nil
}

// pageConfigAssetUsages 扫描一份配置，返回该地址被引用的组件与属性路径。
func pageConfigAssetUsages(channel string, versionNo int, scope string, raw map[string]interface{}, assetURL string) []pageAssetUsage {
	out := make([]pageAssetUsage, 0)
	cfg, err := decodePageConfig(raw)
	if err != nil {
		return out
	}
	// public/... 与 /uploads/... 是同一张图的两种写法，引用反查按归一后的地址比较。
	assetKey := pageAssetURLKey(assetURL)
	for i, comp := range cfg.Components {
		for path, value := range pageAssetRefPaths(comp.Props, "props") {
			if pageAssetURLKey(value) == assetKey {
				out = append(out, pageAssetUsage{
					Channel: channel, VersionNo: versionNo, Scope: scope,
					ComponentIndex: i, ComponentType: comp.Type, PropPath: path,
				})
			}
		}
		// 布局子组件里的图片同样要能反查到引用位置，路径带上列号便于运营定位。
		if comp.Type == "layout" {
			for column, child := range comp.Children {
				if child == nil {
					continue
				}
				prefix := fmt.Sprintf("children[%d].props", column)
				for path, value := range pageAssetRefPaths(child.Props, prefix) {
					if pageAssetURLKey(value) == assetKey {
						out = append(out, pageAssetUsage{
							Channel: channel, VersionNo: versionNo, Scope: scope,
							ComponentIndex: i, ComponentType: child.Type, PropPath: path,
						})
					}
				}
			}
		}
	}
	return out
}

// pageAssetRefPaths 递归收集属性中的「图片类字段」取值及其路径（items[0].image 这种形式）。
func pageAssetRefPaths(props map[string]interface{}, prefix string) map[string]string {
	out := map[string]string{}
	for key, value := range props {
		path := prefix + "." + key
		switch v := value.(type) {
		case string:
			if pageImageFields[key] {
				out[path] = v
			}
		case map[string]interface{}:
			for subPath, subValue := range pageAssetRefPaths(v, path) {
				out[subPath] = subValue
			}
		case []interface{}:
			for i, item := range v {
				switch entry := item.(type) {
				case string:
					if pageImageFields[key] {
						out[fmt.Sprintf("%s[%d]", path, i)] = entry
					}
				case map[string]interface{}:
					for subPath, subValue := range pageAssetRefPaths(entry, fmt.Sprintf("%s[%d]", path, i)) {
						out[subPath] = subValue
					}
				}
			}
		}
	}
	return out
}
