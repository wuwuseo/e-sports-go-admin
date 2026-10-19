package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent"
	"go-admin/internal/ent/esportsdispatchprioritygrant"
	"go-admin/internal/ent/esportsplayer"
)

// 优先派单权下一期名单的游戏分区口径（§5.4 / §6.4）：
// 名额是「每个游戏分区」的，候选池必须先按分区收窄，再在分区内取名次；
// 生成阶段不按服务时段 / 在线快照过滤（可接单性由通知 / 派单阶段判定）。

// seedScopedPlayer 建一名合格在线陪玩，写入游戏分区（空切片表示不限分区），并播种一条已核验评价。
func seedScopedPlayer(t *testing.T, db *ent.Client, userID int, games []string,
	reviewOrderID, rating int) *ent.EsportsPlayer {
	t.Helper()
	player := seedPlayer(t, db, userID, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(player.ID).SetGames(games).SaveX(t.Context())
	seedRankingReview(t, db, reviewOrderID, 1, player.ID, rating)
	return player
}

// seedActivePriorityGrant 直接播种一条本期生效的名单条目（source 决定下一期的自动计算口径）。
func seedActivePriorityGrant(t *testing.T, db *ent.Client, game string, playerID, rank int,
	source esportsdispatchprioritygrant.Source, now time.Time) {
	t.Helper()
	start, end := dispatchPriorityPeriodWindow("weekly", now)
	db.EsportsDispatchPriorityGrant.Create().SetGame(game).SetPlayerID(playerID).
		SetPeriod(esportsdispatchprioritygrant.PeriodWeekly).
		SetPeriodStart(start).SetPeriodEnd(end).SetRank(rank).
		SetSource(source).SetStatus(esportsdispatchprioritygrant.StatusActive).SaveX(t.Context())
}

// nextPeriodPriorityGrants 读某游戏预生成的下一期 pending 名单（按 rank 升序）。
func nextPeriodPriorityGrants(t *testing.T, db *ent.Client, game string,
	now time.Time) []*ent.EsportsDispatchPriorityGrant {
	t.Helper()
	_, end := dispatchPriorityPeriodWindow("weekly", now)
	return db.EsportsDispatchPriorityGrant.Query().Where(
		esportsdispatchprioritygrant.GameEQ(game),
		esportsdispatchprioritygrant.PeriodStartEQ(end),
		esportsdispatchprioritygrant.StatusEQ(esportsdispatchprioritygrant.StatusPending),
	).Order(ent.Asc(esportsdispatchprioritygrant.FieldRank)).AllX(t.Context())
}

// 分区 A 无合格陪玩、分区 B 有多名高分陪玩：为 A 预生成下一期时不应把 B 的陪玩塞进 A 的名单。
func TestDispatchPriorityNextPeriodScopedByGame(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_priority_game_scope_a")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	gameA, gameB := "王者荣耀", "英雄联盟"
	setWithdrawalConfig(t, db, reviewMinSamplesKey, "1")
	setWithdrawalConfig(t, db, dispatchPriorityQuotaKey, "3")

	// 分区 B 的三名高分陪玩：若不按分区收窄，会被算进 A 的前三名并占满名额。
	first := seedScopedPlayer(t, db, 8101, []string{gameB}, 8101, 5)
	seedScopedPlayer(t, db, 8102, []string{gameB}, 8102, 4)
	seedScopedPlayer(t, db, 8103, []string{gameB}, 8103, 4)
	// A 本期名单里只有 B 的陪玩（存量错配数据），下一期重算时不应继续沿用他人占位。
	seedActivePriorityGrant(t, db, gameA, first.ID, 1, esportsdispatchprioritygrant.SourceRanking, now)

	if _, err := rebuildDispatchPriority(ctx, db, now, 100); err != nil {
		t.Fatalf("周期重算应成功，err=%v", err)
	}
	if next := nextPeriodPriorityGrants(t, db, gameA, now); len(next) != 0 {
		t.Fatalf("分区 A 无合格陪玩时不应产生任何名单（更不应塞入 B 分区陪玩），got=%+v", next)
	}
}

// 生成阶段不按服务时段过滤：不在服务时段内的同分区陪玩仍获得本期资格（可接单性在通知 / 派单阶段判定）。
func TestDispatchPriorityNextPeriodIgnoresServiceSlotAtGeneration(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_priority_game_scope_slot")
	defer db.Close()
	ctx := t.Context()
	// UTC+8 = 18:00；名单预生成实际发生在每天 01:00，因此生成阶段不能按「当前时刻的服务时段」剔除。
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	game := "王者荣耀"
	setWithdrawalConfig(t, db, reviewMinSamplesKey, "1")
	setWithdrawalConfig(t, db, dispatchPriorityQuotaKey, "3")

	top := seedScopedPlayer(t, db, 8201, []string{game}, 8201, 5)
	second := seedScopedPlayer(t, db, 8202, []string{game}, 8202, 4)
	// 当前时刻不在服务时段（UTC+8 00:00–01:00）的同分区陪玩：仍应保留本期资格。
	offSlot := seedScopedPlayer(t, db, 8203, []string{game}, 8203, 5)
	db.EsportsPlayer.UpdateOneID(offSlot.ID).
		SetServiceSlotStartMinute(0).SetServiceSlotEndMinute(60).SaveX(ctx)

	seedActivePriorityGrant(t, db, game, top.ID, 1, esportsdispatchprioritygrant.SourceRanking, now)

	if _, err := rebuildDispatchPriority(ctx, db, now, 100); err != nil {
		t.Fatalf("周期重算应成功，err=%v", err)
	}
	next := nextPeriodPriorityGrants(t, db, game, now)
	if len(next) != 3 {
		t.Fatalf("生成阶段不应按服务时段剔除，应预生成 3 名同分区陪玩，got=%d：%+v", len(next), next)
	}
	got := make(map[int]int, len(next))
	for _, grant := range next {
		got[grant.PlayerID] = grant.Rank
		if grant.Rank < 1 || grant.Rank > 3 {
			t.Fatalf("分区内名次应连续为 1..3：%+v", next)
		}
	}
	if _, ok := got[offSlot.ID]; !ok {
		t.Fatalf("服务时段外的同分区陪玩应保留本期资格：%+v", next)
	}
	if got[top.ID] != 1 {
		t.Fatalf("评分最高者应为第 1 名：%+v", next)
	}
	_ = second
}

// games 为空表示不限分区（§6.4 缺省口径）：可进入任意分区的名单。
func TestDispatchPriorityNextPeriodUnlimitedGames(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_priority_game_scope_unlimited")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	game, otherGame := "王者荣耀", "英雄联盟"
	setWithdrawalConfig(t, db, reviewMinSamplesKey, "1")
	setWithdrawalConfig(t, db, dispatchPriorityQuotaKey, "3")

	zoned := seedScopedPlayer(t, db, 8301, []string{game}, 8301, 4)
	// 不限分区：任何游戏分区都可入选。
	unlimited := seedScopedPlayer(t, db, 8302, []string{}, 8302, 5)
	// 其它分区的陪玩：不应进入 A 的名单。
	other := seedScopedPlayer(t, db, 8303, []string{otherGame}, 8303, 5)

	seedActivePriorityGrant(t, db, game, zoned.ID, 1, esportsdispatchprioritygrant.SourceRanking, now)

	if _, err := rebuildDispatchPriority(ctx, db, now, 100); err != nil {
		t.Fatalf("周期重算应成功，err=%v", err)
	}
	next := nextPeriodPriorityGrants(t, db, game, now)
	if len(next) != 2 {
		t.Fatalf("分区 A 应预生成 2 名陪玩（含不限分区者），got=%d：%+v", len(next), next)
	}
	if next[0].PlayerID != unlimited.ID || next[0].Rank != 1 ||
		next[1].PlayerID != zoned.ID || next[1].Rank != 2 {
		t.Fatalf("不限分区陪玩应按评分排在分区内名次：%+v", next)
	}
	for _, grant := range next {
		if grant.PlayerID == other.ID {
			t.Fatalf("其它分区的陪玩不应进入 A 的名单：%+v", grant)
		}
	}
}

// 分区内合格候选数超过 quota 时，只取分区内前 quota 名（名额按分区封顶）。
func TestDispatchPriorityNextPeriodTruncatesWithinGame(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_priority_game_scope_quota")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	game := "王者荣耀"
	setWithdrawalConfig(t, db, reviewMinSamplesKey, "1")
	setWithdrawalConfig(t, db, dispatchPriorityQuotaKey, "2")

	top := seedScopedPlayer(t, db, 8501, []string{game}, 8501, 5)
	second := seedScopedPlayer(t, db, 8502, []string{game}, 8502, 4)
	seedScopedPlayer(t, db, 8503, []string{game}, 8503, 3)
	seedActivePriorityGrant(t, db, game, top.ID, 1, esportsdispatchprioritygrant.SourceRanking, now)

	if _, err := rebuildDispatchPriority(ctx, db, now, 100); err != nil {
		t.Fatalf("周期重算应成功，err=%v", err)
	}
	next := nextPeriodPriorityGrants(t, db, game, now)
	if len(next) != 2 {
		t.Fatalf("分区内名额应封顶为 quota=2，got=%d：%+v", len(next), next)
	}
	if next[0].PlayerID != top.ID || next[1].PlayerID != second.ID {
		t.Fatalf("应按分区内评分取前 2 名：%+v", next)
	}
}

// game 传空串保持既有全量口径（回归保护）：仍按全部合格陪玩排名取名次。
func TestAutoComputeDispatchPriorityEmptyGameKeepsFullPool(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_priority_game_scope_empty_game")
	defer db.Close()
	ctx := t.Context()
	setWithdrawalConfig(t, db, reviewMinSamplesKey, "1")

	first := seedScopedPlayer(t, db, 8401, []string{"王者荣耀"}, 8401, 5)
	second := seedScopedPlayer(t, db, 8402, []string{"英雄联盟"}, 8402, 4)
	third := seedScopedPlayer(t, db, 8403, []string{}, 8403, 3)

	nominees, err := autoComputeDispatchPriority(
		ctx, db, esportsdispatchprioritygrant.SourceRanking, "", 10)
	if err != nil {
		t.Fatalf("全量口径计算应成功，err=%v", err)
	}
	if len(nominees) != 3 {
		t.Fatalf("game 为空时应保留全量候选池，got=%d：%+v", len(nominees), nominees)
	}
	wantIDs := []int{first.ID, second.ID, third.ID}
	for i, nominee := range nominees {
		if nominee.playerID != wantIDs[i] || nominee.rank != i+1 {
			t.Fatalf("全量口径名次不正确：%+v", nominees)
		}
	}
}
