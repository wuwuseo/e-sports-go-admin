package esports

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// 快捷入口 target 指向「已下线的储值卡功能」时的行为（2026-10-20 储值卡移除后的收尾）：
//   - 新写入（保存草稿 / 发布）必须被拒绝，并提示改用可用入口；
//   - 已发布的历史快照不可回写，只能在读取侧剔除，保证客户端不收到死入口。

// quickNavComponents 返回「含一个快捷入口组件 + 必需客服入口」的最小合法组件集。
func quickNavComponents(targets ...string) []pageComponent {
	visible := true
	items := make([]interface{}, 0, len(targets))
	for _, target := range targets {
		items = append(items, map[string]interface{}{"name": "入口-" + target, "target": target})
	}
	return []pageComponent{
		{Type: "search", Visible: &visible},
		{Type: "quick_nav", Visible: &visible, Props: map[string]interface{}{"items": items}},
		{Type: "support_entry", Visible: &visible},
	}
}

// TestSavePageDraftRejectsRemovedNavTarget 保存草稿时就拒绝已下线的快捷入口。
func TestSavePageDraftRejectsRemovedNavTarget(t *testing.T) {
	db := openPageDB(t, "esports_page_nav_draft")
	defer db.Close()
	ctx := t.Context()

	_, err := savePageDraft(ctx, db, pageChannelCustomerHome, 1, pageDraftInput{
		Components: quickNavComponents("category", "prepay_card"),
	})
	if !errors.Is(err, errInvalidInput) {
		t.Fatalf("含已下线 target 的草稿应被拒绝，实际错误：%v", err)
	}
	for _, want := range []string{"prepay_card", "已下线", "储值卡"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误提示应包含 %q，实际：%v", want, err)
		}
	}
}

// TestPublishPageRejectsRemovedNavTarget 发布前同样拒绝已下线的快捷入口。
func TestPublishPageRejectsRemovedNavTarget(t *testing.T) {
	db := openPageDB(t, "esports_page_nav_publish")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()

	// 直接落一份合法草稿，再改成含 prepay_card 的配置绕过保存期校验，模拟「历史脏草稿」。
	if _, err := savePageDraft(ctx, db, pageChannelCustomerHome, 1, pageDraftInput{
		Components: quickNavComponents("category"),
	}); err != nil {
		t.Fatalf("合法草稿应保存成功：%v", err)
	}
	doc, err := db.EsportsPageDocument.Query().Only(ctx)
	if err != nil {
		t.Fatalf("读取页面文档失败：%v", err)
	}
	if _, err = db.EsportsPageDocument.UpdateOneID(doc.ID).
		SetDraftConfig(pageConfigToMap(pageConfig{Components: quickNavComponents("prepay_card")})).
		Save(ctx); err != nil {
		t.Fatalf("写入脏草稿失败：%v", err)
	}

	_, err = publishPage(ctx, db, pageChannelCustomerHome, 1, pagePublishInput{Mode: "immediate"}, now)
	if !errors.Is(err, errInvalidInput) {
		t.Fatalf("含已下线 target 的配置不应允许发布，实际错误：%v", err)
	}
	if !strings.Contains(err.Error(), "prepay_card") {
		t.Fatalf("发布错误应指出具体 target，实际：%v", err)
	}
}

// TestSavePageDraftAcceptsSupportedNavTargets 客户端支持的四个语义入口不受影响。
func TestSavePageDraftAcceptsSupportedNavTargets(t *testing.T) {
	db := openPageDB(t, "esports_page_nav_ok")
	defer db.Close()
	ctx := t.Context()

	if _, err := savePageDraft(ctx, db, pageChannelCustomerHome, 1, pageDraftInput{
		Components: quickNavComponents("category", "orders", "member", "support"),
	}); err != nil {
		t.Fatalf("受支持的 target 应可保存：%v", err)
	}
}

// TestPruneRemovedPageNavTargets 读取侧剔除死入口，其余条目与组件保持原样。
func TestPruneRemovedPageNavTargets(t *testing.T) {
	cfg := pageConfig{Components: quickNavComponents("category", "stored_value_card", "orders")}

	pruned := pruneRemovedPageNavTargets(cfg)
	if len(pruned.Components) != len(cfg.Components) {
		t.Fatalf("只剔条目不应删组件，期望 %d 个组件，实际 %d", len(cfg.Components), len(pruned.Components))
	}
	items, ok := pruned.Components[1].Props["items"].([]interface{})
	if !ok {
		t.Fatal("quick_nav 的 items 应保持数组")
	}
	if len(items) != 2 {
		t.Fatalf("应剔掉 1 个死入口、保留 2 个，实际 %d 个", len(items))
	}
	for _, rawItem := range items {
		item := rawItem.(map[string]interface{})
		if item["target"] == "stored_value_card" {
			t.Fatal("已下线 target 应被剔除")
		}
	}
	// 原配置不得被就地修改（历史快照只读）。
	originalItems := cfg.Components[1].Props["items"].([]interface{})
	if len(originalItems) != 3 {
		t.Fatalf("原配置应保持 3 个条目，实际 %d 个", len(originalItems))
	}
}

