package esports

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportscustomer"
	"go-admin/internal/ent/esportsdispatchprioritygrant"
	"go-admin/internal/ent/esportsdispatchprioritynotice"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsproductcategory"
	"go-admin/internal/ent/systemconfig"
)

// 优先派单权：按游戏分区授予的**独占优先通知**，不是排序加权；仅决定优先通知窗口内
// 谁能先看到并接单，抢单大厅在窗口结束后对所有陪玩开放。名单名次不参与派单排序。
const (
	// 每游戏每期名额上限；缺失或非法时回落默认值。
	dispatchPriorityQuotaKey     = "app.esports.dispatch_priority_quota"
	dispatchPriorityDefaultQuota = 3
	// 轮转负载统计窗口：窗口内当前占用的席位（邀请中 + 已接受）作为负载指标。
	dispatchPriorityLoadWindow = 7 * 24 * time.Hour
)

// dispatchPriorityQuota 读取每游戏名额上限。
func dispatchPriorityQuota(ctx context.Context, db *ent.Client) int {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(dispatchPriorityQuotaKey)).Only(ctx)
	if err != nil {
		return dispatchPriorityDefaultQuota
	}
	value := 0
	if _, scanErr := fmt.Sscanf(strings.TrimSpace(item.Value), "%d", &value); scanErr != nil || value < 1 {
		return dispatchPriorityDefaultQuota
	}
	return value
}

// dispatchPriorityPeriodWindow 按周期返回 [start, end)（UTC 自然周期）：
// daily 当日、weekly 本周一至下周一、biweekly 每月 1–15 日 / 16 日至次月 1 日、monthly 自然月。
func dispatchPriorityPeriodWindow(period string, now time.Time) (time.Time, time.Time) {
	utc := now.UTC()
	monthStart := time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC)
	switch period {
	case "daily":
		day := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
		return day, day.AddDate(0, 0, 1)
	case "biweekly":
		if utc.Day() <= 15 {
			return monthStart, monthStart.AddDate(0, 0, 15)
		}
		return monthStart.AddDate(0, 0, 15), monthStart.AddDate(0, 1, 0)
	case "monthly":
		return monthStart, monthStart.AddDate(0, 1, 0)
	default: // weekly
		day := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
		offset := (int(day.Weekday()) + 6) % 7 // 距本周一的天数
		start := day.AddDate(0, 0, -offset)
		return start, start.AddDate(0, 0, 7)
	}
}

func validDispatchPriorityPeriod(period string) bool {
	switch period {
	case "daily", "weekly", "biweekly", "monthly":
		return true
	}
	return false
}

func validDispatchPrioritySource(source string) bool {
	switch source {
	case "ranking", "points", "threshold", "rotation", "manual":
		return true
	}
	return false
}

// dispatchPriorityInput 名单录入入参。next_period=true 表示预生成下一期名单。
type dispatchPriorityInput struct {
	Game       string `json:"game"`
	PlayerID   int    `json:"player_id"`
	Period     string `json:"period"`
	Rank       int    `json:"rank"`
	Source     string `json:"source"`
	Note       string `json:"note"`
	NextPeriod bool   `json:"next_period"`
}

