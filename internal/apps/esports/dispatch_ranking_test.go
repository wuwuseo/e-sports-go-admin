package esports

import (
	"fmt"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsdispatchprioritygrant"
	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsplayerreview"
	"go-admin/internal/ent/esportsservicesegment"
)

// seedRankingOrder 建一张指定客户、指定履约状态的单席位订单。
func seedRankingOrder(t *testing.T, db *ent.Client, orderNo string, skuID, customerID int,
	status esportsorder.FulfillmentStatus) *ent.EsportsOrder {
	t.Helper()
	return db.EsportsOrder.Create().SetOrderNo(orderNo).SetCustomerID(customerID).SetSkuID(skuID).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetPaymentDeadline(time.Now().UTC().Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(status).SaveX(t.Context())
}

// seedRankingFinishedSegment 建一条 finished 服务段，用于历史合作/完成订单数统计。
func seedRankingFinishedSegment(t *testing.T, db *ent.Client, orderID, playerID int) {
	t.Helper()
	db.EsportsServiceSegment.Create().SetOrderID(orderID).SetPlayerID(playerID).
		SetSeatIndex(1).SetStatus(esportsservicesegment.StatusFinished).
		SetStartedAt(time.Now().UTC()).SaveX(t.Context())
}

// seedRankingReview 建一条已核验、状态正常的客户评价（样本充足口径见 review_min_samples 配置）。
func seedRankingReview(t *testing.T, db *ent.Client, orderID, customerID, playerID, rating int) {
	t.Helper()
	db.EsportsPlayerReview.Create().SetOrderID(orderID).SetCustomerID(customerID).SetPlayerID(playerID).
		SetRating(rating).SetVerified(true).SetStatus(esportsplayerreview.StatusNormal).SaveX(t.Context())
}

// 历史合作加分：两名候选评分与席位相同时，与该客户有历史完成订单的陪玩排前。
func TestDispatchRankHistoryBoostsSort(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_rank_history")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"
	customer := saveCustomerOrFail(t, db, 11)
	sku := seedGameSku(t, db, "DRK-HIST", game)

	withHistory := seedPlayer(t, db, 4101, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	withoutHistory := seedPlayer(t, db, 4102, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	// 历史合作：与当前客户（customer）完成过 1 单。
	histOrder := seedRankingOrder(t, db, "DRK-HIST-OLD", sku.ID, customer.ID, esportsorder.FulfillmentStatusCompleted)
	seedRankingFinishedSegment(t, db, histOrder.ID, withHistory.ID)

	curOrder := seedRankingOrder(t, db, "DRK-HIST-NEW", sku.ID, customer.ID, esportsorder.FulfillmentStatusPendingDispatch)

	candidates, err := dispatchCandidates(ctx, db, curOrder, 0, 20, now)
	if err != nil {
		t.Fatalf("候选应成功，err=%v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("应列出 2 名候选，got=%d", len(candidates))
	}
	if candidates[0].PlayerID != withHistory.ID || candidates[0].HistoryCompleted != 1 {
		t.Fatalf("有历史合作的陪玩应排前：%+v", candidates[0])
	}
	if candidates[1].PlayerID != withoutHistory.ID || candidates[1].HistoryCompleted != 0 {
		t.Fatalf("无历史合作的陪玩应排后：%+v", candidates[1])
	}
}

// 分区等级加分：在生效优先派单权名单（rank 靠前）的陪玩排前。
func TestDispatchRankZoneBoostsSort(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_rank_zone")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"
	customer := saveCustomerOrFail(t, db, 11)
	sku := seedGameSku(t, db, "DRK-ZONE", game)

	listed := seedPlayer(t, db, 4201, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	outsider := seedPlayer(t, db, 4202, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	// listed 在本期生效名单 rank=1。
	start, end := dispatchPriorityPeriodWindow("weekly", now)
	db.EsportsDispatchPriorityGrant.Create().SetGame(game).SetPlayerID(listed.ID).
		SetPeriod(esportsdispatchprioritygrant.PeriodWeekly).
		SetPeriodStart(start).SetPeriodEnd(end).SetRank(1).
		SetSource(esportsdispatchprioritygrant.SourceManual).
		SetStatus(esportsdispatchprioritygrant.StatusActive).SaveX(ctx)

	order := seedRankingOrder(t, db, "DRK-ZONE-NEW", sku.ID, customer.ID, esportsorder.FulfillmentStatusPendingDispatch)
	candidates, err := dispatchCandidates(ctx, db, order, 0, 20, now)
	if err != nil {
		t.Fatalf("候选应成功，err=%v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("应列出 2 名候选，got=%d", len(candidates))
	}
	if candidates[0].PlayerID != listed.ID || candidates[0].ZoneRank != 1 {
		t.Fatalf("名单内陪玩应排前：%+v", candidates[0])
	}
	if candidates[1].PlayerID != outsider.ID || candidates[1].ZoneRank != 0 {
		t.Fatalf("名单外陪玩应排后：%+v", candidates[1])
	}
}

// 客户偏好：客服/客户指定陪玩硬置顶，优先级高于历史合作等加分。
func TestDispatchRankPreferenceBoostsSort(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_rank_pref")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	customer := saveCustomerOrFail(t, db, 11)
	sku := seedGameSku(t, db, "DRK-PREF", "王者荣耀")

	withHistory := seedPlayer(t, db, 4301, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	preferred := seedPlayer(t, db, 4302, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	// withHistory 有历史合作，但 preferred 被指定，仍应硬置顶。
	histOrder := seedRankingOrder(t, db, "DRK-PREF-OLD", sku.ID, customer.ID, esportsorder.FulfillmentStatusCompleted)
	seedRankingFinishedSegment(t, db, histOrder.ID, withHistory.ID)

	order := seedRankingOrder(t, db, "DRK-PREF-NEW", sku.ID, customer.ID, esportsorder.FulfillmentStatusPendingDispatch)
	candidates, err := dispatchCandidates(ctx, db, order, preferred.ID, 20, now)
	if err != nil {
		t.Fatalf("候选应成功，err=%v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("应列出 2 名候选，got=%d", len(candidates))
	}
	if candidates[0].PlayerID != preferred.ID || !candidates[0].Preferred {
		t.Fatalf("指定陪玩应硬置顶，即使他人历史合作更高：%+v", candidates[0])
	}
}

// 排序合成权重可配置且缺省回落：缺失/非法整体回落默认，单字段覆盖、负权重与非法上限回落。
func TestDispatchRankWeightsFallback(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_rank_weights")
	defer db.Close()
	ctx := t.Context()

	if w := dispatchRankWeightsFor(ctx, db); w != defaultDispatchRankWeights() {
		t.Fatalf("无配置应回落默认，got=%+v", w)
	}
	setWithdrawalConfig(t, db, dispatchRankWeightsKey, "{not-json")
	if w := dispatchRankWeightsFor(ctx, db); w != defaultDispatchRankWeights() {
		t.Fatalf("非法 JSON 应回落默认，got=%+v", w)
	}
	setWithdrawalConfig(t, db, dispatchRankWeightsKey, `{"rating":10,"history":0,"history_cap":5}`)
	w := dispatchRankWeightsFor(ctx, db)
	if w.Rating != 10 || w.History != 0 || w.HistoryCap != 5 ||
		w.Preference != defaultDispatchRankWeights().Preference ||
		w.Zone != defaultDispatchRankWeights().Zone ||
		w.Balance != defaultDispatchRankWeights().Balance {
		t.Fatalf("部分字段应覆盖、其余回落默认，got=%+v", w)
	}
	setWithdrawalConfig(t, db, dispatchRankWeightsKey, `{"rating":-1,"history_cap":0,"balance_cap":-3}`)
	w = dispatchRankWeightsFor(ctx, db)
	if w.Rating != 0 || w.HistoryCap != defaultDispatchRankWeights().HistoryCap ||
		w.BalanceCap != defaultDispatchRankWeights().BalanceCap {
		t.Fatalf("负权重应归零、非法上限应回落默认，got=%+v", w)
	}
}

// 排名制自动预生成：按综合评分均分降序取前 quota，样本不足者不入选。
func TestDispatchRankAutoRanking(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_rank_auto_ranking")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"
	customer := saveCustomerOrFail(t, db, 11)
	setWithdrawalConfig(t, db, reviewMinSamplesKey, "1")
	setWithdrawalConfig(t, db, dispatchPriorityQuotaKey, "2")

	high := seedPlayer(t, db, 4401, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	mid := seedPlayer(t, db, 4402, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	unrated := seedPlayer(t, db, 4403, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	seedRankingReview(t, db, 1, customer.ID, high.ID, 5)
	seedRankingReview(t, db, 2, customer.ID, mid.ID, 4)
	// unrated 无评价 → 样本不足，不入选。

	start, end := dispatchPriorityPeriodWindow("weekly", now)
	db.EsportsDispatchPriorityGrant.Create().SetGame(game).SetPlayerID(high.ID).
		SetPeriod(esportsdispatchprioritygrant.PeriodWeekly).
		SetPeriodStart(start).SetPeriodEnd(end).SetRank(1).
		SetSource(esportsdispatchprioritygrant.SourceRanking).
		SetStatus(esportsdispatchprioritygrant.StatusActive).SaveX(ctx)

	if _, err := rebuildDispatchPriority(ctx, db, now, 100); err != nil {
		t.Fatalf("周期重算应成功，err=%v", err)
	}
	next := db.EsportsDispatchPriorityGrant.Query().Where(
		esportsdispatchprioritygrant.GameEQ(game),
		esportsdispatchprioritygrant.PeriodStartEQ(end),
		esportsdispatchprioritygrant.StatusEQ(esportsdispatchprioritygrant.StatusPending),
		esportsdispatchprioritygrant.SourceEQ(esportsdispatchprioritygrant.SourceRanking),
	).Order(ent.Asc(esportsdispatchprioritygrant.FieldRank)).AllX(ctx)
	if len(next) != 2 || next[0].PlayerID != high.ID || next[0].Rank != 1 ||
		next[1].PlayerID != mid.ID || next[1].Rank != 2 {
		t.Fatalf("排名制下一期名单不正确：%+v", next)
	}
	for _, grant := range next {
		if grant.PlayerID == unrated.ID {
			t.Fatalf("样本不足陪玩不应入选排名制名单：%+v", next)
		}
	}
}

// 积分制自动预生成：按历史完成订单数降序取前 quota，无完成记录不入选。
func TestDispatchRankAutoPoints(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_rank_auto_points")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	game := "英雄联盟"
	customer := saveCustomerOrFail(t, db, 11)
	setWithdrawalConfig(t, db, dispatchPriorityQuotaKey, "2")

	top := seedPlayer(t, db, 4501, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	second := seedPlayer(t, db, 4502, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	zero := seedPlayer(t, db, 4503, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	for i := 1; i <= 3; i++ {
		o := seedRankingOrder(t, db, fmt.Sprintf("DRK-PT-OLD-%d", i), 1, customer.ID, esportsorder.FulfillmentStatusCompleted)
		seedRankingFinishedSegment(t, db, o.ID, top.ID)
	}
	o := seedRankingOrder(t, db, "DRK-PT-OLD-SECOND", 1, customer.ID, esportsorder.FulfillmentStatusCompleted)
	seedRankingFinishedSegment(t, db, o.ID, second.ID)
	// zero 无完成记录，不入选。

	start, end := dispatchPriorityPeriodWindow("weekly", now)
	db.EsportsDispatchPriorityGrant.Create().SetGame(game).SetPlayerID(top.ID).
		SetPeriod(esportsdispatchprioritygrant.PeriodWeekly).
		SetPeriodStart(start).SetPeriodEnd(end).SetRank(1).
		SetSource(esportsdispatchprioritygrant.SourcePoints).
		SetStatus(esportsdispatchprioritygrant.StatusActive).SaveX(ctx)

	if _, err := rebuildDispatchPriority(ctx, db, now, 100); err != nil {
		t.Fatalf("周期重算应成功，err=%v", err)
	}
	next := db.EsportsDispatchPriorityGrant.Query().Where(
		esportsdispatchprioritygrant.GameEQ(game),
		esportsdispatchprioritygrant.PeriodStartEQ(end),
		esportsdispatchprioritygrant.StatusEQ(esportsdispatchprioritygrant.StatusPending),
		esportsdispatchprioritygrant.SourceEQ(esportsdispatchprioritygrant.SourcePoints),
	).Order(ent.Asc(esportsdispatchprioritygrant.FieldRank)).AllX(ctx)
	if len(next) != 2 || next[0].PlayerID != top.ID || next[0].Rank != 1 ||
		next[1].PlayerID != second.ID || next[1].Rank != 2 {
		t.Fatalf("积分制下一期名单不正确：%+v", next)
	}
	for _, grant := range next {
		if grant.PlayerID == zero.ID {
			t.Fatalf("无完成记录的陪玩不应入选积分制名单：%+v", next)
		}
	}
}

// 达标制自动预生成：评分 >= rating_min 且完成订单数 >= completed_min 者入选，按评分降序。
func TestDispatchRankAutoThreshold(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_rank_auto_threshold")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"
	customer := saveCustomerOrFail(t, db, 11)
	setWithdrawalConfig(t, db, reviewMinSamplesKey, "1")
	setWithdrawalConfig(t, db, dispatchPriorityQuotaKey, "3")
	setWithdrawalConfig(t, db, dispatchPriorityAutoKey, `{"rating_min":4.0,"completed_min":1}`)

	qualified := seedPlayer(t, db, 4601, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	lowRating := seedPlayer(t, db, 4602, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	noCompleted := seedPlayer(t, db, 4603, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	// qualified：评分 5、完成 2 单 → 达标。
	seedRankingReview(t, db, 1, customer.ID, qualified.ID, 5)
	for i := 1; i <= 2; i++ {
		o := seedRankingOrder(t, db, fmt.Sprintf("DRK-TH-OLD-%d", i), 1, customer.ID, esportsorder.FulfillmentStatusCompleted)
		seedRankingFinishedSegment(t, db, o.ID, qualified.ID)
	}
	// lowRating：评分 3 不达标（完成再多也不行）。
	seedRankingReview(t, db, 3, customer.ID, lowRating.ID, 3)
	o := seedRankingOrder(t, db, "DRK-TH-LOW", 1, customer.ID, esportsorder.FulfillmentStatusCompleted)
	seedRankingFinishedSegment(t, db, o.ID, lowRating.ID)
	// noCompleted：评分 5 但完成 0 不达标。
	seedRankingReview(t, db, 4, customer.ID, noCompleted.ID, 5)

	start, end := dispatchPriorityPeriodWindow("weekly", now)
	db.EsportsDispatchPriorityGrant.Create().SetGame(game).SetPlayerID(qualified.ID).
		SetPeriod(esportsdispatchprioritygrant.PeriodWeekly).
		SetPeriodStart(start).SetPeriodEnd(end).SetRank(1).
		SetSource(esportsdispatchprioritygrant.SourceThreshold).
		SetStatus(esportsdispatchprioritygrant.StatusActive).SaveX(ctx)

	if _, err := rebuildDispatchPriority(ctx, db, now, 100); err != nil {
		t.Fatalf("周期重算应成功，err=%v", err)
	}
	next := db.EsportsDispatchPriorityGrant.Query().Where(
		esportsdispatchprioritygrant.GameEQ(game),
		esportsdispatchprioritygrant.PeriodStartEQ(end),
		esportsdispatchprioritygrant.StatusEQ(esportsdispatchprioritygrant.StatusPending),
		esportsdispatchprioritygrant.SourceEQ(esportsdispatchprioritygrant.SourceThreshold),
	).AllX(ctx)
	if len(next) != 1 || next[0].PlayerID != qualified.ID {
		t.Fatalf("达标制下一期名单应只含达标陪玩，got=%+v", next)
	}
}

// 硬性过滤不因加分被绕过：未合格、容量已满的陪玩即使历史合作/分区等级更高也不进候选。
func TestDispatchRankingDoesNotBypassHardFilters(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_rank_hard")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"
	customer := saveCustomerOrFail(t, db, 11)
	sku := seedGameSku(t, db, "DRK-HARD", game)

	pending := seedPlayer(t, db, 4701, esportsplayer.QualificationPending, esportsplayer.PresenceOnline)
	full := seedPlayer(t, db, 4702, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	ok := seedPlayer(t, db, 4703, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	// full 容量已满：concurrent_limit=1 且已有 1 个在途邀请席位，历史合作更高也不能绕过。
	db.EsportsPlayer.UpdateOneID(full.ID).SetConcurrentLimit(1).SaveX(ctx)
	start, end := dispatchPriorityPeriodWindow("weekly", now)
	db.EsportsDispatchPriorityGrant.Create().SetGame(game).SetPlayerID(full.ID).
		SetPeriod(esportsdispatchprioritygrant.PeriodWeekly).
		SetPeriodStart(start).SetPeriodEnd(end).SetRank(1).
		SetSource(esportsdispatchprioritygrant.SourceManual).
		SetStatus(esportsdispatchprioritygrant.StatusActive).SaveX(ctx)
	histOrder := seedRankingOrder(t, db, "DRK-HARD-OLD", sku.ID, customer.ID, esportsorder.FulfillmentStatusCompleted)
	seedRankingFinishedSegment(t, db, histOrder.ID, full.ID)
	otherOrder := seedRankingOrder(t, db, "DRK-HARD-BUSY", sku.ID, customer.ID, esportsorder.FulfillmentStatusPendingDispatch)
	db.EsportsDispatchSeat.Create().SetOrderID(otherOrder.ID).SetSeatIndex(1).
		SetPlayerID(full.ID).SetStatus(esportsdispatchseat.StatusInvited).
		SetSource(esportsdispatchseat.SourceAssign).
		SetExpiresAt(now.UTC().Add(time.Hour)).SaveX(ctx)

	order := seedRankingOrder(t, db, "DRK-HARD-NEW", sku.ID, customer.ID, esportsorder.FulfillmentStatusPendingDispatch)
	candidates, err := dispatchCandidates(ctx, db, order, 0, 20, now)
	if err != nil {
		t.Fatalf("候选应成功，err=%v", err)
	}
	if len(candidates) != 1 || candidates[0].PlayerID != ok.ID {
		t.Fatalf("硬性过滤应排除未合格与容量已满的陪玩，got=%+v", candidates)
	}
	for _, candidate := range candidates {
		if candidate.PlayerID == pending.ID || candidate.PlayerID == full.ID {
			t.Fatalf("未合格/容量已满陪玩不应因加分进入候选：%+v", candidate)
		}
	}
}
