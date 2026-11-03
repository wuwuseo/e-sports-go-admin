package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsdispatchseat"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
)

// 游戏分区权限 + 技能等级：分区不符、等级不足都被拒，满足者通过；候选池同步过滤。
func TestPlayerGameLevelGuard(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_game_level_guard")
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"

	sku := seedGameSku(t, db, "GL", game)
	db.EsportsSku.UpdateOneID(sku.ID).SetRequiredLevel("senior").SaveX(ctx)

	order := db.EsportsOrder.Create().SetOrderNo("GL-1").SetCustomerID(1).SetSkuID(sku.ID).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetRequiredLevel("senior").
		SetPaymentDeadline(now.UTC().Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).
		SaveX(ctx)

	// 分区不符：games 只含「英雄联盟」。
	wrongGame := seedPlayer(t, db, 9501, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(wrongGame.ID).SetGames([]string{"英雄联盟"}).SaveX(ctx)

	// 等级不足：games 含 game，但 level=novice < senior。
	lowLevel := seedPlayer(t, db, 9502, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(lowLevel.ID).
		SetGames([]string{game}).SetLevel("novice").
		SetGameLevels(map[string]string{game: "novice"}).SaveX(ctx)

	// 满足：games 含 game + level=mentor。
	ok := seedPlayer(t, db, 9503, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(ok.ID).
		SetGames([]string{game}).SetLevel("mentor").
		SetGameLevels(map[string]string{game: "mentor"}).SaveX(ctx)

	// 候选池：分区不符与等级不足的陪玩不应出现，满足者应出现。
	candidates, err := dispatchCandidates(ctx, db, order, 0, 20, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range candidates {
		if c.PlayerID == wrongGame.ID || c.PlayerID == lowLevel.ID {
			t.Fatalf("候选池不应包含不满足硬性条件的陪玩：%+v", candidates)
		}
	}
	found := false
	for _, c := range candidates {
		if c.PlayerID == ok.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("候选池应包含满足条件的陪玩：%+v", candidates)
	}

	// 指派：分区不符、等级不足都被拒，满足者通过。
	if _, err := assignDispatchSeat(ctx, db, order.ID, 1, wrongGame.ID, 11, now); !errors.Is(err, errForbidden) {
		t.Fatalf("分区不符应被拒：%v", err)
	}
	if _, err := assignDispatchSeat(ctx, db, order.ID, 1, lowLevel.ID, 11, now); !errors.Is(err, errForbidden) {
		t.Fatalf("等级不足应被拒：%v", err)
	}
	if _, err := assignDispatchSeat(ctx, db, order.ID, 1, ok.ID, 11, now); err != nil {
		t.Fatalf("满足条件应通过：%v", err)
	}
}

func TestGrabHallFiltersGameLevelAndCapacity(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_grab_hall_eligibility")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	sku := seedGameSku(t, db, "HALL", "王者荣耀")
	order := db.EsportsOrder.Create().SetOrderNo("HALL-1").SetCustomerID(1).SetSkuID(sku.ID).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetRequiredLevel("senior").
		SetPaymentDeadline(now.Add(time.Hour)).SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusGrabbing).SaveX(ctx)
	wrongGame := seedPlayer(t, db, 9701, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(wrongGame.ID).
		SetGames([]string{"英雄联盟"}).SetLevel("mentor").
		SetGameLevels(map[string]string{"英雄联盟": "mentor"}).SaveX(ctx)
	lowLevel := seedPlayer(t, db, 9702, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(lowLevel.ID).
		SetGames([]string{"王者荣耀"}).SetLevel("novice").
		SetGameLevels(map[string]string{"王者荣耀": "novice"}).SaveX(ctx)
	eligible := seedPlayer(t, db, 9703, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(eligible.ID).SetGames([]string{"王者荣耀"}).
		SetLevel("mentor").
		SetGameLevels(map[string]string{"王者荣耀": "mentor"}).
		SetConcurrentLimit(1).SaveX(ctx)
	for _, playerID := range []int{wrongGame.ID, lowLevel.ID} {
		if hall, err := listGrabHall(ctx, db, playerID, now); err != nil || len(hall) != 0 {
			t.Fatalf("无资格陪玩不应看到该单：player=%d, hall=%+v, err=%v", playerID, hall, err)
		}
	}
	if hall, err := listGrabHall(ctx, db, eligible.ID, now); err != nil || len(hall) != 1 || hall[0].ID != order.ID {
		t.Fatalf("有资格陪玩应看到该单：hall=%+v, err=%v", hall, err)
	}
	other := db.EsportsOrder.Create().SetOrderNo("HALL-2").SetCustomerID(1).SetSkuID(sku.ID).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetPaymentDeadline(now.Add(time.Hour)).SaveX(ctx)
	db.EsportsDispatchSeat.Create().SetOrderID(other.ID).SetSeatIndex(1).
		SetPlayerID(eligible.ID).SetStatus(esportsdispatchseat.StatusAccepted).SaveX(ctx)
	if hall, err := listGrabHall(ctx, db, eligible.ID, now); err != nil || len(hall) != 0 {
		t.Fatalf("并发容量已满时不应展示可抢单：hall=%+v, err=%v", hall, err)
	}
}

// 派单要求分区资格来自考核；测试陪玩默认开通全部分区，报单会把 SKU 的 required_level 快照到订单。
func TestPlayerGameLevelUnboundedGames(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_game_level_unbounded")
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"

	sku := seedGameSku(t, db, "GLU", game)
	db.EsportsSku.UpdateOneID(sku.ID).SetRequiredLevel("senior").SaveX(ctx)

	// games 为空（不限分区）但 level=mentor 满足等级。
	player := seedPlayer(t, db, 9601, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	setTestPlayerLevel(t, db, player, "mentor")

	order := db.EsportsOrder.Create().SetOrderNo("GLU-1").SetCustomerID(1).SetSkuID(sku.ID).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetRequiredLevel("senior").
		SetPaymentDeadline(now.UTC().Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).
		SaveX(ctx)

	if _, err := assignDispatchSeat(ctx, db, order.ID, 1, player.ID, 11, now); err != nil {
		t.Fatalf("已开通分区且等级满足应通过：%v", err)
	}
}

// 派单路径同样要求分区资格来自考核：未开通任何分区的陪玩不能指派到有分区的订单。
func TestAssignDispatchSeatRequiresPassedGame(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_seat_requires_game")
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"
	sku := seedGameSku(t, db, "ASRG", game)
	player := seedPlayer(t, db, 9701, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	// 覆盖测试基建的默认分区全集，模拟尚未通过任何分区考核。
	db.EsportsPlayer.UpdateOneID(player.ID).SetGames([]string{}).ExecX(ctx)
	order := db.EsportsOrder.Create().SetOrderNo("ASRG-1").SetCustomerID(1).SetSkuID(sku.ID).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetPaymentDeadline(now.UTC().Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).
		SaveX(ctx)

	if _, err := assignDispatchSeat(ctx, db, order.ID, 1, player.ID, 11, now); !errors.Is(err, errForbidden) {
		t.Fatalf("未通过分区考核不应被指派：%v", err)
	}
	// 开通该分区后可指派。
	db.EsportsPlayer.UpdateOneID(player.ID).SetGames([]string{game}).ExecX(ctx)
	if _, err := assignDispatchSeat(ctx, db, order.ID, 1, player.ID, 11, now); err != nil {
		t.Fatalf("已开通分区应可指派：%v", err)
	}
}

// 等级按分区独立：同一陪玩在不同分区的等级不同，校验只取订单所属分区的等级。
func TestGameLevelScopedByGame(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_game_level_scoped")
	ctx := t.Context()
	player := seedPlayer(t, db, 9710, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(player.ID).
		SetGames([]string{"王者荣耀", "英雄联盟"}).
		SetLevel("mentor"). // 展示用最高档
		SetGameLevels(map[string]string{"王者荣耀": "mentor", "英雄联盟": "novice"}).
		SaveX(ctx)
	player = db.EsportsPlayer.GetX(ctx, player.ID)
	levels := []SkillLevel{{"novice", "新手"}, {"senior", "高级"}, {"mentor", "导师"}}

	if err := checkPlayerGameLevel(levels, player, "王者荣耀", "senior"); err != nil {
		t.Fatalf("王者荣耀 mentor 应通过 senior 门槛：%v", err)
	}
	if err := checkPlayerGameLevel(levels, player, "英雄联盟", "senior"); !errors.Is(err, errForbidden) {
		t.Fatalf("英雄联盟 novice 不应通过 senior 门槛：%v", err)
	}
	// 分区在 games 里但没有等级记录：视为无等级，按最低档以下处理。
	db.EsportsPlayer.UpdateOneID(player.ID).
		SetGameLevels(map[string]string{"王者荣耀": "mentor"}).ExecX(ctx)
	player = db.EsportsPlayer.GetX(ctx, player.ID)
	if err := checkPlayerGameLevel(levels, player, "英雄联盟", "novice"); !errors.Is(err, errForbidden) {
		t.Fatalf("分区无等级记录应拒绝：%v", err)
	}
}
