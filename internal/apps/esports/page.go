package esports

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportspageasset"
	"go-admin/internal/ent/esportspagedocument"
	"go-admin/internal/ent/esportspageversion"
)

// pageChannelCustomerHome 是首版唯一支持的装修渠道：uni-app 客户自助端首页（§16.1）。
const pageChannelCustomerHome = "uni_customer_home"

// pageMaxComponents 是单页组件数量上限，防止配置被无限膨胀。
const pageMaxComponents = 20

// 页面组件文本与图片的运行时校验上限（§16.4）。
const (
	pageTitleLimit    = 60  // title 文本上限（字符）
	pageSubtitleLimit = 120 // subtitle 文本上限（字符）
	pageNameLimit     = 60  // name 文本上限（字符）
	pageImageMinDim   = 1   // 图片尺寸下限（像素）
	pageImageMaxDim   = 4096
)

// 首页装修配置键（§16.4 / §16.5）。
const (
	// pageAllowedDomainsKey 是外部跳转链接的「已审核 HTTPS 域名」白名单（§16.5），逗号分隔；
	// 缺省为空表示不放行任何外部跳转链接（内部跳转使用站内相对路径）。
	pageAllowedDomainsKey = "app.esports.page_allowed_domains"
)

// pageVersionStatusPendingReview 是发布待审核状态。该值需 schema/esports_page.go 的 status
// 枚举补充 "pending_review" 并由主代理统一 go generate 后，StatusValidator 才会接受入库；
// 此处以字符串常量定义，保证在旧生成代码下依然可编译。
const pageVersionStatusPendingReview = esportspageversion.Status("pending_review")

// pageLinkFields 是外部跳转链接字段：仅允许「已审核 HTTPS 域名」或站内相对路径（§16.5）。
var pageLinkFields = map[string]bool{
	"url": true, "link": true, "href": true, "jump": true, "jump_url": true, "target_url": true,
}

// pageImageFields 是素材 / 图片地址字段：仅允许 https:// 或站内相对路径（§16.5）。
var pageImageFields = map[string]bool{
	"image": true, "img": true, "image_url": true, "pic": true, "pic_url": true,
	"banner": true, "icon": true, "avatar": true, "cover": true, "logo": true,
	"src": true, "background": true,
}

// pageComponentTypes 是首版允许的组件类型集合（§16.3）。
var pageComponentTypes = map[string]bool{
	"search": true, "carousel": true, "notice": true, "quick_nav": true,
	"category": true, "product_list": true, "player_recommend": true,
	"activity": true, "member_entry": true, "support_entry": true,
}

// pageComponent 是单个装修组件：类型 + 显隐 + 自由属性。
type pageComponent struct {
	Type    string                 `json:"type"`
	Visible *bool                  `json:"visible"`
	Props   map[string]interface{} `json:"props,omitempty"`
}

// pageConfig 是首页装修的结构化配置。
type pageConfig struct {
	Components []pageComponent `json:"components"`
}

type pageDraftInput struct {
	Components      []pageComponent `json:"components"`
	ExpectedVersion int             `json:"expected_version"`
}

type pagePublishInput struct {
	Mode        string `json:"mode"`
	ScheduledAt string `json:"scheduled_at"`
	Note        string `json:"note"`
}

type pageRollbackInput struct {
	VersionNo int `json:"version_no"`
}

