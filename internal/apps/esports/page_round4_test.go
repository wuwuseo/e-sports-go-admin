package esports

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportspagedocument"
	"go-admin/internal/ent/esportspageversion"
	"go-admin/internal/global"
)

// 第 4 轮新增配置键字面量（RED 阶段生产常量尚未存在）。
const pagePreviewSecretKeyLiteral = "app.esports.page_preview_secret"

// pagePropsFor 构造「含必需客服入口」的组件集，用于逐组件验证新属性键是否被白名单放行。
func pagePropsFor(compType string, props map[string]interface{}) []pageComponent {
	visible := true
	return []pageComponent{
		{Type: "search", Visible: &visible},
		{Type: compType, Visible: &visible, Props: props},
		{Type: "support_entry", Visible: &visible},
	}
}

// ── §16.2-05：属性面板四类字段（间距 / 跳转目标 / 数据来源 / 筛选条件） ──

// 四类字段保存草稿时不得再被 400 拒绝，且未知键仍须拒绝。
func TestPageUniversalPanelPropsAccepted(t *testing.T) {
	db := openPageDB(t, "esports_r4_page_universal")
	channel := pageChannelCustomerHome

	doc, err := savePageDraft(t.Context(), db, channel, 1, pageDraftInput{
		Components: pagePropsFor("product_list", map[string]interface{}{
			"title":       "热销",
			"spacing":     12,
			"link":        "/pages/order/list",
			"link_type":   "page",
			"data_source": "sku",
			"filter":      map[string]interface{}{"game": "王者荣耀"},
			"sort":        "price_asc",
		}),
		ExpectedVersion: 0,
	})
	if err != nil {
		t.Fatalf("四类属性面板字段应被放行：%v", err)
	}
	cfg, err := decodePageConfig(doc.DraftConfig)
	if err != nil {
		t.Fatal(err)
	}
	props := cfg.Components[1].Props
	if props["spacing"] == nil || props["data_source"] == nil || props["filter"] == nil {
		t.Fatalf("草稿未保留四类字段：%+v", props)
	}
	// 未知键仍然拒绝（白名单不能被放宽）。
	if _, err = savePageDraft(t.Context(), db, channel, 1, pageDraftInput{
		Components: pagePropsFor("notice", map[string]interface{}{
			"html": "<script>alert(1)</script>",
		}),
		ExpectedVersion: doc.DraftVersion,
	}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("未声明的属性键仍应被拒，实际 %v", err)
	}
}

// 非法的枚举值 / 越界间距 / 非法时间 / 未知筛选键必须返回参数错误（§16.3-11 未知名规则明确报错）。
func TestPagePanelPropValuesValidated(t *testing.T) {
	db := openPageDB(t, "esports_r4_page_prop_values")
	channel := pageChannelCustomerHome

	cases := []struct {
		name  string
		comp  string
		props map[string]interface{}
	}{
		{"未知数据来源", "product_list", map[string]interface{}{"data_source": "magic"}},
		{"未知取数规则", "product_list", map[string]interface{}{"rule_code": "magic_rule"}},
		{"未知筛选键", "product_list", map[string]interface{}{"filter": map[string]interface{}{"unknown_filter": 1}}},
		{"间距越界", "notice", map[string]interface{}{"spacing": 9999}},
		{"非法有效期", "notice", map[string]interface{}{"valid_to": "not-a-time"}},
		{"未知排序", "category", map[string]interface{}{"sort": "magic_sort"}},
	}
	for _, tc := range cases {
		if _, err := savePageDraft(t.Context(), db, channel, 1, pageDraftInput{
			Components: pagePropsFor(tc.comp, tc.props), ExpectedVersion: 0,
		}); !errors.Is(err, errInvalidInput) {
			t.Fatalf("%s：应返回参数错误，实际 %v", tc.name, err)
		}
	}
}

// ── §16.3-01/03/05/08/09/10：组件字段补全 ──

