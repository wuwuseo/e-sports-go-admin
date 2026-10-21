package esports

import (
	"errors"
	"fmt"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportspagedocument"
	"go-admin/internal/ent/esportspageversion"
	"go-admin/internal/ent/migrate"
	"go-admin/internal/ent/systemconfig"
)

func openPageDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	return enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
}

// pageTestComponents 返回含客服入口的最小合法组件集。
func pageTestComponents() []pageComponent {
	visible := true
	return []pageComponent{
		{Type: "search", Visible: &visible},
		{Type: "support_entry", Visible: &visible},
	}
}

// pageComponentsWithLink 返回含一个外部跳转链接（carousel 条目）的合法组件集。
func pageComponentsWithLink(link string) []pageComponent {
	visible := true
	return []pageComponent{
		{Type: "search", Visible: &visible},
		{Type: "carousel", Visible: &visible, Props: map[string]interface{}{
			"items": []interface{}{map[string]interface{}{
				"image": "https://cdn.example.com/a.png", "link": link,
			}},
		}},
		{Type: "support_entry", Visible: &visible},
	}
}

// setPageAllowedDomains 写入「已审核 HTTPS 域名」白名单；hosts 为空表示清除配置。
func setPageAllowedDomains(t *testing.T, db *ent.Client, hosts string) {
	t.Helper()
	ctx := t.Context()
	db.SystemConfig.Delete().Where(systemconfig.KeyEQ(pageAllowedDomainsKey)).ExecX(ctx)
	if hosts != "" {
		db.SystemConfig.Create().SetKey(pageAllowedDomainsKey).SetName("首页外部跳转已审核域名").
			SetValue(hosts).SetType("string").SetGroup("esports").SetStatus(1).SaveX(ctx)
	}
}

