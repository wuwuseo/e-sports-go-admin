package esports

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsproduct"
	"go-admin/internal/ent/esportsrechargecampaign"
	"go-admin/internal/ent/esportssku"
)

// 发布前资源有效性校验（§16.4 增量补充）：在既有域名白名单与 support_entry 校验之后执行，
// 不改动、不回退既有校验链（validatePageConfigWithAllowedDomains）。

// pageGenericTextLimit 是组件属性中任意字符串字段的通用长度上限（字符）；
// title/subtitle/name 已有更严的专用上限（page.go），此处兜底其余文本字段（如公告 text）。
const pageGenericTextLimit = 500

// pageImageProbeTimeout 是图片 URL 可访问性探测的超时。
const pageImageProbeTimeout = 5 * time.Second

// pageProbeImage 探测外部图片 URL 可访问性（§16.4）：默认 HEAD→GET，失败视为不可访问。
// 以包级变量暴露，便于测试中替换为桩函数，避免单元测试发起真实网络请求。
var pageProbeImage = func(ctx context.Context, rawURL string) error {
	return probePageImageURLWithClient(ctx, &http.Client{Timeout: pageImageProbeTimeout}, rawURL)
}

// 指向商品 / 活动的组件属性键（§16.4）：自由 props 中引用这些键时，发布前校验其有效性。
var (
	pageSKUFields      = map[string]bool{"sku_id": true, "sku_ids": true}
	pageProductFields  = map[string]bool{"product_id": true, "product_ids": true}
	pageCampaignFields = map[string]bool{"campaign_id": true, "campaign_ids": true, "activity_id": true, "activity_ids": true}
)

// validatePageResources 发布前校验资源有效性（§16.4 增量）：图片 URL 可访问、跳转链接合法性、
// 商品/活动有效性、文本长度超限。additive：应在域名白名单校验通过后调用。
func validatePageResources(ctx context.Context, db *ent.Client, cfg pageConfig) error {
	for i, comp := range cfg.Components {
		if err := validatePageResourceProps(ctx, db, i, comp.Props); err != nil {
			return err
		}
	}
	return nil
}

// validatePageResourceProps 递归校验单个组件的自由属性。
func validatePageResourceProps(ctx context.Context, db *ent.Client, compIndex int, props map[string]interface{}) error {
	for key, value := range props {
		if err := validatePageResourceValue(ctx, db, compIndex, key, value); err != nil {
			return err
		}
	}
	return nil
}

func validatePageResourceValue(ctx context.Context, db *ent.Client, compIndex int, key string, value interface{}) error {
	switch v := value.(type) {
	case string:
		// 通用文本长度兜底（专用上限 title/subtitle/name 已在 validatePageValue 校验）。
		if n := len([]rune(v)); n > pageGenericTextLimit {
			return fmt.Errorf("%w：第 %d 个组件字段 %q 文本超长（%d/%d）", errInvalidInput, compIndex+1, key, n, pageGenericTextLimit)
		}
		if pageImageFields[key] && strings.HasPrefix(strings.TrimSpace(v), "https://") {
			if err := pageProbeImage(ctx, strings.TrimSpace(v)); err != nil {
				return fmt.Errorf("%w：第 %d 个组件字段 %q 的图片不可访问（%s）：%v", errInvalidInput, compIndex+1, key, v, err)
			}
		}
		if pageLinkFields[key] {
			if err := validatePageLinkSafety(v, compIndex, key); err != nil {
				return err
			}
		}
		if id, ok := pageEntityFieldID(key, v); ok {
			return pageCheckEntity(ctx, db, key, id, compIndex)
		}
	case float64:
		if id, ok := pageEntityFieldID(key, v); ok {
			return pageCheckEntity(ctx, db, key, id, compIndex)
		}
	case int:
		if id, ok := pageEntityFieldID(key, v); ok {
			return pageCheckEntity(ctx, db, key, id, compIndex)
		}
	case map[string]interface{}:
		for k, sub := range v {
			if err := validatePageResourceValue(ctx, db, compIndex, k, sub); err != nil {
				return err
			}
		}
	case []interface{}:
		// 实体引用列表（sku_ids / product_ids / activity_ids 等）逐项校验。
		if pageSKUFields[key] || pageProductFields[key] || pageCampaignFields[key] {
			for _, item := range v {
				id, ok := pageEntityFieldID(key, item)
				if !ok {
					return fmt.Errorf("%w：第 %d 个组件字段 %q 含无效的 ID", errInvalidInput, compIndex+1, key)
				}
				if err := pageCheckEntity(ctx, db, key, id, compIndex); err != nil {
					return err
				}
			}
			return nil
		}
		for _, item := range v {
			if err := validatePageResourceValue(ctx, db, compIndex, key, item); err != nil {
				return err
			}
		}
	}
	return nil
}