// 各组件新增字段必须能存进草稿（否则后台配不了，是整个 §16.3 批次的前置条件）。
func TestPageComponentExtraFieldsAccepted(t *testing.T) {
	db := openPageDB(t, "esports_r4_page_component_fields")
	channel := pageChannelCustomerHome

	cases := []struct {
		comp  string
		props map[string]interface{}
	}{
		{"search", map[string]interface{}{"placeholder": "搜索陪玩", "hot_words": []interface{}{"上分", "开黑"}, "scope": "both"}},
		{"notice", map[string]interface{}{"text": "公告", "icon": "/static/notice.png", "link": "/pages/order/list", "valid_to": "2030-01-01"}},
		{"category", map[string]interface{}{"title": "分类", "limit": 6, "sort": "hot", "layout": "grid"}},
		{"player_recommend", map[string]interface{}{"title": "推荐", "game": "英雄联盟", "level": "senior", "online": true, "bookable": true}},
		{"activity", map[string]interface{}{"title": "活动", "image": "https://cdn.example.com/a.png", "valid_from": "2026-01-01", "valid_to": "2030-01-01"}},
		{"member_entry", map[string]interface{}{"title": "会员", "subtitle": "查看余额与权益", "style": "card"}},
		{"support_entry", map[string]interface{}{"icon": "/static/cs.png", "text": "联系客服", "order": 1, "position": "bottom"}},
		{"carousel", map[string]interface{}{"items": []interface{}{map[string]interface{}{
			"image": "https://cdn.example.com/b.png", "title": "首充有礼", "link": "/pages/wallet/recharge",
			"valid_to": "2030-01-01",
		}}}},
	}
	for i, tc := range cases {
		if _, err := savePageDraft(t.Context(), db, channel, 1, pageDraftInput{
			Components: pagePropsFor(tc.comp, tc.props), ExpectedVersion: i,
		}); err != nil {
			t.Fatalf("%s 组件新字段应被放行，实际 %v", tc.comp, err)
		}
	}
}

// ── §16.3-02/03/08：有效期到点的组件 / 轮播项不下发 ──

// 过期轮播项必须从下发配置中剔除，未过期的保留；客服入口组件永不被有效期剔除。
func TestPageExpiredItemsFilteredFromPublicConfig(t *testing.T) {
	now := time.Now().UTC()

	expired := pageConfig{Components: []pageComponent{
		{Type: "notice", Visible: boolPtr(true), Props: map[string]interface{}{
			"text": "过期公告", "valid_to": now.Add(-time.Hour).Format(time.RFC3339),
		}},
		{Type: "carousel", Visible: boolPtr(true), Props: map[string]interface{}{
			"items": []interface{}{
				map[string]interface{}{"image": "https://cdn.example.com/old.png", "valid_to": now.Add(-time.Hour).Format(time.RFC3339)},
				map[string]interface{}{"image": "https://cdn.example.com/new.png", "valid_to": now.Add(time.Hour).Format(time.RFC3339)},
			},
		}},
		{Type: "support_entry", Visible: boolPtr(true), Props: map[string]interface{}{
			"text": "联系客服", "valid_to": now.Add(-time.Hour).Format(time.RFC3339),
		}},
	}}
	filtered := filterExpiredPageConfig(expired, now)
	types := make([]string, 0, len(filtered.Components))
	for _, comp := range filtered.Components {
		types = append(types, comp.Type)
	}
	if len(types) != 2 || types[0] != "carousel" || types[1] != "support_entry" {
		t.Fatalf("过期组件应被剔除、客服入口必须保留，实际 %v", types)
	}
	items, _ := filtered.Components[0].Props["items"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("过期轮播项应被剔除，实际 %d 项", len(items))
	}
	if got := items[0].(map[string]interface{})["image"]; got != "https://cdn.example.com/new.png" {
		t.Fatalf("保留的轮播项应为未过期那条，实际 %v", got)
	}
}

