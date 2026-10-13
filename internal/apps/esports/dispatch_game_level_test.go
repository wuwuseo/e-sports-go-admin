package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportssku"
)

// 游戏分区权限 + 技能等级（§5.5）：分区不符、等级不足都被拒，满足者通过；候选池同步过滤。
func TestPlayerGameLevelGuard(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_game_level_guard")
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"

	sku := seedGameSku(t, db, "GL", game)
	db.EsportsSku.UpdateOneID(sku.ID).SetRequiredLevel(esportssku.RequiredLevelSenior).SaveX(ctx)

	order := db.EsportsOrder.Create().SetOrderNo("GL-1").SetCustomerID(1).SetSkuID(sku.ID).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetRequiredLevel(esportsorder.RequiredLevelSenior).
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
		SetGames([]string{game}).SetLevel(esportsplayer.LevelNovice).SaveX(ctx)

	// 满足：games 含 game + level=mentor。
	ok := seedPlayer(t, db, 9503, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(ok.ID).
		SetGames([]string{game}).SetLevel(esportsplayer.LevelMentor).SaveX(ctx)

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

// games 为空表示不限分区（§6.4 缺省）；报单会把 SKU 的 required_level 快照到订单。
func TestPlayerGameLevelUnboundedGames(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_game_level_unbounded")
	ctx := t.Context()
	now := time.Now()
	game := "王者荣耀"

	sku := seedGameSku(t, db, "GLU", game)
	db.EsportsSku.UpdateOneID(sku.ID).SetRequiredLevel(esportssku.RequiredLevelSenior).SaveX(ctx)

	// games 为空（不限分区）但 level=mentor 满足等级。
	player := seedPlayer(t, db, 9601, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(player.ID).SetLevel(esportsplayer.LevelMentor).SaveX(ctx)

	order := db.EsportsOrder.Create().SetOrderNo("GLU-1").SetCustomerID(1).SetSkuID(sku.ID).
		SetQuantity(1).SetUnitPriceCents(1000).SetPayableCents(1000).
		SetPromisedServiceUnits(1).SetRequiredSeats(1).
		SetRequiredLevel(esportsorder.RequiredLevelSenior).
		SetPaymentDeadline(now.UTC().Add(time.Hour)).
		SetPaymentStatus(esportsorder.PaymentStatusPaid).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusPendingDispatch).
		SaveX(ctx)

	if _, err := assignDispatchSeat(ctx, db, order.ID, 1, player.ID, 11, now); err != nil {
		t.Fatalf("games 为空应视为不限分区，等级满足应通过：%v", err)
	}
}
