package esports

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportspageasset"
	"go-admin/internal/ent/esportspagedocument"
	"go-admin/internal/ent/esportspageversion"
)

// pageChannelCustomerHome 是首版唯一支持的装修渠道：uni-app 客户自助端首页（§16.1）。
const pageChannelCustomerHome = "uni_customer_home"

// pageAllowedChannels 是允许装修的渠道集合（§16.1）：首版只装修客户自助端首页，
// 陪玩端与移动客服端首页**不在装修范围**（后两端不得被运营配置改写）。
// 来源与 pageChannelCustomerHome 同一处常量，不另立配置项：新增渠道须同步 §16.1 口径。
var pageAllowedChannels = map[string]bool{pageChannelCustomerHome: true}

// pageChannelAllowed 统一判定装修渠道是否在允许集合内（P0-18）。
// 所有入口（建文档 / 发布 / 回滚 / 审核 / tabBar 读写 / 预览码 / 预览 / 客户端读取）共用此一处，
// 避免某个入口漏拦后仍能越权「装修」陪玩端或客服端首页。
func pageChannelAllowed(channel string) error {
	channel = strings.TrimSpace(channel)
	if pageAllowedChannels[channel] {
		return nil
	}
	return fmt.Errorf("%w：渠道 %q 不在允许装修范围内（当前仅支持 %s，即客户自助端首页；陪玩端与客服端首页不在装修范围）",
		errInvalidInput, channel, pageChannelCustomerHome)
}

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
// images 是轮播图的多图数组（P1：此前只认单数 image/icon，数组元素既不做 https 校验也不做发布前探测，
// 等于绕过整套资源校验）；把它并入同一集合后，数组元素与单数字段走完全相同的校验与探测。
var pageImageFields = map[string]bool{
	"image": true, "img": true, "image_url": true, "pic": true, "pic_url": true,
	"banner": true, "icon": true, "avatar": true, "cover": true, "logo": true,
	"src": true, "background": true, "images": true,
}

// pageComponentTypes 是首版允许的组件类型集合（§16.3）。
var pageComponentTypes = map[string]bool{
	"search": true, "carousel": true, "notice": true, "quick_nav": true,
	"category": true, "product_list": true, "player_recommend": true,
	"activity": true, "member_entry": true, "support_entry": true,
	"image": true, "layout": true,
}

// pageLayoutMaxColumns 是布局容器的最大列数（1×1 / 1×2 / 1×3）。
const pageLayoutMaxColumns = 3

// pageRegisteredRoutes 是 uni-app 已注册页面路径白名单（§16.5，抄录 e-sports-uniapp/src/pages.json）。
// 站内跳转必须是客户端真实注册的页面，避免运营配置出「看似站内、实际未注册」的死链
// 或跳转到未预期的页面；比对前去掉 query / hash。
var pageRegisteredRoutes = map[string]bool{
	"/pages/index/index":        true,
	"/pages/order/list":         true,
	"/pages/mine/index":         true,
	"/pages/messages/index":     true,
	"/pages/login/index":        true,
	"/pages/order/create":       true,
	"/pages/order/detail":       true,
	"/pages/aftersale/list":     true,
	"/pages/wallet/index":       true,
	"/pages/wallet/recharge":    true,
	"/pages/gift/intents":       true,
	"/pages/gift/disputes":      true,
	"/pages/player/home":        true,
	"/pages/player/hall":        true,
	"/pages/player/orders":      true,
	"/pages/player/wallet":      true,
	"/pages/player/addons":      true,
	"/pages/player/notices":     true,
	"/pages/player/profile":     true,
	"/pages/player/identity":    true,
	"/pages/player/transfer":    true,
	"/pages/player/assessments": true,
	"/pages/player/recoveries":  true,
	"/pages/player/appeals":     true,
	"/pages/player/teammates":   true,
	"/pages/cs/login":           true,
	"/pages/cs/workbench":       true,
	"/pages/cs/customers":       true,
	"/pages/cs/customer-detail": true,
	"/pages/cs/order-create":    true,
	"/pages/cs/orders":          true,
	"/pages/cs/order-detail":    true,
	"/pages/cs/scripts":         true,
	"/pages/cs/after-sales":     true,
	"/pages/cs/gift":            true,
	"/pages/wecom/index":        true,
}

// pageUniversalPropKeys 是所有组件通用的属性键（§16.2-05 属性面板四类字段）：
// 间距（spacing）、跳转目标（link / link_type）、数据来源（data_source）、筛选条件（filter / filters / sort）。
var pageUniversalPropKeys = map[string]bool{
	"spacing": true, "link": true, "link_type": true,
	"data_source": true, "filter": true, "filters": true, "sort": true,
}

// pageExtraPropKeys 是第 4 轮为各组件补齐的专属字段（§16.3-01 / 03 / 05 / 08 / 09 / 10 / 11）。
// 必须与 pagePropsWhitelist 一并放行：任何未列入的键都会在保存草稿时被判「不支持的属性键」。
var pageExtraPropKeys = map[string]map[string]bool{
	"search":           {"hot_words": true, "scope": true},
	"carousel":         {"valid_from": true, "valid_to": true},
	"notice":           {"icon": true, "valid_from": true, "valid_to": true},
	"category":         {"limit": true, "layout": true},
	"product_list":     {"data_source": true, "rule_code": true, "rules": true},
	"player_recommend": {"game": true, "level": true, "online": true, "bookable": true},
	"activity":         {"image": true, "images": true, "valid_from": true, "valid_to": true},
	"member_entry":     {"subtitle": true, "style": true, "items": true},
	"support_entry":    {"icon": true, "text": true, "order": true, "position": true, "items": true},
}

// pagePropAllowed 判断某组件类型下的属性键是否被放行（通用键 ∪ 专属补充键 ∪ 既有白名单）。
func pagePropAllowed(compType, key string) bool {
	if pageUniversalPropKeys[key] || pageExtraPropKeys[compType][key] {
		return true
	}
	return pagePropsWhitelist[compType][key]
}

