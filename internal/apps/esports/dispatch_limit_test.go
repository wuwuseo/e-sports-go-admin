package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsorder"
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

func TestPendingInviteReservesAndReleasesCapacity(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_limit_invited")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	player := seedPlayer(t, db, 9403, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(player.ID).SetConcurrentLimit(1).SaveX(ctx)
	first := seedPaidOrder(t, db, "limit-invite-first", 1)
	second := seedPaidOrder(t, db, "limit-invite-second", 1)
	seat, err := assignDispatchSeat(ctx, db, first.ID, 1, player.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assignDispatchSeat(ctx, db, second.ID, 1, player.ID, 11, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("未回应邀请应临时占用容量：%v", err)
	}
	if err := respondPlayerInvite(ctx, db, player.ID, seat.ID, false, now); err != nil {
		t.Fatal(err)
	}
	if _, err := assignDispatchSeat(ctx, db, second.ID, 1, player.ID, 11, now); err != nil {
		t.Fatalf("拒绝邀请后应释放容量：%v", err)
	}
}

func TestPendingInviteReservesDailyCapacity(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_limit_invited_daily")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	player := seedPlayer(t, db, 9407, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(player.ID).SetDailyLimit(1).SaveX(ctx)
	first := seedPaidOrder(t, db, "limit-invite-daily-first", 1)
	second := seedPaidOrder(t, db, "limit-invite-daily-second", 1)
	seat, err := assignDispatchSeat(ctx, db, first.ID, 1, player.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assignDispatchSeat(ctx, db, second.ID, 1, player.ID, 11, now); !errors.Is(err, errInvalidInput) {
		t.Fatalf("未回应邀请也应占用当日上限：%v", err)
	}
	if err := respondPlayerInvite(ctx, db, player.ID, seat.ID, false, now); err != nil {
		t.Fatal(err)
	}
	if _, err := assignDispatchSeat(ctx, db, second.ID, 1, player.ID, 11, now); err != nil {
		t.Fatalf("拒绝后应释放当日临时占位：%v", err)
	}
}

func TestExpiredInviteAndFinishedServiceReleaseConcurrentCapacity(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_limit_released")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	player := seedPlayer(t, db, 9404, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(player.ID).SetConcurrentLimit(1).SaveX(ctx)
	expiring := seedPaidOrder(t, db, "limit-expiring", 1)
	first := seedPaidOrder(t, db, "limit-finished", 1)
	second := seedPaidOrder(t, db, "limit-after-finish", 1)
	if _, err := assignDispatchSeat(ctx, db, expiring.ID, 1, player.ID, 11, now); err != nil {
		t.Fatal(err)
	}
	seat, err := assignDispatchSeat(ctx, db, first.ID, 1, player.ID, 11, now.Add(25*time.Hour))
	if err != nil {
		t.Fatalf("邀请过期后即使清理任务未运行也应释放容量：%v", err)
	}
	serviceAt := now.Add(25 * time.Hour)
	if err := respondPlayerInvite(ctx, db, player.ID, seat.ID, true, serviceAt); err != nil {
		t.Fatalf("自己的已预占邀请应能转为确定席位：%v", err)
	}
	if _, err := startPlayerService(ctx, db, player.ID, first.ID, serviceAt); err != nil {
		t.Fatal(err)
	}
	if _, err := finishPlayerService(ctx, db, player.ID, first.ID, 1, 60, "proof.png", serviceAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := assignDispatchSeat(ctx, db, second.ID, 1, player.ID, 11, serviceAt.Add(2*time.Minute)); err != nil {
		t.Fatalf("服务结束后的历史 accepted 席位不应继续占并发容量：%v", err)
	}
}

func TestCancelledOrderInviteCannotBeAcceptedOrHoldCapacity(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_limit_cancelled")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	player := seedPlayer(t, db, 9405, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(player.ID).SetConcurrentLimit(1).SaveX(ctx)
	cancelled := seedPaidOrder(t, db, "limit-cancelled", 1)
	next := seedPaidOrder(t, db, "limit-after-cancel", 1)
	seat, err := assignDispatchSeat(ctx, db, cancelled.ID, 1, player.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	db.EsportsOrder.UpdateOneID(cancelled.ID).
		SetFulfillmentStatus(esportsorder.FulfillmentStatusCancelled).SaveX(ctx)
	invites, err := listPlayerInvites(ctx, db, player.ID, now)
	if err != nil || len(invites) != 0 {
		t.Fatalf("已取消订单不应继续出现在待接邀请：%v %+v", err, invites)
	}
	if err := respondPlayerInvite(ctx, db, player.ID, seat.ID, true, now); !errors.Is(err, errConflict) {
		t.Fatalf("已取消订单的旧邀请不得接单：%v", err)
	}
	if _, err := assignDispatchSeat(ctx, db, next.ID, 1, player.ID, 11, now); err != nil {
		t.Fatalf("取消订单的邀请不应继续占容量：%v", err)
	}
}

func TestDispatchCapacityReservationRejectsStalePlayerVersion(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_capacity_version")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	player := seedPlayer(t, db, 9406, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := reservePlayerDispatchCapacity(ctx, tx, player, nil, now, 0); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	stale, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stale.Rollback()
	if err := reservePlayerDispatchCapacity(ctx, stale, player, nil, now, 0); !errors.Is(err, errConflict) {
		t.Fatalf("旧陪玩版本不得再次抢占容量：%v", err)
	}
}

func TestDispatchCandidatesExcludeFullPlayer(t *testing.T) {
	db := openDispatchPriorityDB(t, "esports_dispatch_candidate_capacity")
	defer db.Close()
	ctx := t.Context()
	now := time.Now().UTC()
	sku := seedGameSku(t, db, "CAPACITY-CANDIDATE", "王者荣耀")
	first := seedPaidOrder(t, db, "capacity-candidate-first", 1)
	second := seedPaidOrder(t, db, "capacity-candidate-second", 1)
	db.EsportsOrder.UpdateOneID(first.ID).SetSkuID(sku.ID).SaveX(ctx)
	second = db.EsportsOrder.UpdateOneID(second.ID).SetSkuID(sku.ID).SaveX(ctx)
	player := seedPlayer(t, db, 9408, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(player.ID).SetConcurrentLimit(1).SaveX(ctx)
	seat, err := assignDispatchSeat(ctx, db, first.ID, 1, player.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	contains := func() bool {
		t.Helper()
		candidates, err := dispatchCandidates(ctx, db, second, 0, 20, now)
		if err != nil {
			t.Fatal(err)
		}
		for _, candidate := range candidates {
			if candidate.PlayerID == player.ID {
				return true
			}
		}
		return false
	}
	if contains() {
		t.Fatal("已被邀请占满容量的陪玩不应出现在候选池")
	}
	if err := respondPlayerInvite(ctx, db, player.ID, seat.ID, false, now); err != nil {
		t.Fatal(err)
	}
	if !contains() {
		t.Fatal("拒绝邀请释放容量后，陪玩应重新进入候选池")
	}
}
