package esports

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/global"
)

// P0-18 装修渠道白名单回归：任意 :channel 都不得建文档 / 发布 / 写 tabBar / 发预览码 / 预览，
// 越权「装修」陪玩端或客服端首页。
// 本文件全部辅助物以 chw 前缀命名，避免与并行桶的同名测试辅助物撞名。

// chwForeignChannel 是任意「非白名单」渠道（陪玩端首页）。
const chwForeignChannel = "player_home"

// chwChannelRejected 断言错误是「渠道不在允许装修范围」这一类参数错误。
func chwChannelRejected(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, errInvalidInput) {
		t.Fatalf("非白名单渠道应以参数错误拒绝，实际：%v", err)
	}
	if !strings.Contains(err.Error(), "不在允许装修范围") {
		t.Fatalf("非白名单渠道应给出中文提示，实际：%v", err)
	}
}

// 服务层各入口：建文档 / 发布 / 回滚 / 审核 / 版本列表。
func TestPageChannelWhitelistServiceEntries(t *testing.T) {
	db := openPageDB(t, "esports_page_channel_service")
	ctx := t.Context()
	now := time.Now().UTC()
	channel := chwForeignChannel

	_, err := savePageDraft(ctx, db, channel, 1, pageDraftInput{Components: pageTestComponents()})
	chwChannelRejected(t, err)

	// 绕过保存直接种文档（模拟历史数据 / 越权渠道已存在），发布 / 回滚 / 审核同样拒绝。
	seedPageDocument(t, db, channel, pageTestComponents())

	_, err = publishPage(ctx, db, channel, 1, pagePublishInput{Mode: "immediate"}, now)
	chwChannelRejected(t, err)

	_, err = rollbackPage(ctx, db, channel, 1, 1, now)
	chwChannelRejected(t, err)

	_, err = reviewPagePublish(ctx, db, channel, 1, 2, pagePublishReviewInput{Approved: true}, now)
	chwChannelRejected(t, err)

	_, err = listPageVersions(ctx, db, channel, 10)
	chwChannelRejected(t, err)

	// 白名单渠道不受影响：文档仍在，草稿可正常保存。
	if _, err = savePageDraft(ctx, db, pageChannelCustomerHome, 1, pageDraftInput{
		Components: pageTestComponents(),
	}); err != nil {
		t.Fatalf("白名单渠道应可正常保存草稿，实际：%v", err)
	}
}

// HTTP 各入口：建文档 / 发布 / tabBar 读写 / 预览码 / 预览码兑换 / 客户端读取。
func TestPageChannelWhitelistHandlers(t *testing.T) {
	db := openPageDB(t, "esports_page_channel_handlers")
	previous := global.ORM
	global.ORM = db
	defer func() { global.ORM = previous }()
	admin := seedAdminAccount(t, db, "page-channel-admin")

	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("uid", admin.UID)
		c.Locals("isSuperAdmin", true)
		return c.Next()
	})
	app.Put("/admin/pages/:channel/draft", savePageDraftHandler)
	app.Post("/admin/pages/:channel/publish", publishPageHandler)
	app.Get("/admin/pages/:channel/tabbar", adminPageTabbar)
	app.Put("/admin/pages/:channel/tabbar", savePageTabbarHandler)
	app.Post("/admin/pages/:channel/preview-code", issuePagePreviewCodeHandler)
	app.Get("/page/preview/:code", publicPagePreview)
	app.Get("/page", publicPage)

	foreign := chwForeignChannel
	// 预览码里带着渠道：仅按载荷造一个格式正确的码即可验证「兑换前先拦渠道」。
	previewCode := signPagePreviewPayload("test-secret", foreign+"|1|1900000000")

	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"建文档（保存草稿）", http.MethodPut, "/admin/pages/" + foreign + "/draft",
			`{"components":[{"type":"search","visible":true}],"expected_version":0}`},
		{"发布", http.MethodPost, "/admin/pages/" + foreign + "/publish", `{"mode":"immediate"}`},
		{"tabBar 读", http.MethodGet, "/admin/pages/" + foreign + "/tabbar", ""},
		{"tabBar 写", http.MethodPut, "/admin/pages/" + foreign + "/tabbar", `{"items":[],"expected_version":1}`},
		{"预览码签发", http.MethodPost, "/admin/pages/" + foreign + "/preview-code", ""},
		{"预览（预览码兑换）", http.MethodGet, "/page/preview/" + previewCode, ""},
		{"客户端读取", http.MethodGet, "/page?channel=" + foreign, ""},
	}
	for _, tc := range cases {
		status, payload := sendJSON(t, app, tc.method, tc.path, tc.body)
		if status != fiber.StatusBadRequest {
			t.Fatalf("%s：非白名单渠道应返回 400，实际 %d %+v", tc.name, status, payload)
		}
		message, _ := payload["message"].(string)
		if !strings.Contains(message, "不在允许装修范围") {
			t.Fatalf("%s：应返回中文渠道提示，实际 %q", tc.name, message)
		}
	}

	// 收紧不能把正常渠道一起挡掉：白名单渠道的同一批入口都应可用。
	seedUpgradeConfig(t, db, pagePreviewSecretKey, "test-secret")
	allowed := pageChannelCustomerHome
	draftBody := `{"components":[{"type":"search","visible":true},{"type":"support_entry","visible":true}],"expected_version":0}`
	if status, payload := sendJSON(t, app, http.MethodPut, "/admin/pages/"+allowed+"/draft", draftBody); status != http.StatusOK {
		t.Fatalf("白名单渠道保存草稿应成功：%d %+v", status, payload)
	}
	if status, payload := sendJSON(t, app, http.MethodPost, "/admin/pages/"+allowed+"/publish", `{"mode":"immediate"}`); status != http.StatusOK {
		t.Fatalf("白名单渠道发布应成功：%d %+v", status, payload)
	}
	if status, payload := sendJSON(t, app, http.MethodGet, "/admin/pages/"+allowed+"/tabbar", ""); status != http.StatusOK {
		t.Fatalf("白名单渠道读 tabBar 应成功：%d %+v", status, payload)
	}
	status, payload := sendJSON(t, app, http.MethodPost, "/admin/pages/"+allowed+"/preview-code", "")
	if status != http.StatusOK {
		t.Fatalf("白名单渠道签发预览码应成功：%d %+v", status, payload)
	}
	data, _ := payload["data"].(map[string]any)
	code, _ := data["code"].(string)
	if code == "" {
		t.Fatalf("预览码缺失：%+v", payload)
	}
	if status, payload = sendJSON(t, app, http.MethodGet, "/page/preview/"+code, ""); status != http.StatusOK {
		t.Fatalf("白名单渠道预览应成功：%d %+v", status, payload)
	}
	if status, payload = sendJSON(t, app, http.MethodGet, "/page?channel="+allowed, ""); status != http.StatusOK {
		t.Fatalf("白名单渠道客户端读取应成功：%d %+v", status, payload)
	}
}