// pagePropsWhitelist 是按组件类型定义的 props 键白名单（§16.5）：只允许客户端真实消费的键
// （e-sports-uniapp/src/pages/index/index.vue 渲染所用，以及管理端属性面板 / 预览的字段），
// 其余任意键（html / script / 事件处理器等）一律返回参数错误，防止未渲染内容透传到客户端。
// carousel 额外保留 items（既有运营配置与回归测试使用，元素为 {image, link}）。
var pagePropsWhitelist = map[string]map[string]bool{
	"search":    {"placeholder": true},
	"carousel":  {"images": true, "interval": true, "items": true},
	"notice":    {"text": true, "color": true},
	"quick_nav": {"items": true},
	"category":  {"title": true, "items": true},
	"product_list": {
		"title": true, "limit": true, "empty_text": true,
		"sku_id": true, "sku_ids": true, "product_id": true, "product_ids": true,
		// §16.3-06（本轮口径收紧）：充值活动 / 充值赠送活动（campaign / activity）与 SKU 没有任何
		// 关联字段，商品列表永远无法据此取数。允许保存等于「先承诺、取数时再反悔」，
		// 因此不再受理这四个键；活动引用请配置到 activity 组件（那里才有真实语义）。
	},
	"player_recommend": {"title": true, "empty_text": true},
	"activity": {
		"title": true, "empty_text": true,
		"campaign_id": true, "campaign_ids": true, "activity_id": true, "activity_ids": true,
	},
	"member_entry":  {"title": true},
	"support_entry": {},
	// 图片组件：单图 + 通用 link / spacing；fit / height / radius 控制展示样式。
	"image": {"image": true, "fit": true, "height": true, "radius": true},
	// 布局只开放列数；子组件在 children 中，不通过 props 承载。
	"layout": {"columns": true},
}

// pageNestedPropKeys 是嵌套属性（items 元素，如导航项 / 轮播项）允许的键（§16.5）：
// 客户端消费的 name / target / icon 与文本、实体引用键；图片、跳转键复用上面的字段集合。
var pageNestedPropKeys = map[string]bool{
	"name": true, "target": true, "icon": true, "title": true, "subtitle": true, "text": true,
	"limit": true, "interval": true, "color": true, "empty_text": true, "placeholder": true,
	"sku_id": true, "sku_ids": true, "product_id": true, "product_ids": true,
	"campaign_id": true, "campaign_ids": true, "activity_id": true, "activity_ids": true,
	// 第 4 轮补全（§16.2-05 / §16.3）：面板四类字段与组件专属字段在嵌套项内同样放行。
	"spacing": true, "link_type": true, "data_source": true, "filters": true, "sort": true,
	"order": true, "position": true, "style": true, "scope": true, "game": true, "level": true,
	"online": true, "bookable": true, "valid_from": true, "valid_to": true, "rule_code": true,
	"rules": true, "layout": true, "hot_words": true, "status": true, "tag": true,
	"min_price_cents": true, "max_price_cents": true, "category_id": true,
}

// pageFilterKeys 是「筛选条件」（filter / filters）允许的键（§16.2-05 / §16.3-06 / §16.3-07）：
// 只允许已知筛选维度，未知键返回参数错误，避免运营写入服务端根本不解析的条件。
var pageFilterKeys = map[string]bool{
	"game": true, "level": true, "online": true, "bookable": true, "status": true, "tag": true,
	"min_price_cents": true, "max_price_cents": true, "category_id": true,
	"sku_id": true, "campaign_id": true, "activity_id": true, "product_id": true,
}

// pageFilterKeysByComp 是「按组件类型的筛选条件白名单」（§16.2-05 / §16.3-06 / §16.3-07）。
// pageFilterKeys 只是所有组件的并集：只按并集放行会让运营把 A 组件的筛选键配到 B 组件上
// （保存 200、取数时才 400 或干脆不生效），因此这里再按组件收窄一层（叠加校验，并集校验不变）。
//
// 未登记筛选维度的组件给**空集合**：没有任何服务端取数会消费它的筛选条件，
// 允许保存等于「先承诺、取数时再反悔」（§7 不假成功），因此不接受保存。
var pageFilterKeysByComp = map[string]map[string]bool{
	// 商品列表（§16.3-06 / §16.3-11）：SKU 侧可落地的维度。
	// game 经分类（EsportsSku.category_id → EsportsProductCategory.game）落到 SKU；
	// 充值活动维度不在其列（SKU 与活动无关联字段，活动引用属于 activity 组件）。
	"product_list": {
		"game": true, "category_id": true, "min_price_cents": true, "max_price_cents": true,
		"status": true, "sku_id": true, "product_id": true,
	},
	// 陪玩推荐（§16.3-07）：游戏分区 / 技能等级 / 在线 / 可接单。
	"player_recommend": {"game": true, "level": true, "online": true, "bookable": true},
	// 活动组件：活动 / 充值活动引用（真正有语义的组件）。
	"activity": {"campaign_id": true, "activity_id": true},
	// 其余组件暂无服务端取数语义。
	"search": {}, "carousel": {}, "notice": {}, "quick_nav": {},
	"category": {}, "member_entry": {}, "support_entry": {}, "image": {}, "layout": {},
}

// pageRulesKeys 是「取数规则」（rules）允许的键（§16.3-11）：规则码与排序 / 数量 / 筛选。
var pageRulesKeys = map[string]bool{
	"rule_code": true, "data_source": true, "sort": true, "limit": true, "filter": true, "filters": true,
}

