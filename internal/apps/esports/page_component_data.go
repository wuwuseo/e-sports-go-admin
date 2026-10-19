package esports

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsproductcategory"
	"go-admin/internal/ent/esportssku"
)

// 装修组件「按 props 取数」（§16.2-05 / §16.3-06 / §16.3-07 / §16.3-11）：
// 客户端把组件 props 透传为公开数据接口的同名查询参数，服务端据此**收窄**取数。
//
// 契约（可验收）：
//   - 未配置的键 = 不额外过滤，保持既有默认返回；
//   - 配置了但取值非法 / 无数据关系的键 = 明确 400（中文提示），绝不静默忽略或回落全量；
//   - 过滤只收窄，不放宽既有公开口径（商品仅在售、陪玩仅合格且在线的推荐列表）。

const (
	// pageSkuListLimitMax 是公开商品列表的既有返回上限（原 publicSkus 硬编码 Limit(100)）。
	pageSkuListLimitMax = 100
	// playerRecommendScanCap 是陪玩推荐的候选扫描上限（原 listPublicPlayers 的既有上限 200）。
	playerRecommendScanCap = 200
)

// skuCatalogProps 是 product_list 组件的取数参数（同名 props / 查询参数）。
type skuCatalogProps struct {
	SkuIDs        []int
	ProductIDs    []int
	CategoryID    int
	Game          string
	MinPriceCents int64
	MaxPriceCents int64
	Status        string
	DataSource    string
	RuleCode      string
	Sort          string
	Limit         int
}

// playerRecommendProps 是 player_recommend 组件的取数参数（同名 props / 查询参数）。
// Online / Bookable 是开关：true=按该维度收窄，false 或未配置=不额外收窄（与「未配置=不过滤」一致）。
type playerRecommendProps struct {
	Game     string
	Level    string
	Online   bool
	Bookable bool
	Limit    int
}

// skuCatalogFilterDims 是 product_list 能落到 SKU 查询上的筛选维度。
// 其余 pageFilterKeys（tag / campaign_id / activity_id / level / online / bookable）在 SKU 侧
// 没有数据关系，配置了必须明确失败（见 parseSkuCatalogProps / applySkuCatalogFilter）。
var skuCatalogFilterDims = map[string]bool{
	"game": true, "category_id": true, "min_price_cents": true, "max_price_cents": true,
	"status": true, "sku_id": true, "product_id": true,
}

// playerRecommendFilterDims 是 player_recommend 能落到陪玩查询上的筛选维度（§16.3-07）。
var playerRecommendFilterDims = map[string]bool{
	"game": true, "level": true, "online": true, "bookable": true,
}

// ── 参数解析（handler 层：只做参数解析） ──

func parsePlayerRecommendProps(c fiber.Ctx) (playerRecommendProps, error) {
	// 沿用既有 queryLimit 的宽松口径（非法 / <1 / >500 回落 fallback），再由取数函数按既有上限收紧。
	props := playerRecommendProps{Limit: componentPropLimit(c.Query("limit"), 100)}
	filter, err := parseComponentFilterDict(c)
	if err != nil {
		return props, err
	}
	for key := range filter {
		if !playerRecommendFilterDims[key] {
			return props, fmt.Errorf("%w：陪玩推荐不支持的筛选条件 %q", errInvalidInput, key)
		}
	}
	if v, ok := filter["game"]; ok {
		props.Game = strings.TrimSpace(componentPropString(v))
	}
	if v, ok := filter["level"]; ok {
		props.Level = strings.TrimSpace(componentPropString(v))
	}
	if v, ok := filter["online"]; ok {
		if props.Online, err = componentPropBool(v); err != nil {
			return props, fmt.Errorf("%w：陪玩推荐筛选条件 %q 的取值非法", errInvalidInput, "online")
		}
	}
	if v, ok := filter["bookable"]; ok {
		if props.Bookable, err = componentPropBool(v); err != nil {
			return props, fmt.Errorf("%w：陪玩推荐筛选条件 %q 的取值非法", errInvalidInput, "bookable")
		}
	}
	// 顶层同名参数优先于 filter 字典。
	if raw := strings.TrimSpace(c.Query("game")); raw != "" {
		props.Game = raw
	}
	if raw := strings.TrimSpace(c.Query("level")); raw != "" {
		props.Level = raw
	}
	if raw := strings.TrimSpace(c.Query("online")); raw != "" {
		if props.Online, err = componentQueryBool(raw); err != nil {
			return props, fmt.Errorf("%w：陪玩推荐的在线筛选取值 %q 非法（仅支持 true / false）", errInvalidInput, raw)
		}
	}
	if raw := strings.TrimSpace(c.Query("bookable")); raw != "" {
		if props.Bookable, err = componentQueryBool(raw); err != nil {
			return props, fmt.Errorf("%w：陪玩推荐的可接单筛选取值 %q 非法（仅支持 true / false）", errInvalidInput, raw)
		}
	}
	if err := validatePlayerRecommendProps(props); err != nil {
		return props, err
	}
	return props, nil
}