// grantDispatchPriority 授予优先派单权（人工指定或名单录入）。校验名额上限，
// 切换点前可预生成下一期（next_period），未到期的期落 pending、已到期的期直接 active。
func grantDispatchPriority(ctx context.Context, db *ent.Client, adminID int, in dispatchPriorityInput, now time.Time) (*ent.EsportsDispatchPriorityGrant, error) {
	in.Game = strings.TrimSpace(in.Game)
	in.Note = strings.TrimSpace(in.Note)
	if in.Game == "" || len(in.Game) > 64 || in.PlayerID < 1 || in.Rank < 0 ||
		len([]rune(in.Note)) > 255 ||
		!validDispatchPriorityPeriod(in.Period) || !validDispatchPrioritySource(in.Source) {
		return nil, errInvalidInput
	}
	start, end := dispatchPriorityPeriodWindow(in.Period, now)
	if in.NextPeriod {
		start, end = dispatchPriorityPeriodWindow(in.Period, end)
	}
	if _, err := db.EsportsPlayer.Get(ctx, in.PlayerID); err != nil {
		return nil, err
	}
	// P1-05-02：名单 game 必须能对上真实的商品分类游戏分区，否则该名单永远匹配不到任何订单，
	// 开窗逻辑会**静默跳过**（订单看起来「没进优先通知」）。这里显式报错并指出名单项，
	// 不允许把填错的游戏分区静默落库。
	// 口令：分区名按 SKU → 商品分类（category.game）匹配，因此必须有启用中的分类使用该 game。
	gameExists, err := db.EsportsProductCategory.Query().Where(
		esportsproductcategory.GameEQ(in.Game),
		esportsproductcategory.StatusEQ(esportsproductcategory.StatusEnabled),
	).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if !gameExists {
		return nil, fmt.Errorf("%w：名单项 game「%s」找不到对应的启用游戏分区（商品分类），"+
			"请先在产品分类里维护该游戏分区名，或改成与分类一致的游戏分区", errInvalidInput, in.Game)
	}
	// 先判重（同游戏同期同陪玩），再判名额：重复录入应给出更精确的原因，而不是笼统的「名额已满」。
	duplicated, err := db.EsportsDispatchPriorityGrant.Query().Where(
		esportsdispatchprioritygrant.GameEQ(in.Game),
		esportsdispatchprioritygrant.PeriodStartEQ(start),
		esportsdispatchprioritygrant.PlayerIDEQ(in.PlayerID),
		esportsdispatchprioritygrant.StatusIn(
			esportsdispatchprioritygrant.StatusPending, esportsdispatchprioritygrant.StatusActive),
	).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if duplicated {
		return nil, fmt.Errorf("%w：该陪玩本期已在该游戏的优先派单权名单中", errConflict)
	}
	quota := dispatchPriorityQuota(ctx, db)
	nameCount, err := db.EsportsDispatchPriorityGrant.Query().Where(
		esportsdispatchprioritygrant.GameEQ(in.Game),
		esportsdispatchprioritygrant.PeriodStartEQ(start),
		esportsdispatchprioritygrant.StatusIn(
			esportsdispatchprioritygrant.StatusPending, esportsdispatchprioritygrant.StatusActive),
	).Count(ctx)
	if err != nil {
		return nil, err
	}
	if nameCount >= quota {
		return nil, fmt.Errorf("%w：该游戏本期优先派单权名额已满（%d）", errInvalidInput, quota)
	}
	status := esportsdispatchprioritygrant.StatusPending
	if !now.UTC().Before(start) {
		status = esportsdispatchprioritygrant.StatusActive
	}
	grant, err := db.EsportsDispatchPriorityGrant.Create().
		SetGame(in.Game).SetPlayerID(in.PlayerID).
		SetPeriod(esportsdispatchprioritygrant.Period(in.Period)).
		SetPeriodStart(start).SetPeriodEnd(end).SetRank(in.Rank).
		SetSource(esportsdispatchprioritygrant.Source(in.Source)).
		SetStatus(status).SetGrantedByAdminID(adminID).SetNote(in.Note).Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, fmt.Errorf("%w：该陪玩本期已在该游戏的优先派单权名单中", errConflict)
		}
		return nil, err
	}
	return grant, nil
}

// revokeDispatchPriority 中途人工移出名单（「中途可人工增减」）。
func revokeDispatchPriority(ctx context.Context, db *ent.Client, adminID, id int, reason string, now time.Time) (*ent.EsportsDispatchPriorityGrant, error) {
	reason = strings.TrimSpace(reason)
	if id < 1 || reason == "" || len([]rune(reason)) > 255 {
		return nil, fmt.Errorf("%w：移出名单须填写原因", errInvalidInput)
	}
	changed, err := db.EsportsDispatchPriorityGrant.Update().Where(
		esportsdispatchprioritygrant.IDEQ(id),
		esportsdispatchprioritygrant.StatusIn(
			esportsdispatchprioritygrant.StatusPending, esportsdispatchprioritygrant.StatusActive),
	).SetStatus(esportsdispatchprioritygrant.StatusRevoked).
		SetRevokeReason(reason).SetGrantedByAdminID(adminID).AddVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errConflict
	}
	return db.EsportsDispatchPriorityGrant.Get(ctx, id)
}