// 属性面板枚举取值（§16.2-05 / §16.3）：非法取值一律返回参数错误，避免运营配置出客户端不认的值。
var (
	pageDataSourceValues = map[string]bool{
		"manual": true, "sku": true, "product": true, "campaign": true, "player": true,
		"category": true, "rule": true,
	}
	pageRuleCodeValues = map[string]bool{
		"on_sale": true, "hot": true, "new": true, "discount": true, "member_only": true, "recent": true,
	}
	pageSortValues        = map[string]bool{"default": true, "price_asc": true, "price_desc": true, "newest": true, "hot": true}
	pageLayoutValues      = map[string]bool{"grid": true, "list": true, "scroll": true, "compact": true}
	pageLinkTypeValues    = map[string]bool{"none": true, "page": true, "web": true, "tab": true}
	pageMemberStyleValues = map[string]bool{"card": true, "list": true, "compact": true}
	pageSearchScopeValues = map[string]bool{"sku": true, "player": true, "both": true}
	pagePositionValues    = map[string]bool{"top": true, "bottom": true, "inline": true}
	pagePlayerLevelValues = map[string]bool{"novice": true, "skilled": true, "senior": true, "mentor": true, "any": true}
	pageImageFitValues    = map[string]bool{"widthFix": true, "aspectFill": true, "aspectFit": true}
)

// 属性面板字段取值范围（§16.2-05）。
const (
	pageSpacingMax   = 240 // 间距（px）
	pageHotWordMax   = 10  // 热门词条数上限
	pageHotWordLimit = 30  // 单条热门词长度上限（字符）
	pageFilterKeyMax = 20  // 筛选条件键数量上限
	pageGameLimit    = 40
)

// pageNestedPropKeyAllowed 判断嵌套属性键是否被允许（含图片 / 跳转字段）。
func pageNestedPropKeyAllowed(key string) bool {
	return pageNestedPropKeys[key] || pageImageFields[key] || pageLinkFields[key]
}

// pageComponent 是单个装修组件：类型 + 显隐 + 自由属性。
// Children 仅 layout 使用：按列存放子组件（null 表示空列），当前只开放一层，
// 布局内不能再嵌套布局，避免任意深度导致客户端渲染与运营配置不可控。
type pageComponent struct {
	Type     string                 `json:"type"`
	Visible  *bool                  `json:"visible"`
	Props    map[string]interface{} `json:"props,omitempty"`
	Children []*pageComponent       `json:"children,omitempty"`
}

// pageConfig 是首页装修的结构化配置。
// Tabbar 为底部导航项（§16.3-12）：路径 / 数量 / 顺序构建期固定，后台只能改文案与图标。
type pageConfig struct {
	Components []pageComponent  `json:"components"`
	Tabbar     []pageTabbarItem `json:"tabbar,omitempty"`
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
	return validatePageConfigWithIcons(cfg, nil, false, nil)
}

// validatePageConfigWithAllowedDomains 校验组件类型、数量上限、必需入口，并递归校验每个组件的
// Props：外部跳转链接、素材图片地址（https / 站内相对路径）、文本超限、图片尺寸范围。
// support_entry 缺失或 visible=false 时拒绝（§16.5）。
// enforceDomain 为 true 时外部跳转链接须命中 allowedDomains「已审核 HTTPS 域名」白名单（发布前校验），
// 为 false 时仅校验链接协议（https 或站内相对路径），供保存草稿放行待审核域名。
// 图标短标识按基准清单判定（不含运营扩展项），供不持库的调用方复用。
func validatePageConfigWithAllowedDomains(cfg pageConfig, allowedDomains map[string]bool, enforceDomain bool) error {
	return validatePageConfigWithIcons(cfg, allowedDomains, enforceDomain, nil)
}

// validatePageConfigWithIcons 在 validatePageConfigWithAllowedDomains 的基础上，
// 额外接收「运营扩展的图标短标识清单」（§16.3-04，见 pageIconExtraAllowlist）。
func validatePageConfigWithIcons(cfg pageConfig, allowedDomains map[string]bool, enforceDomain bool,
	extraIcons map[string]bool) error {
	if len(cfg.Components) < 1 {
		return fmt.Errorf("%w：首页组件数量须在 1~%d 之间", errInvalidInput, pageMaxComponents)
	}
	hasVisibleSupport := false
	totalComponents := 0
	for i, comp := range cfg.Components {
		totalComponents += pageComponentCount(comp)
		if err := validatePageComponentNode(i, 0, comp, allowedDomains, enforceDomain, extraIcons); err != nil {
			return err
		}
		if pageComponentHasVisibleSupport(comp) {
			hasVisibleSupport = true
		}
	}
	if totalComponents > pageMaxComponents {
		return fmt.Errorf("%w：首页组件数量须在 1~%d 之间", errInvalidInput, pageMaxComponents)
	}
	// 必需入口：客服与订单入口组件同时承载「联系客服」与「我的订单 / 售后」，且不得隐藏。
	if !hasVisibleSupport {
		return fmt.Errorf("%w：首页必须保留可见的「客服与订单入口」组件", errInvalidInput)
	}
	return nil
}

// validatePageComponentNode 递归校验单个组件（布局最多一层）：
// 布局按 columns 限定列数，children 仅承载叶子组件；普通组件不允许携带 children。
func validatePageComponentNode(compIndex, depth int, comp pageComponent,
	allowedDomains map[string]bool, enforceDomain bool, extraIcons map[string]bool) error {
	if !pageComponentTypes[comp.Type] {
		return fmt.Errorf("%w：不支持的组件类型 %q", errInvalidInput, comp.Type)
	}
	if comp.Type == "layout" {
		if depth > 0 {
			return fmt.Errorf("%w：布局组件不能嵌套布局", errInvalidInput)
		}
		columns, err := pageLayoutColumns(comp.Props)
		if err != nil {
			return fmt.Errorf("%w：第 %d 个布局：%v", errInvalidInput, compIndex+1, err)
		}
		if len(comp.Children) > columns {
			return fmt.Errorf("%w：第 %d 个布局有 %d 列内容，超过 %d 列上限",
				errInvalidInput, compIndex+1, len(comp.Children), columns)
		}
		for childIndex, child := range comp.Children {
			if child == nil {
				continue
			}
			if err := validatePageComponentNode(compIndex, depth+1, *child, allowedDomains, enforceDomain, extraIcons); err != nil {
				return fmt.Errorf("第 %d 个布局第 %d 列：%w", compIndex+1, childIndex+1, err)
			}
		}
	} else if len(comp.Children) > 0 {
		return fmt.Errorf("%w：第 %d 个组件（%s）不支持子组件", errInvalidInput, compIndex+1, comp.Type)
	}
	return validatePageProps(compIndex, comp.Type, comp.Props, allowedDomains, enforceDomain, extraIcons)
}