func validatePlayerRecommendProps(props playerRecommendProps) error {
	if props.Level != "" && !pagePlayerLevelValues[props.Level] {
		return fmt.Errorf("%w：陪玩推荐的技能等级取值 %q 不支持", errInvalidInput, props.Level)
	}
	if props.Game != "" && len([]rune(props.Game)) > pageGameLimit {
		return fmt.Errorf("%w：陪玩推荐筛选条件 %q 的取值过长", errInvalidInput, "game")
	}
	return nil
}

func parseSkuCatalogProps(c fiber.Ctx) (skuCatalogProps, error) {
	props := skuCatalogProps{Limit: componentPropLimit(c.Query("limit"), pageSkuListLimitMax)}
	// rules：取数规则字典（§16.3-11），键集合由 pageRulesKeys 约束。
	rules, err := parseComponentRules(c)
	if err != nil {
		return props, err
	}
	props.DataSource, props.RuleCode, props.Sort = rules.DataSource, rules.RuleCode, rules.Sort
	if rules.Limit > 0 {
		props.Limit = rules.Limit
	}
	if len(rules.Filter) > 0 {
		if err = applySkuCatalogFilter(&props, rules.Filter); err != nil {
			return props, err
		}
	}
	// filter / filters：通用「筛选条件」字典（§16.2-05）。
	filter, err := parseComponentFilterDict(c)
	if err != nil {
		return props, err
	}
	if err = applySkuCatalogFilter(&props, filter); err != nil {
		return props, err
	}
	// 顶层同名参数优先于 rules / filter 字典（显式参数更精确）。
	if raw := strings.TrimSpace(c.Query("data_source")); raw != "" {
		props.DataSource = raw
	}
	if raw := strings.TrimSpace(c.Query("rule_code")); raw != "" {
		props.RuleCode = raw
	}
	if raw := strings.TrimSpace(c.Query("sort")); raw != "" {
		props.Sort = raw
	}
	if raw := strings.TrimSpace(c.Query("game")); raw != "" {
		props.Game = raw
	}
	if raw := strings.TrimSpace(c.Query("category_id")); raw != "" {
		id, idErr := componentPropID(raw)
		if idErr != nil {
			return props, fmt.Errorf("%w：商品列表筛选条件 %q 的取值非法", errInvalidInput, "category_id")
		}
		props.CategoryID = id
	}
	for _, key := range []string{"min_price_cents", "max_price_cents"} {
		raw := strings.TrimSpace(c.Query(key))
		if raw == "" {
			continue
		}
		amount, amountErr := componentPropInt64(raw)
		if amountErr != nil || amount < 0 {
			return props, fmt.Errorf("%w：商品列表筛选条件 %q 的取值非法", errInvalidInput, key)
		}
		if key == "min_price_cents" {
			props.MinPriceCents = amount
		} else {
			props.MaxPriceCents = amount
		}
	}
	if raw := strings.TrimSpace(c.Query("status")); raw != "" {
		props.Status = raw
	}
	// 显式 ID：sku_id/sku_ids 为商品本体，product_id/product_ids 为父商品。
	if raw := strings.TrimSpace(c.Query("sku_id")); raw != "" {
		id, idErr := componentPropID(raw)
		if idErr != nil {
			return props, fmt.Errorf("%w：商品列表的 sku_id 取值非法", errInvalidInput)
		}
		props.SkuIDs = []int{id}
	}
	if ids, idErr := componentIDList(c.Query("sku_ids")); idErr != nil {
		return props, idErr
	} else if len(ids) > 0 {
		props.SkuIDs = ids
	}
	if raw := strings.TrimSpace(c.Query("product_id")); raw != "" {
		id, idErr := componentPropID(raw)
		if idErr != nil {
			return props, fmt.Errorf("%w：商品列表的 product_id 取值非法", errInvalidInput)
		}
		props.ProductIDs = []int{id}
	}
	if ids, idErr := componentIDList(c.Query("product_ids")); idErr != nil {
		return props, idErr
	} else if len(ids) > 0 {
		props.ProductIDs = ids
	}
	// SKU 与充值活动（campaign / activity）没有任何关联字段：配了必须明确失败，
	// 而不是静默忽略（否则运营看到「筛选已保存」但取数毫无变化 = 假成功）。
	for _, key := range []string{"campaign_id", "campaign_ids", "activity_id", "activity_ids"} {
		if strings.TrimSpace(c.Query(key)) != "" {
			return props, fmt.Errorf("%w：商品列表不支持按充值活动（%s）取数：SKU 与充值活动没有关联字段",
				errInvalidInput, key)
		}
	}
	if err = validateSkuCatalogProps(props); err != nil {
		return props, err
	}
	return props, nil
}

