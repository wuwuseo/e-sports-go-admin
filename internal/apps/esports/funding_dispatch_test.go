package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsdispatchprioritygrant"
	"go-admin/internal/ent/esportsdispatchprioritynotice"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
)

// 多席订单种子：已付款、待派单。
func seedDispatchOrderWithSeats(t *testing.T, db *ent.Client, orderNo string, skuID, seats int) *ent.EsportsOrder {
	t.Helper()
	return db.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(1).SetSkuID(skuID).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(seats).
		SetPaymentDeadline(time.Now().UTC().Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).
		SaveX(t.Context())
}

// ：优先派单权只体现为「独占优先通知」，不参与派单排序。
// 两个候选仅「是否在优先名单 / 名单名次」不同时，合成排序分必须相等（ZoneRank 仍作为展示字段保留）。
func TestDispatchPriorityExcludedFromRankingScore(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_priority_not_in_score")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"
	sku := seedGameSku(t, db, "FND-NORANK", game)

	first := seedPlayer(t, db, 13001, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	later := seedPlayer(t, db, 13002, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	outsider := seedPlayer(t, db, 13003, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	start, end := dispatchPriorityPeriodWindow("weekly", now)
	for _, item := range []struct {
		playerID int
		rank     int
	}{{first.ID, 1}, {later.ID, 4}} {
		db.EsportsDispatchPriorityGrant.Create().SetGame(game).SetPlayerID(item.playerID).
			SetPeriod(esportsdispatchprioritygrant.PeriodWeekly).
			SetPeriodStart(start).SetPeriodEnd(end).SetRank(item.rank).
			SetSource(esportsdispatchprioritygrant.SourceManual).
			SetStatus(esportsdispatchprioritygrant.StatusActive).SaveX(ctx)
	}

	order := seedPendingOrderWithSku(t, db, "FND-NORANK-1", sku.ID)
	candidates, err := dispatchCandidates(ctx, db, order, 0, 20, now)
	if err != nil {
		t.Fatalf("候选应成功，err=%v", err)
	}
	if len(candidates) != 3 {
		t.Fatalf("应列出 3 名候选，got=%d", len(candidates))
	}
	// 展示字段仍保留名单名次。
	zoneRanks := map[int]int{}
	for _, candidate := range candidates {
		zoneRanks[candidate.PlayerID] = candidate.ZoneRank
	}
	if zoneRanks[first.ID] != 1 || zoneRanks[later.ID] != 4 || zoneRanks[outsider.ID] != 0 {
		t.Fatalf("名单名次应保留为展示字段：%+v", zoneRanks)
	}
	// 排序分不得因优先名单或名次不同而不同。
	for _, candidate := range candidates {
		if candidate.Score != candidates[0].Score {
			t.Fatalf("优先派单权不应参与排序：player=%d score=%d zone_rank=%d，对照 player=%d score=%d zone_rank=%d",
				candidate.PlayerID, candidate.Score, candidate.ZoneRank,
				candidates[0].PlayerID, candidates[0].Score, candidates[0].ZoneRank)
		}
	}
}

// 指定陪玩仍硬置顶：即使其合成排序分低于他人（偏好权重置 0，仅靠排序分无法置顶）。
func TestPreferredPlayerHardTopsDespiteLowerScore(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_preferred_hard_top")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	setWithdrawalConfig(t, db, dispatchRankWeightsKey, `{"preference":0}`)
	sku := seedGameSku(t, db, "FND-PREF", "王者荣耀")

	withHistory := seedPlayer(t, db, 13101, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	preferred := seedPlayer(t, db, 13102, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	historyOrder := seedRankingOrder(t, db, "FND-PREF-OLD", sku.ID, 1, esportsorder.FulfillmentStatusCompleted)
	seedRankingFinishedSegment(t, db, historyOrder.ID, withHistory.ID)
	order := seedRankingOrder(t, db, "FND-PREF-NEW", sku.ID, 1, esportsorder.FulfillmentStatusPendingDispatch)

	candidates, err := dispatchCandidates(ctx, db, order, preferred.ID, 20, now)
	if err != nil {
		t.Fatalf("候选应成功，err=%v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("应列出 2 名候选，got=%d", len(candidates))
	}
	if candidates[0].PlayerID != preferred.ID || !candidates[0].Preferred {
		t.Fatalf("指定陪玩应硬置顶：%+v", candidates)
	}
	if candidates[0].Score >= candidates[1].Score {
		t.Fatalf("指定陪玩置顶不应依赖合成排序分：preferred_score=%d other_score=%d",
			candidates[0].Score, candidates[1].Score)
	}
}

// 多席订单释放优先通知后不得转 grabbing（大厅只开放单陪玩订单，转了无人可见 → 静默卡单）：
// 保持待派单，只把窗口置 released 并留释放原因/时间，作废待回应通知。
func TestReleaseMultiSeatPriorityKeepsPendingDispatch(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_multi_seat_release")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"
	listed := seedPlayer(t, db, 13201, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	sku := seedGameSku(t, db, "FND-MULTI", game)
	order := seedDispatchOrderWithSeats(t, db, "FND-MULTI-1", sku.ID, 2)
	openPriorityWindow(t, db, order, game, []int{listed.ID}, now.Add(10*time.Minute))

	reason := dispatchReleaseReasonStaff
	released, err := releaseDispatchPriorityNotice(ctx, db, order.ID, reason, now)
	if err != nil {
		t.Fatalf("多席订单释放应成功，err=%v", err)
	}
	if released.FulfillmentStatus != esportsorder.FulfillmentStatusPendingDispatch {
		t.Fatalf("多席订单释放后应保持待派单，got=%s", released.FulfillmentStatus)
	}
	if released.PriorityNoticeStatus != esportsorder.PriorityNoticeStatusReleased {
		t.Fatalf("优先通知窗口应置 released，got=%s", released.PriorityNoticeStatus)
	}
	if released.PriorityReleaseReason != reason || released.PriorityReleasedAt == nil {
		t.Fatalf("释放原因/时间应落库：reason=%q released_at=%v",
			released.PriorityReleaseReason, released.PriorityReleasedAt)
	}
	if db.EsportsDispatchPriorityNotice.Query().Where(
		esportsdispatchprioritynotice.OrderIDEQ(order.ID),
		esportsdispatchprioritynotice.StatusEQ(esportsdispatchprioritynotice.StatusNotified),
	).ExistX(ctx) {
		t.Fatal("释放后不应仍有待回应通知")
	}
}

// 单席订单释放后维持原行为：转 grabbing，且抢单大厅对名单外陪玩可见。
func TestReleaseSingleSeatPriorityEntersGrabHall(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_single_seat_release")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"
	listed := seedPlayer(t, db, 13301, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	outsider := seedPlayer(t, db, 13302, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	sku := seedGameSku(t, db, "FND-SINGLE", game)
	order := seedDispatchOrderWithSeats(t, db, "FND-SINGLE-1", sku.ID, 1)
	openPriorityWindow(t, db, order, game, []int{listed.ID}, now.Add(10*time.Minute))

	released, err := releaseDispatchPriorityNotice(ctx, db, order.ID, dispatchReleaseReasonStaff, now)
	if err != nil {
		t.Fatalf("单席订单释放应成功，err=%v", err)
	}
	if released.FulfillmentStatus != esportsorder.FulfillmentStatusGrabbing {
		t.Fatalf("单席订单释放后应转抢单大厅，got=%s", released.FulfillmentStatus)
	}
	hall, err := listGrabHall(ctx, db, outsider.ID, now)
	if err != nil {
		t.Fatalf("抢单大厅查询应成功，err=%v", err)
	}
	if len(hall) != 1 || hall[0].ID != order.ID {
		t.Fatalf("释放后单席订单应对全部陪玩可见：%+v", hall)
	}
}