// pageLayoutColumns 读取布局列数；未配置按 1 列处理，非法值直接拒绝。
func pageLayoutColumns(props map[string]interface{}) (int, error) {
	raw, ok := props["columns"]
	if !ok || raw == nil {
		return 1, nil
	}
	var value float64
	switch v := raw.(type) {
	case float64:
		value = v
	case int:
		value = float64(v)
	default:
		return 0, fmt.Errorf("列数必须是 1~%d 的整数", pageLayoutMaxColumns)
	}
	if value != float64(int(value)) || int(value) < 1 || int(value) > pageLayoutMaxColumns {
		return 0, fmt.Errorf("列数须为 1~%d 的整数", pageLayoutMaxColumns)
	}
	return int(value), nil
}

// pageComponentCount 统计顶层组件与其子组件数量（含空列占位不算）。
func pageComponentCount(comp pageComponent) int {
	count := 1
	for _, child := range comp.Children {
		if child != nil {
			count += pageComponentCount(*child)
		}
	}
	return count
}

// pageComponentHasVisibleSupport 递归判断组件中是否存在可见的客服入口：
// 布局隐藏时整行不渲染，其内部入口不计入必需入口。
func pageComponentHasVisibleSupport(comp pageComponent) bool {
	if comp.Visible != nil && !*comp.Visible {
		return false
	}
	if comp.Type == "support_entry" {
		return true
	}
	for _, child := range comp.Children {
		if child != nil && pageComponentHasVisibleSupport(*child) {
			return true
		}
	}
	return false
}

// validatePageProps 校验组件自由属性（§16.5）：先按组件类型做 props 键白名单，
// 未知键直接返回参数错误，再递归校验取值。compIndex 从 0 起，错误信息里以 1 起呈现给运营。
func validatePageProps(compIndex int, compType string, props map[string]interface{}, allowedDomains map[string]bool,
	enforceDomain bool, extraIcons map[string]bool) error {
	if _, ok := pagePropsWhitelist[compType]; !ok {
		return fmt.Errorf("%w：第 %d 个组件类型 %q 不支持属性配置", errInvalidInput, compIndex+1, compType)
	}
	for key, value := range props {
		if !pagePropAllowed(compType, key) {
			return fmt.Errorf("%w：第 %d 个组件（%s）不支持的属性键 %q", errInvalidInput, compIndex+1, compType, key)
		}
		// 筛选条件额外按组件类型收窄（§16.3-06 / §16.3-07）：并集校验仍由 validatePageValue 负责。
		if err := validatePageComponentFilterKeys(compIndex, compType, key, value); err != nil {
			return err
		}
		if err := validatePageValue(compIndex, key, value, allowedDomains, enforceDomain, extraIcons); err != nil {
			return err
		}
	}
	return nil
}

// validatePageComponentFilterKeys 校验筛选条件是否属于该组件允许的维度：
// filter / filters 直接校验；rules 内的 filter / filters 同样校验（rules 仅 product_list 放行）。
// 只做「该组件允许哪些筛选键」这一层叠加，整体形态（对象、键在并集内）仍由 validatePageValue 负责。
func validatePageComponentFilterKeys(compIndex int, compType, key string, value interface{}) error {
	switch key {
	case "filter", "filters":
		return validatePageFilterKeySet(compIndex, compType, key, value)
	case "rules":
		rules, ok := value.(map[string]interface{})
		if !ok {
			return nil
		}
		for _, field := range []string{"filter", "filters"} {
			inner, ok := rules[field]
			if !ok {
				continue
			}
			if err := validatePageFilterKeySet(compIndex, compType, field, inner); err != nil {
				return err
			}
		}
	}
	return nil
}

// validatePageFilterKeySet 按组件类型校验筛选条件字典的键集合。
func validatePageFilterKeySet(compIndex int, compType, field string, value interface{}) error {
	dict, ok := value.(map[string]interface{})
	if !ok {
		return nil // 非对象形态不在这里判：保持既有行为，由 validatePageValue 处理。
	}
	for dim := range dict {
		if !pageFilterKeysByComp[compType][dim] {
			return fmt.Errorf("%w：第 %d 个组件（%s）的筛选条件 %s 不支持键 %q",
				errInvalidInput, compIndex+1, compType, field, dim)
		}
	}
	return nil
}

