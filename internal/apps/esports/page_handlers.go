package esports

import (
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportspagedocument"
	"go-admin/internal/global"
)

// adminPageDocuments 列出装修渠道文档（首版仅客户自助端首页一个渠道）。
func adminPageDocuments(c fiber.Ctx) error {
	if _, err := requestAdmin(c); err != nil {
		return businessError(c, err)
	}
	items, err := global.ORM.EsportsPageDocument.Query().
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

// approvePagePublishHandler / rejectPagePublishHandler 审核待发布版本（§16.4）。
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

// publicPage 是客户端读取入口（AuthPublic，§16.5）：只返回已发布配置，
// 不含任何个人数据或动态参数。
func publicPage(c fiber.Ctx) error {
	channel := strings.TrimSpace(c.Query("channel"))
	if channel == "" {
		channel = pageChannelCustomerHome
	}
	if len(channel) > 48 {
		return businessError(c, errInvalidInput)
	}
	config, versionNo, err := publishedPageConfig(c.Context(), global.ORM, channel)
	if err != nil {
		return businessError(c, err)
	}
	return reply(c, fiber.Map{"channel": channel, "version_no": versionNo, "config": config})
}
