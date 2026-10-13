package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsplayer"
)

// 并发接单上限（§5.5 / §6.4）：陪玩已占满并发席位后，再次指派被拒。
func TestPlayerConcurrentDispatchLimit(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_limit_concurrent")
	ctx := t.Context()
	now := time.Now()

	order1 := seedPaidOrder(t, db, "limit-c-1", 1)
	order2 := seedPaidOrder(t, db, "limit-c-2", 1)
	player := seedPlayer(t, db, 9401, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(player.ID).SetConcurrentLimit(1).SaveX(ctx)

	seat1, err := assignDispatchSeat(ctx, db, order1.ID, 1, player.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := respondPlayerInvite(ctx, db, player.ID, seat1.ID, true, now); err != nil {
		t.Fatal(err)
	}
	if _, err := assignDispatchSeat(ctx, db, order2.ID, 1, player.ID, 11, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("并发上限未拦截：%v", err)
	}
}

// 每日接单上限（§5.5 / §6.4）：陪玩当日接满后，抢单被拒。
func TestPlayerDailyDispatchLimit(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_limit_daily")
	ctx := t.Context()
	now := time.Now()

	player := seedPlayer(t, db, 9402, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(player.ID).SetDailyLimit(1).SaveX(ctx)

	order1 := seedPaidOrder(t, db, "limit-d-1", 1)
	order2 := seedPaidOrder(t, db, "limit-d-2", 1)
	if _, err := publishOrderToGrabHall(ctx, db, order1.ID, 11, "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := grabPlayerOrder(ctx, db, player.ID, order1.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := publishOrderToGrabHall(ctx, db, order2.ID, 11, "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := grabPlayerOrder(ctx, db, player.ID, order2.ID, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("每日上限未拦截：%v", err)
	}
}
