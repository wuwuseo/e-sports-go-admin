package esports

import (
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsoutbox"
	"go-admin/internal/ent/esportsplayer"
)

// 心跳超时自动离线（§6.4）：空闲陪玩置离线，有在途订单的转保护异常并提醒，心跳正常与无心跳基线的档案不动。
func TestPlayerHeartbeatAutoOfflineProtectsInflightOrders(t *testing.T) {
	db := openPlayerTestDB(t, "esports_player_heartbeat")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)
	stale := now.Add(-30 * time.Minute)

	idle := seedPlayer(t, db, 921, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(idle.ID).SetLastHeartbeatAt(stale).ExecX(ctx)

	busy := seedPlayer(t, db, 922, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(busy.ID).SetLastHeartbeatAt(stale).ExecX(ctx)
	order := seedPaidOrder(t, db, "heartbeat-inflight", 1)
	seat, err := assignDispatchSeat(ctx, db, order.ID, 1, busy.ID, 11, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := respondPlayerInvite(ctx, db, busy.ID, seat.ID, true, now); err != nil {
		t.Fatal(err)
	}

	fresh := seedPlayer(t, db, 923, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(fresh.ID).SetLastHeartbeatAt(now.Add(-time.Minute)).ExecX(ctx)

	// 无心跳基线的存量档案：本轮不处理，避免升级后一次性把在线陪玩全部离线。
	legacy := seedPlayer(t, db, 924, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	if legacy.LastHeartbeatAt != nil {
		t.Fatalf("构造的存量档案不应有心跳：%+v", legacy)
	}

	count, err := autoOfflineStalePlayers(ctx, db, now, 100)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("处理条数不正确：%d", count)
	}
	if got := db.EsportsPlayer.GetX(ctx, idle.ID); got.Presence != esportsplayer.PresenceOffline {
		t.Fatalf("空闲失联陪玩未转离线：%+v", got)
	}
	if got := db.EsportsPlayer.GetX(ctx, busy.ID); got.Presence != esportsplayer.PresenceProtectedAbnormal {
		t.Fatalf("有在途订单的失联陪玩未转保护异常：%+v", got)
	}
	if got := db.EsportsPlayer.GetX(ctx, fresh.ID); got.Presence != esportsplayer.PresenceOnline {
		t.Fatalf("心跳正常陪玩被误离线：%+v", got)
	}
	if got := db.EsportsPlayer.GetX(ctx, legacy.ID); got.Presence != esportsplayer.PresenceOnline {
		t.Fatalf("无心跳基线档案被误离线：%+v", got)
	}
	if alerts := db.EsportsOutbox.Query().Where(
		esportsoutbox.EventTypeEQ("player.heartbeat_timeout"),
	).CountX(ctx); alerts != 2 {
		t.Fatalf("失联提醒条数不正确：%d", alerts)
	}
	// 已离线的陪玩不再重复处理。
	if again, err := autoOfflineStalePlayers(ctx, db, now, 100); err != nil || again != 0 {
		t.Fatalf("重复运行未幂等：%d %v", again, err)
	}
}

// 保活只刷新时戳，不改变在线状态；上下线与时写入心跳基线。
func TestPlayerHeartbeatKeepsPresenceSemantics(t *testing.T) {
	db := openPlayerTestDB(t, "esports_player_heartbeat_stamp")
	defer db.Close()
	ctx := t.Context()
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.UTC)

	online := seedPlayer(t, db, 931, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	updated, err := recordPlayerHeartbeat(ctx, db, online.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if updated.LastHeartbeatAt == nil || !updated.LastHeartbeatAt.Equal(now.UTC()) ||
		updated.Presence != esportsplayer.PresenceOnline {
		t.Fatalf("保活结果不正确：%+v", updated)
	}

	// 离线陪玩保活不会把它变成在线。
	offline := seedPlayer(t, db, 932, esportsplayer.QualificationQualified, esportsplayer.PresenceOffline)
	stamped, err := recordPlayerHeartbeat(ctx, db, offline.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if stamped.Presence != esportsplayer.PresenceOffline {
		t.Fatalf("保活改变了离线状态：%+v", stamped)
	}
	// 上线写入心跳基线，供超时任务判断失联。
	back, err := setPlayerPresence(ctx, db, offline.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if back.Presence != esportsplayer.PresenceOnline || back.LastHeartbeatAt == nil {
		t.Fatalf("上线未写入心跳基线：%+v", back)
	}
	if _, err := recordPlayerHeartbeat(ctx, db, 0, now); err == nil {
		t.Fatal("非法陪玩 ID 未被拦截")
	}
}