func validateSkuCatalogProps(props skuCatalogProps) error {
	if props.DataSource != "" && !pageDataSourceValues[props.DataSource] {
		return fmt.Errorf("%w：商品列表不支持的数据来源 %q", errInvalidInput, props.DataSource)
	}
	if props.RuleCode != "" && !pageRuleCodeValues[props.RuleCode] {
		return fmt.Errorf("%w：商品列表不支持的取数规则 %q", errInvalidInput, props.RuleCode)
	}
	if props.Sort != "" && !pageSortValues[props.Sort] {
		return fmt.Errorf("%w：商品列表不支持的排序取值 %q", errInvalidInput, props.Sort)
	}
	if props.Game != "" && len([]rune(props.Game)) > pageGameLimit {
		return fmt.Errorf("%w：商品列表筛选条件 %q 的取值过长", errInvalidInput, "game")
	}
	if props.MinPriceCents > 0 && props.MaxPriceCents > 0 && props.MinPriceCents > props.MaxPriceCents {
		return fmt.Errorf("%w：商品列表的价格区间下限高于上限", errInvalidInput)
	}
	return nil
}

// skuCatalogRuleProps 是 rules 字典（§16.3-11）里的取数规则。
type skuCatalogRuleProps struct {
	RuleCode   string
	DataSource string
	Sort       string
	Limit      int
	Filter     map[string]interface{}
}

