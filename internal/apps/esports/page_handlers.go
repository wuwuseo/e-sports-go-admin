package esports

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportspagedocument"
	"go-admin/internal/global"
)

// adminPageDocuments 列出装修渠道文档（首版仅客户自助端首页一个渠道）。
// P0-18：只列出允许装修的渠道，历史遗留的越权渠道文档不再回显（读 / 写 / 发布本就已拦截）。
func adminPageDocuments(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	channels := make([]string, 0, len(pageAllowedChannels))
	for channel := range pageAllowedChannels {
		channels = append(channels, channel)
	}
	items, err := global.ORM.EsportsPageDocument.Query().
		Where(esportspagedocument.ChannelIn(channels...)).
		Order(ent.Asc(esportspagedocument.FieldChannel)).Limit(50).All(c.Context())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

// adminPageDocument 读取渠道装修工作副本与当前草稿组件；未装修时回系统默认模板。
func adminPageDocument(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	channel := strings.TrimSpace(c.Params("channel"))
	if channel == "" || len(channel) > 48 {
		return businessError(c, errInvalidInput)
	}
	// P0-18：非白名单渠道不读取工作副本（避免成为探测 / 改写其它端首页的入口）。
	if err := pageChannelAllowed(channel); err != nil {
		return businessError(c, err)
	}
	doc, err := global.ORM.EsportsPageDocument.Query().
		Where(esportspagedocument.ChannelEQ(channel)).Only(c.Context())
	if ent.IsNotFound(err) {
		return reply(c, fiber.Map{
			"document": fiber.Map{
				"channel": channel, "status": "draft",
				"draft_version": 0, "published_version_no": 0, "updated_by": 0,
			},
			"components": defaultPageConfig().Components,
		})
	}
	if err != nil {
		return businessError(c, err)
	}
	cfg, err := decodePageConfig(doc.DraftConfig)
	if err != nil {
		return businessError(c, err)
	}
	// 编辑器读取侧同样剔除已下线功能的快捷入口：否则存量脏草稿会把已下线 target
	// 原样回传，保存 / 发布时被 validatePageEnumValue 永远拦下（表现为「校验未通过」死循环）。
	// 这里只归一化读取结果、不改库；编辑器下次保存即自然回写干净配置。
	cfg = pruneRemovedPageNavTargets(cfg)
	return reply(c, fiber.Map{"document": doc, "components": cfg.Components})
}

func savePageDraftHandler(c fiber.Ctx) error {
	admin, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in pageDraftInput
	if err = c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	doc, err := savePageDraft(c.Context(), global.ORM, c.Params("channel"), admin.ID, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, doc)
}

func publishPageHandler(c fiber.Ctx) error {
	admin, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in pagePublishInput
	if err = c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	version, err := publishPage(c.Context(), global.ORM, c.Params("channel"), admin.ID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, version)
}

func rollbackPageHandler(c fiber.Ctx) error {
	admin, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in pageRollbackInput
	if err = c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	version, err := rollbackPage(c.Context(), global.ORM, c.Params("channel"), admin.ID, in.VersionNo, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, version)
}

// approvePagePublishHandler / rejectPagePublishHandler 审核待发布版本。
// 需在 app.go 接线（perm 列表见下）：
//
//	{Method: "POST", Path: "/admin/pages/:channel/publish-review/:versionId/approve", Auth: cmfapp.AuthAdmin, Handler: approvePagePublishHandler}
//	{Method: "POST", Path: "/admin/pages/:channel/publish-review/:versionId/reject",  Auth: cmfapp.AuthAdmin, Handler: rejectPagePublishHandler}
//
// 权限码（PermAPI）：
//
//	{Type: cmfapp.PermAPI, Name: "审核通过发布", Code: "esports:pages:publish_approve",
//	 Path: "/api/v1/esports/admin/pages/{channel}/publish-review/{versionId}/approve", Method: "POST", Parent: "esports:page:menu"}
//	{Type: cmfapp.PermAPI, Name: "驳回发布", Code: "esports:pages:publish_reject",
//	 Path: "/api/v1/esports/admin/pages/{channel}/publish-review/{versionId}/reject", Method: "POST", Parent: "esports:page:menu"}
func approvePagePublishHandler(c fiber.Ctx) error {
	return reviewPagePublishHandler(c, true)
}

func rejectPagePublishHandler(c fiber.Ctx) error {
	return reviewPagePublishHandler(c, false)
}

func reviewPagePublishHandler(c fiber.Ctx, approved bool) error {
	admin, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	versionID, err := strconv.Atoi(c.Params("versionId"))
	if err != nil || versionID < 1 {
		return fail(c, fiber.StatusBadRequest, "版本 ID 无效")
	}
	var in pagePublishReviewInput
	if err = c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	in.Approved = approved // 以路径为准，忽略请求体中的 approved 字段。
	// 超管身份仅取权限中间件的认证结果，不接受请求体声明。
	in.isSuperAdmin, _ = c.Locals("isSuperAdmin").(bool)
	version, err := reviewPagePublish(c.Context(), global.ORM, c.Params("channel"), versionID, admin.ID, in, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, version)
}

func adminPageVersions(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listPageVersions(c.Context(), global.ORM, c.Params("channel"), queryInt(c, "limit"))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func adminPageAssets(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := listPageAssets(c.Context(), global.ORM, queryInt(c, "limit"))
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, items)
}

func createPageAssetHandler(c fiber.Ctx) error {
	admin, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in pageAssetInput
	if err = c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	asset, err := savePageAsset(c.Context(), global.ORM, admin.ID, in)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, asset)
}

