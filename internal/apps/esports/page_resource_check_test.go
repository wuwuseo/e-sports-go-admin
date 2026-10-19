package esports

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go-admin/internal/apps/esports/schema"
	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsrechargecampaign"
	"go-admin/internal/ent/esportssku"
)

// 单元测试不发起真实网络请求：默认用桩函数替换图片探测。
// 需要在真实探测语义下测试时，测试内再显式覆盖 pageProbeImage 并在结束时恢复。
func init() {
	pageProbeImage = func(context.Context, string) error { return nil }
}

// 恢复默认真实探测（用于单个测试用例局部控制探测结果）。
func withPageProbe(t *testing.T, probe func(ctx context.Context, rawURL string) error) {
	t.Helper()
	original := pageProbeImage
	pageProbeImage = probe
	t.Cleanup(func() { pageProbeImage = original })
}

// seedOnSaleSKU 创建一个上架中的 SKU。
func seedOnSaleSKU(t *testing.T, db *ent.Client, code string) *ent.EsportsSku {
	t.Helper()
	return db.EsportsSku.Create().SetCode(code).SetTitle("上架商品").
		SetServiceType("play").SetUnitPriceCents(1000).SetServiceUnits(1).
		SetServiceUnitName("小时").SetStatus(esportssku.StatusOnSale).SaveX(t.Context())
}

// seedActiveCampaign 创建一个进行中的充值活动。
func seedActiveCampaign(t *testing.T, db *ent.Client, code string) *ent.EsportsRechargeCampaign {
	t.Helper()
	now := time.Now().UTC()
	return db.EsportsRechargeCampaign.Create().SetCode(code).SetName("进行中活动").
		SetActivityType(esportsrechargecampaign.ActivityTypeFullGift).
		SetTiers([]schema.CampaignTier{{ThresholdCents: 1000, BonusCents: 100}}).
		SetAudience(esportsrechargecampaign.AudienceAll).
		SetStackRule(esportsrechargecampaign.StackRuleExclusive).
		SetBonusValidDays(30).SetStartsAt(now).SetEndsAt(now.Add(time.Hour)).
		SetStatus(esportsrechargecampaign.StatusActive).SaveX(t.Context())
}

// 图片不可访问时发布前资源校验被拒（§16.4 增量）。
func TestPageResourceCheckImageUnreachable(t *testing.T) {
	db := openPageDB(t, "esports_page_res_image")
	// P0-17：探测只对已审核域名发起，因此先把该域名登记进白名单，再断言探测结果。
	setPageAllowedDomains(t, db, "broken.example.com")
	withPageProbe(t, func(_ context.Context, rawURL string) error {
		if strings.Contains(rawURL, "broken") {
			return errors.New("connection refused")
		}
		return nil
	})
	visible := true
	cfg := pageConfig{Components: []pageComponent{
		{Type: "search", Visible: &visible},
		{Type: "carousel", Visible: &visible, Props: map[string]interface{}{
			"items": []interface{}{map[string]interface{}{"image": "https://broken.example.com/a.png"}},
		}},
		{Type: "support_entry", Visible: &visible},
	}}
	if err := validatePageResources(t.Context(), db, cfg); !errors.Is(err, errInvalidInput) {
		t.Fatalf("不可访问图片应被拒，实际：%v", err)
	}
}

// 跳转链接合法性：站内路径穿越、外部链接携带凭证信息均被拒（§16.4 增量）。
func TestPageResourceCheckLinkSafety(t *testing.T) {
	db := openPageDB(t, "esports_page_res_link")
	visible := true
	for _, link := range []string{
		"/../etc/passwd",
		"https://user:pass@example.com/x",
	} {
		cfg := pageConfig{Components: []pageComponent{
			{Type: "search", Visible: &visible},
			{Type: "carousel", Visible: &visible, Props: map[string]interface{}{
				"items": []interface{}{map[string]interface{}{"image": "/a.png", "link": link}},
			}},
			{Type: "support_entry", Visible: &visible},
		}}
		if err := validatePageResources(t.Context(), db, cfg); !errors.Is(err, errInvalidInput) {
			t.Fatalf("非法跳转链接 %q 应被拒，实际：%v", link, err)
		}
	}
}

