package esports

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsdispatchprioritygrant"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsservicesegment"
	"go-admin/internal/ent/systemconfig"
)

// ── 派单排序合成（加分项） ──
//
// 候选排序在窗口可见性与硬性过滤之后，把可计算的加分项按可配置权重合成一个排序分：
//   评分（综合评分均分） + 在途席位均衡 + 历史合作 + 客户偏好。
// 客服/客户指定陪玩（Preferred）仍硬置顶，不参与加权，语义不变。
//
// 优先派单权（名单名次）**不参与排序**：它只体现为独占优先通知，
// 名单名次仅作为候选的展示字段（ZoneRank）。

// 排序合成权重配置键（JSON，缺失或非法时逐字段回落默认值）。
const dispatchRankWeightsKey = "app.esports.dispatch_ranking_weights"

// dispatchRankWeights 是排序合成权重。权重为 0 表示该分量不参与排序；
// HistoryCap / BalanceCap 是历史合作与在途席位的归一化上限（须 >=1）；
// DistanceCapKm 是距离分量的归一化半径（km，须 >=1）：客户与陪玩距离 >= 该半径得 0 分。
//
// Zone 为历史配置项（app.esports.dispatch_ranking_weights 的 zone 字段），为保证既有配置
// 仍可解析而保留；优先派单权不参与排序，该权重不再影响排序结果。
type dispatchRankWeights struct {
	Rating     int `json:"rating"`
	History    int `json:"history"`
	Preference int `json:"preference"`
	Zone       int `json:"zone"`
	Balance    int `json:"balance"`
	// Distance / Timezone 是 新增分量，默认 0（不参与排序，保持既有行为不变）。
	Distance int `json:"distance"`
	Timezone int `json:"timezone"`
	// DistanceCapKm 距离归一化半径（km）：仅当 Distance > 0 时生效。
	DistanceCapKm int `json:"distance_cap_km"`
	HistoryCap    int `json:"history_cap"`
	BalanceCap    int `json:"balance_cap"`
}

// defaultDispatchRankDistanceCapKm 距离分量未配置归一化半径时的回落值（km）。
const defaultDispatchRankDistanceCapKm = 200

func defaultDispatchRankWeights() dispatchRankWeights {
	return dispatchRankWeights{
		Rating: 40, History: 30, Preference: 60, Zone: 30, Balance: 20,
		// 新增分量默认 0：不参与排序，既有部署的排序结果逐位不变。
		Distance: 0, Timezone: 0, DistanceCapKm: defaultDispatchRankDistanceCapKm,
		HistoryCap: 10, BalanceCap: 10,
	}
}

// dispatchRankWeightsFor 读取排序合成权重：整个 JSON 缺失/非法回落全默认；
// 单个字段缺失回落该字段默认，非法值（负权重、非正上限）回落默认。
func dispatchRankWeightsFor(ctx context.Context, db *ent.Client) dispatchRankWeights {
	w := defaultDispatchRankWeights()
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(dispatchRankWeightsKey)).Only(ctx)
	if err != nil {
		return w
	}
	// 用指针区分「字段缺失」与「显式给 0」：权重允许为 0（不参与排序）。
	var raw struct {
		Rating        *int `json:"rating"`
		History       *int `json:"history"`
		Preference    *int `json:"preference"`
		Zone          *int `json:"zone"`
		Balance       *int `json:"balance"`
		Distance      *int `json:"distance"`
		Timezone      *int `json:"timezone"`
		DistanceCapKm *int `json:"distance_cap_km"`
		HistoryCap    *int `json:"history_cap"`
		BalanceCap    *int `json:"balance_cap"`
	}
	if err := json.Unmarshal([]byte(item.Value), &raw); err != nil {
		return w
	}
	clampNonNegative := func(v *int) int {
		if v == nil {
			return 0 // 哨兵：不覆盖
		}
		if *v < 0 {
			return 0
		}
		return *v
	}
	if raw.Rating != nil {
		w.Rating = clampNonNegative(raw.Rating)
	}
	if raw.History != nil {
		w.History = clampNonNegative(raw.History)
	}
	if raw.Preference != nil {
		w.Preference = clampNonNegative(raw.Preference)
	}
	if raw.Zone != nil {
		w.Zone = clampNonNegative(raw.Zone)
	}
	if raw.Balance != nil {
		w.Balance = clampNonNegative(raw.Balance)
	}
	if raw.Distance != nil {
		w.Distance = clampNonNegative(raw.Distance)
	}
	if raw.Timezone != nil {
		w.Timezone = clampNonNegative(raw.Timezone)
	}
	if raw.DistanceCapKm != nil && *raw.DistanceCapKm >= 1 {
		w.DistanceCapKm = *raw.DistanceCapKm
	}
	if raw.HistoryCap != nil && *raw.HistoryCap >= 1 {
		w.HistoryCap = *raw.HistoryCap
	}
	if raw.BalanceCap != nil && *raw.BalanceCap >= 1 {
		w.BalanceCap = *raw.BalanceCap
	}
	return w
}

