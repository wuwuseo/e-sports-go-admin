package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/enttest"
	"go-admin/internal/ent/esportspagedocument"
	"go-admin/internal/ent/esportspageversion"
	"go-admin/internal/ent/migrate"
)

func openPageDB(t *testing.T, name string) *ent.Client {
	t.Helper()
	return enttest.Open(t, "sqlite3", "file:"+name+"?mode=memory&cache=shared&_fk=1",
		enttest.WithMigrateOptions(migrate.WithForeignKeys(false)))
}

// pageTestComponents 返回含必需入口的最小合法组件集。
func pageTestComponents() []pageComponent {
	visible := true
	return []pageComponent{
		{Type: "search", Visible: &visible},
		{Type: "support_entry", Visible: &visible},
	}
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

// 缺少客服与订单入口组件时保存 / 发布被拒。
func TestPageValidateRequiredEntry(t *testing.T) {
	visible := true
	in := pageDraftInput{Components: []pageComponent{{Type: "search", Visible: &visible}}}
	if err := validatePageConfig(pageConfig{Components: in.Components}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("缺少必需入口应报参数错误，实际：%v", err)
	}
}

// 发布生成不可变快照并切换生效版本；再次发布把旧版本转历史。
func TestPagePublishAndRollback(t *testing.T) {
	db := openPageDB(t, "esports_page_publish")
	now := time.Now().UTC()
	channel := pageChannelCustomerHome

	if _, err := savePageDraft(t.Context(), db, channel, 1, pageDraftInput{
		Components: pageTestComponents(), ExpectedVersion: 0,
	}); err != nil {
		t.Fatalf("保存草稿失败：%v", err)
	}
	v1, err := publishPage(t.Context(), db, channel, 1, pagePublishInput{Mode: "immediate"}, now)
	if err != nil {
		t.Fatalf("首次发布失败：%v", err)
	}
	if v1.VersionNo != 1 || v1.Status != esportspageversion.StatusPublished {
		t.Fatalf("首次发布版本异常：%+v", v1)
	}

	v2, err := publishPage(t.Context(), db, channel, 1, pagePublishInput{Mode: "immediate"}, now)
	if err != nil {
		t.Fatalf("二次发布失败：%v", err)
	}
	if v2.VersionNo != 2 {
		t.Fatalf("二次发布版本号应为 2，实际 %d", v2.VersionNo)
	}
	old, err := db.EsportsPageVersion.Get(t.Context(), v1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if old.Status != esportspageversion.StatusHistory {
		t.Fatalf("旧生效版本应转历史，实际 %s", old.Status)
	}

	// 回滚是把历史快照重新发布为新版本，不原地改旧记录。
	v3, err := rollbackPage(t.Context(), db, channel, 1, 1, now)
	if err != nil {
		t.Fatalf("回滚失败：%v", err)
	}
	if v3.VersionNo != 3 || v3.RollbackFromVersionNo != 1 {
		t.Fatalf("回滚版本异常：%+v", v3)
	}
	doc, err := db.EsportsPageDocument.Query().Where(esportspagedocument.ChannelEQ(channel)).Only(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if doc.PublishedVersionNo != 3 {
		t.Fatalf("文档生效版本应指向回滚新版本，实际 %d", doc.PublishedVersionNo)
	}
}

// 客户端读取：未装修回系统默认模板且版本号 0；发布后返回已发布配置。
func TestPublishedPageConfig(t *testing.T) {
	db := openPageDB(t, "esports_page_public")
	channel := pageChannelCustomerHome

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
	if _, err = publishPage(t.Context(), db, channel, 1, pagePublishInput{Mode: "immediate"}, time.Now()); err != nil {
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

// 定时发布：过去时间被拒；到点后由系统任务激活为生效版本。
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
		t.Fatalf("定时发布失败：%v", err)
	}
	if version.Status != esportspageversion.StatusScheduled {
		t.Fatalf("定时版本状态应为 scheduled，实际 %s", version.Status)
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

// 素材库：地址协议校验与增删。
func TestPageAsset(t *testing.T) {
	db := openPageDB(t, "esports_page_asset")

	if _, err := savePageAsset(t.Context(), db, 1, pageAssetInput{
		Name: "图标", URL: "http://cdn.example.com/a.png",
	}); !errors.Is(err, errInvalidInput) {
		t.Fatalf("非 HTTPS 外部地址应被拒，实际：%v", err)
	}

	asset, err := savePageAsset(t.Context(), db, 1, pageAssetInput{
		Name: "图标", URL: "https://cdn.example.com/a.png", Width: 120, Height: 120,
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