// pageEntityFieldID 从属性值中解析出实体 ID（数字或数字字符串）；非实体字段返回 ok=false。
func pageEntityFieldID(key string, value interface{}) (int, bool) {
	if !pageSKUFields[key] && !pageProductFields[key] && !pageCampaignFields[key] {
		return 0, false
	}
	switch v := value.(type) {
	case float64:
		if v < 0 || v != float64(int(v)) {
			return 0, false
		}
		return int(v), true
	case int:
		if v < 0 {
			return 0, false
		}
		return v, true
	case string:
		id, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || id < 0 {
			return 0, false
		}
		return id, true
	default:
		return 0, false
	}
}

// pageCheckEntity 校验商品 / 活动引用有效（§16.4）：须存在且处于可售卖 / 进行中状态。
func pageCheckEntity(ctx context.Context, db *ent.Client, key string, id, compIndex int) error {
	var err error
	switch key {
	case "sku_id", "sku_ids":
		err = pageCheckSKU(ctx, db, id)
	case "product_id", "product_ids":
		err = pageCheckProduct(ctx, db, id)
	case "campaign_id", "campaign_ids", "activity_id", "activity_ids":
		err = pageCheckCampaign(ctx, db, id)
	}
	if err == nil {
		return nil
	}
	if ent.IsNotFound(err) {
		return fmt.Errorf("%w：第 %d 个组件字段 %q 引用的资源 %d 不存在或未生效", errInvalidInput, compIndex+1, key, id)
	}
	return err
}

func pageCheckSKU(ctx context.Context, db *ent.Client, id int) error {
	_, err := db.EsportsSku.Query().Where(esportssku.IDEQ(id), esportssku.StatusEQ(esportssku.StatusOnSale)).Only(ctx)
	return err
}

func pageCheckProduct(ctx context.Context, db *ent.Client, id int) error {
	_, err := db.EsportsProduct.Query().Where(esportsproduct.IDEQ(id), esportsproduct.StatusEQ(esportsproduct.StatusOnSale)).Only(ctx)
	return err
}

func pageCheckCampaign(ctx context.Context, db *ent.Client, id int) error {
	_, err := db.EsportsRechargeCampaign.Query().Where(
		esportsrechargecampaign.IDEQ(id), esportsrechargecampaign.StatusEQ(esportsrechargecampaign.StatusActive),
	).Only(ctx)
	return err
}

// validatePageLinkSafety 校验跳转链接合法性（§16.4 增量）：站内相对路径拒绝协议相对 / 路径穿越 /
// 控制字符；外部链接须为可解析的 https 且不含凭证信息。
func validatePageLinkSafety(raw string, compIndex int, field string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "/") {
		if strings.HasPrefix(raw, "//") || strings.Contains(raw, "..") || strings.ContainsAny(raw, "\r\n\t ") {
			return fmt.Errorf("%w：第 %d 个组件字段 %q 的站内跳转路径非法", errInvalidInput, compIndex+1, field)
		}
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%w：第 %d 个组件字段 %q 的跳转链接不合法", errInvalidInput, compIndex+1, field)
	}
	if u.User != nil {
		return fmt.Errorf("%w：第 %d 个组件字段 %q 的跳转链接不得包含凭证信息", errInvalidInput, compIndex+1, field)
	}
	return nil
}

// probePageImageURLWithClient 对外部图片 URL 先 HEAD 探测，失败回退 GET（部分 CDN 不支持 HEAD），
// 2xx/3xx 视为可访问。
func probePageImageURLWithClient(ctx context.Context, client *http.Client, rawURL string) error {
	if client == nil {
		client = &http.Client{Timeout: pageImageProbeTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			return nil
		}
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err = client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