func validatePageValue(compIndex int, key string, value interface{}, allowedDomains map[string]bool,
	enforceDomain bool, extraIcons map[string]bool) error {
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
		case "game":
			if n := len([]rune(v)); n > pageGameLimit {
				return fmt.Errorf("%w：第 %d 个组件字段 %q 文本超长（%d/%d）", errInvalidInput, compIndex+1, key, n, pageGameLimit)
			}
		}
		if err := validatePageEnumValue(compIndex, key, v); err != nil {
			return err
		}
		if err := validatePagePropTime(compIndex, key, v); err != nil {
			return err
		}
		if pageLinkFields[key] {
			return validatePageLink(v, allowedDomains, enforceDomain, compIndex, key)
		}
		if key == "icon" {
			return validatePageIcon(v, compIndex, extraIcons)
		}
		if pageImageFields[key] {
			return validatePageImageURL(v, compIndex, key)
		}
	case float64:
		if err := validatePageNumericProp(compIndex, key, v); err != nil {
			return err
		}
	case int:
		if err := validatePageNumericProp(compIndex, key, float64(v)); err != nil {
			return err
		}
	case bool:
		// online / bookable 等开关类字段无额外约束（类型已由 JSON 解码保证）。
	case []interface{}:
		if key == "hot_words" {
			if len(v) > pageHotWordMax {
				return fmt.Errorf("%w：第 %d 个组件字段 %q 词条过多（%d/%d）", errInvalidInput, compIndex+1, key, len(v), pageHotWordMax)
			}
			for _, item := range v {
				word, ok := item.(string)
				if !ok || strings.TrimSpace(word) == "" || len([]rune(word)) > pageHotWordLimit {
					return fmt.Errorf("%w：第 %d 个组件字段 %q 含非法词条", errInvalidInput, compIndex+1, key)
				}
			}
		}
		for _, item := range v {
			if err := validatePageValue(compIndex, key, item, allowedDomains, enforceDomain, extraIcons); err != nil {
				return err
			}
		}
	case map[string]interface{}:
		// 嵌套属性（items 元素等）同样做键白名单，避免绕过顶层白名单注入未知键。
		for k := range v {
			if !pageNestedPropKeyAllowed(k) {
				return fmt.Errorf("%w：第 %d 个组件属性子项不支持的键 %q", errInvalidInput, compIndex+1, k)
			}
		}
		// 筛选条件 / 取数规则是「受约束的字典」：键集合必须落在已知维度内（§16.2-05 / §16.3-11）。
		if key == "filter" || key == "filters" {
			if len(v) > pageFilterKeyMax {
				return fmt.Errorf("%w：第 %d 个组件字段 %q 条件过多（%d/%d）", errInvalidInput, compIndex+1, key, len(v), pageFilterKeyMax)
			}
			for k := range v {
				if !pageFilterKeys[k] {
					return fmt.Errorf("%w：第 %d 个组件字段 %q 不支持的筛选条件 %q", errInvalidInput, compIndex+1, key, k)
				}
			}
		}
		if key == "rules" {
			for k := range v {
				if !pageRulesKeys[k] {
					return fmt.Errorf("%w：第 %d 个组件字段 %q 不支持的取数规则键 %q", errInvalidInput, compIndex+1, key, k)
				}
			}
		}
		for k, sub := range v {
			if err := validatePageValue(compIndex, k, sub, allowedDomains, enforceDomain, extraIcons); err != nil {
				return err
			}
		}
	}
	return nil
}

// validatePageEnumValue 校验属性面板枚举取值（§16.2-05 / §16.3）：非法取值返回参数错误，
// 而不是把客户端不认的值原样存进配置。
func validatePageEnumValue(compIndex int, key, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	// 快捷入口 target 不是受约束枚举（客户端按语义分支跳转），但已随功能下线的
	// target 必须拒绝新写入：否则运营会把一个「点了没反应」的入口重新发到客户端。
	if key == "target" {
		if feature, removed := pageRemovedNavTargets[strings.ToLower(value)]; removed {
			return fmt.Errorf("%w：第 %d 个组件的快捷入口 target=%q 属于已下线的%s功能，请改用 %s",
				errInvalidInput, compIndex+1, value, feature, pageNavTargetHint)
		}
	}
	var allowed map[string]bool
	switch key {
	case "data_source":
		allowed = pageDataSourceValues
	case "rule_code":
		allowed = pageRuleCodeValues
	case "sort":
		allowed = pageSortValues
	case "layout":
		allowed = pageLayoutValues
	case "link_type":
		allowed = pageLinkTypeValues
	case "style":
		allowed = pageMemberStyleValues
	case "scope":
		allowed = pageSearchScopeValues
	case "position":
		allowed = pagePositionValues
	case "level":
		allowed = pagePlayerLevelValues
	case "fit":
		allowed = pageImageFitValues
	default:
		return nil
	}
	if !allowed[value] {
		return fmt.Errorf("%w：第 %d 个组件字段 %q 的取值 %q 不支持", errInvalidInput, compIndex+1, key, value)
	}
	return nil
}

// validatePagePropTime 校验有效期字段（valid_from / valid_to，§16.3-02 / 03 / 08）：
// 支持 RFC3339 与「2006-01-02」「2006-01-02 15:04」写法，非法时间返回参数错误。
func validatePagePropTime(compIndex int, key, value string) error {
	if key != "valid_from" && key != "valid_to" {
		return nil
	}
	if strings.TrimSpace(value) == "" {
		return nil
	}
	if _, err := parsePagePropTime(value); err != nil {
		return fmt.Errorf("%w：第 %d 个组件字段 %q 的时间格式非法（%q）", errInvalidInput, compIndex+1, key, value)
	}
	return nil
}

// validatePageNumericProp 校验数值型属性（§16.2-05）：间距 0~240 px。
func validatePageNumericProp(compIndex int, key string, value float64) error {
	if key == "width" || key == "height" {
		return validatePageDimension(value, compIndex, key)
	}
	if key == "spacing" {
		if value < 0 || value > pageSpacingMax {
			return fmt.Errorf("%w：第 %d 个组件字段 %q 的间距须在 0~%d 之间", errInvalidInput, compIndex+1, key, pageSpacingMax)
		}
	}
	if key == "radius" {
		if value < 0 || value > 48 {
			return fmt.Errorf("%w：第 %d 个组件字段 %q 的圆角须在 0~48 之间", errInvalidInput, compIndex+1, key)
		}
	}
	return nil
}

// pageIconAllowlistKey 是「客户端内置图标短标识允许清单」的系统配置键
// （裸键 page_icon_allowlist，逗号分隔）。它只在基准清单之上做**加法**，
// 运营可以扩展清单，但无法收窄（收窄会让存量已发布页面在新口径下不可发布）。
const pageIconAllowlistKey = "app.esports.page_icon_allowlist"