// dispatchRankFactors 是各排序分量归一化后的分值（0~100）。优先派单权不是排序分量。
type dispatchRankFactors struct {
	RatingScore     int
	HistoryScore    int
	PreferenceScore int
	BalanceScore    int
	// DistanceScore / TimezoneScore 为 的距离与时区分量（0~100）。
	DistanceScore int
	TimezoneScore int
}

// dispatchRankScore 按权重把各分量合成为排序分，越大越靠前。
func dispatchRankScore(w dispatchRankWeights, f dispatchRankFactors) int {
	return w.Rating*f.RatingScore + w.History*f.HistoryScore +
		w.Preference*f.PreferenceScore + w.Balance*f.BalanceScore +
		w.Distance*f.DistanceScore + w.Timezone*f.TimezoneScore
}

// ── 距离 / 时区分量 ──
//
// 参考点：订单所属客户档案的定位（latitude_e6 / longitude_e6）与时区（timezone）。
// 任一缺失即视为「无加分」（分值 0），不臆造位置，因此存量数据不会因新分量产生排序抖动。

// dispatchDistanceMeters 两点球面距离（米）。任一坐标非「有效微度」时第二个返回值为 false：
// (0,0) 在业务上表示未采集定位（几内亚湾），不作为真实坐标参与计算。
func dispatchDistanceMeters(latE6A, lonE6A, latE6B, lonE6B int) (int64, bool) {
	if (latE6A == 0 && lonE6A == 0) || (latE6B == 0 && lonE6B == 0) {
		return 0, false
	}
	const earthRadiusMeters = 6371000.0
	toRad := func(e6 int) float64 { return float64(e6) / 1e6 * math.Pi / 180 }
	lat1, lat2 := toRad(latE6A), toRad(latE6B)
	dLat := lat2 - lat1
	dLon := toRad(lonE6B) - toRad(lonE6A)
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1)*math.Cos(lat2)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return int64(2 * earthRadiusMeters * math.Asin(math.Min(1, math.Sqrt(h)))), true
}

// dispatchDistanceScore 把距离归一化为 0~100 的加分：同点 100 分，距离 >= 半径 0 分，线性衰减。
// capKm < 1 时回落默认半径。
func dispatchDistanceScore(meters int64, capKm int) int {
	if meters < 0 {
		return 0
	}
	if capKm < 1 {
		capKm = defaultDispatchRankDistanceCapKm
	}
	capMeters := int64(capKm) * 1000
	if meters >= capMeters {
		return 0
	}
	return int(100 - meters*100/capMeters)
}

// dispatchTimezoneScore 时区加分：双方时区非空且一致得 100 分，否则 0 分（未知不加分也不扣分）。
func dispatchTimezoneScore(playerTimezone, customerTimezone string) int {
	playerTimezone = strings.TrimSpace(playerTimezone)
	customerTimezone = strings.TrimSpace(customerTimezone)
	if playerTimezone == "" || customerTimezone == "" {
		return 0
	}
	if playerTimezone == customerTimezone {
		return 100
	}
	return 0
}

