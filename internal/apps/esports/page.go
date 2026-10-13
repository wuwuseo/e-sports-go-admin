package esports

import (
	"context"
	"encoding/json"
	"fmt"
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
// ponytail: 仅校验组件类型、数量上限与必需入口；图片可访问性、链接合法性、外部域名白名单、
// 商品 / 活动有效性、素材尺寸与文本超限等运行时校验未实现。
func validatePageConfig(cfg pageConfig) error {
	if len(cfg.Components) < 1 || len(cfg.Components) > pageMaxComponents {
		return fmt.Errorf("%w：首页组件数量须在 1~%d 之间", errInvalidInput, pageMaxComponents)
	}
	hasSupport := false
	for _, comp := range cfg.Components {
		if !pageComponentTypes[comp.Type] {
			return fmt.Errorf("%w：不支持的组件类型 %q", errInvalidInput, comp.Type)
		}
		if comp.Type == "support_entry" {
			hasSupport = true
		}
	}
	// 必需入口：客服与订单入口组件同时承载「联系客服」与「我的订单 / 售后」。
	if !hasSupport {
		return fmt.Errorf("%w：首页必须保留「客服与订单入口」组件", errInvalidInput)
	}
	return nil
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
	if err := validatePageConfig(cfg); err != nil {
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

// publishPage 发布首页草稿（§16.4）。一次性版本切换：生成不可变快照并把旧生效版本转历史，
// 同渠道同一时刻只生效一版。mode=scheduled 时先落 scheduled 状态待系统任务到点激活。
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
	if err = validatePageConfig(cfg); err != nil {
		return nil, err
	}
	maxNo, err := nextPageVersionNo(ctx, tx.Client(), doc.ID)
	if err != nil {
		return nil, err
	}
	create := tx.EsportsPageVersion.Create().SetDocumentID(doc.ID).SetChannel(channel).
		SetVersionNo(maxNo + 1).SetConfig(pageConfigToMap(cfg)).
		SetPublishedBy(adminID).SetNote(in.Note)
	if mode == "immediate" {
		create.SetStatus(esportspageversion.StatusPublished).
			SetPublishMode(esportspageversion.PublishModeImmediate).SetPublishedAt(now.UTC())
		if _, err = tx.EsportsPageVersion.Update().Where(
			esportspageversion.DocumentIDEQ(doc.ID), esportspageversion.StatusEQ(esportspageversion.StatusPublished),
		).SetStatus(esportspageversion.StatusHistory).Save(ctx); err != nil {
			return nil, err
		}
		if _, err = tx.EsportsPageDocument.UpdateOne(doc).
			SetPublishedVersionNo(maxNo + 1).SetStatus(esportspagedocument.StatusPublished).
			SetUpdatedBy(adminID).Save(ctx); err != nil {
			return nil, err
		}
	} else {
		create.SetStatus(esportspageversion.StatusScheduled).
			SetPublishMode(esportspageversion.PublishModeScheduled).SetScheduledAt(scheduledAt)
		if _, err = tx.EsportsPageDocument.UpdateOne(doc).
			SetStatus(esportspagedocument.StatusPending).SetUpdatedBy(adminID).Save(ctx); err != nil {
			return nil, err
		}
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

// rollbackPage 回滚（§16.4）：以历史快照为模板重新发布为新版本，不原地修改旧记录。
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
	maxNo, err := nextPageVersionNo(ctx, tx.Client(), doc.ID)
	if err != nil {
		return nil, err
	}
	if _, err = tx.EsportsPageVersion.Update().Where(
		esportspageversion.DocumentIDEQ(doc.ID), esportspageversion.StatusEQ(esportspageversion.StatusPublished),
	).SetStatus(esportspageversion.StatusHistory).Save(ctx); err != nil {
		return nil, err
	}
	version, err := tx.EsportsPageVersion.Create().SetDocumentID(doc.ID).SetChannel(channel).
		SetVersionNo(maxNo + 1).SetConfig(source.Config).
		SetStatus(esportspageversion.StatusPublished).SetPublishMode(esportspageversion.PublishModeImmediate).
		SetPublishedAt(now.UTC()).SetPublishedBy(adminID).
		SetRollbackFromVersionNo(versionNo).
		SetNote(fmt.Sprintf("回滚至版本 %d", versionNo)).Save(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.EsportsPageDocument.UpdateOne(doc).
		SetPublishedVersionNo(maxNo + 1).SetStatus(esportspagedocument.StatusPublished).
		SetUpdatedBy(adminID).Save(ctx); err != nil {
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
	// 外部链接仅允许 HTTPS，内部跳转使用站内相对路径。
	// ponytail: 未做已审核域名白名单强校验，仅限制协议与长度。
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