// parseComponentRules 解析 rules 查询参数（JSON 对象）；键集合必须落在 pageRulesKeys 内。
func parseComponentRules(c fiber.Ctx) (skuCatalogRuleProps, error) {
	var rules skuCatalogRuleProps
	raw := strings.TrimSpace(c.Query("rules"))
	if raw == "" {
		return rules, nil
	}
	dict := map[string]interface{}{}
	if err := json.Unmarshal([]byte(raw), &dict); err != nil {
		return rules, fmt.Errorf("%w：取数规则 rules 须为 JSON 对象", errInvalidInput)
	}
	for key := range dict {
		if !pageRulesKeys[key] {
			return rules, fmt.Errorf("%w：取数规则不支持的键 %q", errInvalidInput, key)
		}
	}
	if v, ok := dict["rule_code"]; ok {
		rules.RuleCode = strings.TrimSpace(componentPropString(v))
	}
	if v, ok := dict["data_source"]; ok {
		rules.DataSource = strings.TrimSpace(componentPropString(v))
	}
	if v, ok := dict["sort"]; ok {
		rules.Sort = strings.TrimSpace(componentPropString(v))
	}
	if v, ok := dict["limit"]; ok {
		limit, err := componentPropInt64(v)
		if err != nil || limit < 1 {
			return rules, fmt.Errorf("%w：取数规则的 limit 取值非法", errInvalidInput)
		}
		rules.Limit = int(limit)
	}
	for _, key := range []string{"filter", "filters"} {
		v, ok := dict[key]
		if !ok {
			continue
		}
		sub, ok := v.(map[string]interface{})
		if !ok {
			return rules, fmt.Errorf("%w：取数规则的 %s 须为 JSON 对象", errInvalidInput, key)
		}
		if rules.Filter == nil {
			rules.Filter = map[string]interface{}{}
		}
		for dim, value := range sub {
			rules.Filter[dim] = value
		}
	}
	return rules, nil
}

// parseComponentFilterDict 解析 filter / filters 查询参数（JSON 对象，§16.2-05）。
func parseComponentFilterDict(c fiber.Ctx) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	for _, key := range []string{"filter", "filters"} {
		raw := strings.TrimSpace(c.Query(key))
		if raw == "" {
			continue
		}
		dict := map[string]interface{}{}
		if err := json.Unmarshal([]byte(raw), &dict); err != nil {
			return nil, fmt.Errorf("%w：组件筛选条件 %s 须为 JSON 对象", errInvalidInput, key)
		}
		for dim, value := range dict {
			out[dim] = value
		}
	}
	return out, nil
}

// applySkuCatalogFilter 把筛选条件字典落到 SKU 取数参数上；不适用的维度明确失败。
func applySkuCatalogFilter(props *skuCatalogProps, filter map[string]interface{}) error {
	for key, value := range filter {
		if !skuCatalogFilterDims[key] {
			return fmt.Errorf("%w：商品列表不支持的筛选条件 %q", errInvalidInput, key)
		}
		switch key {
		case "game":
			props.Game = strings.TrimSpace(componentPropString(value))
		case "status":
			props.Status = strings.TrimSpace(componentPropString(value))
		case "category_id":
			id, err := componentPropIDFromValue(value)
			if err != nil {
				return fmt.Errorf("%w：商品列表筛选条件 %q 的取值非法", errInvalidInput, key)
			}
			props.CategoryID = id
		case "sku_id":
			id, err := componentPropIDFromValue(value)
			if err != nil {
				return fmt.Errorf("%w：商品列表筛选条件 %q 的取值非法", errInvalidInput, key)
			}
			props.SkuIDs = []int{id}
		case "product_id":
			id, err := componentPropIDFromValue(value)
			if err != nil {
				return fmt.Errorf("%w：商品列表筛选条件 %q 的取值非法", errInvalidInput, key)
			}
			props.ProductIDs = []int{id}
		case "min_price_cents", "max_price_cents":
			amount, err := componentPropInt64(value)
			if err != nil || amount < 0 {
				return fmt.Errorf("%w：商品列表筛选条件 %q 的取值非法", errInvalidInput, key)
			}
			if key == "min_price_cents" {
				props.MinPriceCents = amount
			} else {
				props.MaxPriceCents = amount
			}
		}
	}
	return nil
}

// ── 取数（service 层：业务口径与查询） ──