// pageIconDefaultAllowlist 是短标识图标的**基准清单**（§16.3-04 向后兼容收紧）：
// 取自本仓现有代码 / 文档里**实际出现过**的短标识——app.go 的 PermMenu Icon
// （icon-apps、icon-list、icon-wallet …）与 §16.3-04 本身举例的 icon-cs。
// 客户端当前尚未内置图标集，因此清单外的新短标识一律拒绝，由运营按需通过
// app.esports.page_icon_allowlist 扩展，避免运营写出 icon-nonexistent 这类坏图。
var pageIconDefaultAllowlist = []string{
	"icon-apps",
	"icon-archive",
	"icon-bar-chart",
	"icon-check-circle",
	"icon-cs",
	"icon-customer-service",
	"icon-desktop",
	"icon-edit",
	"icon-exclamation-circle",
	"icon-file",
	"icon-gift",
	"icon-heart",
	"icon-list",
	"icon-safe",
	"icon-share-alt",
	"icon-star",
	"icon-user",
	"icon-wallet",
}

// pageIconExtraAllowlist 读运营扩展的图标短标识清单（逗号分隔，`app.esports.page_icon_allowlist`）。
// 缺失 / 空值返回 nil（只按基准清单判定）；非法项（空串）忽略。
func pageIconExtraAllowlist(ctx context.Context, db *ent.Client) map[string]bool {
	raw := assessmentConfigString(ctx, db, pageIconAllowlistKey, "")
	if raw == "" {
		return nil
	}
	out := make(map[string]bool)
	for _, part := range strings.Split(raw, ",") {
		if id := strings.TrimSpace(part); id != "" {
			out[id] = true
		}
	}
	return out
}

// pageIconAllowed 判断短标识是否命中允许清单（基准清单 ∪ 运营扩展项）。
func pageIconAllowed(identifier string, extra map[string]bool) bool {
	for _, allowed := range pageIconDefaultAllowlist {
		if identifier == allowed {
			return true
		}
	}
	return extra[identifier]
}

// validatePageIcon 校验图标字段（§16.3-03 / §16.3-04 / §16.3-10）：允许 https / 站内相对路径，
// 短标识（如 icon-cs）必须命中允许清单（基准清单 ∪ extra 运营扩展项），
// 清单外一律参数错误——客户端没有内置图标集，放行任意短标识会渲染成坏图。
func validatePageIcon(raw string, compIndex int, extra map[string]bool) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "/") {
		return validatePageImageURL(raw, compIndex, "icon")
	}
	if len([]rune(raw)) > 40 {
		return fmt.Errorf("%w：第 %d 个组件字段 %q 的图标标识过长", errInvalidInput, compIndex+1, "icon")
	}
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return fmt.Errorf("%w：第 %d 个组件字段 %q 的图标取值非法（仅允许 URL 或短标识）", errInvalidInput, compIndex+1, "icon")
	}
	if !pageIconAllowed(raw, extra) {
		return fmt.Errorf("%w：第 %d 个组件字段 %q 的图标短标识 %q 不在允许清单内（仅允许 https 图片地址、站内相对路径，或后台「页面图标短标识允许清单」中已登记的标识）",
			errInvalidInput, compIndex+1, "icon", raw)
	}
	return nil
}

// parsePagePropTime 解析有效期时间（RFC3339 / 日期 / 日期时间，按 UTC+8 业务时区解释无时区的写法）。
func parsePagePropTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if layout == "2006-01-02" || layout == "2006-01-02 15:04" || layout == "2006-01-02 15:04:05" {
			if parsed, err := time.ParseInLocation(layout, raw, cstZone); err == nil {
				return parsed.UTC(), nil
			}
			continue
		}
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, errInvalidInput
}

// validatePageLink 校验外部跳转链接（§16.5）：站内相对路径须命中已注册页面路径白名单，
// 外部链接仅允许 https://；enforceDomain 为 true 时还须命中「已审核 HTTPS 域名」白名单，
// 白名单为空不放行任何外部链接。
func validatePageLink(raw string, allowedDomains map[string]bool, enforceDomain bool, compIndex int, field string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil // 空值跳过。
	}
	if strings.HasPrefix(raw, "/") {
		path := pageRoutePath(raw)
		if !pageRegisteredRoutes[path] {
			return fmt.Errorf("%w：第 %d 个组件字段 %q 的站内跳转路径 %q 不是已注册页面", errInvalidInput, compIndex+1, field, path)
		}
		return nil
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

// pageRoutePath 取站内路径的路径部分（去掉 query / hash 与末尾斜杠），用于已注册路由比对。
func pageRoutePath(raw string) string {
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	if len(raw) > 1 {
		raw = strings.TrimRight(raw, "/")
	}
	return raw
}

// 底部导航（tabBar）编辑口径（§16.3-12，审核报告 §8.1-8 后半放开）：
// 允许增删条目与编辑 path / order，但条目数必须是 1~5 项。
const (
	pageTabbarMinItems  = 1
	pageTabbarMaxItems  = 5
	pageTabbarTextLimit = 12  // 单项文案上限（字符）
	pageTabbarIconLimit = 200 // 单项图标地址上限（字符）
)

// pageValidateTabbarPath 校验底部导航路径（§16.3-12）：必须是站内相对路径且命中已注册页面白名单。
//
// 白名单来源与站内跳转链接**完全一致**：pageRegisteredRoutes（抄录 e-sports-uniapp/src/pages.json）。
// 后端并不掌握前端真实页面清单，读 pages.json 不现实，因此复用这份既有的「已注册页面」常量，
// 不新造配置项、也不引入第二份清单（两份清单会漂移，反而制造死链）。
// 比对前按 pageRoutePath 去掉 query / hash 与末尾斜杠，返回规范化后的路径供落库。
func pageValidateTabbarPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%w：底部导航路径不能为空", errInvalidInput)
	}
	if !strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("%w：底部导航路径 %q 必须是站内相对路径（以 / 开头）", errInvalidInput, raw)
	}
	if strings.HasPrefix(raw, "//") || strings.Contains(raw, "://") || strings.Contains(raw, "..") ||
		strings.ContainsAny(raw, "\r\n\t ") {
		return "", fmt.Errorf("%w：底部导航路径 %q 非法（不得包含协议、// 开头、.. 或空白字符）", errInvalidInput, raw)
	}
	path := pageRoutePath(raw)
	if !pageRegisteredRoutes[path] {
		return "", fmt.Errorf("%w：底部导航路径 %q 不是已注册页面", errInvalidInput, path)
	}
	return path, nil
}