// normalizeCappedScore 把非负计数按上限归一化为 0~100（上限为 0 时返回 0，避免除零）。
func normalizeCappedScore(value, cap int) int {
	if cap <= 0 {
		return 0
	}
	if value > cap {
		value = cap
	}
	return value * 100 / cap
}

// activePriorityRanks 返回某游戏分区当前生效名单中「陪玩 → 排名」映射。
// 名单条目按 (game, period_start, player_id) 唯一，同陪玩同游戏同期只会有一条。
func activePriorityRanks(ctx context.Context, db *ent.Client, game string, now time.Time) (map[int]int, error) {
	ranks := map[int]int{}
	if strings.TrimSpace(game) == "" {
		return ranks, nil
	}
	grants, err := db.EsportsDispatchPriorityGrant.Query().Where(
		esportsdispatchprioritygrant.GameEQ(game),
		esportsdispatchprioritygrant.StatusEQ(esportsdispatchprioritygrant.StatusActive),
		esportsdispatchprioritygrant.PeriodStartLTE(now.UTC()),
		esportsdispatchprioritygrant.PeriodEndGT(now.UTC()),
	).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, grant := range grants {
		ranks[grant.PlayerID] = grant.Rank
	}
	return ranks, nil
}

// playerCustomerCompletedOrders 统计「陪玩 → 该陪玩与该客户的历史已完成订单数」。
// 口径：陪玩 finished 的服务段所关联订单中 customer_id 等于目标客户的订单数（按订单去重，
// 因 (order_id, player_id) 唯一，同一陪玩同一订单至多一个 finished 段）。
func playerCustomerCompletedOrders(ctx context.Context, db *ent.Client, playerIDs []int, customerID int) (map[int]int, error) {
	counts := make(map[int]int, len(playerIDs))
	if customerID < 1 || len(playerIDs) == 0 {
		return counts, nil
	}
	segments, err := db.EsportsServiceSegment.Query().Where(
		esportsservicesegment.PlayerIDIn(playerIDs...),
		esportsservicesegment.StatusEQ(esportsservicesegment.StatusFinished),
	).Select(esportsservicesegment.FieldPlayerID, esportsservicesegment.FieldOrderID).All(ctx)
	if err != nil {
		return nil, err
	}
	orderSet := make(map[int]bool, len(segments))
	for _, segment := range segments {
		orderSet[segment.OrderID] = true
	}
	orderIDs := make([]int, 0, len(orderSet))
	for id := range orderSet {
		orderIDs = append(orderIDs, id)
	}
	targetOrders, err := db.EsportsOrder.Query().Where(
		esportsorder.IDIn(orderIDs...),
		esportsorder.CustomerIDEQ(customerID),
	).Select(esportsorder.FieldID).All(ctx)
	if err != nil {
		return nil, err
	}
	target := make(map[int]bool, len(targetOrders))
	for _, order := range targetOrders {
		target[order.ID] = true
	}
	for _, segment := range segments {
		if target[segment.OrderID] {
			counts[segment.PlayerID]++
		}
	}
	return counts, nil
}

// ── 优先派单权自动计算（：排名制 / 积分制 / 达标制） ──
//
// 数据源全部复用既有字段，不新增外部数据源：
//   - ranking（排名制）：qualified 陪玩按综合评分均分降序，样本不足者不入选；
//   - points（积分制）：qualified 陪玩按历史完成订单数（finished 服务段去重）降序，无完成记录不入选；
//   - threshold（达标制）：qualified 陪玩中「评分 >= rating_min 且 完成订单数 >= completed_min」者，
//     按评分降序。
// 均为「宁缺毋滥」：候选不足 quota 时名单少于名额，不填满。资质/档期/接单上限等硬性过滤
// 仍在派单入口生效，名单预生成不绕过。

// 优先权自动计算阈值配置键（JSON）。
const dispatchPriorityAutoKey = "app.esports.dispatch_priority_auto"