// GET /page 下发的配置不得含过期公告 / 轮播项。
func TestPublicPageDropsExpiredComponents(t *testing.T) {
	db := openPageDB(t, "esports_r4_page_expire_public")
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()
	channel := pageChannelCustomerHome
	now := time.Now().UTC()

	doc := seedPageDocument(t, db, channel, pageConfig{Components: []pageComponent{
		{Type: "notice", Visible: boolPtr(true), Props: map[string]interface{}{
			"text": "过期公告", "valid_to": now.Add(-time.Hour).Format(time.RFC3339),
		}},
		{Type: "support_entry", Visible: boolPtr(true)},
	}}.Components)
	seedPublishedVersion(t, db, doc, channel, pageConfig{Components: []pageComponent{
		{Type: "notice", Visible: boolPtr(true), Props: map[string]interface{}{
			"text": "过期公告", "valid_to": now.Add(-time.Hour).Format(time.RFC3339),
		}},
		{Type: "support_entry", Visible: boolPtr(true)},
	}})

	app := fiber.New()
	app.Get("/page", publicPage)
	status, payload := sendJSON(t, app, http.MethodGet, "/page?channel="+channel, "")
	if status != http.StatusOK {
		t.Fatalf("读取已发布页面失败：%d %+v", status, payload)
	}
	config, _ := payload["data"].(map[string]any)["config"].(map[string]any)
	if config == nil {
		t.Fatalf("已发布配置结构异常：%+v", payload)
	}
	comps, _ := config["components"].([]any)
	for _, raw := range comps {
		if comp, _ := raw.(map[string]any); comp != nil && comp["type"] == "notice" {
			t.Fatalf("过期公告不应下发：%+v", comp)
		}
	}
}

// ── §16.2-06 / §16.5-04：素材 MIME 白名单与字节上限 ──

func TestPageAssetMimeAndSizeLimits(t *testing.T) {
	db := openPageDB(t, "esports_r4_page_asset_limits")
	ctx := t.Context()

	// 超过字节上限：拒绝且不落库。
	if _, err := savePageAsset(ctx, db, 1, pageAssetInput{
		Name: "超大图", URL: "https://cdn.example.com/big.png", Mime: "image/png",
		SizeBytes: pageAssetMaxBytes + 1,
	}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("超过字节上限应被拒，实际 %v", err)
	}
	// 非白名单 MIME：拒绝且不落库。
	if _, err := savePageAsset(ctx, db, 1, pageAssetInput{
		Name: "脚本", URL: "https://cdn.example.com/x.js", Mime: "application/javascript",
	}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非白名单 MIME 应被拒，实际 %v", err)
	}
	// 未声明 MIME：P1-20 起必填，缺失即拒绝（不再从地址后缀推断）。
	if _, err := savePageAsset(ctx, db, 1, pageAssetInput{
		Name: "图标", URL: "https://cdn.example.com/icon.png", SizeBytes: 1024, Width: 64, Height: 64,
	}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("未声明 MIME 应被拒，实际 %v", err)
	}
	// 显式声明图片类型 + 上限内字节数：接受并按声明落库。
	asset, err := savePageAsset(ctx, db, 1, pageAssetInput{
		Name: "图标", URL: "https://cdn.example.com/icon.png", Mime: "image/png",
		SizeBytes: 1024, Width: 64, Height: 64,
	})
	if err != nil {
		t.Fatalf("类型与体积合法的素材应被接受：%v", err)
	}
	if asset.Mime != "image/png" {
		t.Fatalf("素材应按声明 MIME 落库，实际 %q", asset.Mime)
	}
	if _, err := savePageAsset(ctx, db, 1, pageAssetInput{
		Name: "无类型", URL: "https://cdn.example.com/download", SizeBytes: 1024,
	}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("无法推断类型且未声明 MIME 应被拒，实际 %v", err)
	}
	if n := db.EsportsPageAsset.Query().CountX(ctx); n != 1 {
		t.Fatalf("被拒素材不应落库，实际库中 %d 条", n)
	}
}