// pageValidateTabbar 校验并规范化底部导航（§16.3-12，本轮放开路径与顺序）：
//   - 条目数 1~5（允许增删）；
//   - path 必填、必须命中已注册页面白名单、同名路径不得重复；
//   - order 可选：未填时按提交顺序补 1..N；填了必须是 1~5 且互不重复；
//   - 文案 1~12 字、图标仅允许 https 或站内相对路径（客户端导航栏只消费图片地址）。
//
// 校验通过后按 order 升序返回，客户端拿到的数组顺序即运营配置的展示顺序。
func pageValidateTabbar(items []pageTabbarItem) ([]pageTabbarItem, error) {
	if len(items) < pageTabbarMinItems || len(items) > pageTabbarMaxItems {
		return nil, fmt.Errorf("%w：底部导航须为 %d~%d 项（当前 %d 项）",
			errInvalidInput, pageTabbarMinItems, pageTabbarMaxItems, len(items))
	}
	out := make([]pageTabbarItem, 0, len(items))
	seenPath := make(map[string]int, len(items))
	seenOrder := make(map[int]int, len(items))
	for i, item := range items {
		path, err := pageValidateTabbarPath(item.Path)
		if err != nil {
			return nil, err
		}
		if prev, ok := seenPath[path]; ok {
			return nil, fmt.Errorf("%w：底部导航路径 %s 重复（第 %d 项与第 %d 项）", errInvalidInput, path, prev+1, i+1)
		}
		seenPath[path] = i

		order := i + 1
		if item.Order != nil {
			order = *item.Order
		}
		if order < 1 || order > pageTabbarMaxItems {
			return nil, fmt.Errorf("%w：底部导航第 %d 项 order 须在 1~%d 之间", errInvalidInput, i+1, pageTabbarMaxItems)
		}
		if prev, ok := seenOrder[order]; ok {
			return nil, fmt.Errorf("%w：底部导航 order %d 重复（第 %d 项与第 %d 项）", errInvalidInput, order, prev+1, i+1)
		}
		seenOrder[order] = i

		text := strings.TrimSpace(item.Text)
		if text == "" || len([]rune(text)) > pageTabbarTextLimit {
			return nil, fmt.Errorf("%w：底部导航第 %d 项文案须为 1~%d 个字", errInvalidInput, i+1, pageTabbarTextLimit)
		}
		icon := strings.TrimSpace(item.Icon)
		if len([]rune(icon)) > pageTabbarIconLimit {
			return nil, fmt.Errorf("%w：底部导航第 %d 项图标标识过长", errInvalidInput, i+1)
		}
		if icon != "" && !strings.HasPrefix(icon, "https://") && !strings.HasPrefix(icon, "/") {
			// 注意：底部导航**不接受短标识**（与组件字段 §16.3-04 的图标清单口径不同）——
			// 客户端导航栏当前只消费图片地址，短标识会渲染成坏图，故这里直接拒绝。
			return nil, fmt.Errorf("%w：底部导航第 %d 项图标仅允许 HTTPS 链接或站内相对路径（不支持短标识）", errInvalidInput, i+1)
		}
		orderCopy := order
		out = append(out, pageTabbarItem{Path: path, Text: text, Icon: icon, Order: &orderCopy, Visible: item.Visible})
	}
	sort.SliceStable(out, func(a, b int) bool { return *out[a].Order < *out[b].Order })
	return out, nil
}