// dispatchPriorityAutoConfig 是达标制的阈值。RatingMin 为最低均分（1~5），CompletedMin 为最低完成订单数。
type dispatchPriorityAutoConfig struct {
	RatingMin    float64 `json:"rating_min"`
	CompletedMin int     `json:"completed_min"`
}

func defaultDispatchPriorityAutoConfig() dispatchPriorityAutoConfig {
	return dispatchPriorityAutoConfig{RatingMin: 4.0, CompletedMin: 1}
}

// dispatchPriorityAutoFor 读取达标制阈值：JSON 缺失/非法回落默认；单字段非法回落该字段默认。
func dispatchPriorityAutoFor(ctx context.Context, db *ent.Client) dispatchPriorityAutoConfig {
	c := defaultDispatchPriorityAutoConfig()
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(dispatchPriorityAutoKey)).Only(ctx)
	if err != nil {
		return c
	}
	var parsed dispatchPriorityAutoConfig
	if err := json.Unmarshal([]byte(item.Value), &parsed); err != nil {
		return c
	}
	if parsed.RatingMin > 0 && parsed.RatingMin <= 5 {
		c.RatingMin = parsed.RatingMin
	}
	if parsed.CompletedMin >= 0 {
		c.CompletedMin = parsed.CompletedMin
	}
	return c
}

// priorityNominee 是自动计算产出的名单候选。
type priorityNominee struct {
	playerID int
	rank     int
}

// playerCompletedOrderCounts 统计「陪玩 → 历史完成订单数」（全局，finished 服务段去重）。
func playerCompletedOrderCounts(ctx context.Context, db *ent.Client, playerIDs []int) (map[int]int, error) {
	counts := make(map[int]int, len(playerIDs))
	if len(playerIDs) == 0 {
		return counts, nil
	}
	segments, err := db.EsportsServiceSegment.Query().Where(
		esportsservicesegment.PlayerIDIn(playerIDs...),
		esportsservicesegment.StatusEQ(esportsservicesegment.StatusFinished),
	).Select(esportsservicesegment.FieldPlayerID).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, segment := range segments {
		counts[segment.PlayerID]++
	}
	return counts, nil
}

// dispatchPriorityAutoSource 由本期名单的获得方式决定下一期自动计算口径：
// ranking > points > threshold > rotation（缺省延续）。人工指定与轮换仍走「延续上期」。
func dispatchPriorityAutoSource(grants []*ent.EsportsDispatchPriorityGrant) esportsdispatchprioritygrant.Source {
	hasRanking, hasPoints, hasThreshold := false, false, false
	for _, grant := range grants {
		switch grant.Source {
		case esportsdispatchprioritygrant.SourceRanking:
			hasRanking = true
		case esportsdispatchprioritygrant.SourcePoints:
			hasPoints = true
		case esportsdispatchprioritygrant.SourceThreshold:
			hasThreshold = true
		}
	}
	switch {
	case hasRanking:
		return esportsdispatchprioritygrant.SourceRanking
	case hasPoints:
		return esportsdispatchprioritygrant.SourcePoints
	case hasThreshold:
		return esportsdispatchprioritygrant.SourceThreshold
	default:
		return esportsdispatchprioritygrant.SourceRotation
	}
}