type pageAssetInput struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Mime      string `json:"mime"`
	SizeBytes int64  `json:"size_bytes"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

// defaultPageConfig 是系统默认模板（§16.1）：未装修或配置读取失败时客户端仍可进入
// 商品、订单、我的与联系客服入口。
func defaultPageConfig() pageConfig {
	visible := true
	return pageConfig{Components: []pageComponent{
		{Type: "search", Visible: &visible},
		{Type: "quick_nav", Visible: &visible, Props: map[string]interface{}{
			"items": []interface{}{
				map[string]interface{}{"name": "商品分类", "target": "category"},
				map[string]interface{}{"name": "我的订单", "target": "orders"},
				map[string]interface{}{"name": "储值卡", "target": "prepay_card"},
				map[string]interface{}{"name": "会员", "target": "member"},
				map[string]interface{}{"name": "联系客服", "target": "support"},
			},
		}},
		{Type: "product_list", Visible: &visible},
		{Type: "support_entry", Visible: &visible},
	}}
}

// validatePageConfig 校验发布 / 保存前的配置完整性（§16.4 发布前自动检查）。
// 保持无 ctx / db 的既有签名：不启用域名白名单，供测试与纯静态校验使用。
func validatePageConfig(cfg pageConfig) error {
	return validatePageConfigWithAllowedDomains(cfg, nil, false)
}

// validatePageConfigWithAllowedDomains 校验组件类型、数量上限、必需入口，并递归校验每个组件的
// Props：外部跳转链接、素材图片地址（https / 站内相对路径）、文本超限、图片尺寸范围。
// support_entry 缺失或 visible=false 时拒绝（§16.5）。
// enforceDomain 为 true 时外部跳转链接须命中 allowedDomains「已审核 HTTPS 域名」白名单（发布前校验），
// 为 false 时仅校验链接协议（https 或站内相对路径），供保存草稿放行待审核域名。
func validatePageConfigWithAllowedDomains(cfg pageConfig, allowedDomains map[string]bool, enforceDomain bool) error {
	if len(cfg.Components) < 1 || len(cfg.Components) > pageMaxComponents {
		return fmt.Errorf("%w：首页组件数量须在 1~%d 之间", errInvalidInput, pageMaxComponents)
	}
	hasVisibleSupport := false
	for i, comp := range cfg.Components {
		if !pageComponentTypes[comp.Type] {
			return fmt.Errorf("%w：不支持的组件类型 %q", errInvalidInput, comp.Type)
		}
		if comp.Type == "support_entry" && (comp.Visible == nil || *comp.Visible) {
			hasVisibleSupport = true
		}
		if err := validatePageProps(i, comp.Props, allowedDomains, enforceDomain); err != nil {
			return err
		}
	}
	// 必需入口：客服与订单入口组件同时承载「联系客服」与「我的订单 / 售后」，且不得隐藏。
	if !hasVisibleSupport {
		return fmt.Errorf("%w：首页必须保留可见的「客服与订单入口」组件", errInvalidInput)
	}
	return nil
}

// validatePageProps 递归校验组件自由属性。compIndex 从 0 起，错误信息里以 1 起呈现给运营。
func validatePageProps(compIndex int, props map[string]interface{}, allowedDomains map[string]bool, enforceDomain bool) error {
	for key, value := range props {
		if err := validatePageValue(compIndex, key, value, allowedDomains, enforceDomain); err != nil {
			return err
		}
	}
	return nil
}

func validatePageValue(compIndex int, key string, value interface{}, allowedDomains map[string]bool, enforceDomain bool) error {
	switch v := value.(type) {
	case string:
		switch key {
		case "title":
			if n := len([]rune(v)); n > pageTitleLimit {
				return fmt.Errorf("%w：第 %d 个组件字段 %q 文本超长（%d/%d）", errInvalidInput, compIndex+1, key, n, pageTitleLimit)
			}
		case "subtitle":
			if n := len([]rune(v)); n > pageSubtitleLimit {
				return fmt.Errorf("%w：第 %d 个组件字段 %q 文本超长（%d/%d）", errInvalidInput, compIndex+1, key, n, pageSubtitleLimit)
			}
		case "name":
			if n := len([]rune(v)); n > pageNameLimit {
				return fmt.Errorf("%w：第 %d 个组件字段 %q 文本超长（%d/%d）", errInvalidInput, compIndex+1, key, n, pageNameLimit)
			}
		}
		if pageLinkFields[key] {
			return validatePageLink(v, allowedDomains, enforceDomain, compIndex, key)
		}
		if pageImageFields[key] {
			return validatePageImageURL(v, compIndex, key)
		}
	case float64:
		if key == "width" || key == "height" {
			return validatePageDimension(float64(v), compIndex, key)
		}
	case int:
		if key == "width" || key == "height" {
			return validatePageDimension(float64(v), compIndex, key)
		}
	case map[string]interface{}:
		for k, sub := range v {
			if err := validatePageValue(compIndex, k, sub, allowedDomains, enforceDomain); err != nil {
				return err
			}
		}
	case []interface{}:
		for _, item := range v {
			if err := validatePageValue(compIndex, key, item, allowedDomains, enforceDomain); err != nil {
				return err
			}
		}
	}
	return nil
}

// validatePageLink 校验外部跳转链接（§16.5）：站内相对路径或 https://；
// enforceDomain 为 true 时还须命中「已审核 HTTPS 域名」白名单，白名单为空不放行任何外部链接。
func validatePageLink(raw string, allowedDomains map[string]bool, enforceDomain bool, compIndex int, field string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "/") {
		return nil // 空值跳过；站内相对路径（已注册路由）。
	}
	if !strings.HasPrefix(raw, "https://") {
		return fmt.Errorf("%w：第 %d 个组件字段 %q 的外部跳转链接仅允许 https:// 或站内相对路径", errInvalidInput, compIndex+1, field)
	}
	if !enforceDomain {
		return nil
	}
	host := pageURLHost(raw)
	if host == "" || !allowedDomains[host] {
		return fmt.Errorf("%w：第 %d 个组件字段 %q 的外部跳转域名 %q 未经审核", errInvalidInput, compIndex+1, field, host)
	}
	return nil
}

// validatePageImageURL 校验素材 / 图片地址（§16.5）：https:// 或站内相对路径，不允许 http。
func validatePageImageURL(raw string, compIndex int, field string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "/") {
		return nil // 空值跳过；站内相对路径。
	}
	if !strings.HasPrefix(raw, "https://") {
		return fmt.Errorf("%w：第 %d 个组件字段 %q 的素材地址仅允许 https:// 或站内相对路径", errInvalidInput, compIndex+1, field)
	}
	return nil
}

// validatePageDimension 校验图片尺寸：0 视为未设置，跳过；其余须在 1~4096 像素之间。
func validatePageDimension(v float64, compIndex int, field string) error {
	if v == 0 {
		return nil
	}
	if v < pageImageMinDim || v > pageImageMaxDim {
		return fmt.Errorf("%w：第 %d 个组件字段 %q 图片尺寸须在 %d~%d 之间", errInvalidInput, compIndex+1, field, pageImageMinDim, pageImageMaxDim)
	}
	return nil
}

// pageURLHost 提取外部链接的域名（小写、去端口）。
func pageURLHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// pageAllowedDomains 读取外部跳转链接的「已审核 HTTPS 域名」白名单（§16.5）；
// 缺省为空表示不放行任何外部跳转链接。
func pageAllowedDomains(ctx context.Context, db *ent.Client) map[string]bool {
	raw := assessmentConfigString(ctx, db, pageAllowedDomainsKey, "")
	if raw == "" {
		return nil
	}
	out := make(map[string]bool)
	for _, part := range strings.Split(raw, ",") {
		if host := strings.ToLower(strings.TrimSpace(part)); host != "" {
			out[host] = true
		}
	}
	return out
}

func pageConfigToMap(cfg pageConfig) map[string]interface{} {
	data, _ := json.Marshal(cfg)
	out := map[string]interface{}{}
	_ = json.Unmarshal(data, &out)
	return out
}

func decodePageConfig(raw map[string]interface{}) (pageConfig, error) {
	if len(raw) == 0 {
		return defaultPageConfig(), nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return pageConfig{}, errInvalidInput
	}
	var cfg pageConfig
	if err = json.Unmarshal(data, &cfg); err != nil {
		return pageConfig{}, errInvalidInput
	}
	if len(cfg.Components) == 0 {
		return defaultPageConfig(), nil
	}
	return cfg, nil
}

// savePageDraft 保存首页装修草稿（§16.2）。expectedVersion 为 0 表示首次创建；
// 已存在时以 draft_version 作乐观锁，版本不一致返回冲突，避免多人覆盖。
// ponytail: 保存草稿不影响客户端，需显式发布才生效；保存后文档状态回落 draft，
// 既有待发布的定时版本仍保留并由系统任务到点激活。
func savePageDraft(ctx context.Context, db *ent.Client, channel string, adminID int, in pageDraftInput) (*ent.EsportsPageDocument, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" || len(channel) > 48 {
		return nil, errInvalidInput
	}
	cfg := pageConfig{Components: in.Components}
	if err := validatePageConfigWithAllowedDomains(cfg, pageAllowedDomains(ctx, db), false); err != nil {
		return nil, err
	}
	config := pageConfigToMap(cfg)
	doc, err := db.EsportsPageDocument.Query().Where(esportspagedocument.ChannelEQ(channel)).Only(ctx)
	if ent.IsNotFound(err) {
		if in.ExpectedVersion != 0 {
			return nil, errConflict
		}
		return db.EsportsPageDocument.Create().SetChannel(channel).SetDraftConfig(config).
			SetDraftVersion(1).SetStatus(esportspagedocument.StatusDraft).SetUpdatedBy(adminID).Save(ctx)
	}
	if err != nil {
		return nil, err
	}
	if in.ExpectedVersion != doc.DraftVersion {
		return nil, errConflict
	}
	changed, err := db.EsportsPageDocument.Update().Where(
		esportspagedocument.IDEQ(doc.ID), esportspagedocument.DraftVersionEQ(doc.DraftVersion),
	).SetDraftConfig(config).SetStatus(esportspagedocument.StatusDraft).
		SetUpdatedBy(adminID).AddDraftVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsPageDocument.Get(ctx, doc.ID)
}

// nextPageVersionNo 返回渠道当前最大版本号（无版本返回 0）。
func nextPageVersionNo(ctx context.Context, db *ent.Client, documentID int) (int, error) {
	last, err := db.EsportsPageVersion.Query().
		Where(esportspageversion.DocumentIDEQ(documentID)).
		Order(ent.Desc(esportspageversion.FieldVersionNo)).First(ctx)
	if ent.IsNotFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return last.VersionNo, nil
}

// publishPage 提交发布申请（§16.4 / §16.5）。运营编辑提交后一律落 pending_review 待审核，
// 由有发布权限且非申请人的管理员审核（reviewPagePublish）后才切换生效：
// immediate 审核通过即生效，scheduled 审核通过后转 scheduled 待系统任务到点激活。
// 提交前执行发布校验（组件完整性、已审核 HTTPS 域名、可见的客服与订单入口）。
func publishPage(ctx context.Context, db *ent.Client, channel string, adminID int, in pagePublishInput, now time.Time) (*ent.EsportsPageVersion, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" || len(channel) > 48 {
		return nil, errInvalidInput
	}
	in.Note = strings.TrimSpace(in.Note)
	if len([]rune(in.Note)) > 255 {
		return nil, errInvalidInput
	}
	mode := strings.TrimSpace(in.Mode)
	if mode == "" {
		mode = "immediate"
	}
	if mode != "immediate" && mode != "scheduled" {
		return nil, errInvalidInput
	}
	var scheduledAt time.Time
	if mode == "scheduled" {
		parsed, err := parseReportTime(strings.TrimSpace(in.ScheduledAt))
		if err != nil || parsed.IsZero() || !parsed.After(now.UTC()) {
			return nil, fmt.Errorf("%w：定时发布时间必须晚于当前时间", errInvalidInput)
		}
		scheduledAt = parsed
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	doc, err := tx.EsportsPageDocument.Query().Where(esportspagedocument.ChannelEQ(channel)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, fmt.Errorf("%w：请先保存草稿再发布", errInvalidInput)
	}
	if err != nil {
		return nil, err
	}
	cfg, err := decodePageConfig(doc.DraftConfig)
	if err != nil {
		return nil, err
	}
	if err = validatePageConfigWithAllowedDomains(cfg, pageAllowedDomains(ctx, db), true); err != nil {
		return nil, err
	}
	// 发布前资源有效性校验（§16.4 增量）：图片可访问、跳转链接合法、商品/活动有效、文本超限。
	if err = validatePageResources(ctx, db, cfg); err != nil {
		return nil, err
	}
	maxNo, err := nextPageVersionNo(ctx, tx.Client(), doc.ID)
	if err != nil {
		return nil, err
	}
	create := tx.EsportsPageVersion.Create().SetDocumentID(doc.ID).SetChannel(channel).
		SetVersionNo(maxNo + 1).SetConfig(pageConfigToMap(cfg)).
		SetPublishedBy(adminID).SetNote(in.Note).
		SetStatus(pageVersionStatusPendingReview)
	if mode == "scheduled" {
		create.SetPublishMode(esportspageversion.PublishModeScheduled).SetScheduledAt(scheduledAt)
	} else {
		create.SetPublishMode(esportspageversion.PublishModeImmediate)
	}
	version, err := create.Save(ctx)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return version, nil
}

// pagePublishReviewInput 发布审核输入（§16.4）：Approved 由路由路径决定（approve/reject），
// Reason 为驳回原因（驳回必填）。
type pagePublishReviewInput struct {
	Approved bool   `json:"approved"`
	Reason   string `json:"reason"`
}

// reviewPagePublish 审核一条待发布版本（§16.4 / §16.5）。审核人不得为发布申请人；
// 通过后按 publish_mode 切换生效：immediate 一次性版本切换（旧生效版本转历史、更新文档生效指针），
// scheduled 转 scheduled 待系统任务到点激活；驳回则转历史不生效。
func reviewPagePublish(ctx context.Context, db *ent.Client, channel string, versionID, adminID int, in pagePublishReviewInput, now time.Time) (*ent.EsportsPageVersion, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" || versionID < 1 {
		return nil, errInvalidInput
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if !in.Approved && in.Reason == "" {
		return nil, fmt.Errorf("%w：驳回须填写原因", errInvalidInput)
	}
	if len([]rune(in.Reason)) > 255 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	version, err := tx.EsportsPageVersion.Get(ctx, versionID)
	if err != nil {
		return nil, err
	}
	if version.Channel != channel || version.Status != pageVersionStatusPendingReview {
		return nil, errConflict
	}
	if version.PublishedBy == adminID {
		return nil, fmt.Errorf("%w：审核人不得为发布申请人", errForbidden)
	}
	if in.Approved {
		if version.PublishMode == esportspageversion.PublishModeScheduled {
			// 定时发布：通过后转 scheduled 待系统任务到点激活，文档转 pending。
			changed, err := tx.EsportsPageVersion.Update().Where(
				esportspageversion.IDEQ(versionID),
				esportspageversion.StatusEQ(pageVersionStatusPendingReview),
			).SetStatus(esportspageversion.StatusScheduled).
				SetNote(pageReviewNote(version.Note, adminID, "已通过")).Save(ctx)
			if err != nil {
				return nil, err
			}
			if changed != 1 {
				return nil, errConflict
			}
			if _, err = tx.EsportsPageDocument.Update().Where(esportspagedocument.IDEQ(version.DocumentID)).
				SetStatus(esportspagedocument.StatusPending).SetUpdatedBy(adminID).Save(ctx); err != nil {
				return nil, err
			}
		} else {
			if _, err = tx.EsportsPageVersion.Update().Where(
				esportspageversion.DocumentIDEQ(version.DocumentID),
				esportspageversion.StatusEQ(esportspageversion.StatusPublished),
			).SetStatus(esportspageversion.StatusHistory).Save(ctx); err != nil {
				return nil, err
			}
			changed, err := tx.EsportsPageVersion.Update().Where(
				esportspageversion.IDEQ(versionID),
				esportspageversion.StatusEQ(pageVersionStatusPendingReview),
			).SetStatus(esportspageversion.StatusPublished).SetPublishedAt(now.UTC()).
				SetNote(pageReviewNote(version.Note, adminID, "已通过")).Save(ctx)
			if err != nil {
				return nil, err
			}
			if changed != 1 {
				return nil, errConflict
			}
			if _, err = tx.EsportsPageDocument.Update().Where(esportspagedocument.IDEQ(version.DocumentID)).
				SetPublishedVersionNo(version.VersionNo).SetStatus(esportspagedocument.StatusPublished).
				SetUpdatedBy(adminID).Save(ctx); err != nil {
				return nil, err
			}
		}
	} else {
		changed, err := tx.EsportsPageVersion.Update().Where(
			esportspageversion.IDEQ(versionID),
			esportspageversion.StatusEQ(pageVersionStatusPendingReview),
		).SetStatus(esportspageversion.StatusHistory).
			SetNote(pageReviewNote(version.Note, adminID, "已驳回："+in.Reason)).Save(ctx)
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, errConflict
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return db.EsportsPageVersion.Get(ctx, versionID)
}

// pageReviewNote 在版本备注后追加审核痕迹，保证不超过 note 字段 255 字符上限。
func pageReviewNote(base string, adminID int, action string) string {
	suffix := fmt.Sprintf("；审核人 %d：%s", adminID, action)
	combined := []rune(base + suffix)
	if len(combined) > 255 {
		combined = combined[len(combined)-255:]
	}
	return string(combined)
}

// rollbackPage 提交回滚申请（§16.4 / §16.5）：以历史快照为模板生成一条待审核版本，
// 不原地修改旧记录；审核通过后重新发布为新版本（不恢复历史版本状态）。
// 回滚目标快照同样执行发布校验（已审核 HTTPS 域名、可见的客服与订单入口）。
func rollbackPage(ctx context.Context, db *ent.Client, channel string, adminID, versionNo int, now time.Time) (*ent.EsportsPageVersion, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" || versionNo < 1 {
		return nil, errInvalidInput
	}
	tx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	doc, err := tx.EsportsPageDocument.Query().Where(esportspagedocument.ChannelEQ(channel)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, fmt.Errorf("%w：渠道尚未装修", errInvalidInput)
	}
	if err != nil {
		return nil, err
	}
	source, err := tx.EsportsPageVersion.Query().Where(
		esportspageversion.DocumentIDEQ(doc.ID), esportspageversion.VersionNoEQ(versionNo),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, fmt.Errorf("%w：目标版本不存在", errInvalidInput)
	}
	if err != nil {
		return nil, err
	}
	sourceCfg, err := decodePageConfig(source.Config)
	if err != nil {
		return nil, err
	}
	if err = validatePageConfigWithAllowedDomains(sourceCfg, pageAllowedDomains(ctx, db), true); err != nil {
		return nil, err
	}
	// 回滚同样执行发布前资源有效性校验（§16.4 增量）。
	if err = validatePageResources(ctx, db, sourceCfg); err != nil {
		return nil, err
	}
	maxNo, err := nextPageVersionNo(ctx, tx.Client(), doc.ID)
	if err != nil {
		return nil, err
	}
	version, err := tx.EsportsPageVersion.Create().SetDocumentID(doc.ID).SetChannel(channel).
		SetVersionNo(maxNo + 1).SetConfig(source.Config).
		SetStatus(pageVersionStatusPendingReview).SetPublishMode(esportspageversion.PublishModeImmediate).
		SetPublishedBy(adminID).SetRollbackFromVersionNo(versionNo).
		SetNote(fmt.Sprintf("回滚至版本 %d（待审核）", versionNo)).Save(ctx)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return version, nil
}

func listPageVersions(ctx context.Context, db *ent.Client, channel string, limit int) ([]*ent.EsportsPageVersion, error) {
	doc, err := db.EsportsPageDocument.Query().Where(esportspagedocument.ChannelEQ(strings.TrimSpace(channel))).Only(ctx)
	if ent.IsNotFound(err) {
		return []*ent.EsportsPageVersion{}, nil
	}
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return db.EsportsPageVersion.Query().Where(esportspageversion.DocumentIDEQ(doc.ID)).
		Order(ent.Desc(esportspageversion.FieldVersionNo)).Limit(limit).All(ctx)
}

// publishedPageConfig 是客户端读取入口（§16.4）：返回当前生效快照，未发布或读取失败时回落系统默认模板。
func publishedPageConfig(ctx context.Context, db *ent.Client, channel string) (map[string]interface{}, int, error) {
	doc, err := db.EsportsPageDocument.Query().Where(esportspagedocument.ChannelEQ(strings.TrimSpace(channel))).Only(ctx)
	if ent.IsNotFound(err) {
		return pageConfigToMap(defaultPageConfig()), 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	if doc.PublishedVersionNo < 1 {
		return pageConfigToMap(defaultPageConfig()), 0, nil
	}
	version, err := db.EsportsPageVersion.Query().Where(
		esportspageversion.DocumentIDEQ(doc.ID), esportspageversion.VersionNoEQ(doc.PublishedVersionNo),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return pageConfigToMap(defaultPageConfig()), 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	return version.Config, version.VersionNo, nil
}

func savePageAsset(ctx context.Context, db *ent.Client, adminID int, in pageAssetInput) (*ent.EsportsPageAsset, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.URL = strings.TrimSpace(in.URL)
	in.Mime = strings.TrimSpace(in.Mime)
	if in.Name == "" || len([]rune(in.Name)) > 160 || in.URL == "" || len(in.URL) > 512 ||
		len(in.Mime) > 80 || in.SizeBytes < 0 || in.Width < 0 || in.Height < 0 {
		return nil, errInvalidInput
	}
	// 素材是运营上传的图片/资源，外部地址强制 HTTPS，内部跳转使用站内相对路径。
	// 与首页组件的「链接跳转」口径（http/https 均可）区分：素材落库须可安全访问。
	if !strings.HasPrefix(in.URL, "https://") && !strings.HasPrefix(in.URL, "/") {
		return nil, fmt.Errorf("%w：素材地址仅允许 HTTPS 链接或站内相对路径", errInvalidInput)
	}
	return db.EsportsPageAsset.Create().SetName(in.Name).SetURL(in.URL).SetMime(in.Mime).
		SetSizeBytes(in.SizeBytes).SetWidth(in.Width).SetHeight(in.Height).
		SetCreatedBy(adminID).Save(ctx)
}

func listPageAssets(ctx context.Context, db *ent.Client, limit int) ([]*ent.EsportsPageAsset, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	return db.EsportsPageAsset.Query().Order(ent.Desc(esportspageasset.FieldID)).Limit(limit).All(ctx)
}

func deletePageAsset(ctx context.Context, db *ent.Client, id int) error {
	if id < 1 {
		return errInvalidInput
	}
	changed, err := db.EsportsPageAsset.Delete().Where(esportspageasset.IDEQ(id)).Exec(ctx)
	if err != nil {
		return err
	}
	if changed != 1 {
		return &ent.NotFoundError{}
	}
	return nil
}

// activateScheduledPage 把一条到点的定时版本切为生效；返回是否完成切换。
func activateScheduledPage(ctx context.Context, db *ent.Client, versionID int, now time.Time) (bool, error) {
	tx, err := db.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	version, err := tx.EsportsPageVersion.Get(ctx, versionID)
	if err != nil {
		return false, err
	}
	if _, err = tx.EsportsPageVersion.Update().Where(
		esportspageversion.DocumentIDEQ(version.DocumentID),
		esportspageversion.StatusEQ(esportspageversion.StatusPublished),
	).SetStatus(esportspageversion.StatusHistory).Save(ctx); err != nil {
		return false, err
	}
	changed, err := tx.EsportsPageVersion.Update().Where(
		esportspageversion.IDEQ(versionID), esportspageversion.StatusEQ(esportspageversion.StatusScheduled),
	).SetStatus(esportspageversion.StatusPublished).SetPublishedAt(now.UTC()).Save(ctx)
	if err != nil {
		return false, err
	}
	if changed != 1 {
		// 已被其他任务激活，回滚本次事务。
		return false, nil
	}
	if _, err = tx.EsportsPageDocument.Update().Where(esportspagedocument.IDEQ(version.DocumentID)).
		SetPublishedVersionNo(version.VersionNo).SetStatus(esportspagedocument.StatusPublished).Save(ctx); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// publishScheduledPages 是定时发布任务（§16.4）：激活所有到点且状态仍为 scheduled 的版本。
func publishScheduledPages(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	versions, err := db.EsportsPageVersion.Query().Where(
		esportspageversion.StatusEQ(esportspageversion.StatusScheduled),
		esportspageversion.ScheduledAtLTE(now.UTC()),
	).Order(ent.Asc(esportspageversion.FieldVersionNo)).Limit(limit).All(ctx)
	if err != nil {
		return 0, err
	}
	activated := 0
	for _, version := range versions {
		done, err := activateScheduledPage(ctx, db, version.ID, now)
		if err != nil {
			return activated, err
		}
		if done {
			activated++
		}
	}
	return activated, nil
}