// listPublicSkusByProps 按 product_list 的 props 取公开商品（仅在售；过滤只收窄）。
func listPublicSkusByProps(ctx context.Context, db *ent.Client, props skuCatalogProps) ([]*ent.EsportsSku, error) {
	switch props.DataSource {
	case "", "sku", "product", "manual", "category", "rule":
	default:
		return nil, fmt.Errorf("%w：商品列表不支持的数据来源 %q", errInvalidInput, props.DataSource)
	}
	if props.DataSource == "rule" && props.RuleCode == "" {
		return nil, fmt.Errorf("%w：商品列表的数据来源为规则时须同时指定 rule_code", errInvalidInput)
	}
	if props.DataSource == "product" && len(props.ProductIDs) == 0 {
		return nil, fmt.Errorf("%w：商品列表的数据来源为商品时须同时指定 product_id / product_ids", errInvalidInput)
	}
	if props.DataSource == "category" && props.CategoryID < 1 {
		return nil, fmt.Errorf("%w：商品列表的数据来源为分类时须同时指定分类", errInvalidInput)
	}
	if props.DataSource == "manual" && len(props.SkuIDs) == 0 && len(props.ProductIDs) == 0 {
		// 「手工指定」却没有任何 ID：明确空结果，不回落成全量在售。
		return []*ent.EsportsSku{}, nil
	}
	// 公开口径：只在售；status 只能配置成 on_sale（配置成下架/草稿必须明确失败而不是静默返回空）。
	if props.Status != "" && props.Status != string(esportssku.StatusOnSale) {
		return nil, fmt.Errorf("%w：公开商品列表仅返回在售商品，不支持按状态 %q 取数", errInvalidInput, props.Status)
	}
	limit := props.Limit
	if limit < 1 || limit > pageSkuListLimitMax {
		limit = pageSkuListLimitMax
	}
	q := db.EsportsSku.Query().Where(esportssku.StatusEQ(esportssku.StatusOnSale))
	if len(props.SkuIDs) > 0 {
		q.Where(esportssku.IDIn(props.SkuIDs...))
	}
	if len(props.ProductIDs) > 0 {
		q.Where(esportssku.ProductIDIn(props.ProductIDs...))
	}
	if props.CategoryID > 0 {
		q.Where(esportssku.CategoryIDIn(props.CategoryID))
	}
	if props.Game != "" {
		// SKU 无 game 字段：游戏分区落在其分类上（EsportsProductCategory.game）。
		ids, err := skuCategoryIDsForGame(ctx, db, props.Game)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return []*ent.EsportsSku{}, nil
		}
		q.Where(esportssku.CategoryIDIn(ids...))
	}
	if props.MinPriceCents > 0 {
		q.Where(esportssku.UnitPriceCentsGTE(props.MinPriceCents))
	}
	if props.MaxPriceCents > 0 {
		q.Where(esportssku.UnitPriceCentsLTE(props.MaxPriceCents))
	}
	q, err := applySkuCatalogOrder(q, props)
	if err != nil {
		return nil, err
	}
	return q.Limit(limit).All(ctx)
}