// autoComputeDispatchPriority 按获得方式自动计算下一期名单：候选池**先按游戏分区收窄**，
// 再在分区内取前 quota 名并返回（按 rank 升序，长度不超过 quota）。
//
// 分区口径与派单/优先通知阶段一致：player.Games 为空表示不限分区（缺省口径），
// 任何游戏都可入选；非空时必须包含 game。game 为空串时不做分区过滤（保持既有全量口径）。
//
// 生成阶段**不按服务时段 / 在线快照过滤**：名单是「本期资格」，可接单性由通知 / 派单阶段
// （activePriorityPlayerIDs 取当前可接单）判定。名单预生成发生在每天 01:00，若在生成时按
// 服务时段过滤，白天档陪玩会被整期排除，甚至产生空名单使该分区下期不再参与自动生成。
//
// 之所以先收窄再排名：名额是「每个游戏分区」的，若在全量合格陪玩里取名次，其它游戏（甚至无该
// 分区权限）的陪玩会占满名额，而通知阶段按分区/可接单过滤后该游戏可能一个有效陪玩都不剩。
// 收窄后 quota 即「该游戏分区内的名额」，因此 len(nominees) <= quota 恒成立。
func autoComputeDispatchPriority(ctx context.Context, db *ent.Client, source esportsdispatchprioritygrant.Source,
	game string, quota int) ([]priorityNominee, error) {
	if quota < 1 {
		return nil, errInvalidInput
	}
	// 仅用于分区匹配；写入侧沿用调用方已 trim 的 game。
	game = strings.TrimSpace(game)
	players, err := db.EsportsPlayer.Query().Where(
		esportsplayer.QualificationEQ(esportsplayer.QualificationQualified),
	).Order(ent.Asc(esportsplayer.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	// 候选池收窄：只排除分区不符的陪玩；不按服务时段 / 容量在生成阶段过滤（见函数注释）。
	filteredPlayers := make([]*ent.EsportsPlayer, 0, len(players))
	for _, player := range players {
		if game != "" && len(player.Games) > 0 && !playerGamesContain(player.Games, game) {
			continue
		}
		filteredPlayers = append(filteredPlayers, player)
	}
	players = filteredPlayers
	if len(players) == 0 {
		return nil, nil
	}
	playerIDs := make([]int, 0, len(players))
	for _, player := range players {
		playerIDs = append(playerIDs, player.ID)
	}
	completed, err := playerCompletedOrderCounts(ctx, db, playerIDs)
	if err != nil {
		return nil, err
	}
	cfg := dispatchPriorityAutoFor(ctx, db)

	type candidate struct {
		playerID  int
		rating    float64
		rated     bool
		completed int
	}
	candidates := make([]candidate, 0, len(players))
	for _, player := range players {
		c := candidate{playerID: player.ID, completed: completed[player.ID]}
		if source == esportsdispatchprioritygrant.SourceRanking ||
			source == esportsdispatchprioritygrant.SourceThreshold {
			rating, err := playerRatingSummary(ctx, db, player.ID)
			if err != nil {
				return nil, err
			}
			c.rating = rating.Average
			c.rated = !rating.Insufficient
		}
		candidates = append(candidates, c)
	}

	switch source {
	case esportsdispatchprioritygrant.SourceRanking:
		// 样本不足不入选。
		filtered := candidates[:0]
		for _, c := range candidates {
			if c.rated {
				filtered = append(filtered, c)
			}
		}
		candidates = filtered
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].rating != candidates[j].rating {
				return candidates[i].rating > candidates[j].rating
			}
			return candidates[i].playerID < candidates[j].playerID
		})
	case esportsdispatchprioritygrant.SourcePoints:
		// 无完成记录不入选。
		filtered := candidates[:0]
		for _, c := range candidates {
			if c.completed >= 1 {
				filtered = append(filtered, c)
			}
		}
		candidates = filtered
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].completed != candidates[j].completed {
				return candidates[i].completed > candidates[j].completed
			}
			return candidates[i].playerID < candidates[j].playerID
		})
	case esportsdispatchprioritygrant.SourceThreshold:
		filtered := candidates[:0]
		for _, c := range candidates {
			if c.rated && c.rating >= cfg.RatingMin && c.completed >= cfg.CompletedMin {
				filtered = append(filtered, c)
			}
		}
		candidates = filtered
		sort.SliceStable(candidates, func(i, j int) bool {
			if candidates[i].rating != candidates[j].rating {
				return candidates[i].rating > candidates[j].rating
			}
			return candidates[i].playerID < candidates[j].playerID
		})
	default:
		return nil, errInvalidInput
	}

	nominees := make([]priorityNominee, 0, quota)
	for i, c := range candidates {
		if i >= quota {
			break
		}
		nominees = append(nominees, priorityNominee{playerID: c.playerID, rank: i + 1})
	}
	return nominees, nil
}
