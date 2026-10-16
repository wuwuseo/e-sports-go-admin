package esports

import (
	"context"
	"encoding/json"
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

// ── 派单排序合成（§5.5 加分项） ──
//
// 候选排序在窗口可见性与硬性过滤之后，把可计算的加分项按可配置权重合成一个排序分：
//   评分（综合评分均分） + 在途席位均衡 + 历史合作 + 客户偏好 + 分区等级。
// 客服/客户指定陪玩（Preferred）仍硬置顶，不参与加权，语义不变。

// 排序合成权重配置键（JSON，缺失或非法时逐字段回落默认值）。
const dispatchRankWeightsKey = "app.esports.dispatch_ranking_weights"

// dispatchRankWeights 是排序合成权重。权重为 0 表示该分量不参与排序；
// HistoryCap / BalanceCap 是历史合作与在途席位的归一化上限（须 >=1）。
type dispatchRankWeights struct {
	Rating     int `json:"rating"`
	History    int `json:"history"`
	Preference int `json:"preference"`
	Zone       int `json:"zone"`
	Balance    int `json:"balance"`
	HistoryCap int `json:"history_cap"`
	BalanceCap int `json:"balance_cap"`
}

func defaultDispatchRankWeights() dispatchRankWeights {
	return dispatchRankWeights{
		Rating: 40, History: 30, Preference: 60, Zone: 30, Balance: 20,
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
		Rating     *int `json:"rating"`
		History    *int `json:"history"`
		Preference *int `json:"preference"`
		Zone       *int `json:"zone"`
		Balance    *int `json:"balance"`
		HistoryCap *int `json:"history_cap"`
		BalanceCap *int `json:"balance_cap"`
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
	if raw.HistoryCap != nil && *raw.HistoryCap >= 1 {
		w.HistoryCap = *raw.HistoryCap
	}
	if raw.BalanceCap != nil && *raw.BalanceCap >= 1 {
		w.BalanceCap = *raw.BalanceCap
	}
	return w
}

// dispatchRankFactors 是各排序分量归一化后的分值（0~100）。
type dispatchRankFactors struct {
	RatingScore     int
	HistoryScore    int
	PreferenceScore int
	ZoneScore       int
	BalanceScore    int
}

// dispatchRankScore 按权重把各分量合成为排序分，越大越靠前。
func dispatchRankScore(w dispatchRankWeights, f dispatchRankFactors) int {
	return w.Rating*f.RatingScore + w.History*f.HistoryScore +
		w.Preference*f.PreferenceScore + w.Zone*f.ZoneScore + w.Balance*f.BalanceScore
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

// dispatchZoneScore 分区等级分：不在当前生效名单为 0；在名单未排名（rank<1）给 60；
// rank=1 给 100，之后每降一名减 10，下限 10（rank 越小越靠前）。
func dispatchZoneScore(rank int, inList bool) int {
	if !inList {
		return 0
	}
	if rank < 1 {
		return 60
	}
	score := 100 - (rank-1)*10
	if score < 10 {
		score = 10
	}
	return score
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

// ── 优先派单权自动计算（§5.4：排名制 / 积分制 / 达标制） ──
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

// autoComputeDispatchPriority 按获得方式自动计算下一期名单（qualified 陪玩为候选池），
// 返回按 rank 升序、长度不超过 quota 的候选。
func autoComputeDispatchPriority(ctx context.Context, db *ent.Client, source esportsdispatchprioritygrant.Source, quota int) ([]priorityNominee, error) {
	if quota < 1 {
		return nil, errInvalidInput
	}
	players, err := db.EsportsPlayer.Query().Where(
		esportsplayer.QualificationEQ(esportsplayer.QualificationQualified),
	).Order(ent.Asc(esportsplayer.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
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