func deletePageAssetHandler(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return fail(c, fiber.StatusBadRequest, "素材 ID 无效")
	}
	if err = deletePageAsset(c.Context(), global.ORM, id); err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{"id": id})
}

// publicPage 是客户端读取入口（AuthPublic，）：只返回已发布配置，不含任何个人数据或动态参数。
//
// 第 4 轮补齐：
//   - capab 声明客户端支持的组件类型；不兼容时回退最近有效版本，再不行回系统默认模板；
//   - version_no / If-None-Match 命中同一版本时返回 304，不再重复全量下发；
//   - 下发前剔除已过有效期的组件与轮播项（03/08），并附带底部导航。
func publicPage(c fiber.Ctx) error {
	channel := strings.TrimSpace(c.Query("channel"))
	if channel == "" {
		channel = pageChannelCustomerHome
	}
	if len(channel) > 48 {
		return businessError(c, errInvalidInput)
	}
	// P0-18：客户端读取同样只接受白名单渠道，避免任意 :channel 的装修配置被下发到其它端。
	if err := pageChannelAllowed(channel); err != nil {
		return businessError(c, err)
	}
	rawCapab, hasCapab := c.Queries()["capab"]
	caps := pageCapabilities(rawCapab, hasCapab)
	snapshot, err := pageSnapshotForClient(c.Context(), global.ORM, channel, caps, time.Now())
	if err != nil {
		return businessError(c, err)
	}
	etag := pageSnapshotETag(snapshot)
	if strings.TrimSpace(c.Get("If-None-Match")) == etag ||
		(parseQueryInt(c.Query("version_no")) == snapshot.VersionNo && snapshot.VersionNo > 0) {
		c.Set("ETag", etag)
		c.Set("X-Page-Version", strconv.Itoa(snapshot.VersionNo))
		return c.Status(fiber.StatusNotModified).Send(nil)
	}
	cfg, err := decodePageConfig(snapshot.Config)
	if err != nil {
		return businessError(c, err)
	}
	cfg = normalizePageConfigForClient(cfg, time.Now())
	tabbar, err := pageTabbarFromConfig(snapshot.Config)
	if err != nil {
		return businessError(c, err)
	}
	c.Set("ETag", etag)
	c.Set("X-Page-Version", strconv.Itoa(snapshot.VersionNo))
	return reply(c, fiber.Map{
		"channel": channel, "version_no": snapshot.VersionNo,
		"current_version_no": snapshot.CurrentVersionNo,
		"compatible":         snapshot.Compatible, "fallback": snapshot.Fallback,
		"config": pageConfigToMap(cfg), "tabbar": visiblePageTabbar(tabbar),
	})
}

// pageSnapshotETag 生成快照的缓存标识：版本号相同即内容相同（版本快照不可变）。
func pageSnapshotETag(snapshot *pageSnapshot) string {
	return fmt.Sprintf(`"page-%s-%d"`, snapshot.Channel, snapshot.VersionNo)
}

// parseQueryInt 宽松解析查询参数中的整数（非法 / 缺失返回 0）。
func parseQueryInt(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	return value
}

// publicPagePreview 设备预览码兑换入口（AuthPublic，）：只回草稿配置，不含个人数据。
// 需在 app.go 接线：{Method: "GET", Path: "/page/preview/:code", Auth: cmfapp.AuthPublic, Handler: publicPagePreview}
func publicPagePreview(c fiber.Ctx) error {
	// P0-18：预览码里带着渠道，兑换前先做渠道白名单判定；非白名单渠道不返回任何草稿内容。
	if channel := pagePreviewCodeChannel(c.Params("code")); channel != "" {
		if err := pageChannelAllowed(channel); err != nil {
			return businessError(c, err)
		}
	}
	config, versionNo, err := resolvePagePreviewCode(c.Context(), global.ORM, c.Params("code"), time.Now())
	if err != nil {
		return businessError(c, err)
	}
	cfg, err := decodePageConfig(config)
	if err != nil {
		return businessError(c, err)
	}
	cfg = normalizePageConfigForClient(cfg, time.Now())
	tabbar, err := pageTabbarFromConfig(config)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{
		"channel": cfgChannelHint(config, c.Query("channel")), "draft_version": versionNo,
		"preview": true, "config": pageConfigToMap(cfg), "tabbar": visiblePageTabbar(tabbar),
	})
}