// skuCategoryIDsForGame 取某游戏分区下的商品分类 ID（SKU 经 category_id 挂到分类上）。
func skuCategoryIDsForGame(ctx context.Context, db *ent.Client, game string) ([]int, error) {
	items, err := db.EsportsProductCategory.Query().
		Where(esportsproductcategory.GameEQ(game)).
		Select(esportsproductcategory.FieldID).All(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids, nil
}

// applySkuCatalogOrder 应用排序：显式 sort 优先，其次 rule_code 决定的默认排序，否则沿用既有 ID 倒序。
func applySkuCatalogOrder(q *ent.EsportsSkuQuery, props skuCatalogProps) (*ent.EsportsSkuQuery, error) {
	sortKey := props.Sort
	if sortKey == "" {
		switch props.RuleCode {
		case "", "on_sale":
			sortKey = "newest"
		case "hot":
			sortKey = "hot"
		case "new", "recent":
			sortKey = "newest"
		case "discount", "member_only":
			return nil, fmt.Errorf("%w：商品列表暂不支持取数规则 %q（SKU 无折扣 / 会员专属标记）",
				errInvalidInput, props.RuleCode)
		default:
			return nil, fmt.Errorf("%w：商品列表不支持的取数规则 %q", errInvalidInput, props.RuleCode)
		}
	}
	switch sortKey {
	case "default", "newest":
		return q.Order(ent.Desc(esportssku.FieldID)), nil
	case "hot":
		// 热销复用既有 quota_sold（已售份数）字段。
		return q.Order(ent.Desc(esportssku.FieldQuotaSold), ent.Desc(esportssku.FieldID)), nil
	case "price_asc":
		return q.Order(ent.Asc(esportssku.FieldUnitPriceCents), ent.Desc(esportssku.FieldID)), nil
	case "price_desc":
		return q.Order(ent.Desc(esportssku.FieldUnitPriceCents), ent.Desc(esportssku.FieldID)), nil
	default:
		return nil, fmt.Errorf("%w：商品列表不支持的排序取值 %q", errInvalidInput, sortKey)
	}
}

// listPublicPlayersByProps 按 player_recommend 的 props 取推荐陪玩。
// 既有默认口径不变：资质合格 + 在线；game / level / online / bookable 只在默认之上继续收窄。
//
// 注意（口径限制，勿误以为它在任何数据下都能收窄）：bookable=true 复用既有字段与判定——
// 服务时段 0/0（未配置）、固排未开启（fixed_team_enabled=false）、接单上限 0（未配置）
// 在既有口径里都表示「不限」。因此这些字段在库里普遍为空时，bookable 的实际收窄效果≈0，
// 只等同于「在线且连接正常」；要让它真正生效，须先让陪玩档案填上服务时段 / 接单上限等配置。
func listPublicPlayersByProps(ctx context.Context, db *ent.Client, props playerRecommendProps, now time.Time) ([]publicPlayerView, error) {
	limit := props.Limit
	if limit < 1 || limit > playerRecommendScanCap {
		limit = 100
	}
	q := db.EsportsPlayer.Query().Where(
		esportsplayer.QualificationEQ(esportsplayer.QualificationQualified),
		esportsplayer.PresenceEQ(esportsplayer.PresenceOnline),
	)
	if levels := playerMinLevelValues(props.Level); len(levels) > 0 {
		q.Where(esportsplayer.LevelIn(levels...))
	}
	// 候选窗口取既有上限：游戏分区 / 连接 / 服务时段 / 容量都在内存里判定，
	// 先按 limit 截断会让「配置了筛选」的页面少返回合格陪玩。
	candidates, err := q.Order(ent.Desc(esportsplayer.FieldID)).Limit(playerRecommendScanCap).All(ctx)
	if err != nil {
		return nil, err
	}
	timeout := playerHeartbeatTimeout(ctx, db)
	var capacityTx *ent.Tx
	defer func() {
		if capacityTx != nil {
			_ = capacityTx.Rollback()
		}
	}()
	game := strings.TrimSpace(props.Game)
	level := strings.TrimSpace(props.Level)
	views := make([]publicPlayerView, 0, len(candidates))
	for _, player := range candidates {
		// 游戏分区沿用既有口径：档案 games 为空表示不限分区（§6.4 缺省）。
		if game != "" && len(player.Games) > 0 && !playerGamesContain(player.Games, game) {
			continue
		}
		if level != "" && level != "any" && playerLevelRank(player.Level.String()) < playerLevelRank(level) {
			continue
		}
		// 在线 / 可接单都要求连接正常（复用 playerConnectionHealthy 的心跳口径，不另立一套）。
		if (props.Online || props.Bookable) && !playerConnectionHealthyWith(player, now, timeout) {
			continue
		}
		if props.Bookable {
			// 可接单复用既有判定：服务时段（§6.4-06）∩ 固排时段 ∩ 并发 / 每日接单容量。
			if !playerServiceSlotAllows(player, now) || !playerFixedTeamSlotAllows(player, now) {
				continue
			}
			if player.ConcurrentLimit > 0 || player.DailyLimit > 0 {
				if capacityTx == nil {
					tx, txErr := db.Tx(ctx)
					if txErr != nil {
						return nil, txErr
					}
					capacityTx = tx
				}
				// nil 目标订单 = 只做「是否还有容量」的粗筛（checkPlayerDispatchLimit 的既有语义）。
				if limitErr := checkPlayerDispatchLimit(ctx, capacityTx, player, nil, now, 0); limitErr != nil {
					continue
				}
			}
		}
		games := player.Games
		if games == nil {
			games = []string{}
		}
		views = append(views, publicPlayerView{
			ID: player.ID, DisplayName: player.DisplayName, Level: player.Level.String(),
			Games: games, Qualification: player.Qualification.String(),
			Presence: player.Presence.String(), Online: player.Presence == esportsplayer.PresenceOnline,
		})
		if len(views) >= limit {
			break
		}
	}
	return views, nil
}

// playerMinLevelValues 把「最低技能等级」prop 展开为可下推 SQL 的等级集合。
// 等级序沿用既有 playerLevelRank（novice < skilled < senior < mentor），不另立一套；
// 空值 / any 表示不限等级。
func playerMinLevelValues(level string) []esportsplayer.Level {
	level = strings.TrimSpace(level)
	if level == "" || level == "any" {
		return nil
	}
	minRank := playerLevelRank(level)
	out := make([]esportsplayer.Level, 0, 4)
	for _, candidate := range []string{"novice", "skilled", "senior", "mentor"} {
		if playerLevelRank(candidate) >= minRank {
			out = append(out, esportsplayer.Level(candidate))
		}
	}
	return out
}

// ── 公共参数工具 ──

// componentPropLimit 解析 limit：沿用既有 queryLimit 的宽松口径（非法 / <1 / >500 回落 fallback），
// 具体上限由取数函数按各自既有上限再收紧（取更严者）。
func componentPropLimit(raw string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 1 || value > 500 {
		return fallback
	}
	return value
}

// componentIDList 解析 ID 列表：支持逗号分隔与 JSON 数组两种写法。
func componentIDList(raw string) ([]int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if strings.HasPrefix(raw, "[") {
		var values []int
		if err := json.Unmarshal([]byte(raw), &values); err != nil {
			return nil, fmt.Errorf("%w：ID 列表须为逗号分隔或 JSON 数组", errInvalidInput)
		}
		for _, value := range values {
			if value < 1 {
				return nil, fmt.Errorf("%w：ID 列表含非法值 %d", errInvalidInput, value)
			}
		}
		return values, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.Atoi(part)
		if err != nil || id < 1 {
			return nil, fmt.Errorf("%w：ID 列表含非法值 %q", errInvalidInput, part)
		}
		out = append(out, id)
	}
	return out, nil
}

func componentPropID(raw string) (int, error) {
	id, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || id < 1 {
		return 0, errInvalidInput
	}
	return id, nil
}

func componentPropIDFromValue(value interface{}) (int, error) {
	id, err := componentPropInt64(value)
	if err != nil || id < 1 {
		return 0, errInvalidInput
	}
	return int(id), nil
}

func componentPropInt64(value interface{}) (int64, error) {
	switch v := value.(type) {
	case float64:
		if v != float64(int64(v)) {
			return 0, errInvalidInput
		}
		return int64(v), nil
	case int:
		return int64(v), nil
	case int64:
		return v, nil
	case json.Number:
		return v.Int64()
	case string:
		return strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	default:
		return 0, errInvalidInput
	}
}

func componentPropString(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case bool:
		return strconv.FormatBool(v)
	default:
		return ""
	}
}

func componentPropBool(value interface{}) (bool, error) {
	switch v := value.(type) {
	case bool:
		return v, nil
	case float64:
		if v == 1 {
			return true, nil
		}
		if v == 0 {
			return false, nil
		}
		return false, errInvalidInput
	case string:
		return componentQueryBool(v)
	default:
		return false, errInvalidInput
	}
}

func componentQueryBool(raw string) (bool, error) {
	flag, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false, errInvalidInput
	}
	return flag, nil
}