// TestNormalizePageConfigDropsEmptiedQuickNav 全部入口都是死入口时，整块 quick_nav 不下发。
func TestNormalizePageConfigDropsEmptiedQuickNav(t *testing.T) {
	cfg := pageConfig{Components: quickNavComponents("prepay_card", "stored_value_card")}

	normalized := normalizePageConfigForClient(cfg, time.Now())
	for _, comp := range normalized.Components {
		if comp.Type == "quick_nav" {
			t.Fatal("全部条目被剔除后 quick_nav 整块不应下发")
		}
	}
	// 客服入口组件不应被剔除，否则客户端会失去客服/订单入口。
	found := false
	for _, comp := range normalized.Components {
		if comp.Type == "support_entry" {
			found = true
		}
	}
	if !found {
		t.Fatal("剔除快捷入口不应连带动到 support_entry")
	}
}

// TestNormalizePageConfigKeepsCleanNav 没有死入口时配置原样通过。
func TestNormalizePageConfigKeepsCleanNav(t *testing.T) {
	cfg := pageConfig{Components: quickNavComponents("category", "orders")}

	normalized := normalizePageConfigForClient(cfg, time.Now())
	var navFound bool
	for _, comp := range normalized.Components {
		if comp.Type != "quick_nav" {
			continue
		}
		navFound = true
		items := comp.Props["items"].([]interface{})
		if len(items) != 2 {
			t.Fatalf("干净配置的入口数不应变化，实际 %d 个", len(items))
		}
	}
	if !navFound {
		t.Fatal("干净配置不应被删掉 quick_nav 组件")
	}
}

// TestAdminPageDocumentPrunesRemovedNavTarget 编辑器读取草稿时也剔除已下线入口。
//
// 这是「装修页保存 / 发布永远报校验未通过」的回归测试：存量脏草稿含 prepay_card 时，
// 编辑器原先会把脏项原样回传，保存被 validatePageEnumValue 拦下，形成死循环。
// 期望：读取即归一化、且不回写数据库；编辑器把读到的配置原样保存即可自愈。
func TestAdminPageDocumentPrunesRemovedNavTarget(t *testing.T) {
	db := openPageDB(t, "esports_page_nav_admin_read")
	defer db.Close()
	ctx := t.Context()

	// 先落合法草稿，再直接改成含 prepay_card 的配置绕过保存期校验，模拟历史脏草稿。
	if _, err := savePageDraft(ctx, db, pageChannelCustomerHome, 1, pageDraftInput{
		Components: quickNavComponents("category"),
	}); err != nil {
		t.Fatalf("合法草稿应保存成功：%v", err)
	}
	doc, err := db.EsportsPageDocument.Query().Only(ctx)
	if err != nil {
		t.Fatalf("读取页面文档失败：%v", err)
	}
	dirty := pageConfig{Components: quickNavComponents("category", "prepay_card")}
	if _, err = db.EsportsPageDocument.UpdateOneID(doc.ID).
		SetDraftConfig(pageConfigToMap(dirty)).Save(ctx); err != nil {
		t.Fatalf("写入脏草稿失败：%v", err)
	}

	previous := global.ORM
	global.ORM = db
	t.Cleanup(func() { global.ORM = previous })

	admin := seedAdminAccount(t, db, "pnt-admin")
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("uid", admin.UID)
		c.Locals("isSuperAdmin", true)
		return c.Next()
	})
	app.Get("/admin/pages/:channel", adminPageDocument)

	status, payload := pcqDoGet(t, app, "/admin/pages/"+pageChannelCustomerHome)
	if status != http.StatusOK {
		t.Fatalf("读取草稿接口应成功，status=%d payload=%v", status, payload)
	}
	data, _ := payload["data"].(map[string]any)
	comps, _ := data["components"].([]any)
	if len(comps) == 0 {
		t.Fatalf("响应缺少 components：%v", payload)
	}

	var targets []string
	for _, raw := range comps {
		comp, _ := raw.(map[string]any)
		if comp["type"] != "quick_nav" {
			continue
		}
		props, _ := comp["props"].(map[string]any)
		items, _ := props["items"].([]any)
		for _, rawItem := range items {
			item, _ := rawItem.(map[string]any)
			target, _ := item["target"].(string)
			targets = append(targets, strings.TrimSpace(target))
		}
	}
	if !containsString(targets, "category") {
		t.Fatalf("可用入口不应被剔除，实际 %v", targets)
	}
	if containsString(targets, "prepay_card") {
		t.Fatalf("编辑器读到的配置不应再含已下线入口，实际 %v", targets)
	}

	// 读取侧只做归一化，不回写快照外的业务数据（草稿仍保持原样，避免误伤历史记录）。
	kept, err := db.EsportsPageDocument.Get(ctx, doc.ID)
	if err != nil {
		t.Fatalf("重新读取文档失败：%v", err)
	}
	raw, err := json.Marshal(kept.DraftConfig)
	if err != nil {
		t.Fatalf("序列化草稿失败：%v", err)
	}
	if !strings.Contains(string(raw), "prepay_card") {
		t.Fatal("读取接口不应回写数据库，草稿应保持原样")
	}

	// 模拟「打开编辑器后直接保存」：回传归一化后的配置必须能保存、且随后可以发布。
	// ExpectedVersion 取编辑器读到的 draft_version（乐观锁，与真实前端一致）。
	clean := pageConfig{Components: quickNavComponents(targets...)}
	if _, err = savePageDraft(ctx, db, pageChannelCustomerHome, 1, pageDraftInput{
		Components: clean.Components, ExpectedVersion: doc.DraftVersion,
	}); err != nil {
		t.Fatalf("归一化后的配置应可保存（自愈链路）：%v", err)
	}
	if _, err = publishPage(ctx, db, pageChannelCustomerHome, admin.ID,
		pagePublishInput{Mode: "immediate"}, time.Now()); err != nil {
		t.Fatalf("自愈后的草稿应可提交发布：%v", err)
	}
}
