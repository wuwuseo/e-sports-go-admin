package esports

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsdispatchprioritygrant"
	"go-admin/internal/ent/esportsdispatchprioritynotice"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/systemconfig"
)

// 优先派单权（§5.4）：按游戏分区授予的**派单排序加权**，不是独占权；仅作用于人工指派与派单排序，
// 抢单大厅先到先得、不受影响。
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

// dispatchPriorityInput 名单录入入参（§5.4）。next_period=true 表示预生成下一期名单。
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

// revokeDispatchPriority 中途人工移出名单（§5.4「中途可人工增减」）。
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

// rebuildDispatchPriority 周期重算（§5.4）：过期名单转 expired、到点名单转 active，
// 并在切换点前为下一期预生成名单（延续本期生效名单，source=rotation）。返回处理的条目数。
// ponytail: 排名制 / 积分制 / 达标制的**自动计算**未接入（缺少可用的排名、积分与达标数据源），
// 自动重算只做「上一期名单延续到下一期」；人工指定与人工增减不受影响。
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
		for i, grant := range grants {
			if i >= quota {
				break
			}
			if _, err = db.EsportsDispatchPriorityGrant.Create().
				SetGame(grant.Game).SetPlayerID(grant.PlayerID).
				SetPeriod(grant.Period).SetPeriodStart(nextStart).SetPeriodEnd(nextPeriodEnd).
				SetRank(i + 1).
				SetSource(esportsdispatchprioritygrant.SourceRotation).
				SetStatus(esportsdispatchprioritygrant.StatusPending).
				SetNote("由上期名单预生成").Save(ctx); err != nil {
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

// dispatchCandidate 是派单候选（§5.5 首版）。字段同时用于排序诊断，便于客服理解排序依据。
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
}

// dispatchCandidates 为订单列出**当前可派单对象**（§5.5 首版）。
//
// 可见性由优先通知窗口决定（§5.4 业务口径）：
//   - 订单处于优先通知中 → 只返回**被通知的名单内陪玩**（其他人在此也看不到，与抢单大厅一致）；
//   - 窗口已结束（超时 / 全部拒绝 / 客服提前释放）或本就没有名单 → 返回全部合格在线陪玩。
//
// 硬性条件：资质 qualified、在线（与人工指派的门槛一致）；已在该订单席位上的陪玩不重复列出。
// 排序：客服指定陪玩（preferredPlayerID）→ 评分高者优先（样本不足不当作低分、按同等处理）
//
//	→ 在途席位数少者优先（轮转均衡）→ 档案 ID 稳定兜底。
//
// ponytail: 游戏分区权限、技能等级不低于订单要求、历史合作与客户偏好、
// 距离或时区等加分项未实现（缺少相应数据源）；接单上限已在接单动作硬校验（§5.5），候选池未按上限过滤；
// 评分按每位候选单独计算，候选量级大时应改为一次聚合。
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
	requiredLevel := order.RequiredLevel.String()
	candidates := make([]*dispatchCandidate, 0, len(players))
	for _, player := range players {
		if occupied[player.ID] {
			continue
		}
		// 游戏分区权限 + 技能等级（§5.5）：不满足硬性条件的陪玩不出现在候选池。
		if err := checkPlayerGameLevel(player, game, requiredLevel); err != nil {
			continue
		}
		rating, err := playerRatingSummary(ctx, db, player.ID)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, &dispatchCandidate{
			PlayerID:           player.ID,
			DisplayName:        player.DisplayName,
			Level:              player.Level.String(),
			Qualification:      player.Qualification.String(),
			Presence:           player.Presence.String(),
			Preferred:          preferredPlayerID > 0 && player.ID == preferredPlayerID,
			FromPriorityNotice: notified[player.ID],
			RatingAverage:      rating.Average,
			RatingSamples:      rating.SampleCount,
			RatingInsuffici:    rating.Insufficient,
			ActiveSeats:        load[player.ID],
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		// 1) 客服指定陪玩最前。
		if a.Preferred != b.Preferred {
			return a.Preferred
		}
		// 2) 评分：样本充足的按均分降序；样本不足不当作低分，视作同等。
		aRated := !a.RatingInsuffici
		bRated := !b.RatingInsuffici
		if aRated != bRated {
			return aRated
		}
		if aRated && bRated && a.RatingAverage != b.RatingAverage {
			return a.RatingAverage > b.RatingAverage
		}
		// 4) 轮转均衡：在途席位少者优先。
		if a.ActiveSeats != b.ActiveSeats {
			return a.ActiveSeats < b.ActiveSeats
		}
		// 5) 稳定兜底，避免同分候选顺序抖动。
		return a.PlayerID < b.PlayerID
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates, nil
}