// 素材可反查被哪些页面组件引用，未被引用的标记为失效。
func TestPageAssetUsageLookup(t *testing.T) {
	db := openPageDB(t, "esports_r4_page_asset_usage")
	ctx := t.Context()
	channel := pageChannelCustomerHome
	url := "https://cdn.example.com/used.png"

	doc := seedPageDocument(t, db, channel, pageConfig{Components: []pageComponent{
		{Type: "search", Visible: boolPtr(true)},
		{Type: "carousel", Visible: boolPtr(true), Props: map[string]interface{}{
			"items": []interface{}{map[string]interface{}{"image": url}},
		}},
		{Type: "support_entry", Visible: boolPtr(true)},
	}}.Components)
	seedPublishedVersion(t, db, doc, channel, pageConfig{Components: pageTestComponents()})

	asset := db.EsportsPageAsset.Create().SetName("在用图").SetURL(url).
		SetMime("image/png").SetSizeBytes(2048).SetCreatedBy(1).SaveX(ctx)
	stale := db.EsportsPageAsset.Create().SetName("孤儿图").SetURL("https://cdn.example.com/orphan.png").
		SetMime("image/png").SetSizeBytes(2048).SetCreatedBy(1).SaveX(ctx)

	view, err := pageAssetUsageView(ctx, db, asset.ID)
	if err != nil {
		t.Fatalf("素材引用反查失败：%v", err)
	}
	usages, _ := view["usages"].([]pageAssetUsage)
	if len(usages) == 0 {
		t.Fatalf("应查到素材被引用的位置：%+v", view)
	}
	if view["stale"] != false {
		t.Fatalf("在用素材不应标记失效：%+v", view)
	}
	if usages[0].ComponentType != "carousel" || usages[0].Scope != "draft" {
		t.Fatalf("引用位置信息不完整：%+v", usages[0])
	}
	orphanView, err := pageAssetUsageView(ctx, db, stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if orphanView["stale"] != true {
		t.Fatalf("未被引用的素材应标记失效：%+v", orphanView)
	}
}

// ── §16.3-12：底部导航文案 / 图标可改；§8.1-8 后半放开增删条目与 path / order 编辑 ──

func TestPageTabbarEditableWithPathAndOrder(t *testing.T) {
	db := openPageDB(t, "esports_r4_page_tabbar")
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()
	ctx := t.Context()
	channel := pageChannelCustomerHome
	now := time.Now().UTC()

	if _, err := savePageDraft(ctx, db, channel, 1, pageDraftInput{
		Components: pageTestComponents(), ExpectedVersion: 0,
	}); err != nil {
		t.Fatal(err)
	}
	items := defaultPageTabbar()
	items[0].Text = "下单"
	items[0].Icon = "/static/tab-order.png"
	doc, err := savePageTabbar(ctx, db, channel, 1, 1, items)
	if err != nil {
		t.Fatalf("保存导航文案失败：%v", err)
	}
	stored, err := pageTabbarFromConfig(doc.DraftConfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 3 || stored[0].Text != "下单" || stored[0].Icon != "/static/tab-order.png" {
		t.Fatalf("导航文案 / 图标未生效：%+v", stored)
	}

	// 发布后 GET /page 必须返回新文案。
	version, err := publishPage(ctx, db, channel, 1, pagePublishInput{Mode: "immediate"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reviewPagePublish(ctx, db, channel, version.ID, 2, pagePublishReviewInput{Approved: true}, now); err != nil {
		t.Fatal(err)
	}
	app := fiber.New()
	app.Get("/page", publicPage)
	status, payload := sendJSON(t, app, http.MethodGet, "/page?channel="+channel, "")
	if status != http.StatusOK {
		t.Fatalf("读取已发布页面失败：%d", status)
	}
	data, _ := payload["data"].(map[string]any)
	tabbar, _ := data["tabbar"].([]any)
	if len(tabbar) != 3 {
		t.Fatalf("已发布导航应为 3 项：%+v", data)
	}
	if first, _ := tabbar[0].(map[string]any); first == nil || first["text"] != "下单" {
		t.Fatalf("GET /page 未返回新导航文案：%+v", tabbar[0])
	}

	// §8.1-8 后半（本轮放开）：允许新增条目（路径命中已注册页面白名单即可）。
	base := doc.DraftVersion
	extra := append(append([]pageTabbarItem{}, items...), pageTabbarItem{Path: "/pages/messages/index", Text: "消息"})
	doc, err = savePageTabbar(ctx, db, channel, 1, base, extra)
	if err != nil {
		t.Fatalf("新增已注册页面的导航项应被允许，实际 %v", err)
	}
	// 未注册页面的路径仍被拒（白名单仍是安全闸门）。
	renamed := append([]pageTabbarItem{}, items...)
	renamed[0].Path = "/pages/not-registered/index"
	if _, err = savePageTabbar(ctx, db, channel, 1, doc.DraftVersion, renamed); !errors.Is(err, errInvalidInput) {
		t.Fatalf("未注册页面的导航路径应被拒，实际 %v", err)
	}
	base = doc.DraftVersion

	// §16.3-04：底部导航**不接受短标识**（与组件字段的图标清单口径不同）——客户端导航栏只消费图片地址。
	// 这里锁定「短标识被拒」，并验证反向（HTTPS 图片地址仍可用），避免把正常配置一起挡掉。
	badIcon := append([]pageTabbarItem{}, items...)
	badIcon[0].Icon = "icon-nonexistent"
	if _, err = savePageTabbar(ctx, db, channel, 1, base, badIcon); !errors.Is(err, errInvalidInput) {
		t.Fatalf("底部导航的图标短标识应被拒，实际 %v", err)
	}
	// 反向：HTTPS 图片地址必须仍然可用（收紧不能把正常配置一起挡掉）。
	okIcon := append([]pageTabbarItem{}, items...)
	okIcon[0].Icon = "https://cdn.example.com/tabbar/home.png"
	if _, err = savePageTabbar(ctx, db, channel, 1, base, okIcon); err != nil {
		t.Fatalf("底部导航的 HTTPS 图标应通过，实际 %v", err)
	}
}

// ── §16.4-02：设备预览码（换取草稿而非已发布版本） ──

func TestPagePreviewCodeResolvesDraft(t *testing.T) {
	db := openPageDB(t, "esports_r4_page_preview")
	ctx := t.Context()
	channel := pageChannelCustomerHome
	now := time.Now().UTC()

	if _, err := savePageDraft(ctx, db, channel, 1, pageDraftInput{
		Components: pagePropsFor("notice", map[string]interface{}{"text": "草稿公告"}), ExpectedVersion: 0,
	}); err != nil {
		t.Fatal(err)
	}

	// 未配置签名密钥：明确失败，不伪成功。
	if _, err := issuePagePreviewCode(ctx, db, channel, now); !errors.Is(err, errNotConfigured) {
		t.Fatalf("未配置预览签名密钥应明确失败，实际 %v", err)
	}
	seedUpgradeConfig(t, db, pagePreviewSecretKeyLiteral, "preview-secret-1")

	code, err := issuePagePreviewCode(ctx, db, channel, now)
	if err != nil {
		t.Fatalf("生成预览码失败：%v", err)
	}
	if code.Code == "" || code.Path == "" || code.DraftVersion != 1 {
		t.Fatalf("预览码结构异常：%+v", code)
	}
	// 伪造 / 篡改的预览码不可用。
	if _, _, err := resolvePagePreviewCode(ctx, db, code.Code+"x", now); err == nil {
		t.Fatal("被篡改的预览码不应可用")
	}
	// 过期预览码不可用。
	if _, _, err := resolvePagePreviewCode(ctx, db, code.Code, now.Add(time.Hour*24)); err == nil {
		t.Fatal("过期预览码不应可用")
	}
	config, versionNo, err := resolvePagePreviewCode(ctx, db, code.Code, now)
	if err != nil {
		t.Fatalf("兑换预览码失败：%v", err)
	}
	if versionNo != 1 {
		t.Fatalf("预览码应指向草稿版本 1，实际 %d", versionNo)
	}
	cfg, err := decodePageConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Components) != 3 {
		t.Fatalf("预览码应返回当前草稿配置，实际 %+v", cfg.Components)
	}
}

// ── §16.4-06：按客户端能力下发兼容快照 + 按版本缓存 ──

func TestPageCapabilityCompatAndVersionCache(t *testing.T) {
	db := openPageDB(t, "esports_r4_page_compat")
	ctx := t.Context()
	channel := pageChannelCustomerHome
	now := time.Now().UTC()

	// v1：仅 search + support_entry（老客户端可用）。
	if _, err := savePageDraft(ctx, db, channel, 1, pageDraftInput{
		Components: pageTestComponents(), ExpectedVersion: 0,
	}); err != nil {
		t.Fatal(err)
	}
	v1, err := publishPage(ctx, db, channel, 1, pagePublishInput{Mode: "immediate"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reviewPagePublish(ctx, db, channel, v1.ID, 2, pagePublishReviewInput{Approved: true}, now); err != nil {
		t.Fatal(err)
	}
	// v2：加入 carousel（新客户端才能渲染）。
	// 图片域名须在已审核域名白名单内（P0-17：发布前只探测白名单内的外部图片）。
	setPageAllowedDomains(t, db, "cdn.example.com")
	if _, err = savePageDraft(ctx, db, channel, 1, pageDraftInput{
		Components: pagePropsFor("carousel", map[string]interface{}{
			"items": []interface{}{map[string]interface{}{"image": "https://cdn.example.com/a.png"}},
		}), ExpectedVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	v2, err := publishPage(ctx, db, channel, 1, pagePublishInput{Mode: "immediate"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reviewPagePublish(ctx, db, channel, v2.ID, 2, pagePublishReviewInput{Approved: true}, now); err != nil {
		t.Fatal(err)
	}

	// 能力声明支持 carousel：拿到当前版本 v2。
	snapshot, err := pageSnapshotForClient(ctx, db, channel, map[string]bool{
		"search": true, "support_entry": true, "carousel": true,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.VersionNo != 2 || !snapshot.Compatible || snapshot.Fallback {
		t.Fatalf("能力匹配时应返回当前生效版本：%+v", snapshot)
	}
	// 老客户端（不支持 carousel）：回退到最近可用版本 v1，而不是下发渲染不了的新组件。
	snapshot, err = pageSnapshotForClient(ctx, db, channel, map[string]bool{
		"search": true, "support_entry": true,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.VersionNo != 1 || snapshot.Compatible {
		t.Fatalf("不兼容时应返回最近有效页：%+v", snapshot)
	}
	// 完全不兼容：回系统默认模板。
	fallback, err := pageSnapshotForClient(ctx, db, channel, map[string]bool{"nothing": true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !fallback.Fallback || fallback.VersionNo != 0 {
		t.Fatalf("完全不兼容时应回默认模板：%+v", fallback)
	}
	_ = esportspageversion.StatusPublished
}

// 版本未变时命中缓存（304），不重复全量下发。
func TestPublicPageReturnsNotModifiedOnSameVersion(t *testing.T) {
	db := openPageDB(t, "esports_r4_page_304")
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()
	channel := pageChannelCustomerHome

	doc := seedPageDocument(t, db, channel, pageTestComponents())
	seedPublishedVersion(t, db, doc, channel, pageConfig{Components: pageTestComponents()})

	app := fiber.New()
	app.Get("/page", publicPage)
	status, payload := sendJSON(t, app, http.MethodGet, "/page?channel="+channel, "")
	if status != http.StatusOK {
		t.Fatalf("首次读取应 200：%d %+v", status, payload)
	}
	versionNo, _ := payload["data"].(map[string]any)["version_no"].(float64)
	if versionNo != 1 {
		t.Fatalf("首次读取应返回版本 1：%+v", payload)
	}
	// 客户端带上已知版本号：命中缓存，304 且不再回全量配置。
	status, payload = sendJSON(t, app, http.MethodGet,
		"/page?channel="+channel+"&version_no=1", "")
	if status != http.StatusNotModified {
		t.Fatalf("版本未变应返回 304，实际 %d %+v", status, payload)
	}
	if payload != nil {
		t.Fatalf("304 不应带响应体：%+v", payload)
	}
	// 能力不兼容的老客户端 + 已缓存兼容版本：同样命中 304（它拿到的仍是同一份兼容快照）。
	status, payload = sendJSON(t, app, http.MethodGet,
		"/page?channel="+channel+"&version_no=1&capab=search,support_entry", "")
	if status != http.StatusNotModified {
		t.Fatalf("兼容快照版本未变应 304，实际 %d %+v", status, payload)
	}
}

// seedPublishedVersion 直接把一份配置落成生效版本（绕过发布审核，供读取侧测试使用）。
func seedPublishedVersion(t *testing.T, db *ent.Client, doc *ent.EsportsPageDocument, channel string, cfg pageConfig) *ent.EsportsPageVersion {
	t.Helper()
	ctx := t.Context()
	version := db.EsportsPageVersion.Create().SetDocumentID(doc.ID).SetChannel(channel).
		SetVersionNo(1).SetConfig(pageConfigToMap(cfg)).
		SetStatus(esportspageversion.StatusPublished).
		SetPublishMode(esportspageversion.PublishModeImmediate).SetPublishedBy(9).
		SetPublishedAt(time.Now().UTC()).SaveX(ctx)
	db.EsportsPageDocument.UpdateOneID(doc.ID).SetPublishedVersionNo(1).
		SetStatus(esportspagedocument.StatusPublished).ExecX(ctx)
	return version
}

// pageAssetMaxBytes 用于断言上限（RED 阶段引用生产常量；若未定义则编译失败即为 RED 证据）。
var _ = pageAssetMaxBytes