// listDispatchPriority 列出名单条目，可按游戏与状态过滤，同一期按排名升序。
func listDispatchPriority(ctx context.Context, db *ent.Client, game, status string, limit int) ([]*ent.EsportsDispatchPriorityGrant, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	q := db.EsportsDispatchPriorityGrant.Query()
	if game = strings.TrimSpace(game); game != "" {
		q.Where(esportsdispatchprioritygrant.GameEQ(game))
	}
	if status = strings.TrimSpace(status); status != "" {
		switch status {
		case "pending", "active", "revoked", "expired":
			q.Where(esportsdispatchprioritygrant.StatusEQ(esportsdispatchprioritygrant.Status(status)))
		default:
			return nil, errInvalidInput
		}
	}
	return q.Order(
		ent.Desc(esportsdispatchprioritygrant.FieldPeriodStart),
		ent.Asc(esportsdispatchprioritygrant.FieldRank),
	).Limit(limit).All(ctx)
}

// ── 陪玩端「我的优先派单权」状态查询 ──
//
// 与 的通知列表互补：通知列表回答「现在有哪张单只通知了我」，本状态回答
// 「我这一期是否在名单里、怎么来的、有效期到什么时候、这个游戏分区还剩几个名额」。

// dispatchPriorityHolding 是一条持权记录。
type dispatchPriorityHolding struct {
	Game string `json:"game"`
	// Period 周期类型（daily/weekly/biweekly/monthly）。
	Period string `json:"period"`
	// PeriodStart / PeriodEnd 有效期（左闭右开）；period_end 之后条目由重算任务转 expired。
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`
	Rank        int       `json:"rank"`
	// Source 获得方式：ranking / points / threshold / rotation / manual。
	Source string `json:"source"`
	// Status 名单条目状态（active / pending）；本接口不返回 revoked / expired。
	Status string `json:"status"`
	// Active 是否当前生效（status=active 且 now ∈ [period_start, period_end)）。
	Active bool   `json:"active"`
	Note   string `json:"note"`
	// Quota / Used / Remaining 是该游戏该期的名额口径（Used 含 pending 与 active 条目）。
	Quota     int `json:"quota"`
	Used      int `json:"used"`
	Remaining int `json:"remaining"`
}

// dispatchPriorityGameQuota 按游戏汇总的名额占用。
type dispatchPriorityGameQuota struct {
	Game      string `json:"game"`
	Quota     int    `json:"quota"`
	Used      int    `json:"used"`
	Remaining int    `json:"remaining"`
}

// playerPriorityStatusView 是陪玩端优先派单权状态。
type playerPriorityStatusView struct {
	PlayerID int `json:"player_id"`
	// Holding 当前是否有生效中的优先派单权（pending 的下一期名单不算持有）。
	Holding  bool                        `json:"holding"`
	Holdings []dispatchPriorityHolding   `json:"holdings"`
	Games    []dispatchPriorityGameQuota `json:"games"`
}

// playerPriorityStatus 查询陪玩本人的优先派单权状态：
//   - 只返回未过期的 pending / active 条目（revoked / expired 属于历史，不在「当前是否持有」范围内）；
//   - Active 同时要求 now ∈ [period_start, period_end)，避免周期切换点前后把未生效名单误报为生效；
//   - Quota 取 app.esports.dispatch_priority_quota（缺失/非法回落 3），Used 按「同游戏同 period_start」的
//     未失效条目计数，Remaining = max(0, Quota − Used)。
func playerPriorityStatus(ctx context.Context, db *ent.Client, playerID int, now time.Time) (*playerPriorityStatusView, error) {
	if playerID < 1 {
		return nil, errInvalidInput
	}
	now = now.UTC()
	grants, err := db.EsportsDispatchPriorityGrant.Query().Where(
		esportsdispatchprioritygrant.PlayerIDEQ(playerID),
		esportsdispatchprioritygrant.StatusIn(
			esportsdispatchprioritygrant.StatusPending, esportsdispatchprioritygrant.StatusActive),
		esportsdispatchprioritygrant.PeriodEndGT(now),
	).Order(
		ent.Asc(esportsdispatchprioritygrant.FieldPeriodStart),
		ent.Asc(esportsdispatchprioritygrant.FieldRank),
	).Limit(50).All(ctx)
	if err != nil {
		return nil, err
	}
	view := &playerPriorityStatusView{
		PlayerID: playerID,
		Holdings: make([]dispatchPriorityHolding, 0, len(grants)),
		Games:    []dispatchPriorityGameQuota{},
	}
	// 名额占用按「游戏 + 期起点」统计（同游戏同期才有名额上限）。
	used := map[string]int{}
	quotaByGame := map[string]int{}
	seenGame := map[string]bool{}
	for _, grant := range grants {
		key := priorityQuotaKey(grant.Game, grant.PeriodStart)
		if _, ok := used[key]; !ok {
			count, err := db.EsportsDispatchPriorityGrant.Query().Where(
				esportsdispatchprioritygrant.GameEQ(grant.Game),
				esportsdispatchprioritygrant.PeriodStartEQ(grant.PeriodStart.UTC()),
				esportsdispatchprioritygrant.StatusIn(
					esportsdispatchprioritygrant.StatusPending, esportsdispatchprioritygrant.StatusActive),
			).Count(ctx)
			if err != nil {
				return nil, err
			}
			used[key] = count
		}
		if !seenGame[grant.Game] {
			seenGame[grant.Game] = true
			quotaByGame[grant.Game] = dispatchPriorityQuota(ctx, db)
		}
		quota := quotaByGame[grant.Game]
		remaining := quota - used[key]
		if remaining < 0 {
			remaining = 0
		}
		active := grant.Status == esportsdispatchprioritygrant.StatusActive &&
			!now.Before(grant.PeriodStart.UTC()) && now.Before(grant.PeriodEnd.UTC())
		if active {
			view.Holding = true
		}
		view.Holdings = append(view.Holdings, dispatchPriorityHolding{
			Game: grant.Game, Period: grant.Period.String(),
			PeriodStart: grant.PeriodStart.UTC(), PeriodEnd: grant.PeriodEnd.UTC(),
			Rank: grant.Rank, Source: grant.Source.String(), Status: grant.Status.String(),
			Active: active, Note: grant.Note,
			Quota: quota, Used: used[key], Remaining: remaining,
		})
	}
	for _, grant := range grants {
		if grant.Game == "" {
			continue
		}
		found := false
		for _, item := range view.Games {
			if item.Game == grant.Game {
				found = true
				break
			}
		}
		if found {
			continue
		}
		key := priorityQuotaKey(grant.Game, grant.PeriodStart)
		quota := quotaByGame[grant.Game]
		remaining := quota - used[key]
		if remaining < 0 {
			remaining = 0
		}
		view.Games = append(view.Games, dispatchPriorityGameQuota{
			Game: grant.Game, Quota: quota, Used: used[key], Remaining: remaining,
		})
	}
	return view, nil
}

// priorityQuotaKey 名额统计键：同游戏同「期起点」共用一份名额。
func priorityQuotaKey(game string, periodStart time.Time) string {
	return game + "|" + periodStart.UTC().Format(time.RFC3339)
}

// rebuildDispatchPriority 周期重算：过期名单转 expired、到点名单转 active，
// 并在切换点前为下一期预生成名单。获得方式为排名制/积分制/达标制的名单按各自口径自动计算
// （见 autoComputeDispatchPriority），轮换制与人工指定仍延续本期名单。返回处理的条目数。
func rebuildDispatchPriority(ctx context.Context, db *ent.Client, now time.Time, limit int) (int, error) {
	if limit < 1 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	utc := now.UTC()
	total := 0
	changed, err := db.EsportsDispatchPriorityGrant.Update().Where(
		esportsdispatchprioritygrant.StatusEQ(esportsdispatchprioritygrant.StatusActive),
		esportsdispatchprioritygrant.PeriodEndLTE(utc),
	).SetStatus(esportsdispatchprioritygrant.StatusExpired).AddVersion(1).Save(ctx)
	if err != nil {
		return total, err
	}
	total += changed
	changed, err = db.EsportsDispatchPriorityGrant.Update().Where(
		esportsdispatchprioritygrant.StatusEQ(esportsdispatchprioritygrant.StatusPending),
		esportsdispatchprioritygrant.PeriodStartLTE(utc),
	).SetStatus(esportsdispatchprioritygrant.StatusActive).AddVersion(1).Save(ctx)
	if err != nil {
		return total, err
	}
	total += changed
	// 预生成下一期：按「游戏 + 周期」分组，用本期生效名单生成下一期 pending 名单。
	active, err := db.EsportsDispatchPriorityGrant.Query().Where(
		esportsdispatchprioritygrant.StatusEQ(esportsdispatchprioritygrant.StatusActive),
		esportsdispatchprioritygrant.PeriodStartLTE(utc),
		esportsdispatchprioritygrant.PeriodEndGT(utc),
	).Order(ent.Asc(esportsdispatchprioritygrant.FieldRank)).Limit(limit).All(ctx)
	if err != nil {
		return total, err
	}
	type groupKey struct {
		game   string
		period string
	}
	groups := map[groupKey][]*ent.EsportsDispatchPriorityGrant{}
	for _, grant := range active {
		key := groupKey{game: grant.Game, period: grant.Period.String()}
		groups[key] = append(groups[key], grant)
	}
	quota := dispatchPriorityQuota(ctx, db)
	sourceNote := map[esportsdispatchprioritygrant.Source]string{
		esportsdispatchprioritygrant.SourceRanking:   "按排名制自动计算",
		esportsdispatchprioritygrant.SourcePoints:    "按积分制自动计算",
		esportsdispatchprioritygrant.SourceThreshold: "按达标制自动计算",
		esportsdispatchprioritygrant.SourceRotation:  "由上期名单预生成",
	}
	for key, grants := range groups {
		_, nextEnd := dispatchPriorityPeriodWindow(key.period, utc)
		nextStart, nextPeriodEnd := dispatchPriorityPeriodWindow(key.period, nextEnd)
		existing, err := db.EsportsDispatchPriorityGrant.Query().Where(
			esportsdispatchprioritygrant.GameEQ(key.game),
			esportsdispatchprioritygrant.PeriodStartEQ(nextStart),
		).Count(ctx)
		if err != nil {
			return total, err
		}
		if existing > 0 {
			// 下一期名单已存在（人工录入或此前已预生成），不覆盖。
			continue
		}
		// 获得方式决定下一期口径：排名/积分/达标按各自规则自动计算，轮换与人工指定延续上期。
		source := dispatchPriorityAutoSource(grants)
		var nominees []priorityNominee
		if source == esportsdispatchprioritygrant.SourceRotation {
			for i, grant := range grants {
				if i >= quota {
					break
				}
				nominees = append(nominees, priorityNominee{playerID: grant.PlayerID, rank: i + 1})
			}
		} else {
			// 按本游戏分区取名额：候选池在分区内收窄后再排名，避免其它游戏占用名额。
			nominees, err = autoComputeDispatchPriority(ctx, db, source, key.game, quota)
			if err != nil {
				return total, err
			}
		}
		for _, nominee := range nominees {
			if _, err = db.EsportsDispatchPriorityGrant.Create().
				SetGame(key.game).SetPlayerID(nominee.playerID).
				SetPeriod(esportsdispatchprioritygrant.Period(key.period)).
				SetPeriodStart(nextStart).SetPeriodEnd(nextPeriodEnd).
				SetRank(nominee.rank).
				SetSource(source).
				SetStatus(esportsdispatchprioritygrant.StatusPending).
				SetNote(sourceNote[source]).Save(ctx); err != nil {
				if ent.IsConstraintError(err) {
					continue
				}
				return total, err
			}
			total++
		}
	}
	return total, nil
}

// orderGame 解析订单所属游戏分区（SKU → 分类 → 游戏）；解析不到返回空串，表示不受优先派单权影响。
func orderGame(ctx context.Context, db *ent.Client, skuID int) (string, error) {
	return orderGameWith(ctx, db.EsportsSku, db.EsportsProductCategory, skuID)
}

// orderGameIn 与 orderGame 同逻辑，供事务内（tx）复用。
func orderGameIn(ctx context.Context, tx *ent.Tx, skuID int) (string, error) {
	return orderGameWith(ctx, tx.EsportsSku, tx.EsportsProductCategory, skuID)
}

// orderGameWith 由 SKU 解析所属分类的游戏分区。Ent 的 tx 与 db 客户端类型一致，故可共用。
func orderGameWith(ctx context.Context,
	skus *ent.EsportsSkuClient, categories *ent.EsportsProductCategoryClient, skuID int,
) (string, error) {
	sku, err := skus.Get(ctx, skuID)
	if err != nil {
		if ent.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	if sku.CategoryID == nil {
		return "", nil
	}
	category, err := categories.Get(ctx, *sku.CategoryID)
	if err != nil {
		if ent.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(category.Game), nil
}

// dispatchCandidate 是派单候选（首版）。字段同时用于排序诊断，便于客服理解排序依据。
type dispatchCandidate struct {
	PlayerID      int    `json:"player_id"`
	DisplayName   string `json:"display_name"`
	Level         string `json:"level"`
	Qualification string `json:"qualification"`
	Presence      string `json:"presence"`
	// 客服（或客户）指定陪玩，仅作提示置顶，不是优先派单权。
	Preferred bool `json:"preferred"`
	// 该候选来自本单的优先通知名单（窗口内可见）；窗口外恒为 false。
	FromPriorityNotice bool    `json:"from_priority_notice"`
	RatingAverage      float64 `json:"rating_average"`
	RatingSamples      int     `json:"rating_samples"`
	RatingInsuffici    bool    `json:"rating_insufficient"`
	ActiveSeats        int     `json:"active_seats"`
	// 历史合作：该陪玩与该客户的历史已完成订单数（加分项）。
	HistoryCompleted int `json:"history_completed"`
	// 分区等级：当前生效优先派单权名单内的排名，0 表示不在名单或未排名。
	// 仅作展示（客服了解名单情况），不参与排序。
	ZoneRank int `json:"zone_rank"`
	// 距客户的直线距离（米，）；0 表示客户或本人未采集定位（未知，不加分）。
	DistanceMeters int64 `json:"distance_meters"`
	// 是否与客户档案同一天时区；任一方未配置时区时为 false。
	TimezoneMatched bool `json:"timezone_matched"`
	// 合成排序分：各分量按配置权重加权，越大越靠前；仅作排序诊断。
	Score int `json:"score"`
}

// dispatchCandidates 为订单列出**当前可派单对象**（首版）。
//
// 可见性由优先通知窗口决定（业务口径）：
//   - 订单处于优先通知中 → 只返回**被通知的名单内陪玩**（其他人在此也看不到，与抢单大厅一致）；
//   - 窗口已结束（超时 / 全部拒绝 / 客服提前释放）或本就没有名单 → 返回全部合格在线陪玩。
//
// 硬性条件：资质 qualified、在线（与人工指派的门槛一致）；已在该订单席位上的陪玩不重复列出。
// 排序：客服指定陪玩（preferredPlayerID）最前；其余按「评分 / 在途席位均衡 / 历史合作 /
// 客户偏好 / 距离 / 时区」以可配置权重合成排序分降序，同分按档案 ID 稳定兜底。
// 优先派单权名单名次**不参与排序**，只在候选上保留 ZoneRank 展示字段。
// 距离与时区以订单所属客户档案的定位/时区为参考点，权重默认 0（不参与排序）。
//
// ponytail: 距离为球面直线距离，未接实时轨迹/导航距离；真实推送未接入。
// 评分与历史合作按候选逐个聚合，候选量级大时可改为一次批量聚合。
func dispatchCandidates(ctx context.Context, db *ent.Client, order *ent.EsportsOrder,
	preferredPlayerID, limit int, now time.Time) ([]*dispatchCandidate, error) {
	if order == nil {
		return nil, errInvalidInput
	}
	if limit < 1 || limit > 50 {
		limit = 20
	}
	// 窗口内的可见范围：只有被通知的人。窗口外为空集合，表示不限制。
	notified := map[int]bool{}
	windowOpen := dispatchPriorityWindowOpen(order, now)
	if windowOpen {
		notices, err := db.EsportsDispatchPriorityNotice.Query().Where(
			esportsdispatchprioritynotice.OrderIDEQ(order.ID),
			esportsdispatchprioritynotice.StatusEQ(esportsdispatchprioritynotice.StatusNotified),
		).Select(esportsdispatchprioritynotice.FieldPlayerID).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, notice := range notices {
			notified[notice.PlayerID] = true
		}
	}
	// 已在本订单席位上的陪玩不再作为候选。
	seatRows, err := db.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.OrderIDEQ(order.ID),
		esportsdispatchseat.PlayerIDNotNil(),
		esportsdispatchseat.StatusIn(
			esportsdispatchseat.StatusInvited, esportsdispatchseat.StatusAccepted),
	).Select(esportsdispatchseat.FieldPlayerID).All(ctx)
	if err != nil {
		return nil, err
	}
	occupied := map[int]bool{}
	for _, seat := range seatRows {
		if seat.PlayerID != nil {
			occupied[*seat.PlayerID] = true
		}
	}
	playerQuery := db.EsportsPlayer.Query().Where(
		esportsplayer.QualificationEQ(esportsplayer.QualificationQualified),
		// P1-01-03：连接异常（protected_abnormal）不得进入派单候选——「连接异常暂缓新派单」。
		// 候选池只取在线陪玩，再由 playerEligibleForDispatch 复核心跳新鲜度。
		esportsplayer.PresenceEQ(esportsplayer.PresenceOnline),
	)
	if windowOpen {
		if len(notified) == 0 {
			return []*dispatchCandidate{}, nil
		}
		ids := make([]int, 0, len(notified))
		for id := range notified {
			ids = append(ids, id)
		}
		playerQuery.Where(esportsplayer.IDIn(ids...))
	}
	players, err := playerQuery.Order(ent.Asc(esportsplayer.FieldID)).Limit(200).All(ctx)
	if err != nil {
		return nil, err
	}
	// 负载：统计窗口内当前占用（邀请中 + 已接受）的席位数，用于轮转均衡。
	loadRows, err := db.EsportsDispatchSeat.Query().Where(
		esportsdispatchseat.PlayerIDNotNil(),
		esportsdispatchseat.StatusIn(
			esportsdispatchseat.StatusInvited, esportsdispatchseat.StatusAccepted),
		esportsdispatchseat.CreatedAtGTE(now.UTC().Add(-dispatchPriorityLoadWindow)),
	).Select(esportsdispatchseat.FieldPlayerID).All(ctx)
	if err != nil {
		return nil, err
	}
	load := map[int]int{}
	for _, seat := range loadRows {
		if seat.PlayerID != nil {
			load[*seat.PlayerID]++
		}
	}
	game, err := orderGame(ctx, db, order.SkuID)
	if err != nil {
		return nil, err
	}
	requiredLevel := order.RequiredLevel
	levels := skillLevels(ctx, db)
	// 批量收集排序加分项所需数据：历史合作与分区等级、可配置权重。
	playerIDs := make([]int, 0, len(players))
	for _, player := range players {
		playerIDs = append(playerIDs, player.ID)
	}
	historyCounts, err := playerCustomerCompletedOrders(ctx, db, playerIDs, order.CustomerID)
	if err != nil {
		return nil, err
	}
	zoneRanks, err := activePriorityRanks(ctx, db, game, now)
	if err != nil {
		return nil, err
	}
	weights := dispatchRankWeightsFor(ctx, db)
	// ：距离/时区的参考点是订单所属客户档案；客户不存在或无定位时不产生任何加分。
	var customerTZ string
	customerLatE6, customerLonE6 := 0, 0
	if order.CustomerID > 0 {
		customer, err := db.EsportsCustomer.Query().Where(esportscustomer.IDEQ(order.CustomerID)).Only(ctx)
		if err != nil && !ent.IsNotFound(err) {
			return nil, err
		}
		if customer != nil {
			customerTZ = customer.Timezone
			customerLatE6, customerLonE6 = customer.LatitudeE6, customer.LongitudeE6
		}
	}
	capacityTx, err := db.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer capacityTx.Rollback()
	candidates := make([]*dispatchCandidate, 0, len(players))
	for _, player := range players {
		if occupied[player.ID] {
			continue
		}
		// ：连接正常（在线 + 心跳新鲜度）；有在途订单的保护异常陪玩保留资格。
		eligible, err := playerEligibleForDispatch(ctx, db, player, now)
		if err != nil {
			return nil, err
		}
		if !eligible {
			continue
		}
		// 游戏分区权限 + 技能等级：不满足硬性条件的陪玩不出现在候选池。
		if err := checkPlayerGameLevel(levels, player, game, requiredLevel); err != nil {
			continue
		}
		// ：服务时段外 / 可接单价范围外的陪玩不作为候选（不给注定接不了的单）。
		if !playerServiceSlotAllows(player, now) || !playerAcceptsOrderPrice(player, order) {
			continue
		}
		if err := checkPlayerDispatchLimit(ctx, capacityTx, player, order, now, 0); err != nil {
			if errors.Is(err, errInvalidInput) {
				continue
			}
			return nil, err
		}
		rating, err := playerRatingSummary(ctx, db, player.ID)
		if err != nil {
			return nil, err
		}
		// 合成排序分：评分 / 在途席位均衡 / 历史合作 / 客户偏好。
		// 优先派单权（名单名次）不参与排序，只保留 ZoneRank 展示字段。
		ratingScore := 0
		if !rating.Insufficient {
			ratingScore = int(rating.Average * 20) // 均分 1~5 → 0~100
		}
		preferenceScore := 0
		if preferredPlayerID > 0 && player.ID == preferredPlayerID {
			preferenceScore = 100
		}
		zoneRank := zoneRanks[player.ID]
		// ：距离/时区加分（权重为 0 时对排序无影响，但展示字段照常给出）。
		distanceMeters, located := dispatchDistanceMeters(player.LatitudeE6, player.LongitudeE6,
			customerLatE6, customerLonE6)
		if !located {
			distanceMeters = 0
		}
		timezoneMatched := false
		if strings.TrimSpace(customerTZ) != "" &&
			strings.TrimSpace(player.Timezone) == strings.TrimSpace(customerTZ) {
			timezoneMatched = true
		}
		score := dispatchRankScore(weights, dispatchRankFactors{
			RatingScore:     ratingScore,
			HistoryScore:    normalizeCappedScore(historyCounts[player.ID], weights.HistoryCap),
			PreferenceScore: preferenceScore,
			BalanceScore:    normalizeCappedScore(load[player.ID], weights.BalanceCap),
			DistanceScore:   dispatchDistanceScore(distanceMeters, weights.DistanceCapKm),
			TimezoneScore:   dispatchTimezoneScore(player.Timezone, customerTZ),
		})
		candidates = append(candidates, &dispatchCandidate{
			PlayerID:           player.ID,
			DisplayName:        player.DisplayName,
			Level:              player.Level,
			Qualification:      player.Qualification.String(),
			Presence:           player.Presence.String(),
			Preferred:          preferredPlayerID > 0 && player.ID == preferredPlayerID,
			FromPriorityNotice: notified[player.ID],
			RatingAverage:      rating.Average,
			RatingSamples:      rating.SampleCount,
			RatingInsuffici:    rating.Insufficient,
			ActiveSeats:        load[player.ID],
			HistoryCompleted:   historyCounts[player.ID],
			ZoneRank:           zoneRank,
			DistanceMeters:     distanceMeters,
			TimezoneMatched:    timezoneMatched,
			Score:              score,
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		// 客服/客户指定陪玩最前（硬置顶，不参与加权）。
		if a.Preferred != b.Preferred {
			return a.Preferred
		}
		// 合成排序分降序，同分按档案 ID 稳定兜底，避免顺序抖动。
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return a.PlayerID < b.PlayerID
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, nil
}