// seedPageDocument 直接写入一份文档（绕过保存草稿的校验），供发布 / 回滚校验测试使用。
func seedPageDocument(t *testing.T, db *ent.Client, channel string, components []pageComponent) *ent.EsportsPageDocument {
	t.Helper()
	doc, err := db.EsportsPageDocument.Create().SetChannel(channel).
		SetDraftConfig(pageConfigToMap(pageConfig{Components: components})).
		SetDraftVersion(1).SetStatus(esportspagedocument.StatusDraft).Save(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// 草稿乐观锁：首次创建成功，版本不符被拒，版本正确则自增。
func TestPageDraftOptimisticLock(t *testing.T) {
	db := openPageDB(t, "esports_page_draft")
	channel := pageChannelCustomerHome

	doc, err := savePageDraft(t.Context(), db, channel, 1, pageDraftInput{
		Components: pageTestComponents(), ExpectedVersion: 0,
	})
	if err != nil {
		t.Fatalf("首次保存草稿失败：%v", err)
	}
	if doc.DraftVersion != 1 || doc.Status != esportspagedocument.StatusDraft {
		t.Fatalf("首次草稿版本/状态异常：%+v", doc)
	}

	if _, err = savePageDraft(t.Context(), db, channel, 1, pageDraftInput{
		Components: pageTestComponents(), ExpectedVersion: 0,
	}); !errors.Is(err, errConflict) {
		t.Fatalf("过期草稿版本应冲突，实际：%v", err)
	}

	doc, err = savePageDraft(t.Context(), db, channel, 2, pageDraftInput{
		Components: pageTestComponents(), ExpectedVersion: 1,
	})
	if err != nil {
		t.Fatalf("二次保存草稿失败：%v", err)
	}
	if doc.DraftVersion != 2 || doc.UpdatedBy != 2 {
		t.Fatalf("二次草稿版本/操作人异常：%+v", doc)
	}
}

// 轮播图每张图片各自保存跳转链接（§16.3-02）：多图多链接在草稿存储与客户端归一化后
// 不得丢失或串号；旧版 images 数组的兼容逻辑不得覆盖 items 中的 link。
func TestPageCarouselKeepsPerImageLinks(t *testing.T) {
	db := openPageDB(t, "esports_page_carousel_links")
	visible := true
	wantLinks := []string{"/pages/order/list", "/pages/mine/index"}
	items := make([]interface{}, 0, len(wantLinks))
	for i, link := range wantLinks {
		items = append(items, map[string]interface{}{
			"image": fmt.Sprintf("/uploads/esports-page/carousel-%d.png", i+1),
			"link":  link,
		})
	}
	doc, err := savePageDraft(t.Context(), db, pageChannelCustomerHome, 1, pageDraftInput{
		Components: []pageComponent{
			{Type: "carousel", Visible: &visible, Props: map[string]interface{}{"items": items}},
			{Type: "support_entry", Visible: &visible},
		},
		ExpectedVersion: 0,
	})
	if err != nil {
		t.Fatalf("保存轮播草稿失败：%v", err)
	}
	cfg, err := decodePageConfig(doc.DraftConfig)
	if err != nil {
		t.Fatalf("解码草稿失败：%v", err)
	}
	// 下发前归一化（有效期剔除等）也必须保持每张图的独立链接。
	for _, round := range []pageConfig{cfg, normalizePageConfigForClient(cfg, time.Now())} {
		if len(round.Components) == 0 || round.Components[0].Type != "carousel" {
			t.Fatalf("草稿缺少轮播组件：%+v", round.Components)
		}
		stored, ok := round.Components[0].Props["items"].([]interface{})
		if !ok || len(stored) != len(wantLinks) {
			t.Fatalf("轮播项数量异常：%+v", round.Components[0].Props["items"])
		}
		for i, link := range wantLinks {
			item, ok := stored[i].(map[string]interface{})
			if !ok {
				t.Fatalf("第 %d 个轮播项类型异常：%T", i+1, stored[i])
			}
			if got, _ := item["link"].(string); got != link {
				t.Fatalf("第 %d 个轮播项链接丢失或串号：got %q want %q", i+1, got, link)
			}
		}
	}
}

// 素材按地址幂等：组件上传 / 历史补登记可能重复提交同一张图，
// 同一 URL 只落一条素材记录；public/... 与 /uploads/... 视为同一张图。
func TestPageAssetCreateIdempotentByURL(t *testing.T) {
	db := openPageDB(t, "esports_page_asset_dedup")
	input := pageAssetInput{
		Name: "轮播图.png", URL: "public/esports-page/carousel.png",
		Mime: "image/png", SizeBytes: 1024, Width: 64, Height: 48,
	}
	first, err := savePageAsset(t.Context(), db, 1, input)
	if err != nil {
		t.Fatalf("首次登记素材失败：%v", err)
	}
	input.Name = "同名重复登记.png"
	input.URL = "/uploads/esports-page/carousel.png" // 同一张图的旧写法
	second, err := savePageAsset(t.Context(), db, 2, input)
	if err != nil {
		t.Fatalf("重复登记素材失败：%v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("同一张图应复用既有素材：first=%d second=%d", first.ID, second.ID)
	}
	items, err := listPageAssets(t.Context(), db, 100)
	if err != nil {
		t.Fatalf("读取素材列表失败：%v", err)
	}
	if len(items) != 1 {
		t.Fatalf("同一张图只应有一条素材记录，实际 %d 条", len(items))
	}
}

// 页面图片允许附件表同款 public/... 存储路径，但路径穿越 / 空段 / 反斜杠必须拒绝。
func TestPageImageAllowsAttachmentStoragePath(t *testing.T) {
	visible := true
	comps := []pageComponent{
		{Type: "image", Visible: &visible, Props: map[string]interface{}{
			"image": "public/esports-page/a.png",
		}},
		{Type: "support_entry", Visible: &visible},
	}
	if err := validatePageConfig(pageConfig{Components: comps}); err != nil {
		t.Fatalf("public/... 存储路径应可保存：%v", err)
	}
	for _, bad := range []string{
		"public/../secret.png",
		"public//secret.png",
		`public/a\b.png`,
		"public/esports-page/",
	} {
		comps[0].Props["image"] = bad
		if err := validatePageConfig(pageConfig{Components: comps}); !errors.Is(err, errInvalidInput) {
			t.Fatalf("非法存储路径 %q 应被拒绝，实际：%v", bad, err)
		}
	}
}

// 素材引用反查要兼容 public/... 与 /uploads/... 两种写法。
func TestPageAssetUsageMatchesStoragePathAndUploads(t *testing.T) {
	visible := true
	cfg := pageConfig{Components: []pageComponent{
		{Type: "image", Visible: &visible, Props: map[string]interface{}{
			"image": "public/esports-page/a.png",
		}},
		{Type: "support_entry", Visible: &visible},
	}}
	usages := pageConfigAssetUsages(pageChannelCustomerHome, 0, "draft",
		pageConfigToMap(cfg), "/uploads/esports-page/a.png")
	if len(usages) != 1 {
		t.Fatalf("素材引用反查应命中 1 处，实际 %d 处：%+v", len(usages), usages)
	}
}

// 首页入口组件不做必需性校验：缺失或隐藏客服 / 订单 / 售后入口都应能保存。
func TestPageEntryComponentsOptional(t *testing.T) {
	visible := true
	hidden := false
	for _, comps := range [][]pageComponent{
		{{Type: "search", Visible: &visible}},                                            // 缺失
		{{Type: "search", Visible: &visible}, {Type: "support_entry", Visible: &hidden}}, // 隐藏
	} {
		if err := validatePageConfig(pageConfig{Components: comps}); err != nil {
			t.Fatalf("入口组件应可省略或隐藏，实际：%v", err)
		}
	}
}

// 发布规则封闭（§16.4 / §16.5）：提交发布申请落待审核，未审核不生效；
// 提交人自审被拒，异人审核通过后一次性版本切换；回滚同样须异人审核。
func TestPagePublishRequiresReviewAndRollback(t *testing.T) {
	db := openPageDB(t, "esports_page_review")
	now := time.Now().UTC()
	channel := pageChannelCustomerHome

	if _, err := savePageDraft(t.Context(), db, channel, 1, pageDraftInput{
		Components: pageTestComponents(), ExpectedVersion: 0,
	}); err != nil {
		t.Fatal(err)
	}

	// 提交发布申请：一律落 pending_review，未审核不生效。
	v1, err := publishPage(t.Context(), db, channel, 1, pagePublishInput{Mode: "immediate"}, now)
	if err != nil {
		t.Fatalf("提交发布申请失败：%v", err)
	}
	if v1.Status != pageVersionStatusPendingReview || v1.VersionNo != 1 {
		t.Fatalf("提交发布应落待审核版本 1，实际：%+v", v1)
	}
	if _, versionNo, err := publishedPageConfig(t.Context(), db, channel); err != nil || versionNo != 0 {
		t.Fatalf("审核前不应生效，versionNo=%d err=%v", versionNo, err)
	}

	// 提交人自审被拒。
	if _, err = reviewPagePublish(t.Context(), db, channel, v1.ID, 1, pagePublishReviewInput{Approved: true}, now); !errors.Is(err, errForbidden) {
		t.Fatalf("提交人自审应被拒，实际：%v", err)
	}

	// 异人审核通过后生效。
	published, err := reviewPagePublish(t.Context(), db, channel, v1.ID, 2, pagePublishReviewInput{Approved: true}, now)
	if err != nil {
		t.Fatalf("异人审核通过失败：%v", err)
	}
	if published.Status != esportspageversion.StatusPublished {
		t.Fatalf("审核通过应已发布，实际 %s", published.Status)
	}
	doc, err := db.EsportsPageDocument.Query().Where(esportspagedocument.ChannelEQ(channel)).Only(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if doc.PublishedVersionNo != 1 || doc.Status != esportspagedocument.StatusPublished {
		t.Fatalf("文档生效指针未更新：%+v", doc)
	}

	// 再次提交发布 → 待审核版本 2，异人通过后旧版本 1 转历史。
	v2, err := publishPage(t.Context(), db, channel, 1, pagePublishInput{Mode: "immediate"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reviewPagePublish(t.Context(), db, channel, v2.ID, 2, pagePublishReviewInput{Approved: true}, now); err != nil {
		t.Fatal(err)
	}
	old, err := db.EsportsPageVersion.Get(t.Context(), v1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if old.Status != esportspageversion.StatusHistory {
		t.Fatalf("旧生效版本应转历史，实际 %s", old.Status)
	}

	// 回滚提交申请 → 待审核版本 3（回滚来源 1），异人审核通过后生效，不原地改旧记录。
	v3, err := rollbackPage(t.Context(), db, channel, 1, 1, now)
	if err != nil {
		t.Fatalf("提交回滚申请失败：%v", err)
	}
	if v3.Status != pageVersionStatusPendingReview || v3.RollbackFromVersionNo != 1 || v3.VersionNo != 3 {
		t.Fatalf("回滚申请版本异常：%+v", v3)
	}
	if _, err = reviewPagePublish(t.Context(), db, channel, v3.ID, 2, pagePublishReviewInput{Approved: true}, now); err != nil {
		t.Fatalf("回滚审核通过失败：%v", err)
	}
	doc, err = db.EsportsPageDocument.Query().Where(esportspagedocument.ChannelEQ(channel)).Only(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if doc.PublishedVersionNo != 3 {
		t.Fatalf("回滚后文档生效版本应指向新版本 3，实际 %d", doc.PublishedVersionNo)
	}
}

// 客户端读取：未装修回系统默认模板且版本号 0；审核发布后返回已发布配置。
func TestPublishedPageConfig(t *testing.T) {
	db := openPageDB(t, "esports_page_public")
	channel := pageChannelCustomerHome
	now := time.Now().UTC()

	config, versionNo, err := publishedPageConfig(t.Context(), db, channel)
	if err != nil {
		t.Fatalf("未装修读取失败：%v", err)
	}
	if versionNo != 0 || len(config["components"].([]interface{})) == 0 {
		t.Fatalf("未装修应回默认模板，实际：%d %+v", versionNo, config)
	}

	if _, err = savePageDraft(t.Context(), db, channel, 1, pageDraftInput{
		Components: pageTestComponents(), ExpectedVersion: 0,
	}); err != nil {
		t.Fatal(err)
	}
	v, err := publishPage(t.Context(), db, channel, 1, pagePublishInput{Mode: "immediate"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reviewPagePublish(t.Context(), db, channel, v.ID, 2, pagePublishReviewInput{Approved: true}, now); err != nil {
		t.Fatal(err)
	}
	config, versionNo, err = publishedPageConfig(t.Context(), db, channel)
	if err != nil {
		t.Fatal(err)
	}
	if versionNo != 1 || len(config["components"].([]interface{})) != 2 {
		t.Fatalf("发布后应返回已发布配置，实际：%d %+v", versionNo, config)
	}
}

// 定时发布：提交申请 → 异人审核通过转 scheduled → 到点由系统任务激活；过去时间被拒。
func TestPublishScheduledPages(t *testing.T) {
	db := openPageDB(t, "esports_page_scheduled")
	channel := pageChannelCustomerHome
	now := time.Now().UTC()

	if _, err := savePageDraft(t.Context(), db, channel, 1, pageDraftInput{
		Components: pageTestComponents(), ExpectedVersion: 0,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := publishPage(t.Context(), db, channel, 1, pagePublishInput{
		Mode: "scheduled", ScheduledAt: now.Add(-time.Hour).Format(time.RFC3339),
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("过去的定时时间应被拒，实际：%v", err)
	}

	future := now.Add(time.Hour)
	version, err := publishPage(t.Context(), db, channel, 1, pagePublishInput{
		Mode: "scheduled", ScheduledAt: future.Format(time.RFC3339),
	}, now)
	if err != nil {
		t.Fatalf("提交定时发布申请失败：%v", err)
	}
	if version.Status != pageVersionStatusPendingReview {
		t.Fatalf("定时发布提交后应为待审核，实际 %s", version.Status)
	}

	// 异人审核通过后转 scheduled。
	if _, err = reviewPagePublish(t.Context(), db, channel, version.ID, 2, pagePublishReviewInput{Approved: true}, now); err != nil {
		t.Fatalf("定时发布审核通过失败：%v", err)
	}
	version, err = db.EsportsPageVersion.Get(t.Context(), version.ID)
	if err != nil {
		t.Fatal(err)
	}
	if version.Status != esportspageversion.StatusScheduled {
		t.Fatalf("定时版本审核通过后应为 scheduled，实际 %s", version.Status)
	}
	doc, err := db.EsportsPageDocument.Query().Where(esportspagedocument.ChannelEQ(channel)).Only(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if doc.Status != esportspagedocument.StatusPending {
		t.Fatalf("定时发布中文档状态应为 pending，实际 %s", doc.Status)
	}

	count, err := publishScheduledPages(t.Context(), db, future.Add(time.Minute), 100)
	if err != nil || count != 1 {
		t.Fatalf("定时任务应激活 1 个版本，实际 %d err=%v", count, err)
	}
	activated, err := db.EsportsPageVersion.Get(t.Context(), version.ID)
	if err != nil {
		t.Fatal(err)
	}
	if activated.Status != esportspageversion.StatusPublished {
		t.Fatalf("到点版本应转生效，实际 %s", activated.Status)
	}
	// 重复执行不应重复激活。
	count, err = publishScheduledPages(t.Context(), db, future.Add(2*time.Minute), 100)
	if err != nil || count != 0 {
		t.Fatalf("重复执行不应再激活，实际 %d err=%v", count, err)
	}
}

// 强制已审核 HTTPS 域名（§16.5）：外部跳转链接发布前校验，未配置白名单 / 未审核域名 / http 一律拒绝；
// 保存草稿放行待审核域名；immediate 与 scheduled 走同样校验。
func TestPagePublishDomainWhitelist(t *testing.T) {
	db := openPageDB(t, "esports_page_domain")
	channel := pageChannelCustomerHome
	now := time.Now().UTC()

	// 保存草稿不拦未审核域名（域名白名单是发布前校验）。
	doc, err := savePageDraft(t.Context(), db, channel, 1, pageDraftInput{
		Components: pageComponentsWithLink("https://unapproved.example.com/x"), ExpectedVersion: 0,
	})
	if err != nil {
		t.Fatalf("保存草稿不应拦未审核域名：%v", err)
	}

	// 未配置白名单：外部跳转链接一律拒绝。
	if _, err = publishPage(t.Context(), db, channel, 1, pagePublishInput{Mode: "immediate"}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("未配置白名单发布应被拒，实际：%v", err)
	}
	// 定时发布同样拦截。
	if _, err = publishPage(t.Context(), db, channel, 1, pagePublishInput{
		Mode: "scheduled", ScheduledAt: now.Add(time.Hour).Format(time.RFC3339),
	}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("未配置白名单定时发布应被拒，实际：%v", err)
	}

	// 白名单只含 allowed.example.com：未审核域名仍被拒（图片域名一并登记，避免被图片探测拦下）。
	setPageAllowedDomains(t, db, "allowed.example.com,cdn.example.com")
	if _, err = publishPage(t.Context(), db, channel, 1, pagePublishInput{Mode: "immediate"}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("未审核域名发布应被拒，实际：%v", err)
	}

	// 改草稿为白名单域名后发布成功；非 https（http）链接被拒。
	if _, err = savePageDraft(t.Context(), db, channel, 1, pageDraftInput{
		Components: pageComponentsWithLink("http://allowed.example.com/x"), ExpectedVersion: doc.DraftVersion,
	}); err == nil {
		t.Fatalf("http 外部跳转链接保存应被拒")
	}
	if _, err = savePageDraft(t.Context(), db, channel, 1, pageDraftInput{
		Components: pageComponentsWithLink("https://allowed.example.com/x"), ExpectedVersion: doc.DraftVersion,
	}); err != nil {
		t.Fatalf("更新草稿失败：%v", err)
	}
	if _, err = publishPage(t.Context(), db, channel, 1, pagePublishInput{Mode: "immediate"}, now); err != nil {
		t.Fatalf("白名单域名发布应成功：%v", err)
	}
}

// 隐藏客服与订单入口不再受限：保存草稿与提交发布都应放行。
func TestPageSupportEntryHiddenAllowed(t *testing.T) {
	db := openPageDB(t, "esports_page_support")
	channel := pageChannelCustomerHome
	now := time.Now().UTC()
	hidden := false
	components := []pageComponent{
		{Type: "search", Visible: boolPtr(true)},
		{Type: "support_entry", Visible: &hidden},
	}

	if _, err := savePageDraft(t.Context(), db, channel, 1, pageDraftInput{
		Components: components, ExpectedVersion: 0,
	}); err != nil {
		t.Fatalf("隐藏客服与订单入口的草稿应可保存，实际：%v", err)
	}
	if _, err := publishPage(t.Context(), db, channel, 1, pagePublishInput{Mode: "immediate"}, now); err != nil {
		t.Fatalf("隐藏客服与订单入口应可提交发布，实际：%v", err)
	}
}

// 回滚不再校验入口可见性，但未审核域名的历史快照仍被拒。
func TestPageRollbackDomainValidation(t *testing.T) {
	db := openPageDB(t, "esports_page_rollback_validation")
	channel := pageChannelCustomerHome
	now := time.Now().UTC()
	seedPageDocument(t, db, channel, pageTestComponents())
	doc, err := db.EsportsPageDocument.Query().Where(esportspagedocument.ChannelEQ(channel)).Only(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	cfg := pageConfig{Components: pageComponentsWithLink("https://unapproved.example.com/x")}
	if _, err = db.EsportsPageVersion.Create().SetDocumentID(doc.ID).SetChannel(channel).
		SetVersionNo(1).SetConfig(pageConfigToMap(cfg)).
		SetStatus(esportspageversion.StatusHistory).SetPublishMode(esportspageversion.PublishModeImmediate).
		SetPublishedBy(9).Save(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = rollbackPage(t.Context(), db, channel, 1, 1, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("回滚到未审核域名的历史快照应被拒，实际：%v", err)
	}
}

// 素材库：地址协议校验与增删。
func TestPageAsset(t *testing.T) {
	db := openPageDB(t, "esports_page_asset")

	if _, err := savePageAsset(t.Context(), db, 1, pageAssetInput{
		Name: "图标", URL: "http://cdn.example.com/a.png",
	}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非 HTTPS 外部地址应被拒，实际：%v", err)
	}

	asset, err := savePageAsset(t.Context(), db, 1, pageAssetInput{
		Name: "图标", URL: "https://cdn.example.com/a.png", Mime: "image/png", SizeBytes: 2048,
		Width: 120, Height: 120,
	})
	if err != nil {
		t.Fatalf("新增素材失败：%v", err)
	}
	items, err := listPageAssets(t.Context(), db, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("素材列表异常：%d err=%v", len(items), err)
	}
	if err = deletePageAsset(t.Context(), db, asset.ID); err != nil {
		t.Fatalf("删除素材失败：%v", err)
	}
	if err = deletePageAsset(t.Context(), db, asset.ID); !ent.IsNotFound(err) {
		t.Fatalf("重复删除应报记录不存在，实际：%v", err)
	}
}

func boolPtr(v bool) *bool {
	return &v
}
