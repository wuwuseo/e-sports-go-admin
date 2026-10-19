package esports

import (
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsdispatchprioritynotice"
	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
)

// P1-01-01：心跳超时且**有在途订单**时，状态必须落 protected_abnormal（不是 offline），
// 且对外可展示文案是「连接异常（受保护）」而不是「已下线」；该陪玩同时不得进入派单候选、
// 不得进入优先通知名单。
func TestPresenceProtectedAbnormalSemanticNotOffline(t *testing.T) {
	db := openPlayerTestDB(t, "esports_presence_semantic")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()
	game := "P1在线语义分区"

	busy := seedPlayer(t, db, 7101, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	order := seedPaidOrder(t, db, "presence-inflight-1", 1)
	acceptSeat(t, db, order.ID, 1, busy.ID, 11, now)
	db.EsportsPlayer.UpdateOneID(busy.ID).
		SetLastHeartbeatAt(now.Add(-2 * time.Hour).UTC()).SaveX(ctx)

	count, err := autoOfflineStalePlayers(ctx, db, now, 100)
	if err != nil || count != 1 {
		t.Fatalf("心跳超时任务应处理 1 人，count=%d err=%v", count, err)
	}
	got := db.EsportsPlayer.GetX(ctx, busy.ID)
	if got.Presence != esportsplayer.PresenceProtectedAbnormal {
		t.Fatalf("有在途订单时不得落成「已下线」，got=%s", got.Presence)
	}

	// 陪玩端首页：直接展示的文案字段不得叫「已下线」。
	dashboard, err := playerDashboard(ctx, db, busy.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.PresenceLabel != "连接异常（受保护）" ||
		strings.Contains(dashboard.PresenceLabel, "已下线") {
		t.Fatalf("首页状态文案必须表达「连接异常（受保护）」，got=%q", dashboard.PresenceLabel)
	}
	if dashboard.Presence != "protected_abnormal" {
		t.Fatalf("首页 presence 语义不正确：%+v", dashboard)
	}

	// 看板逐人明细：客服同样看到「连接异常（受保护）」。
	board, err := loadDispatchBoard(ctx, db, 11, true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range board.Players {
		if item.PlayerID != busy.ID {
			continue
		}
		found = true
		if item.PresenceLabel != "连接异常（受保护）" || strings.Contains(item.PresenceLabel, "已下线") {
			t.Fatalf("看板状态文案必须表达「连接异常（受保护）」，got=%q", item.PresenceLabel)
		}
	}
	if !found {
		t.Fatalf("看板应列出该合格陪玩：%+v", board.Players)
	}

	// 不得进入派单候选。
	target := seedPaidOrder(t, db, "presence-inflight-target", 1)
	candidates, err := dispatchCandidates(ctx, db, target, 0, 20, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range candidates {
		if item.PlayerID == busy.ID {
			t.Fatalf("连接异常（受保护）的陪玩不得进入派单候选：%+v", candidates)
		}
	}

	// 不得进入优先通知名单（即便他在该分区的生效名单里）。
	sku := seedGameSku(t, db, "P1-PRESENCE-SKU", game)
	if _, err = grantDispatchPriority(ctx, db, 7, basePriorityInput(game, busy.ID), now); err != nil {
		t.Fatal(err)
	}
	noticed := seedPendingOrderWithSku(t, db, "P1-PRESENCE-NOTICE", sku.ID)
	tx, err := db.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = onOrderEnteredPendingDispatch(ctx, tx, noticed.ID, now); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsOrder.GetX(ctx, noticed.ID); got.PriorityNoticeStatus == esportsorder.PriorityNoticeStatusPending {
		t.Fatalf("全部名单成员连接异常时不应开窗：%+v", got)
	}
	if n := db.EsportsDispatchPriorityNotice.Query().Where(
		esportsdispatchprioritynotice.OrderIDEQ(noticed.ID)).CountX(ctx); n != 0 {
		t.Fatalf("连接异常（受保护）的名单成员不得收到优先通知，got=%d", n)
	}
}

// P1-01-02：人工下线不被下一次心跳自动改回 online；自动上线只对「因心跳超时被动下线」生效。
func TestManualOfflineNotAutoOnlinedByHeartbeat(t *testing.T) {
	db := openPlayerTestDB(t, "esports_presence_auto_online")
	defer db.Close()
	ctx := t.Context()
	now := time.Now()

	// ① 人工下线：自动上线开关被关闭，下一次心跳不改变 online/offline。
	manual := seedPlayer(t, db, 7201, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(manual.ID).SetAutoOnline(true).SaveX(ctx)
	if _, err := setPlayerPresence(ctx, db, manual.ID, false); err != nil {
		t.Fatalf("人工下线应成功，err=%v", err)
	}
	if got := db.EsportsPlayer.GetX(ctx, manual.ID); got.AutoOnline {
		t.Fatal("人工下线必须关闭自动上线开关，否则下一次心跳会把人工下线改回在线")
	}
	afterHeartbeat, err := recordPlayerHeartbeat(ctx, db, manual.ID, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("心跳应成功，err=%v", err)
	}
	if afterHeartbeat.Presence != esportsplayer.PresenceOffline {
		t.Fatalf("人工下线的陪玩不得被心跳改回 online，got=%s", afterHeartbeat.Presence)
	}

	// ② 被动下线（心跳超时自动离线）→ 下一次心跳恢复 online。
	passive := seedPlayer(t, db, 7202, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	db.EsportsPlayer.UpdateOneID(passive.ID).SetAutoOnline(true).
		SetLastHeartbeatAt(now.Add(-2 * time.Hour).UTC()).SaveX(ctx)
	if _, err = autoOfflineStalePlayers(ctx, db, now, 100); err != nil {
		t.Fatal(err)
	}
	if got := db.EsportsPlayer.GetX(ctx, passive.ID); got.Presence != esportsplayer.PresenceOffline {
		t.Fatalf("无在途订单的失联陪玩应自动离线，got=%s", got.Presence)
	}
	recovered, err := recordPlayerHeartbeat(ctx, db, passive.ID, now)
	if err != nil {
		t.Fatalf("心跳应成功，err=%v", err)
	}
	if recovered.Presence != esportsplayer.PresenceOnline {
		t.Fatalf("因心跳超时被动下线的陪玩应能自动恢复在线，got=%s", recovered.Presence)
	}
}