// cfgChannelHint 返回预览配置对应的渠道（预览码内已含渠道，这里只做回显）。
func cfgChannelHint(_ map[string]interface{}, fallback string) string {
	return fallback
}

// pagePreviewCodeChannel 只解析预览码载荷里的渠道（**不验签**：验签由 resolvePagePreviewCode 完成）。
// 用途是在兑换草稿前先做渠道白名单判定（P0-18）；解析失败返回空串，交由兑换流程按既有口径报错。
func pagePreviewCodeChannel(code string) string {
	code = strings.TrimSpace(code)
	dot := strings.LastIndex(code, ".")
	if dot <= 0 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(code[:dot])
	if err != nil {
		return ""
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 3 {
		return ""
	}
	return parts[0]
}

// issuePagePreviewCodeHandler 生成设备预览码（，管理端）。
// 需在 app.go 接线 + PermAPI：
//
//	POST /admin/pages/:channel/preview-code → issuePagePreviewCodeHandler
//	PermAPI Code: "esports:pages:preview_code"，Path: "/api/v1/esports/admin/pages/{channel}/preview-code"
func issuePagePreviewCodeHandler(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	// P0-18：预览码只对白名单渠道签发。
	if err := pageChannelAllowed(c.Params("channel")); err != nil {
		return businessError(c, err)
	}
	view, err := issuePagePreviewCode(c.Context(), global.ORM, c.Params("channel"), time.Now())
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// savePageTabbarHandler 保存底部导航（，管理端）：允许增删条目（1~5 项）与编辑
// path / order / 文案 / 图标；path 须为命中已注册页面白名单的站内相对路径（见 pageValidateTabbar）。
// 需在 app.go 接线 + PermAPI：
//
//	PUT /admin/pages/:channel/tabbar → savePageTabbarHandler
//	PermAPI Code: "esports:pages:tabbar_update"，Path: "/api/v1/esports/admin/pages/{channel}/tabbar"
func savePageTabbarHandler(c fiber.Ctx) error {
	admin, err := requestAdmin(c)
	if err != nil {
		return businessError(c, err)
	}
	var in struct {
		Items           []pageTabbarItem `json:"items"`
		ExpectedVersion int              `json:"expected_version"`
	}
	if err = c.Bind().Body(&in); err != nil {
		return fail(c, fiber.StatusBadRequest, "请求正文无效")
	}
	// P0-18：tabBar 写入只对白名单渠道放行。
	if err := pageChannelAllowed(c.Params("channel")); err != nil {
		return businessError(c, err)
	}
	doc, err := savePageTabbar(c.Context(), global.ORM, c.Params("channel"), admin.ID, in.ExpectedVersion, in.Items)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, doc)
}

// pageAssetUsagesHandler 素材被引用位置反查（，管理端）。
// 需在 app.go 接线 + PermAPI：
//
//	GET /admin/page-assets/:id/usages → pageAssetUsagesHandler
//	PermAPI Code: "esports:pages:asset_usages"，Path: "/api/v1/esports/admin/page-assets/{id}/usages"
func pageAssetUsagesHandler(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil || id < 1 {
		return fail(c, fiber.StatusBadRequest, "素材 ID 无效")
	}
	view, err := pageAssetUsageView(c.Context(), global.ORM, id)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, view)
}

// adminPageTabbar 读取当前渠道的底部导航（未装修时回系统默认），供管理端面板回显。
func adminPageTabbar(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	channel := strings.TrimSpace(c.Params("channel"))
	if channel == "" || len(channel) > 48 {
		return businessError(c, errInvalidInput)
	}
	// P0-18：tabBar 读取只对白名单渠道放行。
	if err := pageChannelAllowed(channel); err != nil {
		return businessError(c, err)
	}
	doc, err := global.ORM.EsportsPageDocument.Query().
		Where(esportspagedocument.ChannelEQ(channel)).Only(c.Context())
	if ent.IsNotFound(err) {
		return reply(c, fiber.Map{"channel": channel, "draft_version": 0, "items": defaultPageTabbar()})
	}
	if err != nil {
		return businessError(c, err)
	}
	items, err := pageTabbarFromConfig(doc.DraftConfig)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{"channel": channel, "draft_version": doc.DraftVersion, "items": items})
}