// 通用文本长度超限（§16.4 增量）：公告 text 超过 500 字符被拒。
func TestPageResourceCheckTextTooLong(t *testing.T) {
	db := openPageDB(t, "esports_page_res_text")
	visible := true
	cfg := pageConfig{Components: []pageComponent{
		{Type: "search", Visible: &visible},
		{Type: "notice", Visible: &visible, Props: map[string]interface{}{"text": strings.Repeat("长", 501)}},
		{Type: "support_entry", Visible: &visible},
	}}
	if err := validatePageResources(t.Context(), db, cfg); !errors.Is(err, errInvalidInput) {
		t.Fatalf("超长文本应被拒，实际：%v", err)
	}
}

// 商品 / 活动有效性（§16.4 增量）：不存在或未生效（非上架 / 非进行中）被拒。
func TestPageResourceCheckEntityValidity(t *testing.T) {
	db := openPageDB(t, "esports_page_res_entity")
	visible := true
	sku := seedOnSaleSKU(t, db, "SKU-ON")
	camp := seedActiveCampaign(t, db, "CAMP-ON")

	build := func(props map[string]interface{}) pageConfig {
		return pageConfig{Components: []pageComponent{
			{Type: "search", Visible: &visible},
			{Type: "product_list", Visible: &visible, Props: props},
			// §16.3-06（本轮口径收紧）：活动引用只属于 activity 组件，
			// product_list 不再受理 campaign_id / activity_id（保存即 400）。
			{Type: "activity", Visible: &visible, Props: map[string]interface{}{"campaign_id": camp.ID}},
			{Type: "support_entry", Visible: &visible},
		}}
	}
	// 有效引用：上架 SKU（商品列表）+ 进行中活动（活动组件）。
	if err := validatePageResources(t.Context(), db, build(map[string]interface{}{
		"sku_id": sku.ID,
	})); err != nil {
		t.Fatalf("有效实体引用应通过：%v", err)
	}
	// 不存在的 SKU。
	if err := validatePageResources(t.Context(), db, build(map[string]interface{}{
		"sku_id": 999999,
	})); !errors.Is(err, errInvalidInput) {
		t.Fatalf("不存在的 SKU 应被拒，实际：%v", err)
	}
	// 存在但未生效（下架 SKU）。
	offSku := db.EsportsSku.Create().SetCode("SKU-OFF").SetTitle("下架商品").
		SetServiceType("play").SetUnitPriceCents(1000).SetServiceUnits(1).
		SetServiceUnitName("小时").SetStatus(esportssku.StatusOffSale).SaveX(t.Context())
	if err := validatePageResources(t.Context(), db, build(map[string]interface{}{
		"sku_id": offSku.ID,
	})); !errors.Is(err, errInvalidInput) {
		t.Fatalf("下架 SKU 应被拒，实际：%v", err)
	}
}

// 发布前资源校验接入 publishPage（§16.4 增量）：引用不存在资源被拒，有效资源通过。
func TestPagePublishResourceValidityWired(t *testing.T) {
	db := openPageDB(t, "esports_page_res_publish")
	channel := pageChannelCustomerHome
	now := time.Now().UTC()
	sku := seedOnSaleSKU(t, db, "SKU-PUB")

	withSku := func(id int) []pageComponent {
		visible := true
		return []pageComponent{
			{Type: "search", Visible: &visible},
			{Type: "product_list", Visible: &visible, Props: map[string]interface{}{"sku_id": id}},
			{Type: "support_entry", Visible: &visible},
		}
	}
	// 草稿引用不存在的 SKU：保存草稿放行（资源校验仅发布前），发布被拒。
	doc, err := savePageDraft(t.Context(), db, channel, 1, pageDraftInput{
		Components: withSku(999999), ExpectedVersion: 0,
	})
	if err != nil {
		t.Fatalf("保存草稿失败：%v", err)
	}
	if _, err = publishPage(t.Context(), db, channel, 1, pagePublishInput{Mode: "immediate"}, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("引用不存在 SKU 发布应被拒，实际：%v", err)
	}

	// 改为有效 SKU 后发布通过。
	if _, err = savePageDraft(t.Context(), db, channel, 1, pageDraftInput{
		Components: withSku(sku.ID), ExpectedVersion: doc.DraftVersion,
	}); err != nil {
		t.Fatalf("更新草稿失败：%v", err)
	}
	if _, err = publishPage(t.Context(), db, channel, 1, pagePublishInput{Mode: "immediate"}, now); err != nil {
		t.Fatalf("有效 SKU 引用发布应通过：%v", err)
	}
}
