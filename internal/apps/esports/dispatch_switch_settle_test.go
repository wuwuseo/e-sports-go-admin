package esports

import (
	"errors"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"go-admin/internal/ent/esportsorder"
	"go-admin/internal/ent/esportsplayer"
	"go-admin/internal/ent/esportsservicesegment"
)

// P1-03：改派 / 换人时，被换下陪玩名下**暂停中**的服务段也要被结算（按有效时长），
// 且该段一旦被改派终止，原陪玩不能再 resume 恢复它继续累加时长。
func TestReassignSettlesPausedSegmentAndBlocksResume(t *testing.T) {
	db := openPlayerTestDB(t, "esports_switch_settle")
	defer db.Close()
	ctx := t.Context()
	base := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)

	old := seedPlayer(t, db, 7401, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	mate := seedPlayer(t, db, 7402, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)
	fresh := seedPlayer(t, db, 7403, esportsplayer.QualificationQualified, esportsplayer.PresenceOnline)

	// 双席订单：另一席仍在服务中，保证改派后订单仍是 in_service（这样 resume 才会真正打到服务段守卫）。
	order := seedPaidOrder(t, db, "switch-settle-1", 2)
	acceptSeat(t, db, order.ID, 1, old.ID, 11, base)
	acceptSeat(t, db, order.ID, 2, mate.ID, 11, base)
	if _, err := startPlayerService(ctx, db, old.ID, order.ID, base); err != nil {
		t.Fatal(err)
	}
	if _, err := startPlayerService(ctx, db, mate.ID, order.ID, base); err != nil {
		t.Fatal(err)
	}
	// 被换下的陪玩此刻处于暂停中：服务 10 秒后暂停。
	if _, err := setPlayerServicePause(ctx, db, old.ID, order.ID, true, "客户暂离", base.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}

	// 服务中改派：把第 1 席换给新陪玩。
	if _, err := assignDispatchSeat(ctx, db, order.ID, 1, fresh.ID, 11, base.Add(40*time.Second)); err != nil {
		t.Fatalf("服务中改派应成功，err=%v", err)
	}
	segment := db.EsportsServiceSegment.Query().Where(
		esportsservicesegment.PlayerIDEQ(old.ID)).OnlyX(ctx)
	if segment.Status != esportsservicesegment.StatusFinished {
		t.Fatalf("暂停中的服务段在改派后必须进入终态，got=%s", segment.Status)
	}
	if segment.EndedAt == nil {
		t.Fatalf("结算必须写 ended_at：%+v", segment)
	}
	// 有效时长 = 墙钟 40 秒 − 暂停 30 秒 = 10 秒。
	if segment.SystemSeconds != 10 {
		t.Fatalf("改派结算必须按有效时长（墙钟 − 暂停），got=%d", segment.SystemSeconds)
	}
	if segment.PausedSeconds != 30 || segment.PausedAt != nil {
		t.Fatalf("在途暂停时长必须落库并清空 paused_at：%+v", segment)
	}
	if got := db.EsportsOrder.GetX(ctx, order.ID); got.FulfillmentStatus != esportsorder.FulfillmentStatusInService {
		t.Fatalf("仍有成员在服务，订单应保持服务中：%+v", got)
	}

	// 被换下的陪玩不能再恢复该服务段（订单仍在服务中，守卫必须由服务段状态兜住）。
	if _, err := setPlayerServicePause(ctx, db, old.ID, order.ID, false, "", base.Add(50*time.Second)); !errors.Is(err, errConflict) {
		t.Fatalf("改派终止的服务段不得被 resume 恢复，err=%v", err)
	}
	if again := db.EsportsServiceSegment.GetX(ctx, segment.ID); again.Status != esportsservicesegment.StatusFinished {
		t.Fatalf("恢复被拒后服务段状态不得变化，got=%s", again.Status)
	}
}