// validatePageImageURL 校验素材 / 图片地址（§16.5 / P0-17）：站内相对路径或图标短标识放行；
// 外部地址仅允许 https://，且必须通过网络目标校验（拒绝 http、带用户名 / 口令、非 80/443 显式端口、
// 指向本机 / 私网 / 链路本地地址）。域名白名单在发布前探测阶段强制（见 validatePageResources），
// 与外部跳转链接的口径一致：保存草稿放行待审核域名，发布前才强制白名单。
func validatePageImageURL(raw string, compIndex int, field string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "/") {
		return nil // 空值跳过；站内相对路径。
	}
	if !strings.HasPrefix(raw, "https://") {
		return fmt.Errorf("%w：第 %d 个组件字段 %q 的素材地址仅允许 https:// 或站内相对路径", errInvalidInput, compIndex+1, field)
	}
	if err := pageValidateHTTPSURLTarget(raw); err != nil {
		return fmt.Errorf("%w：第 %d 个组件字段 %q 的素材地址%s", errInvalidInput, compIndex+1, field, err)
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
	// P0-18：只能建 / 改允许装修的渠道文档，任意 :channel 都不放行。
	if err := pageChannelAllowed(channel); err != nil {
		return nil, err
	}
	cfg := pageConfig{Components: in.Components}
	if err := validatePageConfigWithIcons(cfg, pageAllowedDomains(ctx, db), false,
		pageIconExtraAllowlist(ctx, db)); err != nil {
		return nil, err
	}
	// 底部导航（§16.3-12）不在本请求体内：保存组件草稿时须原样保留已配置的导航文案 / 图标，
	// 否则一次「保存草稿」就会把运营改好的导航重置掉。
	if existing, err := db.EsportsPageDocument.Query().Where(esportspagedocument.ChannelEQ(channel)).Only(ctx); err == nil {
		if tabbar, err := pageTabbarFromConfig(existing.DraftConfig); err == nil {
			cfg.Tabbar = tabbar
		}
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
	// P0-18：非白名单渠道不得发布（否则可通过发布任意渠道快照越权改造其它端首页）。
	if err := pageChannelAllowed(channel); err != nil {
		return nil, err
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
	if err = validatePageConfigWithIcons(cfg, pageAllowedDomains(ctx, db), true,
		pageIconExtraAllowlist(ctx, db)); err != nil {
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
	// P0-18：审核通过即生效，同样只对白名单渠道放行。
	if err := pageChannelAllowed(channel); err != nil {
		return nil, err
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
	// P0-18：回滚会生成可发布的新版本，同样只对白名单渠道放行。
	if err := pageChannelAllowed(channel); err != nil {
		return nil, err
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
	if err = validatePageConfigWithIcons(sourceCfg, pageAllowedDomains(ctx, db), true,
		pageIconExtraAllowlist(ctx, db)); err != nil {
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
	// P0-18：非白名单渠道不暴露发布记录（避免成为探测其它端渠道的入口）。
	if err := pageChannelAllowed(channel); err != nil {
		return nil, err
	}
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

// pageRemovedNavTargets 是「已随功能下线、但存量装修文档里仍可能出现」的快捷入口 target。
//
// 值是语义标识（客户端按 target 分支跳转），下线后既不该新写入（validatePageEnumValue 拒绝），
// 也不该继续下发（pruneRemovedPageNavTargets 在读时剔除）——发布版本是不可变快照，
// 不能回写历史数据，所以只能在读取侧兜底，保证客户端不会拿到一个点了没反应的入口。
var pageRemovedNavTargets = map[string]string{
	"prepay_card":       "储值卡",
	"stored_value_card": "储值卡",
	"stored-value":      "储值卡",
}

// pageNavTargetHint 是报错时给出的可用替代入口（与客户端 index.vue 的 target 分支一致）。
const pageNavTargetHint = "category（商品分类）/ orders（我的订单）/ member（会员）/ support（联系客服）"

// 素材库限制（§16.2-06 / §16.5-04）：仅允许登记白名单图片类型，且单条素材不超过字节上限。
const (
	// pageAssetMaxBytes 是单个素材登记允许的最大字节数（10 MiB）。
	pageAssetMaxBytes = 10 << 20
	// pageAssetURLMaxLen 是素材地址长度上限（与 schema 的 512 对齐）。
	pageAssetURLMaxLen = 512
)

// pageAssetAllowedMimes 是素材允许的 MIME 白名单（§16.5-04）：装修素材只承载图片。
var pageAssetAllowedMimes = map[string]bool{
	"image/jpeg": true, "image/png": true, "image/webp": true, "image/gif": true,
}

// resolvePageAssetMime 校验素材 MIME（P1-20）：mime 必填且必须命中图片类型白名单。
// 不再「未声明时按后缀推断」——推断会让前端漏传 mime 时静默通过，
// 与 size_bytes 一起导致「类型 / 体积限制形同虚设」。
func resolvePageAssetMime(declared string) (string, error) {
	declared = strings.ToLower(strings.TrimSpace(declared))
	if declared == "" {
		return "", fmt.Errorf("%w：素材类型 mime 必填（仅允许 image/jpeg、image/png、image/webp、image/gif）",
			errInvalidInput)
	}
	if !pageAssetAllowedMimes[declared] {
		return "", fmt.Errorf("%w：素材类型 %q 不在白名单（仅允许 image/jpeg、image/png、image/webp、image/gif）",
			errInvalidInput, declared)
	}
	return declared, nil
}

func savePageAsset(ctx context.Context, db *ent.Client, adminID int, in pageAssetInput) (*ent.EsportsPageAsset, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.URL = strings.TrimSpace(in.URL)
	in.Mime = strings.TrimSpace(in.Mime)
	if in.Name == "" || len([]rune(in.Name)) > 160 || in.URL == "" || len(in.URL) > pageAssetURLMaxLen ||
		len(in.Mime) > 80 || in.SizeBytes < 0 || in.Width < 0 || in.Height < 0 {
		return nil, errInvalidInput
	}
	// 素材是运营上传的图片/资源，外部地址强制 HTTPS，内部跳转使用站内相对路径。
	// 与首页组件的「链接跳转」口径（http/https 均可）区分：素材落库须可安全访问。
	if !strings.HasPrefix(in.URL, "https://") && !strings.HasPrefix(in.URL, "/") {
		return nil, fmt.Errorf("%w：素材地址仅允许 HTTPS 链接或站内相对路径", errInvalidInput)
	}
	// §16.2-06 / §16.5-04 / P1-20：mime 与 size_bytes 必填，体积上限与 MIME 白名单一并校验，
	// 缺失 / 超限 / 非图片类型一律不落库（此前二者缺失时全部跳过，上限形同虚设）。
	if in.SizeBytes <= 0 {
		return nil, fmt.Errorf("%w：素材大小 size_bytes 必填且须大于 0（单位：字节，上限 %d 字节）",
			errInvalidInput, pageAssetMaxBytes)
	}
	if in.SizeBytes > pageAssetMaxBytes {
		return nil, fmt.Errorf("%w：素材体积超限（%d 字节 > 上限 %d 字节）", errInvalidInput, in.SizeBytes, pageAssetMaxBytes)
	}
	if in.Width > pageImageMaxDim || in.Height > pageImageMaxDim {
		return nil, fmt.Errorf("%w：素材尺寸须在 %d~%d 之间", errInvalidInput, pageImageMinDim, pageImageMaxDim)
	}
	mime, err := resolvePageAssetMime(in.Mime)
	if err != nil {
		return nil, err
	}
	return db.EsportsPageAsset.Create().SetName(in.Name).SetURL(in.URL).SetMime(mime).
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
